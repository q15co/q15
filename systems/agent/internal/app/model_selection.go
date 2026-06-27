package app

import (
	"strings"

	"github.com/q15co/q15/systems/agent/internal/modelselection"
)

var _ modelselection.Planner = (*routedModelAdapter)(nil)

// Plan resolves model refs against the live registry, filters by hard
// capability requirements, and returns the eligible refs in their original
// order (current model first, then roster order). Refs not in the current
// roster are skipped with a clear reason.
func (r *routedModelAdapter) Plan(
	modelRefs []string,
	requirements modelselection.Requirements,
) (modelselection.Plan, error) {
	plan := modelselection.Plan{
		EligibleRefs: make([]string, 0, len(modelRefs)),
		Skipped:      make([]modelselection.Skip, 0),
	}

	for _, ref := range modelRefs {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		m, ok := r.lookupModel(ref)
		if !ok {
			plan.Skipped = append(plan.Skipped, modelselection.Skip{
				Ref:    ref,
				Reason: "not in current roster (provider down or model deprecated)",
			})
			continue
		}
		caps := modelselection.Capabilities{
			Text:        m.Capabilities.Text,
			ToolCalling: m.Capabilities.ToolCalling,
		}
		if reason := caps.MissingReason(requirements); reason != "" {
			plan.Skipped = append(plan.Skipped, modelselection.Skip{
				Ref:    ref,
				Reason: reason,
			})
			continue
		}
		plan.EligibleRefs = append(plan.EligibleRefs, ref)
	}

	return plan, nil
}
