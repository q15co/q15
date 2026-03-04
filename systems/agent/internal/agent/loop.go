package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

const (
	defaultPromptBody   = "an autonomous shell-capable assistant running for the user in a sandboxed environment. Prioritize doing over announcing intent: proactively execute tasks with the available tools and continue until the task is complete or you are genuinely blocked. Ask clarifying questions only when the goal or constraints are ambiguous after reasonable attempts, and ask for confirmation only before high-risk or irreversible actions. Do not request extra authorization for routine, user-requested reads/writes in the workspace or /memory paths. Use the available tools effectively and adapt to the sandbox environment described in the system prompt. Do not claim to be Claude, Anthropic, or any specific vendor/model unless that identity is explicitly provided in this conversation."
	defaultPromptFormat = "You are %s, " + defaultPromptBody
	// DefaultSystemPrompt is used when no explicit system prompt is configured.
	DefaultSystemPrompt              = "You are " + defaultPromptBody
	defaultMaxTurns                  = 96
	defaultRecentTurns               = 6
	toolExecutionSteeringPrompt      = "When tools are available and the user asks for an action, call the relevant tool(s) immediately instead of narrating intent. Avoid planning-only replies like \"I'll do that\" without tool calls. Do not ask for extra authorization for routine user-requested reads/writes in the workspace or /memory paths. Ask confirmation only for destructive or irreversible actions. If clarification is genuinely required, ask one concise question."
	emptyResponseRetrySteeringPrompt = "Your previous response was empty. Return a non-empty result now: either call the required tool(s) immediately or provide a concise direct answer. Do not return an empty response."
	maxEmptyAssistantRetries         = 3
)

// DefaultSystemPromptForName returns the default prompt with a concrete agent
// name injected. It panics when name is empty.
func DefaultSystemPromptForName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		panic("agent name is required")
	}
	return fmt.Sprintf(defaultPromptFormat, name)
}

// Loop coordinates model calls, tool execution, and turn persistence.
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

// Reply runs one end-user turn: call model, execute tools as needed, and
// persist the resulting messages.
func (l *Loop) Reply(ctx context.Context, userInput string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	userInput = strings.TrimSpace(userInput)
	if userInput == "" {
		return "", fmt.Errorf("empty user input")
	}

	systemText := l.systemText
	if l.store != nil {
		if coreStore, ok := l.store.(CoreMemoryStore); ok {
			coreMemory, err := coreStore.LoadCoreMemory(ctx)
			if err != nil {
				return "", fmt.Errorf("load core memory: %w", err)
			}
			systemText = injectCoreMemory(systemText, coreMemory)
		}
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
	messages = append(messages, Message{Role: SystemRole, Content: systemText})
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
	loopDetector := newToolLoopDetector()
	emptyAssistantRetries := 0

	for turn := 0; turn < l.maxTurns; turn++ {
		requestMessages := copyMessages(messages)
		if len(toolDefs) > 0 || emptyAssistantRetries > 0 {
			requestMessages = append(requestMessages, Message{
				Role:    SystemRole,
				Content: toolExecutionSteeringPrompt,
			})
		}
		if emptyAssistantRetries > 0 {
			requestMessages = append(requestMessages, Message{
				Role:    SystemRole,
				Content: emptyResponseRetrySteeringPrompt,
			})
		}
		result, err := l.complete(ctx, requestMessages, toolDefs)
		if err != nil {
			return "", fmt.Errorf("model complete: %w", err)
		}

		assistantContent := strings.TrimSpace(result.Content)
		if len(result.ToolCalls) == 0 && assistantContent == "" &&
			emptyAssistantRetries < maxEmptyAssistantRetries {
			emptyAssistantRetries++
			continue
		}
		emptyAssistantRetries = 0

		assistantMsg := Message{
			Role:        AssistantRole,
			Content:     assistantContent,
			ToolCalls:   result.ToolCalls,
			ProviderRaw: result.ProviderRaw,
		}
		messages = append(messages, assistantMsg)

		if len(result.ToolCalls) == 0 {
			answer := assistantContent
			if answer == "" {
				answer = "(assistant returned no text)"
			}
			if err := l.persistTurn(ctx, messages, turnStart); err != nil {
				return "", fmt.Errorf("persist turn: %w", err)
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

			if assessment := loopDetector.Record(call, output); assessment.Critical {
				stopSummary := formatStopSummary(
					StopReasonToolLoopDetected,
					l.maxTurns,
					assessment.RepeatCount,
					assessment.NoProgressCount,
				)
				messages = append(messages, Message{
					Role:    AssistantRole,
					Content: stopSummary,
				})
				if err := l.persistTurn(ctx, messages, turnStart); err != nil {
					return "", fmt.Errorf("persist interrupted turn: %w", err)
				}
				return "", &StopError{
					Reason: StopReasonToolLoopDetected,
					Detail: fmt.Sprintf(
						"tool loop detected (repeat=%d, no_progress=%d)",
						assessment.RepeatCount,
						assessment.NoProgressCount,
					),
				}
			}
		}
	}

	stopSummary := formatStopSummary(
		StopReasonToolTurnLimit,
		l.maxTurns,
		loopDetector.MaxRepeatCount(),
		loopDetector.MaxNoProgressCount(),
	)
	messages = append(messages, Message{
		Role:    AssistantRole,
		Content: stopSummary,
	})
	if err := l.persistTurn(ctx, messages, turnStart); err != nil {
		return "", fmt.Errorf("persist interrupted turn: %w", err)
	}
	return "", &StopError{
		Reason: StopReasonToolTurnLimit,
		Detail: fmt.Sprintf("max tool-call turns reached (%d)", l.maxTurns),
	}
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

func (l *Loop) persistTurn(ctx context.Context, messages []Message, turnStart int) error {
	if l.store == nil {
		return nil
	}
	return l.store.AppendTurn(ctx, copyMessages(messages[turnStart:]))
}
