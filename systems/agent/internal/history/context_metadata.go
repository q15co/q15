package history

import (
	"fmt"
	"strconv"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func withContextWindow(
	messages []conversation.Message,
	tools []agent.ToolDefinition,
	model agent.ModelContext,
	limit int,
) ([]conversation.Message, int, error) {
	if len(messages) == 0 || messages[0].Role != conversation.SystemRole {
		messages = append([]conversation.Message{conversation.SystemMessage("")}, messages...)
	}
	partIndex := -1
	for n, part := range messages[0].Parts {
		if part.Type == conversation.TextPartType {
			partIndex = n
			break
		}
	}
	if partIndex < 0 {
		partIndex = len(messages[0].Parts)
		messages[0].Parts = append(messages[0].Parts, conversation.Text("", ""))
	}
	prefix := messages[0].Parts[partIndex].Text
	estimate := 0
	for range 8 {
		messages[0].Parts[partIndex].Text = prefix + "\n\n" + renderContextWindow(
			model,
			limit,
			estimate,
		)
		actual, err := agent.RequestEstimatedTokens(messages, tools)
		if err != nil {
			return nil, 0, err
		}
		if actual <= estimate {
			return messages, estimate, nil
		}
		// The counter is part of the measured request. A small allowance keeps
		// changing counter digits from making the displayed estimate too small.
		estimate = actual + 8
	}
	return nil, 0, fmt.Errorf("context token metadata did not converge")
}

func renderContextWindow(model agent.ModelContext, limit, estimate int) string {
	attributes := map[string]string{
		"model_ref":               model.Ref,
		"estimated_input_tokens":  strconv.Itoa(estimate),
		"working_budget_tokens":   strconv.Itoa(limit),
		"remaining_budget_tokens": strconv.Itoa(max(0, limit-estimate)),
		"model_context_tokens":    "unknown",
		"output_reserve_tokens":   "unknown",
		"safety_reserve_tokens":   "unknown",
		"estimator":               "o200k_base_margin25",
	}
	if model.Ref == "" {
		attributes["model_ref"] = "unknown"
	}
	if model.MaxContextTokens > 0 {
		attributes["model_context_tokens"] = strconv.Itoa(model.MaxContextTokens)
		attributes["output_reserve_tokens"] = strconv.Itoa(outputReserve(model))
		attributes["safety_reserve_tokens"] = strconv.Itoa(max(1, model.MaxContextTokens/20))
	}
	return agent.RenderPromptElement(
		"context_window",
		attributes,
		"Input counts estimate the complete request, including instructions, history, the active conversation, tool definitions and this tag. Historical estimated_tokens counts describe the shown turn content. Remaining space is under the working input budget; the output reserve is separate. Counts are conservative estimates and provider usage may differ. These counters describe this model call.",
	)
}

func outputReserve(model agent.ModelContext) int {
	reserve := min(8192, model.MaxContextTokens/4)
	if model.MaxOutputTokens > 0 {
		reserve = min(reserve, model.MaxOutputTokens)
	}
	return reserve
}
