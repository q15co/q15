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
	RunEventRunStarted          RunEventType = "run_started"
	RunEventModelTurnStarted    RunEventType = "model_turn_started"
	RunEventModelTurnDelta      RunEventType = "model_turn_delta"
	RunEventModelReasoningDelta RunEventType = "model_reasoning_delta"
	RunEventToolStarted         RunEventType = "tool_started"
	RunEventToolFinished        RunEventType = "tool_finished"
	RunEventRunFinished         RunEventType = "run_finished"
	RunEventRunFailed           RunEventType = "run_failed"
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
	// Seq is the durable transcript turn sequence this run owns, reserved when the
	// run starts and written to the transcript when it finishes. It is zero when no
	// transcript store is attached. It is not Turn, which is the engine's per-run
	// loop counter.
	Seq int64
	// Delta is incremental assistant content for ModelTurnDelta or provider
	// reasoning text for ModelReasoningDelta. ModelTurnStarted begins a new attempt;
	// subscribers should replace previous answer and reasoning drafts there.
	Delta string
	Err   error
}

// RunObserver receives structured loop progress events synchronously, in order.
// Text deltas are raw and unthrottled; subscribers should coalesce rendering
// work in bounded storage. The engine never queues events or spawns per-event work.
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

// seqStampingObserver stamps one run's reserved transcript sequence onto every
// event, so that no emitter has to remember to set RunEvent.Seq.
type seqStampingObserver struct {
	inner RunObserver
	seq   int64
}

func (o seqStampingObserver) OnRunEvent(ctx context.Context, event RunEvent) {
	event.Seq = o.seq
	o.inner.OnRunEvent(ctx, event)
}

// stampRunSeq returns observer with every event's Seq set to seq. A nil
// observer is returned unchanged, because there is nothing to forward to.
func stampRunSeq(observer RunObserver, seq int64) RunObserver {
	if observer == nil {
		return observer
	}
	return seqStampingObserver{inner: observer, seq: seq}
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
