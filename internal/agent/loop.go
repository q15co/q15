package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

const (
	DefaultSystemPrompt = "You are a helpful assistant with excellent skills in using nixos and the fish shell"
	defaultMaxTurns     = 12
)

type Loop struct {
	mu         sync.Mutex
	model      Model
	tools      ToolRunner
	models     []string
	systemText string
	maxTurns   int
	messages   []Message
}

var _ Agent = (*Loop)(nil)

func NewLoop(model Model, tools ToolRunner, models []string, systemText string) *Loop {
	systemText = strings.TrimSpace(systemText)
	if systemText == "" {
		systemText = DefaultSystemPrompt
	}
	models = normalizeModels(models)
	return &Loop{
		model:      model,
		tools:      tools,
		models:     models,
		systemText: systemText,
		maxTurns:   defaultMaxTurns,
		messages: []Message{
			{Role: SystemRole, Content: systemText},
		},
	}
}

func normalizeModels(models []string) []string {
	if len(models) == 0 {
		return []string{"kimi-k2.5"}
	}

	out := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	if len(out) == 0 {
		return []string{"kimi-k2.5"}
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
		return "", fmt.Errorf("no tool runner configured for call %q", call.Name)
	}
	if strings.TrimSpace(call.ID) == "" {
		return "", fmt.Errorf("tool call is missing id")
	}
	return l.tools.Run(ctx, call)
}

func (l *Loop) complete(ctx context.Context, messages []Message, tools []ToolDefinition) (ModelResult, error) {
	var lastErr error

	for _, model := range l.models {
		result, err := l.model.Complete(ctx, model, messages, tools)
		if err == nil {
			return result, nil
		}
		lastErr = err
	}

	if lastErr == nil {
		return ModelResult{}, fmt.Errorf("no models configured")
	}
	return ModelResult{}, fmt.Errorf("all models failed (%v): %w", l.models, lastErr)
}
