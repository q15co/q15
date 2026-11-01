package history

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

const (
	maxJudgeCandidates = 64
	maxJudgeTokens     = 16000
	minJudgeUtility    = 0.3
)

// Judge evaluates the marginal usefulness of candidate digests, in input order.
// It must use a direct decision call without re-entering context assembly.
type Judge interface {
	Judge(ctx context.Context, state string, candidates []string) (Judgment, error)
}

// Judgment keeps provider usage separate from the ordered usefulness probabilities.
type Judgment struct {
	Scores      []float64
	InputTokens int
	Model       string
}

func retrievalJudgeState(
	input []conversation.Message,
	recent []conversation.Turn,
	digests map[int64]string,
) (string, string) {
	state := struct {
		CurrentRequest    string `json:"current_request"`
		PreviousTurn      int64  `json:"previous_turn,omitempty"`
		PreviousDigest    string `json:"previous_digest,omitempty"`
		PreviousUser      string `json:"previous_user_excerpt,omitempty"`
		PreviousAssistant string `json:"previous_assistant_tail,omitempty"`
	}{CurrentRequest: retrievalInput(input)}
	source := "none"
	if len(recent) > 0 {
		previous := recent[0]
		state.PreviousTurn = previous.Seq
		if digest := strings.TrimSpace(digests[previous.Seq]); digest != "" {
			state.PreviousDigest = boundedText(digest, 2048)
			source = "digest"
		} else {
			var user, assistant strings.Builder
			for _, message := range previous.Messages {
				for _, part := range message.Parts {
					if part.Type != conversation.TextPartType {
						continue
					}
					switch message.Role {
					case conversation.UserRole:
						user.WriteString(part.Text + "\n")
					case conversation.AssistantRole:
						if part.Disposition != conversation.TextDispositionCommentary {
							assistant.WriteString(part.Text + "\n")
						}
					}
				}
			}
			userText := []rune(strings.TrimSpace(user.String()))
			assistantText := []rune(strings.TrimSpace(assistant.String()))
			state.PreviousUser = string(userText[:min(600, len(userText))])
			state.PreviousAssistant = string(assistantText[max(0, len(assistantText)-800):])
			source = "raw"
		}
	}
	encoded, _ := json.Marshal(state)
	return string(encoded), source
}

func judgeCandidates(state string, hits []SearchHit) ([]string, int, error) {
	tokens, err := agent.RequestEstimatedTokens(
		[]conversation.Message{conversation.UserMessage(state)},
		nil,
	)
	if err != nil {
		return nil, 0, err
	}
	var candidates []string
	for _, hit := range hits[:min(len(hits), maxJudgeCandidates)] {
		// Standalone framing per candidate reserves room for its question. This
		// is an estimate with a margin, not the decision model's tokenizer.
		cost, err := agent.RequestEstimatedTokens(
			[]conversation.Message{conversation.UserMessage(hit.Text)},
			nil,
		)
		if err != nil {
			return nil, tokens, err
		}
		if tokens+cost > maxJudgeTokens {
			break
		}
		candidates = append(candidates, hit.Text)
		tokens += cost
	}
	if len(candidates) == 0 {
		return nil, tokens, errors.New("history judge input exceeds its budget")
	}
	return candidates, tokens, nil
}

func (s *ContextService) judgeHits(
	ctx context.Context,
	state, source string,
	hits []SearchHit,
) ([]SearchHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		log.Printf(
			"q15: history event=retrieval_judged candidates=0 judged=0 kept=0 input_tokens=0 duration_ms=0",
		)
		return hits, nil
	}
	start := time.Now()
	candidates, estimated, err := judgeCandidates(state, hits)
	var result Judgment
	if err == nil {
		result, err = s.judge.Judge(ctx, state, candidates)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err == nil {
		err = validateJudgment(result, len(candidates))
	}
	if err != nil {
		fallback := hits[:min(s.settings.RetrievalLimit, len(hits))]
		reason := "judge_failed"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "deadline"
		}
		// Provider errors may contain source text. Log the failure category only.
		log.Printf(
			"q15: history event=retrieval_judge_fallback level=warn reason=%s candidates=%d judged=%d kept=%d input_tokens=%d duration_ms=%d",
			reason,
			len(hits),
			len(candidates),
			len(fallback),
			result.InputTokens,
			time.Since(start).Milliseconds(),
		)
		return fallback, nil
	}
	type scoredHit struct {
		hit   SearchHit
		score float64
	}
	ordered := make([]scoredHit, len(candidates))
	for n, score := range result.Scores {
		ordered[n] = scoredHit{hit: hits[n], score: score}
	}
	slices.SortStableFunc(
		ordered,
		func(a, b scoredHit) int { return cmp.Compare(b.score, a.score) },
	)
	var kept []SearchHit
	var keptIDs []int64
	var keptScores, droppedScores []float64
	rejected := 0
	for _, candidate := range ordered {
		if candidate.score < minJudgeUtility {
			rejected++
		}
		if candidate.score >= minJudgeUtility && len(kept) < s.settings.RetrievalLimit {
			kept = append(kept, candidate.hit)
			keptIDs = append(keptIDs, candidate.hit.TurnID)
			keptScores = append(keptScores, candidate.score)
		} else {
			droppedScores = append(droppedScores, candidate.score)
		}
	}
	log.Printf(
		"q15: history event=retrieval_judged model=%q state_source=%s candidates=%d judged=%d limited=%d kept=%d rejected=%d threshold=%.2f estimated_input_tokens=%d input_tokens=%d duration_ms=%d kept_turns=%v kept_scores=%v dropped_scores=%v",
		result.Model,
		source,
		len(hits),
		len(candidates),
		len(hits)-len(candidates),
		len(kept),
		rejected,
		minJudgeUtility,
		estimated,
		result.InputTokens,
		time.Since(start).Milliseconds(),
		keptIDs,
		keptScores,
		droppedScores,
	)
	return kept, nil
}

func validateJudgment(result Judgment, count int) error {
	if len(result.Scores) != count {
		return errors.New("history judge score count does not match candidates")
	}
	for _, score := range result.Scores {
		if math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > 1 {
			return errors.New("history judge score is not a probability")
		}
	}
	return nil
}
