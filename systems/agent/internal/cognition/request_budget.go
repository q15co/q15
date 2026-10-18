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

func checkConversionPromptBudget(spec Spec) error {
	prompt, err := renderPrompt(semanticMemoryExtractionJobType, spec)
	if err != nil {
		return err
	}
	// Conversion carries several independently bounded sources. Leave room for
	// tool definitions and accumulated decisions before opening its journal.
	const reserve = 64 * 1024
	limit := cognitionRequestLimit(spec, prompt) - reserve
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

func cognitionRequestLimit(spec Spec, prompt string) int {
	if !spec.repair {
		return defaultCognitionMaxRequestBytes
	}
	// A repair source line is indivisible and bounded at 64 KiB before quoting
	// and JSON escaping. Reserve space beyond its actual encoded prompt for
	// tool definitions, evidence and decisions instead of truncating the source.
	encoded, _ := json.Marshal(conversation.SystemMessage(prompt))
	return max(defaultCognitionMaxRequestBytes, len(encoded)+128*1024)
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
