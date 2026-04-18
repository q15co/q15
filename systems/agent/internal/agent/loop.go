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
)

const (
	defaultMaxTurns                  = 96
	defaultRecentTurns               = 6
	emptyResponseRetrySteeringPrompt = "Your previous response was empty. Return a non-empty result now: either call the required tool(s) immediately or provide a concise direct answer. Do not return an empty response."
	maxEmptyAssistantRetries         = 3
)

// Loop coordinates model calls, tool execution, and turn persistence.
type Loop struct {
	mu          sync.Mutex
	engine      *Engine
	store       ConversationStore
	systemText  string
	maxTurns    int
	recentTurns int
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
	systemText = strings.TrimSpace(systemText)
	if systemText == "" {
		systemText = DefaultSystemPrompt
	}
	if recentTurns == 0 {
		recentTurns = defaultRecentTurns
	}
	if planner == nil {
		planner = modelselection.Passthrough{}
	}
	engine := NewEngineWithPlanner(modelClient, planner, tools, modelRefs)
	return &Loop{
		engine:      engine,
		store:       store,
		systemText:  systemText,
		maxTurns:    engine.maxTurns,
		recentTurns: recentTurns,
	}
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
	l.mu.Lock()
	defer l.mu.Unlock()

	userMessage, err := normalizeUserMessage(userMessage)
	if err != nil {
		return ReplyResult{}, err
	}
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
	userMessage = withUserTemporalMetadata(
		userMessage,
		time.Now().In(time.Local),
		lastUserTimestamp,
		hasLastUserTimestamp,
	)

	emitRunEvent(ctx, observer, RunEvent{
		Type: RunEventRunStarted,
	})

	// Keep the canonical system prefix ordered from most stable to least stable
	// so providers can reuse cached prefixes across working-memory changes.
	systemMessages := []conversation.Message{systemMessage(l.systemText)}
	if l.store != nil {
		if coreStore, ok := l.store.(CoreMemoryStore); ok {
			coreMemory, err := coreStore.LoadCoreMemory(ctx)
			if err != nil {
				emitRunEvent(ctx, observer, RunEvent{
					Type: RunEventRunFailed,
					Err:  err,
				})
				return ReplyResult{}, fmt.Errorf("load core memory: %w", err)
			}
			if message, ok := injectCoreMemory(coreMemory); ok {
				systemMessages = append(systemMessages, message)
			}
		}
		if skillStore, ok := l.store.(SkillCatalogStore); ok {
			skillCatalog, err := skillStore.LoadSkillCatalog(ctx)
			if err != nil {
				emitRunEvent(ctx, observer, RunEvent{
					Type: RunEventRunFailed,
					Err:  err,
				})
				return ReplyResult{}, fmt.Errorf("load skill catalog: %w", err)
			}
			if message, ok := injectSkillCatalog(skillCatalog); ok {
				systemMessages = append(systemMessages, message)
			}
		}
		if workingStore, ok := l.store.(WorkingMemoryStore); ok {
			workingMemory, err := workingStore.LoadWorkingMemory(ctx)
			if err != nil {
				emitRunEvent(ctx, observer, RunEvent{
					Type: RunEventRunFailed,
					Err:  err,
				})
				return ReplyResult{}, fmt.Errorf("load working memory: %w", err)
			}
			if message, ok := injectWorkingMemory(workingMemory); ok {
				systemMessages = append(systemMessages, message)
			}
		}
	}

	var recentMessages []conversation.Message
	if l.store != nil {
		var err error
		recentMessages, err = l.store.LoadRecentMessages(ctx, l.recentTurns)
		if err != nil {
			emitRunEvent(ctx, observer, RunEvent{
				Type: RunEventRunFailed,
				Err:  err,
			})
			return ReplyResult{}, fmt.Errorf("load recent messages: %w", err)
		}
	}

	messages := make([]conversation.Message, 0, len(systemMessages)+len(recentMessages)+1)
	messages = append(messages, copyMessages(systemMessages)...)
	messages = append(messages, copyMessages(recentMessages)...)

	messages = append(messages, copyMessages([]conversation.Message{userMessage})...)
	result, err := l.engine.Run(ctx, EngineRequest{
		Messages: messages,
		UseTools: true,
		Observer: observer,
	})
	if err != nil {
		var stopErr *StopError
		if errors.As(err, &stopErr) {
			turnMessages := append(
				copyMessages([]conversation.Message{userMessage}),
				copyMessages(result.Messages)...,
			)
			if persistErr := l.persistTurn(ctx, turnMessages); persistErr != nil {
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
		copyMessages([]conversation.Message{userMessage}),
		copyMessages(result.Messages)...,
	)
	if err := l.persistTurn(ctx, turnMessages); err != nil {
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
	return ReplyResult{Text: result.FinalText}, nil
}

func (l *Loop) persistTurn(
	ctx context.Context,
	messages []conversation.Message,
) error {
	if l.store == nil {
		return nil
	}
	return l.store.AppendTurn(ctx, copyMessages(messages))
}
