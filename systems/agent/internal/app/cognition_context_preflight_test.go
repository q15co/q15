package app

import (
	"context"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/cognition"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/modelselection"
)

func TestCognitionPreflightSkipsInsufficientKnownContexts(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(
			map[bool]string{false: "all_small", true: "larger_fallback"}[large],
			func(t *testing.T) {
				models := []modelcatalog.Model{
					{
						ProviderModel:    "small",
						Capabilities:     modelcatalog.Capabilities{Chat: true, Text: true},
						MaxContextTokens: 8192,
					},
				}
				refs := []string{"small"}
				if large {
					models = append(
						models,
						modelcatalog.Model{
							ProviderModel:    "large",
							Capabilities:     modelcatalog.Capabilities{Chat: true, Text: true},
							MaxContextTokens: 128 * 1024,
						},
					)
					refs = append(refs, "large")
				}
				registry := testRegistry(t, map[string][]modelcatalog.Model{"synthetic": models})
				client := &fakeModelClient{}
				adapter, err := newModelAdapterWithFactory(
					registry,
					nil,
					func(modelcatalog.Model, q15media.Store) (agent.ModelClient, error) { return client, nil },
				)
				if err != nil {
					t.Fatal(err)
				}
				job := recoveryJob{
					JobDefinition: cognition.NewSemanticMemoryExtractionRegistration(0, 0).NewJob(),
					build: func(context.Context, cognition.ContextLoader) (cognition.Spec, error) {
						return cognition.Spec{
							Objective:          strings.Repeat("synthetic evidence ", 3000),
							CompletionContract: "Return a result.",
						}, nil
					},
					apply: func(context.Context) error { return nil },
				}
				_, err = cognition.NewRunnerWithPlanner(adapter, adapter, nil, refs, nil).
					Run(context.Background(), job, nil)
				if !large {
					if err == nil || len(client.calls) != 0 {
						t.Fatalf(
							"oversized known-context request reached provider: calls=%d error=%v",
							len(client.calls),
							err,
						)
					}
				} else if err != nil || len(client.calls) != 1 || client.calls[0].model != "large" {
					t.Fatalf("preflight did not select fitting fallback: call_count=%d error=%v", len(client.calls), err)
				}
			},
		)
	}
}

func TestContextPreflightIsOptInAndPreservesUnknownMetadata(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{"synthetic": {
		{
			ProviderModel:    "small",
			Capabilities:     modelcatalog.Capabilities{Chat: true, Text: true},
			MaxContextTokens: 8192,
		},
		{ProviderModel: "unknown", Capabilities: modelcatalog.Capabilities{Chat: true, Text: true}},
	}})
	adapter, err := newModelAdapterWithFactory(
		registry,
		nil,
		func(modelcatalog.Model, q15media.Store) (agent.ModelClient, error) { return &fakeModelClient{}, nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		bytes int
		want  int
	}{{0, 2}, {6144, 2}, {6145, 1}} {
		plan, err := adapter.Plan(
			[]string{"small", "unknown"},
			modelselection.Requirements{Text: true, InputBytes: tc.bytes},
		)
		if err != nil || len(plan.EligibleRefs) != tc.want {
			t.Fatalf("input=%d eligible=%v error=%v", tc.bytes, plan.EligibleRefs, err)
		}
		if tc.want == 1 && plan.EligibleRefs[0] != "unknown" {
			t.Fatal("unknown context metadata became unusable")
		}
	}
}

func TestCognitionContextResolverReadsPinnedModelLimits(t *testing.T) {
	registry := testRegistry(t, map[string][]modelcatalog.Model{"synthetic": {
		{
			ProviderModel:    "small",
			Capabilities:     modelcatalog.Capabilities{Chat: true, Text: true},
			MaxContextTokens: 8192,
			MaxOutputTokens:  2048,
		},
		{
			ProviderModel:    "large",
			Capabilities:     modelcatalog.Capabilities{Chat: true, Text: true},
			MaxContextTokens: 1048576,
			MaxOutputTokens:  32768,
		},
		{ProviderModel: "unknown", Capabilities: modelcatalog.Capabilities{Chat: true, Text: true}},
	}})
	resolver := newCognitionContextResolver(registry, func(jobType string) []string {
		return map[string][]string{
			"pinned-small":   {"small"},
			"pinned-large":   {"large"},
			"pinned-unknown": {"unknown"},
			"pinned-missing": {"not-in-roster"},
			"chained":        {"unknown", "large"},
		}[jobType]
	})
	for _, tc := range []struct {
		jobType string
		context int
		output  int
	}{
		{"pinned-small", 8192, 2048},
		{"pinned-large", 1048576, 32768},
		{"pinned-unknown", 0, 0},
		{"pinned-missing", 0, 0},
		// The pinned ref leads: fallback metadata must not inflate the budget.
		{"chained", 0, 0},
	} {
		contextTokens, outputTokens := resolver(tc.jobType)
		if contextTokens != tc.context || outputTokens != tc.output {
			t.Fatalf(
				"resolver(%q) = (%d, %d), want (%d, %d)",
				tc.jobType,
				contextTokens,
				outputTokens,
				tc.context,
				tc.output,
			)
		}
	}
}
