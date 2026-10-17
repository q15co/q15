package cognition

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/memory/claims"
)

func TestRepairRejectsUnledgeredTailRemoval(t *testing.T) {
	ctx := context.Background()
	const first = "The first line is eligible for this bounded run."
	const tail = "This entire second line is outside this run's chunk and must survive."
	pre := repairPreFiles()
	pre[repairNotesPath] = first + "\n" + strings.Repeat(tail, 150) + "\n"
	loader := &repairProbeLoader{
		semanticMemoryJobLoader: &semanticMemoryJobLoader{files: repairRawFiles(pre)},
	}
	for path, content := range pre {
		loader.memoryFiles = append(
			loader.memoryFiles,
			MemoryFile{Path: path, Bytes: int64(len(content))},
		)
	}
	job := NewSemanticMemoryExtractionRegistration(0, 0).NewJob()
	spec, err := job.Build(ctx, loader)
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := cutRepairChunk(spec.FileSnapshots[memoryRuntimePrefix+repairNotesPath].Content)
	if err != nil || len(chunk) != 1 || chunk[0].text != first {
		t.Fatalf(
			"fixture requires only the first line inside the chunk: chunk=%v err=%v",
			chunk,
			err,
		)
	}
	loader.files[repairNotesPath] = []byte("# Nothing left\n")
	loader.files[workingLedgerRelativePath] = []byte(repairLedgerPre + fmt.Sprintf(
		"2026-10-16T05:00Z run=%s src=mem:%s:0 dropped %q (obsolete)\n",
		semanticMemoryExtractionJobType, repairNotesPath, first,
	))
	result, err := job.ApplyResult(ctx, loader, JobOutput{
		Type: semanticMemoryExtractionJobType, Spec: spec,
		Messages: repairRunMessages(memoryRuntimePrefix+repairNotesPath, workingLedgerRuntimePath),
	})
	if err == nil {
		t.Fatalf(
			"accepted deletion of uncited tail outside the chunk: removed=%s exits=%s tail_present=%v ledger=%q",
			result.Metadata["repair_target_removed"],
			result.Metadata["chunk_lines_exited"],
			strings.Contains(
				string(loader.files[repairNotesPath]),
				tail,
			),
			string(loader.files[workingLedgerRelativePath]),
		)
	}
}

func workingRepairSpec(t *testing.T) (*repairProbeLoader, Spec) {
	t.Helper()
	pre := repairPreFiles()
	delete(pre, repairNotesPath)
	pre[workingMemoryRelativePath] = "# Working Memory\n\n## Active Tasks\n- Ship the unfinished release.\n" + strings.Repeat(
		"Further state that is outside the first chunk. ",
		250,
	) + "\n"
	loader := &repairProbeLoader{
		semanticMemoryJobLoader: &semanticMemoryJobLoader{files: repairRawFiles(pre)},
	}
	for path, content := range pre {
		loader.memoryFiles = append(
			loader.memoryFiles,
			MemoryFile{Path: path, Bytes: int64(len(content))},
		)
	}
	spec, err := NewSemanticMemoryExtractionRegistration(
		0,
		0,
	).NewJob().
		Build(context.Background(), loader)
	if err != nil {
		t.Fatal(err)
	}
	if !spec.repair || spec.repairTarget.Path != workingMemoryRelativePath {
		t.Fatalf("expected working-memory repair; got %#v", spec.repairTarget)
	}
	return loader, spec
}

func TestWorkingRepairPermitsOpenLoopsDestination(t *testing.T) {
	_, spec := workingRepairSpec(t)
	err := spec.ToolCallPolicy.CheckToolCall(agent.ToolCall{
		ID: "move-loop", Name: "write_file",
		Arguments: fmt.Sprintf(`{"path":%q,"content":"loop"}`, openLoopsRuntimePath),
	})
	if err != nil {
		t.Fatalf("working repair cannot preserve an unfinished intention in OPEN_LOOPS: %v", err)
	}
}

func TestWorkingRepairAcceptsMovedIntention(t *testing.T) {
	loader, spec := workingRepairSpec(t)
	const intention = "- Ship the unfinished release."
	pre := string(spec.FileSnapshots[workingMemoryRuntimePath].Content)
	offset := strings.Index(pre, intention)
	loader.files[workingMemoryRelativePath] = []byte(strings.Replace(pre, intention+"\n", "", 1))
	loader.files[openLoopsRelativePath] = []byte(fmt.Sprintf(
		"- [l-2026-0001] Ship the unfinished release.\n  {trigger: next-session, owner: agent, src: mem:%s:%d}\n",
		workingMemoryRelativePath,
		offset,
	))
	loader.files[workingLedgerRelativePath] = []byte(repairLedgerPre + fmt.Sprintf(
		"2026-10-16T05:00Z run=%s src=mem:%s:%d moved %q → l-2026-0001 (working/OPEN_LOOPS.md)\n",
		semanticMemoryExtractionJobType, workingMemoryRelativePath, offset, intention,
	))
	_, err := NewSemanticMemoryExtractionRegistration(
		0,
		0,
	).NewJob().
		ApplyResult(context.Background(), loader, JobOutput{
			Type: semanticMemoryExtractionJobType, Spec: spec,
			Messages: repairRunMessages(
				workingMemoryRuntimePath,
				openLoopsRuntimePath,
				workingLedgerRuntimePath,
			),
		})
	if err != nil {
		t.Fatalf("working repair rejected the only prospective-memory exit: %v", err)
	}
}

func TestRepairRequiresClaimCitationToDrainedSource(t *testing.T) {
	fixture := repairHappyFixture()
	fixture.factsPost = strings.Replace(
		fixture.factsPost,
		"src: mem:"+repairNotesPath+":"+fmt.Sprint(repairAlphaOffset()),
		"src: t999",
		1,
	)
	_, _, err := runRepairApply(t, fixture)
	if err == nil {
		t.Fatal(
			"repair accepted a converted claim whose provenance omits the drained memory source",
		)
	}
}

func TestRepairRetainsDuplicateOutsideChunk(t *testing.T) {
	ctx := context.Background()
	line := strings.Repeat("duplicate", 600)
	pre := repairPreFiles()
	pre[repairNotesPath] = line + "\n" + line + "\n"
	loader := &repairProbeLoader{
		semanticMemoryJobLoader: &semanticMemoryJobLoader{files: repairRawFiles(pre)},
	}
	for path, content := range pre {
		loader.memoryFiles = append(
			loader.memoryFiles,
			MemoryFile{Path: path, Bytes: int64(len(content))},
		)
	}
	job := NewSemanticMemoryExtractionRegistration(0, 0).NewJob()
	spec, err := job.Build(ctx, loader)
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := cutRepairChunk(spec.FileSnapshots[memoryRuntimePrefix+repairNotesPath].Content)
	if err != nil || len(chunk) != 1 {
		t.Fatalf("fixture must cut only the first identical line; len=%d err=%v", len(chunk), err)
	}
	loader.files[repairNotesPath] = []byte(line + "\n")
	loader.files[workingLedgerRelativePath] = []byte(repairLedgerPre + fmt.Sprintf(
		"2026-10-16T05:00Z run=%s src=mem:%s:0 dropped %q (obsolete first occurrence)\n",
		semanticMemoryExtractionJobType, repairNotesPath, line,
	))
	_, err = job.ApplyResult(ctx, loader, JobOutput{
		Type: semanticMemoryExtractionJobType, Spec: spec,
		Messages: repairRunMessages(memoryRuntimePrefix+repairNotesPath, workingLedgerRuntimePath),
	})
	if err != nil {
		t.Fatalf(
			"repair refused removal of the cited occurrence while retaining the identical uncited tail: %v",
			err,
		)
	}
}

func TestRepairRejectsUnboundedCitationOffsets(t *testing.T) {
	for _, offset := range []string{"4294967308", "9223372036854775820", "18446744073709551615"} {
		t.Run(offset, func(t *testing.T) {
			fixture := repairHappyFixture()
			fixture.ledgerPost = strings.Replace(fixture.ledgerPost,
				fmt.Sprintf("src=mem:%s:%d", repairNotesPath, repairAlphaOffset()),
				"src=mem:"+repairNotesPath+":"+offset, 1)
			_, loader, err := runRepairApply(t, fixture)
			if err == nil || !strings.Contains(err.Error(), "does not name a content line") {
				t.Fatalf(
					"unbounded citation %s: error=%v, want exact chunk membership refusal",
					offset,
					err,
				)
			}
			if got := string(loader.files[repairNotesPath]); got != repairNotesPre {
				t.Fatalf("target not restored after invalid offset: %q", got)
			}
		})
	}
}

func TestRepairMergePreservesPriorClaimSources(t *testing.T) {
	for _, keepSource := range []bool{true, false} {
		t.Run(fmt.Sprintf("keep_prior_source_%v", keepSource), func(t *testing.T) {
			fixture := repairHappyFixture()
			fixture.ledgerPost = strings.Replace(
				fixture.ledgerPost,
				"→ c-2026-0007",
				"→ c-2026-0001",
				1,
			)
			source := fmt.Sprintf("mem:%s:%d", repairNotesPath, repairAlphaOffset())
			if keepSource {
				source = "t12, " + source
			}
			fixture.factsPost = strings.Replace(validConversionClaim, "src: t12", "src: "+source, 1)
			_, _, err := runRepairApply(t, fixture)
			if keepSource && err != nil {
				t.Fatalf("legitimate merge retaining prior and new sources failed: %v", err)
			}
			if !keepSource &&
				(err == nil || !strings.Contains(err.Error(), "removed existing provenance t12")) {
				t.Fatalf("merge dropping prior evidence: error=%v, want provenance refusal", err)
			}
		})
	}
}

func TestRepairPreservesExistingMemoryClaimSource(t *testing.T) {
	prior := strings.Replace(validConversionClaim, "src: t12", "src: mem:working/old.md:12", 1)
	post := strings.Replace(
		prior,
		"src: mem:working/old.md:12",
		"src: mem:"+repairNotesPath+":12",
		1,
	)
	loader := &semanticMemoryJobLoader{
		files: map[string][]byte{semanticFactsRelativePath: []byte(post)},
	}
	err := checkRepairClaimExits(context.Background(), loader,
		map[string]string{semanticFactsRuntimePath: prior}, nil, nil)
	if err == nil ||
		!strings.Contains(err.Error(), "removed existing provenance mem:working/old.md:12") {
		t.Fatalf("existing memory citation lost: error=%v", err)
	}
}

func TestWorkingRepairEnforcesConfiguredLoopBudget(t *testing.T) {
	loader, spec := workingRepairSpec(t)
	const intention = "- Ship the unfinished release."
	pre := string(spec.FileSnapshots[workingMemoryRuntimePath].Content)
	offset := strings.Index(pre, intention)
	loader.files[workingMemoryRelativePath] = []byte(strings.Replace(pre, intention+"\n", "", 1))
	loop := fmt.Sprintf(
		"- [l-2026-0001] Ship the unfinished release.\n  {trigger: next-session, owner: agent, src: mem:%s:%d}\n",
		workingMemoryRelativePath,
		offset,
	)
	loader.files[openLoopsRelativePath] = []byte(loop)
	loader.files[workingLedgerRelativePath] = []byte(repairLedgerPre + fmt.Sprintf(
		"2026-10-16T05:00Z run=%s src=mem:%s:%d moved %q → l-2026-0001 (working/OPEN_LOOPS.md)\n",
		semanticMemoryExtractionJobType, workingMemoryRelativePath, offset, intention))
	_, err := NewSemanticMemoryExtractionRegistration(0, len(loop)-1).NewJob().ApplyResult(
		context.Background(), loader, JobOutput{Spec: spec, Messages: repairRunMessages(
			workingMemoryRuntimePath, openLoopsRuntimePath, workingLedgerRuntimePath)})
	if err == nil || !strings.Contains(err.Error(), "open loops over byte budget") {
		t.Fatalf("configured open-loop budget bypassed: error=%v", err)
	}
}

func TestRepairMovedLoopsPreserveExistingIntentions(t *testing.T) {
	prior := "- [l-2026-0002] Check the existing release.\n  {trigger: next-session, owner: user, src: t42}\n"
	source := "mem:working/WORKING_MEMORY.md:34"
	fresh := "- [l-2026-0001] Ship the unfinished release.\n  {trigger: next-session, owner: agent, src: " + source + "}\n"
	entries := []claims.LedgerEntry{{Outcome: claims.OutcomeMoved, TargetID: "l-2026-0001",
		SourceSeq: source, Description: "- Ship the unfinished release."}}
	tests := []struct{ name, after, want string }{
		{"append valid loop", prior + fresh, ""},
		{"remove prior loop", fresh, "changed or removed existing open loop"},
		{
			"alter prior loop",
			strings.Replace(prior, "existing release", "different release", 1) + fresh,
			"changed or removed existing open loop",
		},
		{
			"missing memory source",
			prior + strings.Replace(fresh, source, "t99", 1),
			"requires a new open loop citing",
		},
		{
			"alter moved intention",
			prior + strings.Replace(fresh, "unfinished release", "different task", 1),
			"did not preserve its full intention",
		},
		{
			"unledgered loop",
			prior + fresh + "- [l-2026-0003] An extra intention.\n  {trigger: next-session, owner: agent, src: t99}\n",
			"has no moved repair exit",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			loader := &semanticMemoryJobLoader{
				files: map[string][]byte{openLoopsRelativePath: []byte(test.after)},
			}
			err := checkRepairOpenLoops(
				context.Background(),
				loader,
				map[string]string{openLoopsRuntimePath: prior},
				entries,
				0,
			)
			if test.want == "" && err != nil {
				t.Fatalf("valid move: %v", err)
			}
			if test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestRepairRejectsResidualWritesFromFailedTools(t *testing.T) {
	for _, surface := range []string{"oversized root", "unledgered topic claim"} {
		t.Run(surface, func(t *testing.T) {
			loader := &repairProbeLoader{semanticMemoryJobLoader: &semanticMemoryJobLoader{
				files: repairRawFiles(repairPreFiles()),
			}, memoryFiles: repairPreListing()}
			job := NewSemanticMemoryExtractionRegistration(0, 0).NewJob()
			spec, err := job.Build(context.Background(), loader)
			if err != nil {
				t.Fatal(err)
			}
			fixture := repairHappyFixture()
			loader.files[repairNotesPath] = []byte(*fixture.targetPost)
			loader.files[semanticFactsRelativePath] = []byte(fixture.factsPost)
			loader.files[workingLedgerRelativePath] = []byte(fixture.ledgerPost)
			want := "over cap"
			if surface == "oversized root" {
				loader.files[semanticPreferencesRelativePath] = []byte(
					strings.Repeat("x", semanticRootCapBytes+1),
				)
			} else {
				const relative = "semantic/topics/residual.md"
				loader.files[relative] = []byte(strings.Replace(validConversionClaim, "c-2026-0001", "c-2026-0008", 1))
				loader.semanticSizes = map[string]int64{relative: int64(len(loader.files[relative]))}
				want = "has no repair ledger exit"
			}
			// Only the ordinary happy-path writes succeeded. The residual
			// bytes still have to be checked regardless of the tool log.
			_, err = job.ApplyResult(context.Background(), loader, JobOutput{
				Spec: spec, Messages: repairRunMessages(fixture.writes...),
			})
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("residual %s escaped validation: error=%v, want %q", surface, err, want)
			}
		})
	}
}

func TestRepairRejectsReusedHistoricalClaimID(t *testing.T) {
	loader := &semanticMemoryJobLoader{
		files: map[string][]byte{
			semanticFactsRelativePath: []byte(validConversionClaim + repairAdoptedClaim()),
		},
		ledgerHistory: "2026-10-15T00:00Z run=semantic_memory.extract src=t1 retracted c-2026-0007 (no longer true)\n",
	}
	entry, err := claims.ParseLedgerEntry(strings.TrimSpace(repairConvertedExit()))
	if err != nil {
		t.Fatal(err)
	}
	err = checkRepairClaimExits(context.Background(), loader,
		map[string]string{semanticFactsRuntimePath: validConversionClaim},
		[]claims.LedgerEntry{entry}, []string{semanticFactsRuntimePath})
	if err == nil || !strings.Contains(err.Error(), "previously used and cannot be reused") {
		t.Fatalf("historical claim ID was reused by repair: error=%v", err)
	}
}
