package tools

import (
	"context"
	"strings"
	"testing"
)

func TestBrowserShellRunRejectsMissingCommand(t *testing.T) {
	shell := NewBrowserShell(&recordingExec{})

	_, err := shell.Run(context.Background(), `{}`)
	if err == nil {
		t.Fatal("expected missing command error")
	}
	if !strings.Contains(err.Error(), "missing required argument: command") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBrowserShellRunRejectsInvalidDisplayMode(t *testing.T) {
	shell := NewBrowserShell(&recordingExec{})

	_, err := shell.Run(context.Background(), `{"command":"playwright test","display_mode":"gui"}`)
	if err == nil {
		t.Fatal("expected invalid display_mode error")
	}
	if !strings.Contains(err.Error(), "display_mode must be one of") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBrowserShellRunRejectsEmptyExtraPackageEntries(t *testing.T) {
	shell := NewBrowserShell(&recordingExec{})

	_, err := shell.Run(
		context.Background(),
		`{"command":"playwright test","extra_packages":["nixpkgs#jq"," "]}`,
	)
	if err == nil {
		t.Fatal("expected empty extra_packages error")
	}
	if !strings.Contains(err.Error(), "extra_packages[1] must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBrowserShellRunDefaultsToHeadlessAndForwardsPresetPackages(t *testing.T) {
	rec := &recordingExec{}
	shell := NewBrowserShell(rec)

	out, err := shell.Run(context.Background(), `{"command":"playwright test"}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if out != "ok" {
		t.Fatalf("Run() output = %q, want %q", out, "ok")
	}
	if got, want := rec.lastPackages, mergeBrowserShellPackages(browserDisplayModeHeadless, nil); !equalStringSlices(
		got,
		want,
	) {
		t.Fatalf("forwarded packages = %#v, want %#v", got, want)
	}
	for _, want := range []string{
		"export PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1",
		"export PUPPETEER_SKIP_DOWNLOAD=1",
		"export PLAYWRIGHT_HOST_PLATFORM_OVERRIDE=ubuntu-24.04",
		"playwright test",
	} {
		if !strings.Contains(rec.lastCommand, want) {
			t.Fatalf("forwarded command missing %q:\n%s", want, rec.lastCommand)
		}
	}
	if strings.Contains(rec.lastCommand, "dbus-run-session -- /bin/bash -lc") {
		t.Fatalf("headless command unexpectedly wrapped with xvfb:\n%s", rec.lastCommand)
	}
}

func TestBrowserShellDefinitionGuidesCliFirstAndRuntimeChecks(t *testing.T) {
	def := NewBrowserShell(nil).Definition()

	for _, want := range []string{
		"Prefer the built-in browser CLI commands first",
		"Prefer `display_mode` `headless`",
		"Avoid long-running interactive commands such as `playwright open` or `playwright codegen`",
		"check `playwright --help` or `puppeteer --help`",
		"do not rely on `playwright install` or `playwright install-deps`",
	} {
		if !containsString(def.PromptGuidance, want) {
			t.Fatalf("PromptGuidance missing %q: %#v", want, def.PromptGuidance)
		}
	}
}

func TestBrowserShellRunMergesAndDedupesExtraPackages(t *testing.T) {
	rec := &recordingExec{}
	shell := NewBrowserShell(rec)

	_, err := shell.Run(
		context.Background(),
		`{"command":"playwright test","extra_packages":["nixpkgs#jq","nixpkgs#chromium","nixpkgs#jq"]}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := append(
		append([]string(nil), mergeBrowserShellPackages(browserDisplayModeHeadless, nil)...),
		"nixpkgs#jq",
	)
	if got := rec.lastPackages; !equalStringSlices(got, want) {
		t.Fatalf("forwarded packages = %#v, want %#v", got, want)
	}
}

func TestBrowserShellRunWrapsXvfbDisplayMode(t *testing.T) {
	rec := &recordingExec{}
	shell := NewBrowserShell(rec)

	_, err := shell.Run(
		context.Background(),
		`{"command":"playwright open https://example.com","display_mode":"xvfb"}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	for _, want := range []string{
		"/bin/bash -c",
		`echo "dbus-daemon not found in browser runtime" >&2`,
		`dbus_prefix=$(dirname "$(dirname "$dbus_daemon")")`,
		`echo "dbus session config not found: $dbus_session_conf" >&2`,
		"dbus-daemon --config-file=\"$dbus_session_conf\" --fork --print-address=1 --print-pid=3",
		"export DBUS_SESSION_BUS_ADDRESS=\"$dbus_address\"",
		"export XDG_RUNTIME_DIR=\"$runtime_dir\"",
		"Xvfb \"$DISPLAY\" -screen 0 1280x720x24 -nolisten tcp",
		"export DISPLAY=\":${display_num}\"",
		"trap cleanup EXIT INT TERM",
		"playwright open https://example.com",
	} {
		if !strings.Contains(rec.lastCommand, want) {
			t.Fatalf("wrapped command missing %q:\n%s", want, rec.lastCommand)
		}
	}
	if strings.Contains(rec.lastCommand, "dbus-run-session -- /bin/bash -lc") {
		t.Fatalf("xvfb command unexpectedly relies on dbus-run-session:\n%s", rec.lastCommand)
	}
	if strings.Contains(rec.lastCommand, "/bin/bash -lc") {
		t.Fatalf(
			"xvfb command unexpectedly uses login shell and may reset PATH:\n%s",
			rec.lastCommand,
		)
	}
	if got, want := rec.lastPackages, mergeBrowserShellPackages(browserDisplayModeXvfb, nil); !equalStringSlices(
		got,
		want,
	) {
		t.Fatalf("forwarded xvfb packages = %#v, want %#v", got, want)
	}
}

func equalStringSlices(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}
