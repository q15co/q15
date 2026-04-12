package agent

import (
	"fmt"
	"strings"
)

// StopReason classifies why the loop ended without a normal assistant answer.
type StopReason string

const (
	// StopReasonToolTurnLimit indicates the loop hit the configured maximum number
	// of tool-call turns.
	StopReasonToolTurnLimit StopReason = "tool_turn_limit"
	// StopReasonToolLoopDetected indicates repeated tool calls were detected with
	// no meaningful progress.
	StopReasonToolLoopDetected StopReason = "tool_loop_detected"
)

// StopError reports a controlled loop stop condition.
type StopError struct {
	// Reason is the machine-readable stop classification.
	Reason StopReason
	// Detail is a human-readable explanation of the stop condition.
	Detail string
}

// ModelAttemptFailure records one failed model attempt during deterministic
// fallback execution.
type ModelAttemptFailure struct {
	ModelRef string
	Err      error
}

// ModelFallbackError reports that every eligible model failed during one turn.
type ModelFallbackError struct {
	EligibleRefs    []string
	AttemptFailures []ModelAttemptFailure
}

func (e *StopError) Error() string {
	if e == nil {
		return ""
	}
	if e.Detail == "" {
		return fmt.Sprintf("agent stopped (%s)", e.Reason)
	}
	return e.Detail
}

func (e *ModelFallbackError) Error() string {
	if e == nil {
		return ""
	}

	parts := make([]string, 0, len(e.AttemptFailures))
	for _, failure := range e.AttemptFailures {
		modelRef := strings.TrimSpace(failure.ModelRef)
		switch {
		case modelRef != "" && failure.Err != nil:
			parts = append(parts, fmt.Sprintf("%s: %v", modelRef, failure.Err))
		case modelRef != "":
			parts = append(parts, modelRef)
		case failure.Err != nil:
			parts = append(parts, failure.Err.Error())
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("all models failed (%v)", e.EligibleRefs)
	}
	return fmt.Sprintf(
		"all models failed (%v): %s",
		e.EligibleRefs,
		strings.Join(parts, "; "),
	)
}

func (e *ModelFallbackError) Unwrap() error {
	if e == nil {
		return nil
	}
	for i := len(e.AttemptFailures) - 1; i >= 0; i-- {
		if e.AttemptFailures[i].Err != nil {
			return e.AttemptFailures[i].Err
		}
	}
	return nil
}
