// Package agent contains the core orchestration loop and contracts used by the
// runtime to talk to models, tools, and conversation persistence.
package agent

import (
	"context"
	"time"
)

// RunEventType identifies a progress event emitted by the loop.
type RunEventType string

// Run event types emitted by the orchestration loop.
const (
	RunEventRunStarted       RunEventType = "run_started"
	RunEventModelTurnStarted RunEventType = "model_turn_started"
	RunEventToolStarted      RunEventType = "tool_started"
	RunEventToolFinished     RunEventType = "tool_finished"
	RunEventRunFinished      RunEventType = "run_finished"
	RunEventRunFailed        RunEventType = "run_failed"
)

// RunEvent reports loop progress in a transport-owned, model-agnostic format.
type RunEvent struct {
	Type       RunEventType
	At         time.Time
	Turn       int
	ModelRef   string
	ToolCall   ToolCall
	ToolOutput string
	FinalText  string
	Err        error
}

// RunObserver receives structured loop progress events.
type RunObserver interface {
	OnRunEvent(ctx context.Context, event RunEvent)
}

// RunObserverFunc adapts a function to RunObserver.
type RunObserverFunc func(context.Context, RunEvent)

// OnRunEvent implements RunObserver.
func (f RunObserverFunc) OnRunEvent(ctx context.Context, event RunEvent) {
	if f == nil {
		return
	}
	f(ctx, event)
}

func emitRunEvent(ctx context.Context, observer RunObserver, event RunEvent) {
	if observer == nil {
		return
	}
	if event.At.IsZero() {
		event.At = time.Now()
	}
	observer.OnRunEvent(ctx, event)
}
