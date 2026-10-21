package cognition

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func encodedTextBytes(text string) int {
	encoded, _ := json.Marshal(text)
	return len(encoded)
}

const (
	// defaultCognitionMaxRequestBytes floors the request budget: it stays the
	// budget for models with unknown or small contexts.
	defaultCognitionMaxRequestBytes = 256 * 1024
	// absoluteCognitionMaxRequestBytes caps the budget of large-context models so
	// per-run request cost and latency stay bounded regardless of catalog
	// metadata. Tunable.
	absoluteCognitionMaxRequestBytes = 768 * 1024
	// assumedBytesPerToken converts a token allowance to canonical JSON bytes
	// conservatively, in place of a provider tokenizer.
	assumedBytesPerToken = 3
	// requestBudgetUsableNumerator/Denominator spare a fifth of the usable
	// context for provider framing and tokenizer error beyond the output
	// reserve.
	requestBudgetUsableNumerator   = 4
	requestBudgetUsableDenominator = 5
)

// deriveCognitionRequestBudget sizes one run's request byte budget from the
// pinned model's catalog limits: the context tokens minus the output reserve,
// converted at assumedBytesPerToken and discounted by a fifth. Cognition
// requests set no output cap of their own, so the reserve is the model's full
// output limit; an unknown output limit reserves nothing. Unknown or small
// contexts keep the floor and the absolute cap bounds per-run cost.
func deriveCognitionRequestBudget(contextTokens, outputTokens int) int {
	if contextTokens <= 0 {
		return defaultCognitionMaxRequestBytes
	}
	usable := contextTokens - max(outputTokens, 0)
	if usable < 0 {
		usable = 0
	}
	derived := usable * assumedBytesPerToken * requestBudgetUsableNumerator /
		requestBudgetUsableDenominator
	return min(
		absoluteCognitionMaxRequestBytes,
		max(defaultCognitionMaxRequestBytes, derived),
	)
}

func checkConversionPromptBudget(spec Spec) error {
	prompt, err := renderPrompt(semanticMemoryExtractionJobType, spec)
	if err != nil {
		return err
	}
	// Conversion carries several independently bounded sources. Leave room for
	// tool definitions and accumulated decisions before opening its journal. Build
	// is model-agnostic, so the check pins to the floor budget; a larger
	// pinned-model budget only applies to turn-level accumulation.
	const reserve = 64 * 1024
	limit := cognitionRequestLimit(spec, prompt, 0, 0) - reserve
	size := encodedTextBytes(prompt)
	if size <= limit {
		return nil
	}
	largest, largestBytes := "", 0
	for _, section := range spec.PromptSections {
		if bytes := encodedTextBytes(agent.ComposePromptSections(section)); bytes > largestBytes {
			largest, largestBytes = section.Name, bytes
		}
	}
	return fmt.Errorf(
		"conversion prompt exceeds build byte budget (%d>%d): largest_section=%s largest_section_bytes=%d reserve_bytes=%d",
		size,
		limit,
		largest,
		largestBytes,
		reserve,
	)
}

func cognitionRequestLimit(spec Spec, prompt string, contextTokens, outputTokens int) int {
	base := deriveCognitionRequestBudget(contextTokens, outputTokens)
	if !spec.repair {
		return base
	}
	// A repair source line is indivisible and bounded at 64 KiB before quoting
	// and JSON escaping. Reserve space beyond its actual encoded prompt for
	// tool definitions, evidence and decisions instead of truncating the source.
	encoded, _ := json.Marshal(conversation.SystemMessage(prompt))
	return max(base, len(encoded)+128*1024)
}

func describeRequestBudget(jobType string, spec Spec, prompt string, err error) error {
	var budget *agent.RequestBudgetError
	if !errors.As(err, &budget) {
		return err
	}
	fixed := encodedTextBytes(prompt)
	components := make([]string, 0, len(spec.PromptSections))
	for _, section := range spec.PromptSections {
		body := agent.ComposePromptSections(section)
		if body == "" {
			continue
		}
		size := encodedTextBytes(body) - 2
		fixed -= size
		components = append(components, fmt.Sprintf("%s_bytes=%d", section.Name, size))
	}
	return fmt.Errorf(
		"cognition job %q fixed_prompt_bytes=%d %s: %w",
		jobType,
		fixed,
		strings.Join(components, " "),
		err,
	)
}
