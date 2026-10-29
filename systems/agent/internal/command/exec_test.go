package command

import (
	"bytes"
	"context"
	"testing"
)

func TestDefaultExecPreservesNotificationEnvironment(t *testing.T) {
	t.Setenv("NOTIFY_SOCKET", "/test/container-notify.sock")
	var stdout bytes.Buffer
	err := DefaultExec(context.Background(), "/bin/sh",
		[]string{"-c", `printf '%s' "$NOTIFY_SOCKET"`}, nil, &stdout, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "/test/container-notify.sock" {
		t.Fatalf("NOTIFY_SOCKET = %q", stdout.String())
	}
}
