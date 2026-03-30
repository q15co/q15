package modelselection

import (
	"fmt"
	"strings"
)

// Requirements describes the minimum capabilities a model must satisfy for one
// model turn.
type Requirements struct {
	Text        bool
	ImageInput  bool
	ToolCalling bool
}

// Capabilities describes the capability set available for one candidate model.
type Capabilities struct {
	Text        bool
	ImageInput  bool
	ToolCalling bool
}

// String returns a stable, human-readable requirement list for logs/errors.
func (r Requirements) String() string {
	names := make([]string, 0, 3)
	if r.Text {
		names = append(names, "text")
	}
	if r.ImageInput {
		names = append(names, "image_input")
	}
	if r.ToolCalling {
		names = append(names, "tool_calling")
	}
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// Skip records one configured model ref skipped before any provider call and
// why it was excluded.
type Skip struct {
	Ref    string
	Reason string
}

// Plan is the deterministic eligible candidate list for one turn plus the refs
// skipped during planning.
type Plan struct {
	EligibleRefs []string
	Skipped      []Skip
}

// SkipSummary returns a stable, human-readable summary of skipped candidates.
func (p Plan) SkipSummary() string {
	return formatSkips(p.Skipped)
}

// Planner can reduce a configured fallback list to the eligible candidates for
// the current request requirements before provider calls.
type Planner interface {
	Plan(modelRefs []string, requirements Requirements) (Plan, error)
}

// Passthrough preserves the configured model-ref order without applying any
// additional filtering.
type Passthrough struct{}

// Plan returns the provided refs in order after trimming empty entries.
func (Passthrough) Plan(modelRefs []string, _ Requirements) (Plan, error) {
	if len(modelRefs) == 0 {
		return Plan{}, nil
	}

	eligible := make([]string, 0, len(modelRefs))
	for _, modelRef := range modelRefs {
		modelRef = strings.TrimSpace(modelRef)
		if modelRef == "" {
			continue
		}
		eligible = append(eligible, modelRef)
	}
	return Plan{EligibleRefs: eligible}, nil
}

// Missing returns the unmet capability names in stable order.
func (c Capabilities) Missing(requirements Requirements) []string {
	missing := make([]string, 0, 3)
	if requirements.Text && !c.Text {
		missing = append(missing, "text")
	}
	if requirements.ImageInput && !c.ImageInput {
		missing = append(missing, "image_input")
	}
	if requirements.ToolCalling && !c.ToolCalling {
		missing = append(missing, "tool_calling")
	}
	return missing
}

// MissingReason returns a stable human-readable mismatch reason or the empty
// string when all requirements are satisfied.
func (c Capabilities) MissingReason(requirements Requirements) string {
	missing := c.Missing(requirements)
	if len(missing) == 0 {
		return ""
	}
	return fmt.Sprintf("missing capabilities [%s]", strings.Join(missing, ", "))
}

// NoEligibleError reports that configured models were resolved but none matched
// the current request requirements.
type NoEligibleError struct {
	Requirements Requirements
	Skipped      []Skip
}

func (e *NoEligibleError) Error() string {
	if e == nil {
		return "no eligible models"
	}

	message := fmt.Sprintf("no eligible models for requirements [%s]", e.Requirements.String())
	if skipped := formatSkips(e.Skipped); skipped != "none" {
		message += fmt.Sprintf(" (skipped: %s)", skipped)
	}
	return message
}

func formatSkips(skipped []Skip) string {
	if len(skipped) == 0 {
		return "none"
	}

	parts := make([]string, 0, len(skipped))
	for _, skip := range skipped {
		modelRef := strings.TrimSpace(skip.Ref)
		reason := strings.TrimSpace(skip.Reason)
		if modelRef == "" && reason == "" {
			continue
		}
		switch {
		case modelRef == "":
			parts = append(parts, reason)
		case reason == "":
			parts = append(parts, modelRef)
		default:
			parts = append(parts, fmt.Sprintf("%s: %s", modelRef, reason))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, "; ")
}
