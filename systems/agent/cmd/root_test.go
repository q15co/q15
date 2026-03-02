package cmd

import (
	"path/filepath"
	"testing"
)

func TestDefaultConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := defaultConfigDir()
	want := filepath.Join(home, ".config", "q15")
	if got != want {
		t.Fatalf("defaultConfigDir = %q, want %q", got, want)
	}
}

func TestResolveConfigPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "q15")

	tests := []struct {
		name string
		path string
		dir  string
		want string
	}{
		{
			name: "explicit path wins",
			path: "/tmp/custom.toml",
			dir:  dir,
			want: "/tmp/custom.toml",
		},
		{
			name: "falls back to config dir",
			path: "",
			dir:  dir,
			want: filepath.Join(dir, "config.toml"),
		},
		{
			name: "legacy fallback when no dir",
			path: "",
			dir:  "",
			want: "config.toml",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveConfigPath(tc.path, tc.dir)
			if got != tc.want {
				t.Fatalf("resolveConfigPath(%q, %q) = %q, want %q", tc.path, tc.dir, got, tc.want)
			}
		})
	}
}

func TestResolveAuthPath(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "q15")

	tests := []struct {
		name string
		path string
		dir  string
		want string
	}{
		{
			name: "explicit path wins",
			path: "/tmp/auth.json",
			dir:  dir,
			want: "/tmp/auth.json",
		},
		{
			name: "falls back to config dir",
			path: "",
			dir:  dir,
			want: filepath.Join(dir, "auth.json"),
		},
		{
			name: "legacy fallback when no dir",
			path: "",
			dir:  "",
			want: "auth.json",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveAuthPath(tc.path, tc.dir)
			if got != tc.want {
				t.Fatalf("resolveAuthPath(%q, %q) = %q, want %q", tc.path, tc.dir, got, tc.want)
			}
		})
	}
}
