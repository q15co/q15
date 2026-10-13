package secret

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/command"
)

// The null key tests the credential envelope and slot binding without touching
// host keys. A private user namespace avoids the privileged credentials service,
// and --allow-null permits the test on hosts with TPM and Secure Boot enabled.
func TestSealedPayloadIsBoundToItsSlot(t *testing.T) {
	ctx := context.Background()
	ok, version, err := SupportsSystemdCreds(ctx, command.DefaultExec, "")
	if err != nil {
		t.Fatalf("SupportsSystemdCreds() error = %v", err)
	}
	if !ok {
		if version == "" {
			t.Skip("systemd-creds is not installed")
		}
		t.Skipf(
			"systemd-creds %s is older than %d, the first release with the sealing this store uses",
			version,
			SystemdFloor,
		)
	}

	keyArgs := []string{"--with-key=null"}
	if major, _ := systemdMajor(version); major >= 259 {
		keyArgs = append(keyArgs, "--allow-null")
	}
	store, err := Open(Options{
		Dir:     t.TempDir(),
		KeyArgs: keyArgs,
		Exec:    nullKeyExec,
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	const value = "sentinel-value"
	if err := store.Set(ctx, "slot-one", []byte(value)); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	sealed := filepath.Join(store.Dir(), "slot-one"+systemdCredsSuffix)
	raw, err := os.ReadFile(sealed)
	if err != nil {
		t.Fatalf("read the sealed payload: %v", err)
	}
	if bytes.Contains(raw, []byte(value)) {
		t.Fatal("the sealed payload carries the plaintext")
	}

	info, err := os.Stat(sealed)
	if err != nil {
		t.Fatalf("stat the sealed payload: %v", err)
	}
	if perm := info.Mode().Perm(); perm != payloadMode {
		t.Fatalf("sealed payload mode = %o, want %o", perm, payloadMode)
	}

	got, err := store.Lookup(ctx, "slot-one")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if string(got) != value {
		t.Fatalf("Lookup() = %q, want %q", got, value)
	}

	// The same ciphertext in another slot is a different credential, and
	// systemd-creds refuses it rather than handing back somebody else's value.
	other := filepath.Join(store.Dir(), "slot-two"+systemdCredsSuffix)
	if err := os.WriteFile(other, raw, payloadMode); err != nil {
		t.Fatalf("copy the payload: %v", err)
	}
	if _, err := store.Lookup(ctx, "slot-two"); err == nil {
		t.Fatal("a payload copied into another slot decrypted, want a refusal")
	}

	if err := store.Delete("slot-one"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	ids, err := store.IDs()
	if err != nil {
		t.Fatalf("IDs() error = %v", err)
	}
	if len(ids) != 1 || ids[0].ID != "slot-two" {
		t.Fatalf("IDs() = %v, want only the copied slot left", ids)
	}
}

func nullKeyExec(
	ctx context.Context,
	program string,
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) error {
	if os.Geteuid() != 0 {
		args = append([]string{"--user", "--map-root-user", "--", program}, args...)
		program = "unshare"
	}
	return command.DefaultExec(ctx, program, args, stdin, stdout, stderr)
}
