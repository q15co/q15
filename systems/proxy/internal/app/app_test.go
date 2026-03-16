package app

import (
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
