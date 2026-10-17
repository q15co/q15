package claims

import (
	"reflect"
	"strings"
	"testing"
)

func TestClaimPreservesMixedMemoryAndTranscriptSources(t *testing.T) {
	const markdown = "- [c-2026-0001] A durable fact.\n" +
		"  {status: active, kind: fact, confidence: confirmed, src: t42, mem:semantic/topics/read..md:14, mem:working/WORKING_MEMORY.md:99, t42, rev: 2026-10-17}\n"
	want := []string{"t42", "mem:semantic/topics/read..md:14", "mem:working/WORKING_MEMORY.md:99"}
	parsed, _, err := ParseClaims(markdown)
	if err != nil || len(parsed) != 1 {
		t.Fatalf("ParseClaims() = %v, %v", parsed, err)
	}
	if !reflect.DeepEqual(parsed[0].Src, want) {
		t.Fatalf("sources = %v, want %v", parsed[0].Src, want)
	}
	rendered := RenderClaim(parsed[0])
	if !strings.Contains(
		rendered,
		"src: t42, mem:semantic/topics/read..md:14, mem:working/WORKING_MEMORY.md:99, rev:",
	) {
		t.Fatalf("rendered claim lost provenance: %s", rendered)
	}
	again, _, err := ParseClaims(rendered)
	if err != nil || len(again) != 1 || !reflect.DeepEqual(again[0].Src, want) {
		t.Fatalf("rendered claim sources = %v, err=%v", again, err)
	}
}

func TestMemorySourceRejectsNonCanonicalPaths(t *testing.T) {
	for _, source := range []string{
		"mem:semantic/../core/identity.md:0",
		"mem:working/../../history/turn.md:0",
		"mem:semantic//facts.md:0",
		"mem:semantic/./facts.md:0",
		"mem:working/:0",
	} {
		if ValidMemorySource(source) {
			t.Fatalf("accepted noncanonical source %q", source)
		}
	}
	if !ValidMemorySource("mem:semantic/topics/read..md:14") {
		t.Fatal("a filename containing dots is still a valid portable source")
	}
}
