package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestValidateArgsAcceptsNoArgs(t *testing.T) {
	if err := validateArgs(nil); err != nil {
		t.Fatalf("validateArgs(nil) error = %v", err)
	}
}

func TestValidateArgsRejectsUnexpectedArgs(t *testing.T) {
	err := validateArgs([]string{"serve"})
	if err == nil {
		t.Fatal("validateArgs() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "accepts no arguments") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReportedExecFloorCommandsReadsNixManifest(t *testing.T) {
	t.Parallel()

	manifestPath := filepath.Join(t.TempDir(), "exec-floor.manifest")
	if err := os.WriteFile(manifestPath, []byte("sed\nawk\nrg\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(manifest) error = %v", err)
	}
	got, err := reportedExecFloorCommands("local-nix-shell", manifestPath)
	if err != nil {
		t.Fatalf("reportedExecFloorCommands() error = %v", err)
	}
	want := []string{"sed", "awk", "rg"}
	if !slices.Equal(got, want) {
		t.Fatalf("reportedExecFloorCommands() = %v, want %v", got, want)
	}
}

func TestReportedExecFloorCommandsSkipsNonNixExecutor(t *testing.T) {
	t.Parallel()

	got, err := reportedExecFloorCommands("local-shell", filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatalf("reportedExecFloorCommands() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("reportedExecFloorCommands() = %v, want no commands", got)
	}
}
