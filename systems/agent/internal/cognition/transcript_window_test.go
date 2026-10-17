package cognition

import (
	"context"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

type initialByteWindowLoader struct {
	ContextLoader
	window TurnWindow
}

func (l initialByteWindowLoader) LoadLatestTranscriptWindow(
	context.Context,
	int,
) (TurnWindow, error) {
	return l.window, nil
}

func TestInitialCognitionSpecReportsOnlyCoveredTranscriptPrefix(t *testing.T) {
	for _, semantic := range []bool{false, true} {
		t.Run(map[bool]string{false: "working", true: "semantic"}[semantic], func(t *testing.T) {
			loader := initialByteWindowLoader{
				ContextLoader: &workingMemoryJobLoader{},
				window: TurnWindow{
					Messages: []conversation.Message{
						conversation.UserMessage("synthetic covered evidence"),
					},
					LastSeq:        7,
					RemainingTurns: 2,
				},
			}
			var job JobDefinition = workingMemoryConsolidationJob{}
			if semantic {
				loader.ContextLoader = &semanticMemoryJobLoader{
					semantic: agent.SemanticMemory{
						Files: []agent.SemanticMemoryFile{
							{RelativePath: semanticFactsRelativePath, Content: "# Facts\n"},
							{
								RelativePath: semanticPreferencesRelativePath,
								Content:      "# Preferences\n",
							},
							{RelativePath: semanticProjectsRelativePath, Content: "# Projects\n"},
						},
					},
				}
				job = semanticMemoryExtractionJob{}
			}
			spec, err := job.Build(context.Background(), loader)
			if err != nil {
				t.Fatal(err)
			}
			if spec.CheckpointSeq != 7 {
				t.Fatalf("initial spec advances past covered prefix: %d", spec.CheckpointSeq)
			}
			prompt, err := renderPrompt(job.Type(), spec)
			if err != nil ||
				(!strings.Contains(prompt, "synthetic covered evidence") || !strings.Contains(prompt, "2 later turns")) {
				t.Fatalf("missing covered evidence: %v", err)
			}
		})
	}
}
