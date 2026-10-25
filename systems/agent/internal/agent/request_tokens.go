package agent

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/tiktoken-go/tokenizer"
)

var requestTokenizer = sync.OnceValues(func() (tokenizer.Codec, error) {
	return tokenizer.Get(tokenizer.O200kBase)
})

// RequestTokenEstimator identifies the normal tokenizer and its fallback policy.
const RequestTokenEstimator = "o200k_base_or_bytes_margin25"

// RequestEstimatedTokens uses o200k_base with framing and a 25 percent margin.
// If tokenization fails, UTF-8 byte counts keep the estimate conservative.
func RequestEstimatedTokens(messages []conversation.Message, tools []ToolDefinition) (int, error) {
	codec, err := requestTokenizer()
	var countTokens func(string) (int, error)
	if err == nil {
		countTokens = codec.Count
	}
	return requestEstimatedTokens(messages, tools, countTokens)
}

func requestEstimatedTokens(
	messages []conversation.Message,
	tools []ToolDefinition,
	countTokens func(string) (int, error),
) (int, error) {
	tokens := 128
	count := func(body string) {
		n := len(body)
		if countTokens != nil {
			if estimate, err := countTokens(body); err == nil {
				n = estimate
			}
		}
		tokens += n
	}
	for _, message := range messages {
		tokens += 16
		if message.UserTemporal != nil {
			tokens += 64
		}
		for _, part := range message.Parts {
			body := part.Text
			switch part.Type {
			case conversation.ToolCallPartType:
				body = part.Name + "\n" + part.ID + "\n" + part.Arguments
			case conversation.ToolResultPartType:
				body = part.ToolCallID + "\n" + part.Content
			case conversation.MediaPartType:
				// Media tokens depend on provider processing, not base64 length.
				tokens += 8192
			case conversation.ReasoningPartType:
				if body == "" && len(part.Replay) > 0 {
					raw, err := json.Marshal(part.Replay)
					if err != nil {
						return 0, fmt.Errorf("estimate reasoning replay: %w", err)
					}
					body = string(raw)
				}
			}
			count(body)
		}
	}
	if len(tools) > 0 {
		encoded, err := json.Marshal(tools)
		if err != nil {
			return 0, fmt.Errorf("estimate tool definitions: %w", err)
		}
		count(string(encoded))
	}
	return tokens + (tokens+3)/4, nil
}

// TokenBudgetError reports estimates without including conversation content.
type TokenBudgetError struct {
	EstimatedTokens int
	LimitTokens     int
}

func (e *TokenBudgetError) Error() string {
	return fmt.Sprintf(
		"model request exceeds estimated token budget (%d>%d)",
		e.EstimatedTokens,
		e.LimitTokens,
	)
}
