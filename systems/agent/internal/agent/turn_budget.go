package agent

import (
	"strconv"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// TurnBudget belongs to one session and is reused across its engine runs.
type TurnBudget struct {
	limit int
	used  int
}

// NewTurnBudget reserves the last model turn for an answer; zero uses the engine default.
func NewTurnBudget(limit int) *TurnBudget {
	if limit <= 0 {
		limit = defaultMaxTurns
	}
	return &TurnBudget{limit: limit}
}

// Remaining includes the next model call and excludes turns already started.
func (b *TurnBudget) Remaining() int {
	return max(0, b.limit-b.used)
}

func (b *TurnBudget) next() (conversation.Message, bool) {
	remaining := b.Remaining()
	final := remaining == 1
	guidance := "One turn is one model response and any tools it requests. Remaining turns include this call."
	if final {
		guidance += " This is your final turn. Tools are disabled. Give your final answer now, using the available evidence. State any unfinished work or uncertainty."
	} else if remaining <= 3 {
		guidance += " The turn limit is close. Finish essential work and leave the last turn for your final answer; tools will be disabled then."
	}
	b.used++
	return conversation.UserMessage(RenderPromptElement("turn_budget", map[string]string{
		"max_turns":         strconv.Itoa(b.limit),
		"current_turn":      strconv.Itoa(b.used),
		"remaining_turns":   strconv.Itoa(remaining),
		"final_answer_only": strconv.FormatBool(final),
	}, guidance)), final
}
