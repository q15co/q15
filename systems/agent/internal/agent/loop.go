package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

const (
	DefaultSystemPrompt = "You are q15, a helpful shell-capable assistant running for the user in a sandboxed environment. Use the available tools effectively and adapt to the sandbox environment described in the system prompt. Do not claim to be Claude, Anthropic, or any specific vendor/model unless that identity is explicitly provided in this conversation."
	defaultMaxTurns     = 12
	defaultRecentTurns  = 6
)

type Loop struct {
	mu          sync.Mutex
	modelClient ModelClient
	tools       ToolRegistry
	store       ConversationStore
	modelRefs   []string
	systemText  string
	maxTurns    int
	recentTurns int
}

var _ Agent = (*Loop)(nil)

func NewLoop(
	modelClient ModelClient,
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
	modelRefs = normalizeModelRefs(modelRefs)
	return &Loop{
		modelClient: modelClient,
		tools:       tools,
		store:       store,
		modelRefs:   modelRefs,
		systemText:  systemText,
		maxTurns:    defaultMaxTurns,
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

func (l *Loop) Reply(ctx context.Context, userInput string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	userInput = strings.TrimSpace(userInput)
	if userInput == "" {
		return "", fmt.Errorf("empty user input")
	}

	var recentMessages []Message
	if l.store != nil {
		var err error
		recentMessages, err = l.store.LoadRecentMessages(ctx, l.recentTurns)
		if err != nil {
			return "", fmt.Errorf("load recent messages: %w", err)
		}
	}

	messages := make([]Message, 0, 2+len(recentMessages))
	messages = append(messages, Message{Role: SystemRole, Content: l.systemText})
	messages = append(messages, copyMessages(recentMessages)...)

	turnStart := len(messages)
	messages = append(messages, Message{
		Role:    UserRole,
		Content: userInput,
	})

	var toolDefs []ToolDefinition
	if l.tools != nil {
		toolDefs = l.tools.Definitions()
	}

	for turn := 0; turn < l.maxTurns; turn++ {
		result, err := l.complete(ctx, messages, toolDefs)
		if err != nil {
			return "", fmt.Errorf("model complete: %w", err)
		}

		assistantMsg := Message{
			Role:        AssistantRole,
			Content:     strings.TrimSpace(result.Content),
			ToolCalls:   result.ToolCalls,
			ProviderRaw: result.ProviderRaw,
		}
		messages = append(messages, assistantMsg)

		if len(result.ToolCalls) == 0 {
			answer := strings.TrimSpace(result.Content)
			if answer == "" {
				answer = "(assistant returned no text)"
			}
			if l.store != nil {
				if err := l.store.AppendTurn(ctx, copyMessages(messages[turnStart:])); err != nil {
					return "", fmt.Errorf("persist turn: %w", err)
				}
			}
			return answer, nil
		}

		for _, call := range result.ToolCalls {
			output, err := l.runTool(ctx, call)
			if err != nil {
				output = "tool error: " + err.Error()
			}
			messages = append(messages, Message{
				Role:       ToolRole,
				Content:    output,
				ToolCallID: call.ID,
			})
		}
	}

	return "", fmt.Errorf("max tool-call turns reached (%d)", l.maxTurns)
}

func (l *Loop) runTool(ctx context.Context, call ToolCall) (string, error) {
	if l.tools == nil {
		return "", fmt.Errorf("no tool registry configured for call %q", call.Name)
	}
	if strings.TrimSpace(call.ID) == "" {
		return "", fmt.Errorf("tool call is missing id")
	}
	return l.tools.Run(ctx, call)
}

func (l *Loop) complete(
	ctx context.Context,
	messages []Message,
	tools []ToolDefinition,
) (ModelClientResult, error) {
	var lastErr error

	for _, modelRef := range l.modelRefs {
		result, err := l.modelClient.Complete(ctx, modelRef, messages, tools)
		if err == nil {
			return result, nil
		}
		lastErr = err
	}

	if lastErr == nil {
		return ModelClientResult{}, fmt.Errorf("no models configured")
	}
	return ModelClientResult{}, fmt.Errorf("all models failed (%v): %w", l.modelRefs, lastErr)
}

func copyMessages(in []Message) []Message {
	if len(in) == 0 {
		return nil
	}

	out := make([]Message, len(in))
	for i, msg := range in {
		out[i] = msg
		if len(msg.ToolCalls) > 0 {
			out[i].ToolCalls = append([]ToolCall(nil), msg.ToolCalls...)
		}
		if len(msg.ProviderRaw) > 0 {
			out[i].ProviderRaw = append([]byte(nil), msg.ProviderRaw...)
		}
	}
	return out
}
