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
	modelName  string
	systemText string
	maxTurns   int
	messages   []Message
}

var _ Agent = (*Loop)(nil)

func NewLoop(model Model, tools ToolRunner, modelName, systemText string) *Loop {
	systemText = strings.TrimSpace(systemText)
	if systemText == "" {
		systemText = DefaultSystemPrompt
	}
	return &Loop{
		model:      model,
		tools:      tools,
		modelName:  modelName,
		systemText: systemText,
		maxTurns:   defaultMaxTurns,
		messages: []Message{
			{Role: SystemRole, Content: systemText},
		},
	}
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
		result, err := l.model.Complete(ctx, l.modelName, l.messages, toolDefs)
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
