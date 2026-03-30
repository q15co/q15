package app

import (
	"fmt"
	"strings"

	"github.com/q15co/q15/systems/agent/internal/modelselection"
)

type resolvedModelCandidate struct {
	ref      string
	endpoint routedModelEndpoint
}

var _ modelselection.Planner = (*routedModelAdapter)(nil)

func (r *routedModelAdapter) Plan(
	modelRefs []string,
	requirements modelselection.Requirements,
) (modelselection.Plan, error) {
	candidates, err := r.resolveCandidates(modelRefs)
	if err != nil {
		return modelselection.Plan{}, err
	}
	return filterCandidatesByRequirements(candidates, requirements), nil
}

func (r *routedModelAdapter) resolveCandidates(
	modelRefs []string,
) ([]resolvedModelCandidate, error) {
	if len(modelRefs) == 0 {
		return nil, nil
	}

	candidates := make([]resolvedModelCandidate, 0, len(modelRefs))
	for _, modelRef := range modelRefs {
		modelRef = strings.TrimSpace(modelRef)
		if modelRef == "" {
			continue
		}

		endpoint, ok := r.endpoints[modelRef]
		if !ok {
			return nil, fmt.Errorf("unknown configured fallback model %q", modelRef)
		}
		candidates = append(candidates, resolvedModelCandidate{
			ref:      modelRef,
			endpoint: endpoint,
		})
	}
	return candidates, nil
}

func filterCandidatesByRequirements(
	candidates []resolvedModelCandidate,
	requirements modelselection.Requirements,
) modelselection.Plan {
	plan := modelselection.Plan{
		EligibleRefs: make([]string, 0, len(candidates)),
		Skipped:      make([]modelselection.Skip, 0),
	}

	for _, candidate := range candidates {
		capabilities := modelselection.Capabilities{
			Text:        candidate.endpoint.capabilities.Text,
			ImageInput:  candidate.endpoint.capabilities.ImageInput,
			ToolCalling: candidate.endpoint.capabilities.ToolCalling,
		}
		if reason := capabilities.MissingReason(requirements); reason != "" {
			plan.Skipped = append(plan.Skipped, modelselection.Skip{
				Ref:    candidate.ref,
				Reason: reason,
			})
			continue
		}
		plan.EligibleRefs = append(plan.EligibleRefs, candidate.ref)
	}

	return plan
}
