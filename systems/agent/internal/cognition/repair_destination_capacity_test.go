package cognition

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/memory/claims"
)

func TestRepairNearFullDestinationOffersHeadroomAndRefusesOvershootingPlan(t *testing.T) {
	ctx := context.Background()
	loader, _ := typedRepairFixture(t, "Synthetic durable source.\n")
	const crowded = "semantic/topics/crowded.md"
	const sibling = "semantic/topics/crowded-history.md"
	const room = 202
	content := "# Claims\n\n<!--" + strings.Repeat(
		" ",
		semanticTopicCapBytes-room-len("# Claims\n\n<!---->\n"),
	) + "-->\n"
	loader.files[crowded] = []byte(content)
	loader.memoryFiles = append(
		loader.memoryFiles,
		MemoryFile{Path: crowded, Bytes: int64(len(content))},
	)
	job := NewSemanticMemoryExtractionRegistration(0, 0).NewJob()
	spec, err := job.Build(ctx, loader)
	if err != nil {
		t.Fatal(err)
	}
	section, ok := findPromptSection(spec.PromptSections, "repair_destination_sizes")
	want := fmt.Sprintf(
		"%s bytes=%d cap=%d remaining=%d",
		crowded,
		len(content),
		semanticTopicCapBytes,
		room,
	)
	if !ok || !strings.Contains(section.Body, want) {
		t.Errorf("repair prompt omits destination headroom: want %q in %q", want, section.Body)
	}
	response := fmt.Sprintf(
		`{"decisions":[{"offset":0,"outcome":"converted","claim_id":"c-2026-0007","claim":{"path":%q,"text":%q,"status":"active","kind":"fact","confidence":"confirmed"}}]}`,
		crowded,
		"Synthetic durable fact "+strings.Repeat("x", 300)+".",
	)
	before := make(map[string]string)
	for relative, raw := range loader.files {
		before[relative] = string(raw)
	}
	_, _, err = buildRepairWritePlan(ctx, loader, JobOutput{Spec: spec, FinalText: response})
	if err == nil || !strings.Contains(err.Error(), "repair destination "+crowded) {
		t.Errorf("overshooting destination was not refused during planning: %v", err)
	}
	counting := &repairCapacityLoader{repairProbeLoader: loader}
	_, err = job.ApplyResult(ctx, counting, JobOutput{Spec: spec, FinalText: response})
	if err == nil || counting.writes != 0 {
		t.Fatalf("overshooting plan reached persistence: writes=%d error=%v", counting.writes, err)
	}
	for relative, raw := range loader.files {
		if before[relative] != string(raw) {
			t.Fatalf("refused plan changed %s", relative)
		}
	}
	if len(loader.files) != len(before) {
		t.Fatal("refused plan changed the file inventory")
	}
	response = strings.Replace(response, crowded, sibling, 1)
	result, err := job.ApplyResult(ctx, loader, JobOutput{Spec: spec, FinalText: response})
	if err != nil || result.Metadata["chunk_lines_exited"] != "1" {
		t.Fatalf("sibling destination did not commit the chunk: result=%#v error=%v", result, err)
	}
	if string(loader.files[crowded]) != content ||
		len(loader.files[sibling]) > semanticTopicCapBytes {
		t.Fatal("sibling plan changed the crowded destination or exceeded its own cap")
	}
	parsed, _, err := claims.ParseClaims(string(loader.files[sibling]))
	if err != nil || len(parsed) != 1 || parsed[0].ID != "c-2026-0007" {
		t.Fatalf("sibling lost the converted claim: claims=%#v error=%v", parsed, err)
	}
}

type repairCapacityLoader struct {
	*repairProbeLoader
	writes int
}

func (l *repairCapacityLoader) WriteFileBytes(
	ctx context.Context,
	relative string,
	raw []byte,
) error {
	l.writes++
	return l.repairProbeLoader.WriteFileBytes(ctx, relative, raw)
}

func TestRepairDestinationCapacityCountsAllRenderedClaims(t *testing.T) {
	ctx := context.Background()
	for _, destination := range []string{semanticPreferencesRelativePath, "semantic/topics/batched.md"} {
		t.Run(destination, func(t *testing.T) {
			loader, spec := typedRepairFixture(
				t,
				"Synthetic first source.\nSynthetic second source.\n",
			)
			if spec.Baseline[memoryRuntimePrefix+destination] == "" {
				spec.Baseline[memoryRuntimePrefix+destination] = "# Claims\n\n"
			}
			response := fmt.Sprintf(
				`{"decisions":[{"offset":0,"outcome":"converted","claim_id":"c-2026-0007","claim":{"path":%q,"text":"First fact.","status":"active","kind":"fact","confidence":"confirmed"}},{"offset":%d,"outcome":"converted","claim_id":"c-2026-0008","claim":{"path":%q,"text":"Second fact.","status":"active","kind":"fact","confidence":"confirmed"}}]}`,
				destination,
				len("Synthetic first source.\n"),
				destination,
			)
			plan, _, err := buildRepairWritePlan(
				ctx,
				loader,
				JobOutput{Spec: spec, FinalText: response},
			)
			if err != nil {
				t.Fatal(err)
			}
			capBytes := SemanticCapBytes(destination)
			added := len(
				plan.files[destination],
			) - len(
				spec.Baseline[memoryRuntimePrefix+destination],
			)
			padding := strings.Repeat("\n", capBytes-len(plan.files[destination])+1)
			baseline := spec.Baseline[memoryRuntimePrefix+destination] + padding
			if capBytes-len(baseline) <= 0 || capBytes-len(baseline) >= added {
				t.Fatal("fixture must leave positive headroom smaller than the rendered batch")
			}
			spec.Baseline[memoryRuntimePrefix+destination] = baseline
			_, _, err = buildRepairWritePlan(
				ctx,
				loader,
				JobOutput{Spec: spec, FinalText: response},
			)
			if err == nil || !strings.Contains(err.Error(), "repair destination "+destination) {
				t.Fatalf("rendered batch exceeded headroom without a planning refusal: %v", err)
			}
			spec.Baseline[memoryRuntimePrefix+destination] = strings.TrimSuffix(baseline, "\n")
			plan, _, err = buildRepairWritePlan(
				ctx,
				loader,
				JobOutput{Spec: spec, FinalText: response},
			)
			if err != nil || len(plan.files[destination]) != capBytes {
				t.Fatalf(
					"exact-cap batch refused: bytes=%d cap=%d error=%v",
					len(plan.files[destination]),
					capBytes,
					err,
				)
			}
		})
	}
}
