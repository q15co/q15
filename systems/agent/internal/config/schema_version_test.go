package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckSchemaVersion(t *testing.T) {
	tests := []struct {
		name    string
		have    int
		want    int
		wantErr string
	}{
		{name: "absent reads as current", have: 0, want: 1},
		{name: "current", have: 1, want: 1},
		{name: "newer than the build", have: 2, want: 1, wantErr: "newer than this build reads"},
		{name: "older than the build", have: 1, want: 2, wantErr: "older than this build reads"},
		{name: "not a version", have: -1, want: 1, wantErr: "not a valid version"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := checkSchemaVersion(test.have, test.want)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("checkSchemaVersion(%d, %d) error = %v", test.have, test.want, err)
				}
				return
			}
			if err == nil {
				t.Fatalf(
					"checkSchemaVersion(%d, %d) error = nil, want %q",
					test.have,
					test.want,
					test.wantErr,
				)
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", err, test.wantErr)
			}
		})
	}
}

// TestLoadRefusesANewerConfiguration proves the check is wired into the loader
// a stranger's stack actually uses, not only into Validate.
func TestLoadRefusesANewerConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := "schema_version: 2\nproviders:\n  - name: ollama\n    type: ollama\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want a schema version rejection")
	}
}
