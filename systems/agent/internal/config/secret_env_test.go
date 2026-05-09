package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSecretEnvValue(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(filePath, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	emptyPath := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatalf("write empty file: %v", err)
	}

	tests := []struct {
		name     string
		envValue string
		filePath string
		want     string
		wantErr  string
	}{
		{name: "direct env wins", envValue: "env-secret", filePath: filePath, want: "env-secret"},
		{name: "file fallback", filePath: filePath, want: "file-secret"},
		{
			name:    "missing env and file",
			wantErr: `env var "SECRET_VALUE" or "SECRET_VALUE_FILE" is required`,
		},
		{name: "empty file", filePath: emptyPath, wantErr: "points to an empty file"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SECRET_VALUE", tc.envValue)
			t.Setenv("SECRET_VALUE_FILE", tc.filePath)

			got, err := resolveSecretEnvValue("SECRET_VALUE")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf(
						"resolveSecretEnvValue() error = %v, want substring %q",
						err,
						tc.wantErr,
					)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveSecretEnvValue() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("resolveSecretEnvValue() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLookupSecretEnvValue(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(filePath, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}
	emptyPath := filepath.Join(t.TempDir(), "empty.txt")
	if err := os.WriteFile(emptyPath, nil, 0o600); err != nil {
		t.Fatalf("write empty file: %v", err)
	}

	tests := []struct {
		name     string
		envValue string
		filePath string
		want     string
		wantOK   bool
		wantErr  string
	}{
		{
			name:     "direct env wins",
			envValue: "env-secret",
			filePath: filePath,
			want:     "env-secret",
			wantOK:   true,
		},
		{name: "file fallback", filePath: filePath, want: "file-secret", wantOK: true},
		{name: "missing env and file"},
		{name: "empty file", filePath: emptyPath, wantOK: true, wantErr: "points to an empty file"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("OPTIONAL_SECRET", tc.envValue)
			t.Setenv("OPTIONAL_SECRET_FILE", tc.filePath)

			got, ok, err := lookupSecretEnvValue("OPTIONAL_SECRET")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf(
						"LookupSecretEnvValue() error = %v, want substring %q",
						err,
						tc.wantErr,
					)
				}
				if ok != tc.wantOK {
					t.Fatalf("LookupSecretEnvValue() ok = %v, want %v", ok, tc.wantOK)
				}
				return
			}
			if err != nil {
				t.Fatalf("LookupSecretEnvValue() error = %v", err)
			}
			if ok != tc.wantOK {
				t.Fatalf("LookupSecretEnvValue() ok = %v, want %v", ok, tc.wantOK)
			}
			if got != tc.want {
				t.Fatalf("LookupSecretEnvValue() = %q, want %q", got, tc.want)
			}
		})
	}
}
