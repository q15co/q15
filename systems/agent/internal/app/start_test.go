package app

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRunAcceptsNoArgs(t *testing.T) {
	if err := validateArgs(nil); err != nil {
		t.Fatalf("validateArgs(nil) error = %v", err)
	}
}

func TestRunRejectsUnexpectedArgs(t *testing.T) {
	err := validateArgs([]string{"start"})
	if err == nil {
		t.Fatal("validateArgs() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "accepts no arguments") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStartClearsReadinessBeforeLoadingConfig(t *testing.T) {
	if err := markRuntimeReady(runtimeReadyPath, time.Now()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clearRuntimeReady(runtimeReadyPath) })
	err := Start(context.Background())
	if err == nil {
		t.Fatal("Start() error = nil, want error")
	}
	if !strings.Contains(err.Error(), ConfigPath) {
		t.Fatalf("Start() error = %v, want reference to %q", err, ConfigPath)
	}
	if _, err := os.Stat(runtimeReadyPath); !os.IsNotExist(err) {
		t.Fatalf("stale readiness marker survived a failed startup: %v", err)
	}
}
