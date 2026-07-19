package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeReadinessMarkerLifecycle(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "runtime.ready")
	if err := clearRuntimeReady(path); err != nil {
		t.Fatalf("clearRuntimeReady(missing) error = %v", err)
	}

	readyAt := time.Date(2026, time.July, 19, 8, 43, 19, 123, time.UTC)
	if err := markRuntimeReady(path, readyAt); err != nil {
		t.Fatalf("markRuntimeReady() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	if got, want := string(data), readyAt.Format(time.RFC3339Nano)+"\n"; got != want {
		t.Fatalf("readiness marker = %q, want %q", got, want)
	}

	if err := clearRuntimeReady(path); err != nil {
		t.Fatalf("clearRuntimeReady() error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("os.Stat() error = %v, want not-exist", err)
	}
}

func TestRuntimeReadinessMarkerRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	if err := markRuntimeReady("", time.Now()); err == nil {
		t.Fatal("markRuntimeReady(empty path) error = nil")
	}
	if err := markRuntimeReady(filepath.Join(t.TempDir(), "ready"), time.Time{}); err == nil {
		t.Fatal("markRuntimeReady(zero time) error = nil")
	}
	if err := clearRuntimeReady(""); err == nil {
		t.Fatal("clearRuntimeReady(empty path) error = nil")
	}
}
