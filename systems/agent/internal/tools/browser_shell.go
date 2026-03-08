// Package tools provides model-callable runtime tools for the agent.
package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

const (
	browserDisplayModeHeadless = "headless"
	browserDisplayModeXvfb     = "xvfb"
)

var browserShellPresetPackages = []string{
	"nixpkgs#playwright-test",
	"nixpkgs#puppeteer-cli",
	"nixpkgs#chromium",
	"nixpkgs#xorg-server",
	"nixpkgs#fontconfig",
	"nixpkgs#noto-fonts",
	"nixpkgs#noto-fonts-cjk-sans",
	"nixpkgs#noto-fonts-color-emoji",
}

var browserShellXvfbPackages = []string{
	"nixpkgs#dbus",
}

//go:embed browser_shell_xvfb.sh
var browserShellXvfbTemplate string

// BrowserShell executes browser automation commands with a fixed browser-ready
// package set and optional virtual display support.
type BrowserShell struct {
	exec NixShellBashExecutor
}

// NewBrowserShell constructs an exec_browser_shell tool backed by the provided
// executor.
func NewBrowserShell(exec NixShellBashExecutor) *BrowserShell {
	return &BrowserShell{exec: exec}
}

// Definition returns the tool schema exposed to the model.
func (s *BrowserShell) Definition() agent.ToolDefinition {
	return agent.ToolDefinition{
		Name:        "exec_browser_shell",
		Description: "Execute a browser-enabled command in the sandbox with Playwright, Puppeteer, Chromium, and optional Xvfb display support",
		PromptGuidance: []string{
			"Use for browser automation, screenshots, scraping, Playwright, Puppeteer, and browser tests.",
			"Prefer the built-in browser CLI commands first, such as `playwright screenshot`, `playwright pdf`, and `playwright test`, before writing custom scripts.",
			"Prefer `display_mode` `headless`; use `xvfb` only for headed browser commands that still terminate on their own.",
			"Avoid long-running interactive commands such as `playwright open` or `playwright codegen`; exec_browser_shell waits for the command to exit before returning.",
			"When runtime assumptions are uncertain, check `playwright --help` or `puppeteer --help` before assuming `node` or `python` are available.",
			"Use the nixpkgs-provided `playwright` and `puppeteer` wrappers, and do not rely on `playwright install` or `playwright install-deps` in the sandbox.",
			"Use exec_nix_shell_bash instead for ordinary non-browser CLI workflows.",
		},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"command": map[string]string{
					"type": "string",
				},
				"display_mode": map[string]any{
					"type":        "string",
					"description": "Browser display mode. Use headless by default and xvfb for headed browser sessions.",
					"enum":        []string{browserDisplayModeHeadless, browserDisplayModeXvfb},
				},
				"extra_packages": map[string]any{
					"type":        "array",
					"description": "Optional additional nix installables to add on top of the browser package preset.",
					"items": map[string]string{
						"type": "string",
					},
				},
			},
			"required": []string{"command"},
		},
	}
}

// Run executes one browser command from raw JSON tool arguments.
func (s *BrowserShell) Run(ctx context.Context, arguments string) (string, error) {
	var args struct {
		Command       string   `json:"command"`
		DisplayMode   string   `json:"display_mode"`
		ExtraPackages []string `json:"extra_packages"`
	}
	if err := json.Unmarshal([]byte(arguments), &args); err != nil {
		return "", fmt.Errorf("invalid arguments JSON: %w", err)
	}
	args.Command = strings.TrimSpace(args.Command)
	if args.Command == "" {
		return "", fmt.Errorf("missing required argument: command")
	}
	displayMode, err := normalizeBrowserDisplayMode(args.DisplayMode)
	if err != nil {
		return "", err
	}
	extraPackages, err := normalizeOptionalPackages(args.ExtraPackages, "extra_packages")
	if err != nil {
		return "", err
	}
	if s.exec == nil {
		return "", fmt.Errorf("no command executor configured")
	}

	return s.exec.ExecNixShellBash(
		ctx,
		buildBrowserShellCommand(args.Command, displayMode),
		mergeBrowserShellPackages(displayMode, extraPackages),
	)
}

func normalizeBrowserDisplayMode(raw string) (string, error) {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return browserDisplayModeHeadless, nil
	}
	switch raw {
	case browserDisplayModeHeadless, browserDisplayModeXvfb:
		return raw, nil
	default:
		return "", fmt.Errorf(
			"display_mode must be one of %q or %q",
			browserDisplayModeHeadless,
			browserDisplayModeXvfb,
		)
	}
}

func normalizeOptionalPackages(packages []string, field string) ([]string, error) {
	out := make([]string, 0, len(packages))
	for i, pkg := range packages {
		pkg = strings.TrimSpace(pkg)
		if pkg == "" {
			return nil, fmt.Errorf("%s[%d] must not be empty", field, i)
		}
		out = append(out, pkg)
	}
	return out, nil
}

func mergeBrowserShellPackages(displayMode string, extra []string) []string {
	capHint := len(browserShellPresetPackages) + len(extra)
	if displayMode == browserDisplayModeXvfb {
		capHint += len(browserShellXvfbPackages)
	}
	out := make([]string, 0, capHint)
	seen := make(map[string]struct{}, capHint)
	for _, pkg := range browserShellPresetPackages {
		if _, ok := seen[pkg]; ok {
			continue
		}
		seen[pkg] = struct{}{}
		out = append(out, pkg)
	}
	if displayMode == browserDisplayModeXvfb {
		for _, pkg := range browserShellXvfbPackages {
			if _, ok := seen[pkg]; ok {
				continue
			}
			seen[pkg] = struct{}{}
			out = append(out, pkg)
		}
	}
	for _, pkg := range extra {
		if _, ok := seen[pkg]; ok {
			continue
		}
		seen[pkg] = struct{}{}
		out = append(out, pkg)
	}
	return out
}

func buildBrowserShellCommand(command string, displayMode string) string {
	lines := []string{
		"export PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1",
		"export PUPPETEER_SKIP_DOWNLOAD=1",
		"export PLAYWRIGHT_HOST_PLATFORM_OVERRIDE=ubuntu-24.04",
	}
	if displayMode == browserDisplayModeHeadless {
		lines = append(lines, command)
		return strings.Join(lines, "\n")
	}

	lines = append(lines, browserShellXvfbCommand(command))
	return strings.Join(lines, "\n")
}

func browserShellXvfbCommand(command string) string {
	return strings.Join([]string{
		"/bin/bash -c",
		shellSingleQuote(strings.TrimSpace(browserShellXvfbTemplate)),
		shellSingleQuote("q15-browser-shell"),
		shellSingleQuote(command),
	}, " ")
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
