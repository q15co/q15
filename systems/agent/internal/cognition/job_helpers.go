package cognition

import (
	"context"
	"strings"
)

const cognitionSummaryLimit = 200

// stallDegradedFailures is the consecutive-failure count from which a
// checkpoint-advancing job halves its transcript window for the next run.
const stallDegradedFailures = 3

// degradedWindowTurns returns the transcript window size one
// checkpoint-advancing job should request for this run: its configured default
// window, halved while the job's persisted state shows at least
// stallDegradedFailures consecutive failures. The decision lives in the job
// because only the job knows what a smaller window costs; the controller stays
// free of per-job policy.
func degradedWindowTurns(
	ctx context.Context,
	loader ContextLoader,
	jobType string,
	defaultTurns int,
) (int, error) {
	state, err := loader.LoadJobState(ctx, jobType)
	if err != nil {
		return 0, err
	}
	if state.ConsecutiveFailures >= stallDegradedFailures && defaultTurns > 1 {
		return defaultTurns / 2, nil
	}
	return defaultTurns, nil
}

func accessIncludesPath(paths []string, target string) bool {
	for _, path := range paths {
		if path == target {
			return true
		}
	}
	return false
}

func compactCognitionText(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= cognitionSummaryLimit {
		return text
	}
	return string(runes[:cognitionSummaryLimit-3]) + "..."
}
