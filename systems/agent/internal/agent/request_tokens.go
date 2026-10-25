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

// RequestEstimatedTokens uses o200k_base as a portable text proxy, with framing
// allowances and a 25 percent margin. Provider templates and media accounting
// differ, so even models using this vocabulary receive an estimate.
func RequestEstimatedTokens(messages []conversation.Message, tools []ToolDefinition) (int, error) {
	codec, err := requestTokenizer()
	if err != nil {
		return 0, fmt.Errorf("initialize request tokenizer: %w", err)
	}
	tokens := 128
	count := func(body string) error {
		n, err := codec.Count(body)
		tokens += n
		return err
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
			if err := count(body); err != nil {
				return 0, fmt.Errorf("estimate message tokens: %w", err)
			}
		}
	}
	if len(tools) > 0 {
		encoded, err := json.Marshal(tools)
		if err != nil {
			return 0, fmt.Errorf("estimate tool definitions: %w", err)
		}
		if err := count(string(encoded)); err != nil {
			return 0, fmt.Errorf("estimate tool tokens: %w", err)
		}
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
