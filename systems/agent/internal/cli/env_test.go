package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestWithDefaultsFillsMissingStreams(t *testing.T) {
	env := Env{}.WithDefaults()

	if env.Stdout != io.Discard {
		t.Fatalf("Stdout = %v, want io.Discard", env.Stdout)
	}
	if env.Stderr != io.Discard {
		t.Fatalf("Stderr = %v, want io.Discard", env.Stderr)
	}

	got, err := io.ReadAll(env.Stdin)
	if err != nil {
		t.Fatalf("reading the default stdin: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("stdin = %q, want it empty", got)
	}
}

func TestWithDefaultsKeepsSuppliedStreams(t *testing.T) {
	var stdout, stderr bytes.Buffer
	env := Env{
		Stdin:   strings.NewReader("an answer"),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Release: "v1.2.3",
	}.WithDefaults()

	if _, err := io.WriteString(env.Stdout, "data"); err != nil {
		t.Fatalf("writing to Stdout: %v", err)
	}
	if _, err := io.WriteString(env.Stderr, "chatter"); err != nil {
		t.Fatalf("writing to Stderr: %v", err)
	}
	if stdout.String() != "data" {
		t.Fatalf("stdout = %q, want %q", stdout.String(), "data")
	}
	if stderr.String() != "chatter" {
		t.Fatalf("stderr = %q, want %q", stderr.String(), "chatter")
	}

	answered, err := io.ReadAll(env.Stdin)
	if err != nil {
		t.Fatalf("reading stdin: %v", err)
	}
	if string(answered) != "an answer" {
		t.Fatalf("stdin = %q, want %q", answered, "an answer")
	}
	if env.Release != "v1.2.3" {
		t.Fatalf("Release = %q, want %q", env.Release, "v1.2.3")
	}
}
