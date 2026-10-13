package secret

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/command"
)

// TestSealedPayloadIsBoundToItsSlot is the one test that runs the real sealing
// program, because the property it checks belongs to systemd-creds rather than
// to this package: the credential name is embedded in the ciphertext, so a
// payload copied from one slot into another refuses to decrypt.
//
// It runs with a null key, which is the one key option that needs no key
// material and therefore works where there is no user manager and no TPM. That
// means it exercises the plumbing, the file modes and the binding, and not the
// key itself: a real key is the host's business, and the store is deliberately
// built so that failing to reach one is a loud error rather than a silent
// fallback.
//
// The program has to be new enough, and when it is not the test says which
// release it found rather than failing: --user and --with-key=null both arrive
// in systemd 256 (measured: 255.9 answers "unrecognized option '--user'" and
// "Unknown key type: null"), so an older runner has nothing here to exercise,
// and a host without the program at all is a skip for the same reason.
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

	store, err := Open(Options{
		Dir:     t.TempDir(),
		KeyArgs: []string{"--with-key=null"},
		Exec:    command.DefaultExec,
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
