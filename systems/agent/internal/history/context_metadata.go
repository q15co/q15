package history

import (
	"math"
	"strconv"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/buildinfo"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func withContextWindow(
	messages []conversation.Message,
	tools []agent.ToolDefinition,
	model agent.ModelContext,
	limit int,
	tail ...conversation.Message,
) ([]conversation.Message, int, error) {
	base, err := agent.RequestEstimatedTokens(append(messages, tail...), tools)
	if err != nil {
		return nil, 0, err
	}
	// Reserve the whole standalone note, including its framing, plus slack for
	// digit tokenization. The full request is counted once, without a fixed point.
	allowance, err := agent.RequestEstimatedTokens([]conversation.Message{
		conversation.UserMessage(renderContextWindow(model, limit, math.MaxInt)),
	}, nil)
	if err != nil {
		return nil, 0, err
	}
	estimate := base + allowance + 64
	result := append(
		messages,
		conversation.UserMessage(renderContextWindow(model, limit, estimate)),
	)
	return append(result, conversation.CloneMessages(tail)...), estimate, nil
}

func renderContextWindow(model agent.ModelContext, limit, estimate int) string {
	build := buildinfo.Current()
	attributes := map[string]string{
		"git_commit":              build.Commit,
		"source_modified":         build.Modified,
		"build_provenance":        build.Source,
		"model_ref":               model.Ref,
		"estimated_input_tokens":  strconv.Itoa(estimate),
		"working_budget_tokens":   strconv.Itoa(limit),
		"remaining_budget_tokens": strconv.Itoa(max(0, limit-estimate)),
		"model_context_tokens":    "unknown",
		"output_reserve_tokens":   "unknown",
		"safety_reserve_tokens":   "unknown",
		"estimator":               agent.RequestTokenEstimator,
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
		"Current runtime state for this model call. The build identifies the running executable. Use this note for the current build and token budget; version claims and counters in conversation history describe earlier requests. Input counts include instructions, history, the active conversation, tools and an allowance for this note. Remaining space is under the usable model context window; output reserve is separate. The initial context assembly budget only controls starting history and leaves the remaining window available for ongoing work and tool results. This is request capacity, not a cumulative token-spending quota. Counts are conservative estimates; UTF-8 bytes replace tokenization if unavailable. Provider usage may differ. Historical estimated_tokens counts describe the shown turn content.",
	)
}

func outputReserve(model agent.ModelContext) int {
	reserve := min(8192, model.MaxContextTokens/4)
	if model.MaxOutputTokens > 0 {
		reserve = min(reserve, model.MaxOutputTokens)
	}
	return reserve
}
