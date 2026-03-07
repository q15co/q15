// Package sandbox manages the agent's helper-backed execution sandbox.
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Info describes authoritative sandbox runtime metadata and probed tool details.
type Info struct {
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

// Describe returns helper-owned sandbox metadata and probed runtime details.
func (s *Sandbox) Describe(ctx context.Context) (Info, error) {
	s.mu.Lock()
	cfg := s.cfg
	prepared := s.prepared
	s.mu.Unlock()

	info := Info{
		ContainerName:    cfg.ContainerName,
		WorkspaceHostDir: cfg.WorkspaceHostDir,
		WorkspaceDir:     cfg.WorkspaceDir,
	}
	metadata, metadataErr := s.helperMetadata(ctx)
	if metadataErr == nil {
		info.Runtime = metadata.Runtime
		info.BaseImage = metadata.BaseImage
	}

	if !prepared {
		if metadataErr != nil {
			return info, errors.Join(
				fmt.Errorf("load sandbox metadata: %w", metadataErr),
				fmt.Errorf("sandbox is not prepared"),
			)
		}
		return info, fmt.Errorf("sandbox is not prepared")
	}

	out, err := s.ExecRaw(ctx, sandboxProbeCommand())
	if err != nil {
		if metadataErr != nil {
			return info, errors.Join(fmt.Errorf("load sandbox metadata: %w", metadataErr), err)
		}
		return info, err
	}

	applySandboxProbeOutput(&info, out)
	if metadataErr != nil {
		return info, fmt.Errorf("load sandbox metadata: %w", metadataErr)
	}
	return info, nil
}

func applySandboxProbeOutput(info *Info, out string) {
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
