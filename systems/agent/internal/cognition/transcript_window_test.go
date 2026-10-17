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

type resumableByteWindowLoader struct {
	initialByteWindowLoader
	saved      *TranscriptCursor
	usedResume bool
}

func (l *resumableByteWindowLoader) LoadConsolidationCheckpoint(
	context.Context,
) (ConsolidationCheckpoint, error) {
	return ConsolidationCheckpoint{PartialTurn: l.saved}, nil
}

func (l *resumableByteWindowLoader) LoadSemanticExtractionCheckpoint(
	context.Context,
) (SemanticExtractionCheckpoint, error) {
	return SemanticExtractionCheckpoint{PartialTurn: l.saved}, nil
}

func (l *resumableByteWindowLoader) LoadTranscriptWindow(
	_ context.Context,
	after int64,
	_ int,
	cursor *TranscriptCursor,
) (TurnWindow, error) {
	l.usedResume = after == 0 && cursor == l.saved
	return l.window, nil
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

func TestCognitionSpecKeepsPartialCitationSeparateFromCompletedBoundary(t *testing.T) {
	for _, semantic := range []bool{false, true} {
		for _, resume := range []bool{false, true} {
			t.Run(
				map[bool]string{false: "working", true: "semantic"}[semantic]+map[bool]string{false: "/initial", true: "/resume"}[resume],
				func(t *testing.T) {
					loader := &resumableByteWindowLoader{
						initialByteWindowLoader: initialByteWindowLoader{
							ContextLoader: &workingMemoryJobLoader{},
							window: TurnWindow{
								Messages: []conversation.Message{
									conversation.UserMessage("synthetic fragment"),
								},
								LastIncludedSeq: 1,
								PartialTurn: &TranscriptCursor{
									TurnSeq:    1,
									ByteOffset: 200,
									InputHash:  "synthetic",
								},
							},
						},
					}
					if resume {
						loader.saved = &TranscriptCursor{
							TurnSeq:    1,
							ByteOffset: 100,
							InputHash:  "synthetic",
						}
					}
					var job JobDefinition = workingMemoryConsolidationJob{}
					if semantic {
						loader.ContextLoader = &semanticMemoryJobLoader{
							semantic: agent.SemanticMemory{Files: []agent.SemanticMemoryFile{
								{RelativePath: semanticFactsRelativePath, Content: "# Facts\n"},
								{
									RelativePath: semanticPreferencesRelativePath,
									Content:      "# Preferences\n",
								},
								{
									RelativePath: semanticProjectsRelativePath,
									Content:      "# Projects\n",
								},
							}},
						}
						job = semanticMemoryExtractionJob{}
					}
					spec, err := job.Build(context.Background(), loader)
					if err != nil {
						t.Fatal(err)
					}
					if loader.usedResume != resume {
						t.Fatal("saved partial cursor did not select the resume loader")
					}
					if spec.CheckpointSeq != 1 || spec.transcriptProgress == nil ||
						spec.transcriptProgress.CompletedSeq != 0 ||
						spec.transcriptProgress.PartialTurn != loader.window.PartialTurn {
						t.Fatalf(
							"partial turn became a completed boundary: %#v",
							spec.transcriptProgress,
						)
					}
					prompt, err := renderPrompt(job.Type(), spec)
					if err != nil || !strings.Contains(prompt, "only partially included") {
						t.Fatal("partial input was presented as a complete turn")
					}
				},
			)
		}
	}
}
