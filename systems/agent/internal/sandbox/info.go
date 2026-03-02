package sandbox

import (
	"context"
	"fmt"
	"strings"
)

type SandboxInfo struct {
	ContainerName    string
	WorkspaceHostDir string
	WorkspaceDir     string
	Runtime          string
	BaseImage        string

	OSID         string
	OSVersionID  string
	OSPrettyName string

	NixPath     string
	NixVersion  string
	BashPath    string
	BashVersion string
}

func (s *Sandbox) Describe(ctx context.Context) (SandboxInfo, error) {
	s.mu.Lock()
	cfg := s.cfg
	prepared := s.prepared
	s.mu.Unlock()

	info := SandboxInfo{
		ContainerName:    cfg.ContainerName,
		WorkspaceHostDir: cfg.WorkspaceHostDir,
		WorkspaceDir:     cfg.WorkspaceDir,
		Runtime:          "nix-only",
		BaseImage:        "docker.io/library/debian:bookworm-slim",
	}
	if !prepared {
		return info, fmt.Errorf("sandbox is not prepared")
	}

	out, err := s.Exec(ctx, sandboxProbeCommand())
	if err != nil {
		return info, err
	}

	applySandboxProbeOutput(&info, out)
	return info, nil
}

func applySandboxProbeOutput(info *SandboxInfo, out string) {
	if info == nil {
		return
	}
	for _, rawLine := range strings.Split(out, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		switch key {
		case "os_id":
			info.OSID = val
		case "os_version_id":
			info.OSVersionID = val
		case "os_pretty_name":
			info.OSPrettyName = val
		case "nix_path":
			info.NixPath = val
		case "nix_version":
			info.NixVersion = val
		case "bash_path":
			info.BashPath = val
		case "bash_version":
			info.BashVersion = val
		}
	}
}

func sandboxProbeCommand() string {
	return `
if [ -r /etc/os-release ]; then
  . /etc/os-release
  printf 'os_id=%s\n' "${ID:-}"
  printf 'os_version_id=%s\n' "${VERSION_ID:-}"
  printf 'os_pretty_name=%s\n' "${PRETTY_NAME:-}"
fi

if command -v nix >/dev/null 2>&1; then
  nix_path="$(command -v nix || true)"
  printf 'nix_path=%s\n' "$nix_path"
  nix_version="$(nix --version 2>/dev/null | head -n 1 || true)"
  printf 'nix_version=%s\n' "$nix_version"
fi

if command -v bash >/dev/null 2>&1; then
  bash_path="$(command -v bash || true)"
  printf 'bash_path=%s\n' "$bash_path"
  bash_version="$(bash --version 2>/dev/null | head -n 1 || true)"
  printf 'bash_version=%s\n' "$bash_version"
fi

exit 0
`
}
