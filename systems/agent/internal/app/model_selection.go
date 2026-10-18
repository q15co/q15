package app

import (
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/modelselection"
)

var _ modelselection.Planner = (*routedModelAdapter)(nil)

// Plan resolves model refs against the live registry, filters by hard
// capability requirements, and returns the eligible refs in their original
// order supplied by the caller. Refs not in the current
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
		if err := r.registry.RequireChat(r.chatRole(ref, m), m); err != nil {
			plan.Skipped = append(plan.Skipped, modelselection.Skip{Ref: ref, Reason: err.Error()})
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
		// Serialized bytes are a conservative input allowance for bounded text
		// requests. Reserve a quarter of known context for output and provider
		// framing; unknown metadata retains the registry's usable default.
		if requirements.InputBytes > 0 && m.MaxContextTokens > 0 {
			allowance := m.MaxContextTokens - m.MaxContextTokens/4
			if requirements.InputBytes > allowance {
				plan.Skipped = append(
					plan.Skipped,
					modelselection.Skip{
						Ref: ref,
						Reason: fmt.Sprintf(
							"request of %d encoded bytes exceeds conservative context allowance %d (known context %d tokens)",
							requirements.InputBytes,
							allowance,
							m.MaxContextTokens,
						),
					},
				)
				continue
			}
		}
		plan.EligibleRefs = append(plan.EligibleRefs, ref)
	}

	return plan, nil
}
