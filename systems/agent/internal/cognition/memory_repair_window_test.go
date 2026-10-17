package cognition

import (
	"context"
	"strings"
	"testing"
)

func TestRepairShortLinesKeepPromptBounded(t *testing.T) {
	pre := repairPreFiles()
	delete(pre, repairNotesPath)
	pre[semanticFactsRelativePath] = strings.Repeat("x\n", 1024*1024)
	loader := &repairProbeLoader{
		semanticMemoryJobLoader: &semanticMemoryJobLoader{files: repairRawFiles(pre)},
	}
	for relative, content := range pre {
		loader.memoryFiles = append(
			loader.memoryFiles,
			MemoryFile{Path: relative, Bytes: int64(len(content))},
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
	if !spec.repair || len(loader.forbidden) != 0 {
		t.Fatalf("repair=%v forbidden loads=%v", spec.repair, loader.forbidden)
	}
	prompt, err := renderPrompt(semanticMemoryExtractionJobType, spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(prompt) >= 32*1024 {
		t.Fatalf("short-line repair prompt is %d bytes, want below 32768", len(prompt))
	}
}

func TestRepairChunkBoundsLineFramingAndPreservesOffsets(t *testing.T) {
	head := []byte(strings.Repeat("x\n", 4096))
	chunk, err := cutRepairChunk(head)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunk) != 128 {
		t.Fatalf("chunk contains %d short lines, want 128", len(chunk))
	}
	for i, line := range chunk {
		if line.offset != i*2 || line.text != "x" {
			t.Fatalf("line %d = offset %d, text %q", i, line.offset, line.text)
		}
	}
	if chunk[len(chunk)-1].offset+2 >= len(head) {
		t.Fatal("fixture must retain uncited work for a later bounded run")
	}
}

func TestRepairNamedStructureCanMakeBoundedProgress(t *testing.T) {
	const prefix = "# Semantic Facts\n\n## Confirmed Facts\n\n"
	pre := repairPreFiles()
	delete(pre, repairNotesPath)
	pre[semanticFactsRelativePath] = prefix + strings.Repeat("## Extra\n", 3000) +
		"\n## Grounded Inferences\n\n- None\n"
	loader := &repairProbeLoader{
		semanticMemoryJobLoader: &semanticMemoryJobLoader{files: repairRawFiles(pre)},
	}
	for relative, content := range pre {
		loader.memoryFiles = append(
			loader.memoryFiles,
			MemoryFile{Path: relative, Bytes: int64(len(content))},
		)
	}
	job := NewSemanticMemoryExtractionRegistration(0, 0).NewJob()
	spec, err := job.Build(context.Background(), loader)
	if err != nil {
		t.Fatal(err)
	}
	chunk, err := cutRepairChunk([]byte(pre[semanticFactsRelativePath]))
	if err != nil || len(chunk) == 0 {
		t.Fatalf("chunk=%v, err=%v", chunk, err)
	}
	last := chunk[len(chunk)-1]
	end := last.offset + len(last.text) + 1
	post := prefix + pre[semanticFactsRelativePath][end:]
	loader.files[semanticFactsRelativePath] = []byte(post)
	_, err = applyWrittenRepairForTest(context.Background(), loader, JobOutput{
		Type: semanticMemoryExtractionJobType, Spec: spec,
		Messages: repairRunMessages(semanticFactsRuntimePath),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(loader.files[semanticFactsRelativePath])
	if got != post || len(got) >= len(pre[semanticFactsRelativePath]) {
		t.Fatalf(
			"repair did not preserve the uncited suffix while shrinking structure: %d -> %d",
			len(pre[semanticFactsRelativePath]),
			len(got),
		)
	}
	for _, heading := range []string{"## Confirmed Facts", "## Grounded Inferences"} {
		if !strings.Contains(got, heading) {
			t.Fatalf("repair removed canonical heading %q", heading)
		}
	}
}

func TestRepairPreservesExistingCanonicalHeadings(t *testing.T) {
	for _, tc := range []struct {
		path, heading string
	}{
		{semanticFactsRelativePath, "Confirmed Facts"},
		{semanticFactsRelativePath, "Grounded Inferences"},
		{semanticPreferencesRelativePath, "User Preferences"},
		{semanticPreferencesRelativePath, "Collaboration Preferences"},
		{semanticProjectsRelativePath, "Active Projects"},
		{semanticProjectsRelativePath, "Durable Project Knowledge"},
		{workingMemoryRelativePath, "Active Tasks"},
	} {
		t.Run(tc.path+"/"+tc.heading, func(t *testing.T) {
			before := []byte("## " + tc.heading + "\n## Extra\n")
			if err := checkRepairHeadings(tc.path, before, []byte("## Extra\n")); err == nil {
				t.Fatal("repair may not remove an existing canonical heading")
			}
			if err := checkRepairHeadings(tc.path, before, []byte("## "+tc.heading+"\n")); err != nil {
				t.Fatalf("removing unrelated structure: %v", err)
			}
			if err := checkRepairHeadings(tc.path, []byte("# Legacy\n"), []byte{}); err != nil {
				t.Fatalf("legacy input without this heading: %v", err)
			}
		})
	}
}
