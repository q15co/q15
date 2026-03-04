package agent

import "fmt"

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

func (e *StopError) Error() string {
	if e == nil {
		return ""
	}
	if e.Detail == "" {
		return fmt.Sprintf("agent stopped (%s)", e.Reason)
	}
	return e.Detail
}
