package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestActionRequiresBuildahEnvIncludesExecNixShellBash(t *testing.T) {
	if !actionRequiresBuildahEnv("exec-nix-shell-bash") {
		t.Fatal("exec-nix-shell-bash should require buildah environment setup")
	}
}

func TestActionRequiresBuildahEnvExcludesFileActions(t *testing.T) {
	t.Parallel()

	for _, action := range []string{"read-file", "write-file", "edit-file", "apply-patch", "metadata"} {
		if actionRequiresBuildahEnv(action) {
			t.Fatalf("%s should not require buildah environment setup", action)
		}
	}
}

func TestRunRejectsMissingFilePayloads(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(`{"settings":{}}`))
	t.Setenv(helperRequestEnv, encoded)

	tests := []struct {
		action string
		want   string
	}{
		{action: "read-file", want: "missing read_file request payload"},
		{action: "write-file", want: "missing write_file request payload"},
		{action: "edit-file", want: "missing edit_file request payload"},
		{action: "apply-patch", want: "missing apply_patch request payload"},
	}

	for _, tc := range tests {
		t.Run(tc.action, func(t *testing.T) {
			err := run(tc.action)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run(%q) error = %v, want %q", tc.action, err, tc.want)
			}
		})
	}
}
