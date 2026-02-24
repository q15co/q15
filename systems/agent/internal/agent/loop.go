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
)

type Loop struct {
	mu          sync.Mutex
	modelClient ModelClient
	tools       ToolRegistry
	modelRefs   []string
	systemText  string
	maxTurns    int
	messages    []Message
}

var _ Agent = (*Loop)(nil)

func NewLoop(
	modelClient ModelClient,
	tools ToolRegistry,
	modelRefs []string,
	systemText string,
) *Loop {
	systemText = strings.TrimSpace(systemText)
	if systemText == "" {
		systemText = DefaultSystemPrompt
	}
	modelRefs = normalizeModelRefs(modelRefs)
	return &Loop{
		modelClient: modelClient,
		tools:       tools,
		modelRefs:   modelRefs,
		systemText:  systemText,
		maxTurns:    defaultMaxTurns,
		messages: []Message{
			{Role: SystemRole, Content: systemText},
		},
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

	turnStart := len(l.messages)
	l.messages = append(l.messages, Message{
		Role:    UserRole,
		Content: userInput,
	})

	var toolDefs []ToolDefinition
	if l.tools != nil {
		toolDefs = l.tools.Definitions()
	}

	for turn := 0; turn < l.maxTurns; turn++ {
		result, err := l.complete(ctx, l.messages, toolDefs)
		if err != nil {
			l.messages = l.messages[:turnStart]
			return "", fmt.Errorf("model complete: %w", err)
		}

		assistantMsg := Message{
			Role:        AssistantRole,
			Content:     strings.TrimSpace(result.Content),
			ToolCalls:   result.ToolCalls,
			ProviderRaw: result.ProviderRaw,
		}
		l.messages = append(l.messages, assistantMsg)

		if len(result.ToolCalls) == 0 {
			answer := strings.TrimSpace(result.Content)
			if answer == "" {
				answer = "(assistant returned no text)"
			}
			return answer, nil
		}

		for _, call := range result.ToolCalls {
			output, err := l.runTool(ctx, call)
			if err != nil {
				output = "tool error: " + err.Error()
			}
			l.messages = append(l.messages, Message{
				Role:       ToolRole,
				Content:    output,
				ToolCallID: call.ID,
			})
		}
	}

	l.messages = l.messages[:turnStart]
	return "", fmt.Errorf("max tool-call turns reached (%d)", l.maxTurns)
}

func (l *Loop) Reset(ctx context.Context) error {
	_ = ctx
	l.mu.Lock()
	defer l.mu.Unlock()

	l.messages = []Message{
		{Role: SystemRole, Content: l.systemText},
	}
	return nil
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
