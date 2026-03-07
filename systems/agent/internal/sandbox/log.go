package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// VerboseEnabled reports whether sandbox debug logging is enabled.
func VerboseEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("Q15_SANDBOX_VERBOSE"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func verbosef(format string, args ...any) {
	if !VerboseEnabled() {
		return
	}
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stdout, "[sandbox pid=%d] %s\n", os.Getpid(), msg)
}

func logExecNixShellBashRequest(
	containerName string,
	req ExecNixShellBashRequest,
) {
	fmt.Fprintf(
		os.Stdout,
		"[sandbox exec_nix_shell_bash] event=request container=%s command=%s packages=%s\n",
		jsonString(containerName),
		jsonString(req.Command),
		jsonValue(req.Packages),
	)
}

func logExecNixShellBashFailure(
	containerName string,
	req ExecNixShellBashRequest,
	err error,
) {
	if err == nil {
		return
	}
	fmt.Fprintf(
		os.Stdout,
		"[sandbox exec_nix_shell_bash] event=failure container=%s command=%s packages=%s error=%s\n",
		jsonString(containerName),
		jsonString(req.Command),
		jsonValue(req.Packages),
		jsonString(err.Error()),
	)
}

func jsonString(value string) string {
	return jsonValue(value)
}

func jsonValue(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `"<json-encode-error>"`
	}
	return string(encoded)
}
