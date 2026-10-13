// Package secret is the payload store behind podman's shell secret driver.
//
// Two things give it its shape, and both come from outside:
//
// podman's driver contract. With the shell driver, podman keeps only a name, an
// identifier and timestamps in its own database and hands the payload to
// commands of ours. `list` prints one identifier per line, `lookup` prints the
// value, `store` reads the value from standard input, and every command except
// `list` is handed its identifier in SECRET_ID. Standard output belongs to
// podman, so nothing human may go there. The commands are run as
// `<shell> -c <command>`, which is why the driver's arguments are a configured
// string rather than something this package can compose for itself.
//
// The ciphertext. A payload is sealed with systemd-creds to a file named after
// the identifier, at mode 0400 inside a 0700 directory. The credential's name
// is bound into the blob, so a ciphertext copied from one slot to another
// refuses to decrypt. Plaintext only ever travels as a pipe: the operator, or
// podman, into this package, into systemd-creds.
//
// A store is bound to the machine that sealed it. systemd-creds derives its key
// from the user, the machine identifier and, where present, the TPM, so a store
// does not survive a reinstall, a TPM clear or a disk image replayed elsewhere.
// There is no backup story: recovery is re-issuing the credentials at their
// sources, which is why nothing here is designed to be restorable.
package secret

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/command"
)

// Kind is how a store seals what it holds.
type Kind string

const (
	// KindSystemdCreds seals every payload with systemd-creds. It needs systemd
	// 256 or newer, and a key the user manager can reach when the stack starts.
	KindSystemdCreds Kind = "systemd-creds"
	// KindFile writes the payload as a plain file at mode 0600. It is the
	// fallback for a host that cannot seal, and it is offered rather than chosen:
	// nothing selects it without an explicit request.
	KindFile Kind = "file"
)

// The suffix each kind stores under. They differ so that switching a host from
// one kind to the other cannot leave a plaintext payload where the sealing kind
// will later try to decrypt it: the two kinds do not see each other's files.
const (
	systemdCredsSuffix = ".cred"
	fileSuffix         = ".secret"
)

// systemdFloor is the first systemd release with `systemd-creds --user`.
const systemdFloor = 256

// storeDirMode and payloadMode are the permissions the criteria name: the
// directory is private to the user, and the payload is readable only by its
// owner.
const (
	storeDirMode = 0o700
	payloadMode  = 0o400
	plainMode    = 0o600
)

// Options configure a store.
type Options struct {
	// Dir is the directory the payloads live in. Required.
	Dir string
	// Kind is how payloads are sealed. The zero value means systemd-creds.
	Kind Kind
	// Exec runs a program. Required.
	Exec command.ExecFunc
	// KeyArgs are the key options systemd-creds is given. Empty means
	// `--user`, which derives the key from the user, the machine and, where
	// present, the TPM. It is settable because a host may want a specific key,
	// and because a test needs one that needs no key material at all.
	KeyArgs []string
	// Creds is the systemd-creds program to run. Empty means "find it".
	Creds string
}

// Store is a directory of sealed payloads.
type Store struct {
	dir     string
	kind    Kind
	exec    command.ExecFunc
	keyArgs []string
	creds   string
}

// Open returns a store over the directory the options name. Nothing is created
// until something is stored, so opening a store on a new host is not itself a
// write.
func Open(opts Options) (*Store, error) {
	if strings.TrimSpace(opts.Dir) == "" {
		return nil, errors.New("secret: a store directory is required")
	}
	if opts.Exec == nil {
		return nil, errors.New("secret: a command runner is required")
	}
	kind := opts.Kind
	if kind == "" {
		kind = KindSystemdCreds
	}
	if kind != KindSystemdCreds && kind != KindFile {
		return nil, fmt.Errorf("secret: unknown store kind %q", kind)
	}
	keyArgs := opts.KeyArgs
	if len(keyArgs) == 0 {
		keyArgs = []string{"--user"}
	}
	return &Store{
		dir:     opts.Dir,
		kind:    kind,
		exec:    opts.Exec,
		keyArgs: keyArgs,
		creds:   opts.Creds,
	}, nil
}

// Kind reports how this store seals what it holds, which is what a diagnostic
// has to be able to say out loud.
func (s *Store) Kind() Kind { return s.kind }

// Dir reports the directory the payloads live in.
func (s *Store) Dir() string { return s.dir }

// suffix is the file ending this kind stores under.
func (s *Store) suffix() string {
	if s.kind == KindFile {
		return fileSuffix
	}
	return systemdCredsSuffix
}

// ID is one stored payload's identifier, with the time it was last written.
type ID struct {
	ID       string
	Modified time.Time
}

// IDs lists what the store holds, sorted, without reading a payload. An absent
// store is empty rather than an error: a host that has never set a secret is a
// normal host, and `list` is the first command run on one.
func (s *Store) IDs() ([]ID, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("secret: read %s: %w", s.dir, err)
	}

	ids := make([]ID, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name, ok := strings.CutSuffix(entry.Name(), s.suffix())
		if !ok || !validID(name) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("secret: stat %s: %w", entry.Name(), err)
		}
		ids = append(ids, ID{ID: name, Modified: info.ModTime()})
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].ID < ids[j].ID })
	return ids, nil
}

// Set seals a value into the slot the identifier names, replacing whatever was
// there. The write is a temporary file in the store directory followed by a
// rename, so a reader never sees a half-written payload, and the value is
// written with the mode of its kind rather than being made private afterwards.
func (s *Store) Set(ctx context.Context, id string, value []byte) error {
	if err := validateID(id); err != nil {
		return err
	}
	if len(value) == 0 {
		return errors.New("secret: refusing to store an empty value")
	}
	if err := os.MkdirAll(s.dir, storeDirMode); err != nil {
		return fmt.Errorf("secret: create %s: %w", s.dir, err)
	}
	// MkdirAll leaves an existing directory's mode alone, so the mode is set
	// rather than assumed: a directory someone loosened stays loosened
	// otherwise, and everything below it is worth less.
	if err := os.Chmod(s.dir, storeDirMode); err != nil {
		return fmt.Errorf("secret: chmod %s: %w", s.dir, err)
	}

	tmp, err := os.CreateTemp(s.dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("secret: create a temporary payload: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		// Removal is unconditional and its failure is not reported: on the
		// success path the file has already been renamed away, so there is
		// nothing left to remove.
		_ = os.Remove(tmpPath)
	}()

	if err := s.seal(ctx, id, value, tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("secret: close %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, s.payloadMode()); err != nil {
		return fmt.Errorf("secret: chmod %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, s.path(id)); err != nil {
		return fmt.Errorf("secret: install %s: %w", s.path(id), err)
	}
	return nil
}

// Lookup returns a stored value. It exists for the driver contract, which is
// the only route podman needs, and for nothing else: there is no verb that
// prints a stored value, and the package exposes no way to list them in bulk.
func (s *Store) Lookup(ctx context.Context, id string) ([]byte, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	switch s.kind {
	case KindFile:
		value, err := os.ReadFile(s.path(id))
		if err != nil {
			return nil, fmt.Errorf("secret: read %s: %w", s.path(id), err)
		}
		return value, nil
	default:
		var out strings.Builder
		if err := s.runCrypto(ctx, "decrypt", s.decryptArgs(id), nil, &out, io.Discard); err != nil {
			return nil, s.cryptoError("decrypt", id, err)
		}
		return []byte(out.String()), nil
	}
}

// Delete removes a slot. An empty identifier is refused rather than treated as
// "nothing to do": podman's own store calls its driver's delete *before* it has
// assigned a new identifier when creating a secret, so an empty identifier
// arrives routinely, and the driver that tolerates it makes podman take the
// branch where it deletes by prefix. Every identifier has the empty prefix, so
// tolerating it destroys an unrelated secret.
func (s *Store) Delete(id string) error {
	if id == "" {
		return errors.New("secret: refusing to delete an empty identifier")
	}
	if err := validateID(id); err != nil {
		return err
	}
	if err := os.Remove(s.path(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("secret: remove %s: %w", s.path(id), err)
	}
	return nil
}

// seal writes the value into dst in the store's own format.
func (s *Store) seal(ctx context.Context, id string, value []byte, dst *os.File) error {
	if s.kind == KindFile {
		if _, err := dst.Write(value); err != nil {
			return fmt.Errorf("secret: write %s: %w", dst.Name(), err)
		}
		return nil
	}

	// systemd-creds is told to read the plaintext from standard input and write
	// the ciphertext to the file, so the value never enters an argument list and
	// never appears in a process listing.
	if err := s.runCrypto(ctx, "encrypt", s.encryptArgs(id, dst.Name()),
		strings.NewReader(string(value)), io.Discard, io.Discard); err != nil {
		return s.cryptoError("encrypt", id, err)
	}
	return nil
}

// runCrypto runs one systemd-creds operation, handing it to the user manager
// first when this process is somewhere systemd-creds cannot reach its key.
func (s *Store) runCrypto(
	ctx context.Context,
	verb string,
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) error {
	if s.needsDelegation() {
		return s.delegate(ctx, verb, args, stdin, stdout, stderr)
	}
	return s.exec(ctx, s.credsProgram(), args, stdin, stdout, stderr)
}

// encryptArgs is the argument list for sealing one payload.
func (s *Store) encryptArgs(id, out string) []string {
	return s.credsArgs("encrypt", id, "-", out)
}

// decryptArgs is the argument list for opening one payload. The same name is
// passed as on the way in, because systemd-creds binds it into the ciphertext
// and refuses a mismatch.
func (s *Store) decryptArgs(id string) []string {
	return s.credsArgs("decrypt", id, s.path(id), "-")
}

// credsArgs is systemd-creds' own argument order: the verb, then the key
// options, then the credential name, then where to read and where to write.
func (s *Store) credsArgs(verb, id, in, out string) []string {
	args := make([]string, 0, len(s.keyArgs)+4)
	args = append(args, verb)
	args = append(args, s.keyArgs...)
	args = append(args, "--name="+credentialName(id), in, out)
	return args
}

// payloadMode is the mode a sealed payload is left with.
func (s *Store) payloadMode() os.FileMode {
	if s.kind == KindFile {
		return plainMode
	}
	return payloadMode
}

// path is where one identifier's payload lives.
func (s *Store) path(id string) string {
	return filepath.Join(s.dir, id+s.suffix())
}

// credsProgram is the systemd-creds to run: the configured one, or the name the
// operating system resolves.
func (s *Store) credsProgram() string {
	if strings.TrimSpace(s.creds) != "" {
		return s.creds
	}
	return "systemd-creds"
}

// cryptoError turns a failure of the sealing program into a sentence an
// operator can act on. A missing program and a program that ran and refused are
// different problems with different fixes.
func (s *Store) cryptoError(verb, id string, err error) error {
	if command.NotInstalled(err) {
		return fmt.Errorf(
			"secret: %s is not installed, and this store seals payloads with it: %w",
			s.credsProgram(),
			err,
		)
	}
	return fmt.Errorf("secret: %s %s: %w", verb, id, err)
}

// credentialName is the name a payload is bound to. It is derived from the
// identifier so that moving a ciphertext from one slot to another is a name
// mismatch rather than a decryption of somebody else's payload.
func credentialName(id string) string { return "q15-secret-" + id }

// validID reports whether an identifier can name a file in the store. It is
// deliberately strict: the identifier arrives from podman, and it is about to
// become a path.
func validID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	if strings.HasPrefix(id, ".") || strings.Contains(id, "..") {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// validateID reports why an identifier cannot be stored.
func validateID(id string) error {
	if id == "" {
		return errors.New("secret: an identifier is required")
	}
	if !validID(id) {
		return fmt.Errorf("secret: %q is not a usable identifier", id)
	}
	return nil
}

// SupportsSystemdCreds reports whether systemd-creds is present and new enough
// for a user-scoped credential. It answers with the version it found so a
// caller can say which one is too old rather than only that something is.
func SupportsSystemdCreds(
	ctx context.Context,
	exec command.ExecFunc,
	program string,
) (bool, string, error) {
	if strings.TrimSpace(program) == "" {
		program = "systemd-creds"
	}
	var out strings.Builder
	if err := exec(ctx, program, []string{"--version"}, nil, &out, io.Discard); err != nil {
		if command.NotInstalled(err) {
			return false, "", nil
		}
		return false, "", fmt.Errorf("secret: %s --version: %w", program, err)
	}
	version := strings.TrimSpace(out.String())
	major, ok := systemdMajor(version)
	if !ok {
		return false, version, nil
	}
	return major >= systemdFloor, version, nil
}

// systemdMajor reads the version number out of what systemd prints, which looks
// like "systemd 257 (257.7)".
func systemdMajor(out string) (int, bool) {
	for _, field := range strings.Fields(out) {
		field = strings.TrimPrefix(field, "v")
		if n, err := strconv.Atoi(field); err == nil {
			return n, true
		}
	}
	return 0, false
}
