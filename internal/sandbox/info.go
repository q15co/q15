package sandbox

import (
	"context"
	"fmt"
	"strings"
)

type SandboxInfo struct {
	ContainerName    string
	FromImage        string
	WorkspaceHostDir string
	WorkspaceDir     string
	Network          string

	OSID         string
	OSVersionID  string
	OSPrettyName string

	PackageManager  string
	ShellsAvailable []string
	ToolsAvailable  []string
}

func (s *Sandbox) Describe(ctx context.Context) (SandboxInfo, error) {
	s.mu.Lock()
	cfg := s.cfg
	prepared := s.prepared
	s.mu.Unlock()

	info := SandboxInfo{
		ContainerName:    cfg.ContainerName,
		FromImage:        cfg.FromImage,
		WorkspaceHostDir: cfg.WorkspaceHostDir,
		WorkspaceDir:     cfg.WorkspaceDir,
		Network:          cfg.Network,
	}
	if !prepared {
		return info, fmt.Errorf("sandbox is not prepared")
	}

	out, err := s.Exec(ctx, sandboxProbeCommand())
	if err != nil {
		return info, err
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
		case "package_manager":
			info.PackageManager = val
		case "shell":
			info.ShellsAvailable = appendUnique(info.ShellsAvailable, val)
		case "tool":
			info.ToolsAvailable = appendUnique(info.ToolsAvailable, val)
		}
	}

	return info, nil
}

func sandboxProbeCommand() string {
	return `
if [ -r /etc/os-release ]; then
  . /etc/os-release
  printf 'os_id=%s\n' "${ID:-}"
  printf 'os_version_id=%s\n' "${VERSION_ID:-}"
  printf 'os_pretty_name=%s\n' "${PRETTY_NAME:-}"
fi

pm=""
for c in apt-get apk pacman dnf microdnf yum zypper nix; do
  if command -v "$c" >/dev/null 2>&1; then
    pm="$c"
    break
  fi
done
printf 'package_manager=%s\n' "$pm"

for c in /bin/sh /bin/bash /bin/dash /bin/ash /usr/bin/fish; do
  if [ -x "$c" ]; then
    printf 'shell=%s\n' "$c"
  fi
done

for c in git curl wget python3 python node npm jq make gcc go tar unzip; do
  if command -v "$c" >/dev/null 2>&1; then
    printf 'tool=%s\n' "$c"
  fi
done

exit 0
`
}

func appendUnique(items []string, item string) []string {
	item = strings.TrimSpace(item)
	if item == "" {
		return items
	}
	for _, existing := range items {
		if existing == item {
			return items
		}
	}
	return append(items, item)
}
