// Package agent contains the core orchestration loop and contracts used by the
// runtime to talk to models, tools, and conversation persistence.
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/modelselection"
	"github.com/q15co/q15/systems/agent/internal/turnctx"
)

const (
	defaultMaxTurns                  = 96
	defaultRecentTurns               = 6
	emptyResponseRetrySteeringPrompt = "Your previous response was empty. Return a non-empty result now: either call the required tool(s) immediately or provide a concise direct answer. Do not return an empty response."
	maxEmptyAssistantRetries         = 3
)

// Loop coordinates model calls, tool execution, and turn persistence.
type Loop struct {
	mu             sync.Mutex
	engine         *Engine
	store          ConversationStore
	contextBuilder *ContextBuilder
	maxTurns       int
}

var _ Agent = (*Loop)(nil)

// NewLoop constructs a loop with defaults for prompt text, model refs, and
// recent history window size.
func NewLoop(
	modelClient ModelClient,
	tools ToolRegistry,
	modelRefs []string,
	systemText string,
	store ConversationStore,
	recentTurns int,
) *Loop {
	return NewLoopWithPlanner(
		modelClient,
		nil,
		tools,
		modelRefs,
		systemText,
		store,
		recentTurns,
	)
}

// NewLoopWithPlanner constructs a loop with an explicit model-selection
// planner. When planner is nil, the configured model refs are used as-is.
func NewLoopWithPlanner(
	modelClient ModelClient,
	planner modelselection.Planner,
	tools ToolRegistry,
	modelRefs []string,
	systemText string,
	store ConversationStore,
	recentTurns int,
) *Loop {
	return NewLoopWithPlannerAndModelRefSource(
		modelClient,
		planner,
		tools,
		StaticModelRefSource(modelRefs),
		systemText,
		store,
		recentTurns,
	)
}

// NewLoopWithPlannerAndModelRefSource constructs a loop with a dynamic model
// ref source and optional dynamic system prompt snippets.
func NewLoopWithPlannerAndModelRefSource(
	modelClient ModelClient,
	planner modelselection.Planner,
	tools ToolRegistry,
	modelRefSource ModelRefSource,
	systemText string,
	store ConversationStore,
	recentTurns int,
	systemTextSources ...SystemTextSource,
) *Loop {
	if planner == nil {
		planner = modelselection.Passthrough{}
	}
	engine := NewEngineWithPlannerAndModelRefSource(modelClient, planner, tools, modelRefSource)
	return &Loop{
		engine: engine,
		store:  store,
		contextBuilder: NewContextBuilder(
			systemText,
			store,
			recentTurns,
			systemTextSources...,
		),
		maxTurns: engine.maxTurns,
	}
}

func (l *Loop) renderSystemText(ctx context.Context) string {
	if l == nil || l.contextBuilder == nil {
		return DefaultSystemPrompt
	}
	return l.contextBuilder.SystemText(ctx)
}

func normalizeModelRefs(modelRefs []string) []string {
	if len(modelRefs) == 0 {
		return nil
	}

	out := make([]string, 0, len(modelRefs))
	seen := make(map[string]struct{}, len(modelRefs))
	for _, modelRef := range modelRefs {
		modelRef = strings.TrimSpace(modelRef)
		if modelRef == "" {
			continue
		}
		if _, ok := seen[modelRef]; ok {
			continue
		}
		seen[modelRef] = struct{}{}
		out = append(out, modelRef)
	}
	return out
}

// Reply runs one end-user turn: call model, execute tools as needed, and
// persist the resulting messages while optionally reporting progress events.
func (l *Loop) Reply(
	ctx context.Context,
	userMessage conversation.Message,
	observer RunObserver,
) (ReplyResult, error) {
	userMessage, err := normalizeUserMessage(userMessage)
	if err != nil {
		return ReplyResult{}, err
	}
	return l.run(ctx, []conversation.Message{userMessage}, observer)
}

// Continue runs an interactive turn triggered by a runtime event. The input
// stays distinct from user messages in both the prompt and the transcript.
func (l *Loop) Continue(
	ctx context.Context,
	parentTurn int64,
	messages []conversation.Message,
	observer RunObserver,
) (ReplyResult, error) {
	messages = conversation.NormalizeMessages(conversation.CloneMessages(messages))
	if len(messages) == 0 {
		return ReplyResult{}, errors.New("continuation input is required")
	}
	for _, message := range messages {
		if message.Role == conversation.UserRole {
			return ReplyResult{}, errors.New("continuation cannot impersonate a user message")
		}
	}
	return l.run(turnctx.WithParentTurn(ctx, parentTurn), messages, observer)
}

func (l *Loop) run(
	ctx context.Context,
	input []conversation.Message,
	observer RunObserver,
) (ReplyResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var err error
	recordedInput := input
	if input[0].Role != conversation.UserRole {
		// Runtime steering applies to this run; its tool results remain durable.
		recordedInput = nil
		for _, message := range input {
			if message.Role != conversation.SystemRole {
				recordedInput = append(recordedInput, message)
			}
		}
	}
	var turnSeq int64
	if l.store != nil {
		turnSeq, err = l.store.ReserveTurnSeq(ctx)
		if err != nil {
			emitRunEvent(ctx, observer, RunEvent{
				Type: RunEventRunFailed,
				Err:  err,
			})
			return ReplyResult{}, fmt.Errorf("reserve transcript turn sequence: %w", err)
		}
		observer = stampRunSeq(observer, turnSeq)
		ctx = turnctx.WithTurnSeq(ctx, turnSeq)
	}
	if input[0].Role == conversation.UserRole {
		lastUserTimestamp := time.Time{}
		hasLastUserTimestamp := false
		if l.store != nil {
			lastUserTimestamp, hasLastUserTimestamp, err = l.store.LoadLastUserTimestamp(ctx)
			if err != nil {
				emitRunEvent(ctx, observer, RunEvent{
					Type: RunEventRunFailed,
					Err:  err,
				})
				return ReplyResult{}, fmt.Errorf("load last user timestamp: %w", err)
			}
		}
		input[0] = withUserTemporalMetadata(
			input[0], time.Now().In(time.Local), lastUserTimestamp, hasLastUserTimestamp,
		)
	}

	emitRunEvent(ctx, observer, RunEvent{
		Type:       RunEventRunStarted,
		Background: input[0].Role != conversation.UserRole,
	})

	// Keep the canonical system prefix ordered from most stable to least stable
	// so providers can reuse cached prefixes across working-memory changes.
	prepared, err := l.contextBuilder.Prepare(ctx, input...)
	if err != nil {
		emitRunEvent(ctx, observer, RunEvent{
			Type: RunEventRunFailed,
			Err:  err,
		})
		return ReplyResult{}, err
	}
	result, err := l.engine.Run(ctx, EngineRequest{
		Messages:         prepared.Messages,
		ContextFitter:    prepared.FitRequest,
		UseTools:         true,
		SystemTextSource: l.renderSystemText,
		Observer:         observer,
	})
	if err != nil {
		var stopErr *StopError
		if errors.As(err, &stopErr) {
			turnMessages := append(
				copyMessages(recordedInput),
				copyMessages(result.Messages)...,
			)
			if persistErr := l.persistTurn(ctx, turnSeq, turnMessages); persistErr != nil {
				emitRunEvent(ctx, observer, RunEvent{
					Type:      RunEventRunFailed,
					Turn:      result.Turn,
					ModelRef:  result.ModelRef,
					FinalText: result.FinalText,
					Err:       persistErr,
				})
				return ReplyResult{}, fmt.Errorf("persist interrupted turn: %w", persistErr)
			}
		}
		emitRunEvent(ctx, observer, RunEvent{
			Type:      RunEventRunFailed,
			Turn:      result.Turn,
			ModelRef:  result.ModelRef,
			FinalText: result.FinalText,
			Err:       err,
		})
		return ReplyResult{}, err
	}

	turnMessages := append(
		copyMessages(recordedInput),
		copyMessages(result.Messages)...,
	)
	if err := l.persistTurn(ctx, turnSeq, turnMessages); err != nil {
		emitRunEvent(ctx, observer, RunEvent{
			Type:      RunEventRunFailed,
			Turn:      result.Turn,
			ModelRef:  result.ModelRef,
			FinalText: result.FinalText,
			Err:       err,
		})
		return ReplyResult{}, fmt.Errorf("persist turn: %w", err)
	}

	emitRunEvent(ctx, observer, RunEvent{
		Type:      RunEventRunFinished,
		Turn:      result.Turn,
		ModelRef:  result.ModelRef,
		FinalText: result.FinalText,
	})
	return ReplyResult{
		Text:        result.FinalText,
		Attachments: conversation.CloneParts(result.Attachments),
		MediaRefs:   append([]string(nil), result.MediaRefs...),
	}, nil
}

func (l *Loop) persistTurn(
	ctx context.Context,
	seq int64,
	messages []conversation.Message,
) error {
	if l.store == nil {
		return nil
	}
	return l.store.AppendTurnAtSeq(ctx, seq, copyMessages(messages))
}
