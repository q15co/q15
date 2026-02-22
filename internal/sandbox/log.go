package sandbox

import (
	"fmt"
	"os"
	"strings"
)

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
