package secret

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// recorder stands in for the process seam. It keeps what was run and what the
// programs were handed to read, so a test can assert both the argument list and
// the promise that a value never enters one.
type recorder struct {
	calls  [][]string
	stdin  string
	result string
	err    error
}

func (r *recorder) exec(
	_ context.Context,
	program string,
	args []string,
	stdin io.Reader,
	stdout, _ io.Writer,
) error {
	r.calls = append(r.calls, append([]string{program}, args...))
	if stdin != nil {
		value, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		r.stdin += string(value)
	}
	if r.err != nil {
		return r.err
	}
	// systemd-creds writes its ciphertext to the path it is given rather than to
	// standard output, so the stand-in does the same: writing to the stream
	// instead would leave the store's own file handling untested.
	if program == "systemd-creds" && len(args) > 0 && args[0] == "encrypt" {
		if err := os.WriteFile(args[len(args)-1], []byte(r.result), 0o600); err != nil {
			return err
		}
	}
	if stdout != nil {
		_, _ = io.WriteString(stdout, r.result)
	}
	return nil
}

func openTestStore(t *testing.T, kind Kind, rec *recorder) *Store {
	t.Helper()
	withoutDelegation(t)
	store, err := Open(Options{Dir: t.TempDir(), Kind: kind, Exec: rec.exec})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return store
}

// withoutDelegation pins the process facts a unit test cannot choose. The
// sandbox these tests run in may well be uid 0 inside somebody else's user
// namespace, which is exactly the context that hands the cryptography to the
// user manager: without pinning, the tests would depend on where they run.
func withoutDelegation(t *testing.T) {
	t.Helper()
	pinDelegation(t, 1000, "")
}

// withDelegation pins the process facts to podman's own context.
func withDelegation(t *testing.T) {
	t.Helper()
	pinDelegation(t, 0, "         0       1000          1\n")
}

func pinDelegation(t *testing.T, euid int, uidMap string) {
	t.Helper()
	previous := delegationFacts
	delegationFacts = func() (int, string) { return euid, uidMap }
	t.Cleanup(func() { delegationFacts = previous })
}

// TestInPodmansNamespaceTheCryptoIsHandedToTheUserManager covers the step that
// makes the driver work at all under rootless podman: the sealing program is
// run by the user manager, by absolute path, with the value still arriving on
// standard input.
func TestInPodmansNamespaceTheCryptoIsHandedToTheUserManager(t *testing.T) {
	withDelegation(t)
	rec := &recorder{}
	store, err := Open(Options{
		Dir:   t.TempDir(),
		Exec:  rec.exec,
		Creds: "/usr/bin/systemd-creds",
	})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	if err := store.Set(context.Background(), "slot", []byte("s3cret-value")); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("calls = %v, want one", rec.calls)
	}

	call := rec.calls[0]
	want := []string{
		"systemd-run", "--user", "--wait", "--pipe", "--quiet", "--collect",
		"/usr/bin/systemd-creds", "encrypt", "--user", "--name=q15-secret-slot", "-",
	}
	if len(call) != len(want)+1 {
		t.Fatalf("call = %v, want %v and an output path", call, want)
	}
	for i, word := range want {
		if call[i] != word {
			t.Fatalf("call[%d] = %q, want %q", i, call[i], word)
		}
	}
	if rec.stdin != "s3cret-value" {
		t.Fatalf("stdin = %q, want the value", rec.stdin)
	}
}

// TestPlainStoreRoundTripsAValue covers the whole surface a caller sees: a value
// goes in, the same value comes out, and what lands on disk is private to its
// owner.
func TestPlainStoreRoundTripsAValue(t *testing.T) {
	rec := &recorder{}
	store := openTestStore(t, KindFile, rec)
	ctx := context.Background()

	if err := store.Set(ctx, "abc123", []byte("hunter2")); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	value, err := store.Lookup(ctx, "abc123")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if string(value) != "hunter2" {
		t.Fatalf("Lookup() = %q, want %q", value, "hunter2")
	}

	payload, err := os.Stat(filepath.Join(store.Dir(), "abc123"+fileSuffix))
	if err != nil {
		t.Fatalf("stat payload: %v", err)
	}
	if got := payload.Mode().Perm(); got != plainMode {
		t.Fatalf("payload mode = %o, want %o", got, plainMode)
	}
	directory, err := os.Stat(store.Dir())
	if err != nil {
		t.Fatalf("stat store: %v", err)
	}
	if got := directory.Mode().Perm(); got != storeDirMode {
		t.Fatalf("store mode = %o, want %o", got, storeDirMode)
	}

	ids, err := store.IDs()
	if err != nil {
		t.Fatalf("IDs() error = %v", err)
	}
	if len(ids) != 1 || ids[0].ID != "abc123" {
		t.Fatalf("IDs() = %v, want just abc123", ids)
	}

	if err := store.Delete("abc123"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if err := store.Delete("abc123"); err != nil {
		t.Fatalf("Delete() of an absent slot error = %v, want success", err)
	}
	if ids, err := store.IDs(); err != nil || len(ids) != 0 {
		t.Fatalf("IDs() = %v, %v, want empty", ids, err)
	}
}

// TestSetSealsThroughSystemdCreds pins the two halves of the sealing contract:
// the value arrives on standard input, and it never appears in the argument
// list, where a process listing would show it.
func TestSetSealsThroughSystemdCreds(t *testing.T) {
	rec := &recorder{}
	store := openTestStore(t, KindSystemdCreds, rec)

	if err := store.Set(context.Background(), "slot", []byte("s3cret-value")); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("calls = %v, want one", rec.calls)
	}

	call := rec.calls[0]
	want := []string{"systemd-creds", "encrypt", "--user", "--name=q15-secret-slot", "-"}
	if len(call) != len(want)+1 {
		t.Fatalf("call = %v, want %v and an output path", call, want)
	}
	for i, word := range want {
		if call[i] != word {
			t.Fatalf("call[%d] = %q, want %q", i, call[i], word)
		}
	}
	if !strings.HasPrefix(call[len(call)-1], store.Dir()) {
		t.Fatalf("output = %q, want a path inside %q", call[len(call)-1], store.Dir())
	}
	if rec.stdin != "s3cret-value" {
		t.Fatalf("stdin = %q, want the value", rec.stdin)
	}
	for _, word := range call {
		if strings.Contains(word, "s3cret-value") {
			t.Fatalf("call = %v, want the value nowhere in the argument list", call)
		}
	}
}

// TestLookupAsksForTheSameSlotItWrote checks the binding mechanism: the name is
// the same on both sides, which is what makes a ciphertext in the wrong slot a
// mismatch rather than somebody else's payload.
func TestLookupAsksForTheSameSlotItWrote(t *testing.T) {
	rec := &recorder{result: "s3cret-value"}
	store := openTestStore(t, KindSystemdCreds, rec)

	value, err := store.Lookup(context.Background(), "slot")
	if err != nil {
		t.Fatalf("Lookup() error = %v", err)
	}
	if string(value) != "s3cret-value" {
		t.Fatalf("Lookup() = %q, want the program's output", value)
	}
	if len(rec.calls) != 1 {
		t.Fatalf("calls = %v, want one", rec.calls)
	}
	call := rec.calls[0]
	if call[0] != "systemd-creds" || call[1] != "decrypt" || call[3] != "--name=q15-secret-slot" {
		t.Fatalf("call = %v, want a decrypt of q15-secret-slot", call)
	}
	if call[4] != filepath.Join(store.Dir(), "slot"+systemdCredsSuffix) {
		t.Fatalf("call = %v, want it to read this slot", call)
	}
}

// TestDeleteRefusesAnEmptyIdentifier pins the trap that cost a real secret once:
// podman's own store deletes by the empty prefix, so a driver that accepts an
// empty identifier hands it the branch that destroys an unrelated slot.
func TestDeleteRefusesAnEmptyIdentifier(t *testing.T) {
	rec := &recorder{}
	store := openTestStore(t, KindFile, rec)
	ctx := context.Background()

	if err := store.Set(ctx, "keepme", []byte("value")); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if err := store.Delete(""); err == nil {
		t.Fatal("Delete(\"\") error = nil, want a refusal")
	}
	ids, err := store.IDs()
	if err != nil {
		t.Fatalf("IDs() error = %v", err)
	}
	if len(ids) != 1 || ids[0].ID != "keepme" {
		t.Fatalf("IDs() = %v, want the unrelated slot to survive", ids)
	}
}

// TestIdentifiersThatCouldNameAPathAreRefused keeps the identifier, which
// arrives from podman, from becoming a path out of the store.
func TestIdentifiersThatCouldNameAPathAreRefused(t *testing.T) {
	store := openTestStore(t, KindFile, &recorder{})
	ctx := context.Background()

	for _, id := range []string{"", "..", "../escape", "a/b", ".hidden", "with space", "tab\tid"} {
		if err := store.Set(ctx, id, []byte("value")); err == nil {
			t.Fatalf("Set(%q) error = nil, want a refusal", id)
		}
	}
}

// TestIDsIgnoreTheOtherKindsFiles is why the two kinds store under different
// suffixes: a host that switches from the plaintext fallback to sealing must not
// hand the sealing kind a file it cannot open.
func TestIDsIgnoreTheOtherKindsFiles(t *testing.T) {
	rec := &recorder{}
	store := openTestStore(t, KindSystemdCreds, rec)
	dir := store.Dir()

	if err := os.WriteFile(filepath.Join(dir, "plain-one"+fileSuffix), []byte("value"), plainMode); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("value"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := store.Set(context.Background(), "sealed-one", []byte("value")); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	ids, err := store.IDs()
	if err != nil {
		t.Fatalf("IDs() error = %v", err)
	}
	if len(ids) != 1 || ids[0].ID != "sealed-one" {
		t.Fatalf("IDs() = %v, want only this kind's payloads", ids)
	}
}

// TestEmptyValueIsRefused keeps a rotation from quietly turning into a deletion.
func TestEmptyValueIsRefused(t *testing.T) {
	store := openTestStore(t, KindFile, &recorder{})
	if err := store.Set(context.Background(), "slot", nil); err == nil {
		t.Fatal("Set() error = nil, want a refusal")
	}
}

// TestNeedsDelegationForPinsTheDecision is the whole reason the decision is a
// pure function: the case it covers only happens inside a container.
func TestNeedsDelegationForPinsTheDecision(t *testing.T) {
	podmanNamespace := "         0       1000          1\n"
	ordinaryMap := "         0          0 4294967295\n"

	cases := []struct {
		name    string
		kind    Kind
		keyArgs []string
		euid    int
		uidMap  string
		want    bool
	}{
		{
			"podman's namespace, user key",
			KindSystemdCreds,
			[]string{"--user"},
			0,
			podmanNamespace,
			true,
		},
		{"an ordinary root process", KindSystemdCreds, []string{"--user"}, 0, ordinaryMap, false},
		{"an ordinary user", KindSystemdCreds, []string{"--user"}, 1000, podmanNamespace, false},
		{
			"a null key has nothing to delegate",
			KindSystemdCreds,
			[]string{"--with-key=null"},
			0,
			podmanNamespace,
			false,
		},
		{
			"the plaintext kind does no crypto",
			KindFile,
			[]string{"--user"},
			0,
			podmanNamespace,
			false,
		},
		{"no uid map to read", KindSystemdCreds, []string{"--user"}, 0, "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := needsDelegationFor(tc.kind, tc.keyArgs, tc.euid, tc.uidMap)
			if got != tc.want {
				t.Fatalf("needsDelegationFor() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSupportsSystemdCredsReadsTheVersion is the check behind offering the
// fallback: user-scoped credentials arrived in 256, and a host below that has to
// be told rather than left to fail at the first write.
func TestSupportsSystemdCredsReadsTheVersion(t *testing.T) {
	cases := []struct {
		name    string
		result  string
		err     error
		wantOK  bool
		wantVer string
	}{
		{
			name:    "new enough",
			result:  "systemd 257 (257.7)\n",
			wantOK:  true,
			wantVer: "systemd 257 (257.7)",
		},
		{
			name:    "too old",
			result:  "systemd 255 (255.4)\n",
			wantOK:  false,
			wantVer: "systemd 255 (255.4)",
		},
		{name: "not installed", err: notInstalled(), wantOK: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{result: tc.result, err: tc.err}
			ok, version, err := SupportsSystemdCreds(context.Background(), rec.exec, "")
			if err != nil {
				t.Fatalf("SupportsSystemdCreds() error = %v", err)
			}
			if ok != tc.wantOK {
				t.Fatalf("SupportsSystemdCreds() = %v, want %v", ok, tc.wantOK)
			}
			if version != tc.wantVer {
				t.Fatalf("version = %q, want %q", version, tc.wantVer)
			}
		})
	}
}

// TestKindsSuffixesDiffer guards the property the two suffixes exist for.
func TestKindsSuffixesDiffer(t *testing.T) {
	if systemdCredsSuffix == fileSuffix {
		t.Fatal("the two kinds store under the same suffix, which lets one read the other's files")
	}
}

// TestOpenRefusesAnUnknownKind keeps a typo in a flag from silently sealing
// nothing.
func TestOpenRefusesAnUnknownKind(t *testing.T) {
	if _, err := Open(Options{Dir: t.TempDir(), Kind: "plaintext", Exec: (&recorder{}).exec}); err == nil {
		t.Fatal("Open() error = nil, want a refusal")
	}
	if _, err := Open(Options{Dir: "", Exec: (&recorder{}).exec}); err == nil {
		t.Fatal("Open() error = nil, want a refusal for a missing directory")
	}
	if _, err := Open(Options{Dir: t.TempDir()}); err == nil {
		t.Fatal("Open() error = nil, want a refusal for a missing runner")
	}
}

// TestCryptoErrorNamesTheMissingProgram is the sentence an operator acts on: a
// host without systemd-creds has an install to do, not a permission to fix.
func TestCryptoErrorNamesTheMissingProgram(t *testing.T) {
	rec := &recorder{err: notInstalled()}
	store := openTestStore(t, KindSystemdCreds, rec)

	err := store.Set(context.Background(), "slot", []byte("value"))
	if err == nil {
		t.Fatal("Set() error = nil, want a failure")
	}
	if !strings.Contains(err.Error(), "systemd-creds is not installed") {
		t.Fatalf("error = %q, want it to name the missing program", err)
	}
}

// TestSealedPayloadIsWrittenPrivatelyAndAtomically checks the mode the criteria
// name, and that no plaintext is left behind in the directory.
func TestSealedPayloadIsWrittenPrivatelyAndAtomically(t *testing.T) {
	rec := &recorder{result: "ciphertext"}
	store := openTestStore(t, KindSystemdCreds, rec)

	if err := store.Set(context.Background(), "slot", []byte("value")); err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	entries, err := os.ReadDir(store.Dir())
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("store holds %v, want only the payload", names)
	}
	info, err := entries[0].Info()
	if err != nil {
		t.Fatalf("stat payload: %v", err)
	}
	if got := info.Mode().Perm(); got != payloadMode {
		t.Fatalf("payload mode = %o, want %o", got, payloadMode)
	}

	raw, err := os.ReadFile(filepath.Join(store.Dir(), entries[0].Name()))
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	if !bytes.Equal(raw, []byte("ciphertext")) {
		t.Fatalf("payload = %q, want what the program wrote", raw)
	}
}

// notInstalled is the error a program that is not installed produces, built the
// way the standard library builds it so the production code's check on it is
// exercised rather than imitated.
func notInstalled() error {
	return &exec.Error{Name: "systemd-creds", Err: exec.ErrNotFound}
}
