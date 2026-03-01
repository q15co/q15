package agent

import "fmt"

type StopReason string

const (
	StopReasonToolTurnLimit    StopReason = "tool_turn_limit"
	StopReasonToolLoopDetected StopReason = "tool_loop_detected"
)

type StopError struct {
	Reason StopReason
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
