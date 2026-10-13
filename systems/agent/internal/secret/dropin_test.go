package secret

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDriverDropInRegistersFourCommandsByAbsolutePath is the registration the
// criteria name: a drop-in, the shell driver, and every command named by its
// full path because podman runs each one through a shell whose PATH is not ours.
func TestDriverDropInRegistersFourCommandsByAbsolutePath(t *testing.T) {
	contents, err := DriverDropIn([]string{"/usr/local/bin/q15", "secret-driver"}, "--stack", "Q15")
	if err != nil {
		t.Fatalf("DriverDropIn() error = %v", err)
	}
	text := string(contents)

	if !strings.Contains(text, `driver = "shell"`) {
		t.Fatalf("drop-in = %q, want the shell driver", text)
	}
	if !strings.Contains(text, "[secrets.opts]") {
		t.Fatalf("drop-in = %q, want the driver options section", text)
	}

	registered := `'/usr/local/bin/q15' 'secret-driver' '--stack' 'Q15'`
	for _, verb := range []string{DriverList, DriverLookup, DriverStore, DriverDelete} {
		want := verb + ` = "` + registered + " " + verb + `"`
		if !strings.Contains(text, want) {
			t.Fatalf("drop-in = %q, want it to carry %q", text, want)
		}
	}
}

// TestDriverDropInRefusesARelativePath keeps a command podman cannot run from
// being registered as though it could.
func TestDriverDropInRefusesARelativePath(t *testing.T) {
	if _, err := DriverDropIn([]string{"q15", "secret-driver"}); err == nil {
		t.Fatal("DriverDropIn() error = nil, want a refusal")
	}
	if _, err := DriverDropIn(nil); err == nil {
		t.Fatal("DriverDropIn() error = nil, want a refusal with no command")
	}
}

// TestWriteDropInIsIdempotentAndLeavesTheUsersFileAlone is the second half of
// "a drop-in, never an edit": the file is written once, the second write is a
// no-op the caller can see, and a containers.conf beside it is untouched.
func TestWriteDropInIsIdempotentAndLeavesTheUsersFileAlone(t *testing.T) {
	config := t.TempDir()
	userFile := filepath.Join(config, "containers", "containers.conf")
	if err := os.MkdirAll(filepath.Dir(userFile), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	const userContents = "[secrets]\ndriver = \"file\"\n"
	if err := os.WriteFile(userFile, []byte(userContents), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	dropIn := filepath.Join(config, "containers", "containers.conf.d", DropInFileName)
	contents := []byte("[secrets]\ndriver = \"shell\"\n")

	wrote, err := WriteDropIn(dropIn, contents)
	if err != nil {
		t.Fatalf("WriteDropIn() error = %v", err)
	}
	if !wrote {
		t.Fatal("WriteDropIn() wrote = false, want the first write to land")
	}

	wrote, err = WriteDropIn(dropIn, contents)
	if err != nil {
		t.Fatalf("WriteDropIn() error = %v", err)
	}
	if wrote {
		t.Fatal("WriteDropIn() wrote = true, want the second write to be a no-op")
	}

	got, err := os.ReadFile(userFile)
	if err != nil {
		t.Fatalf("read the user's file: %v", err)
	}
	if string(got) != userContents {
		t.Fatalf("the user's containers.conf = %q, want it untouched", got)
	}

	info, err := os.Stat(dropIn)
	if err != nil {
		t.Fatalf("stat drop-in: %v", err)
	}
	if perm := info.Mode().Perm(); perm != dropInMode {
		t.Fatalf("drop-in mode = %o, want %o", perm, dropInMode)
	}
}

// TestDefaultDropInPathNeverNamesTheUsersFile pins the path's shape: inside the
// drop-in directory, and a file of q15's own.
func TestDefaultDropInPathNeverNamesTheUsersFile(t *testing.T) {
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)

	path, err := DefaultDropInPath()
	if err != nil {
		t.Fatalf("DefaultDropInPath() error = %v", err)
	}
	want := filepath.Join(config, "containers", "containers.conf.d", DropInFileName)
	if path != want {
		t.Fatalf("DefaultDropInPath() = %q, want %q", path, want)
	}
	if filepath.Base(path) == "containers.conf" {
		t.Fatal("the drop-in path names the user's own configuration file")
	}
}
