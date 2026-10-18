package cognition

import (
	"fmt"
	"sort"
	"unicode/utf8"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

const verificationSnapshotBytes = 16 * 1024
const verificationConflictBytes = 64 * 1024

func boundVerificationSection(section agent.PromptSection, limit int) agent.PromptSection {
	if encodedTextBytes(section.Body) <= limit {
		return section
	}
	body := section.Body
	marker := fmt.Sprintf(
		"\n[Evidence omitted after this prefix to fit the review byte budget; original_bytes=%d. This is partial evidence; do not infer absence or completeness.]",
		len(body),
	)
	endAt := func(n int) int {
		for n > 0 && n < len(body) && !utf8.RuneStart(body[n]) {
			n--
		}
		return n
	}
	firstTooLarge := sort.Search(len(body)+1, func(n int) bool {
		return encodedTextBytes(body[:endAt(n)]+marker) > limit
	})
	section.Body = body[:endAt(max(0, firstTooLarge-1))] + marker
	return section
}

func boundedVerificationConflicts(entries []conflictedClaimEntry) ([]conflictedClaimEntry, int) {
	groups, omitted := boundVerificationConflicts(
		groupConflictedClaims(entries), verificationConflictBytes,
		encodedTextBytes(renderConflictedClaims(nil)), renderConflictedClaimGroup,
	)
	var selected []conflictedClaimEntry
	for _, group := range groups {
		selected = append(selected, group...)
	}
	return selected, omitted
}

func boundVerificationConflicts(
	groups [][]conflictedClaimEntry,
	limit, overhead int,
	renderGroup func([]conflictedClaimEntry) string,
) ([][]conflictedClaimEntry, int) {
	var selected [][]conflictedClaimEntry
	omitted := 0
	used := overhead
	for _, group := range groups {
		// Leave room to disclose excluded groups without splitting a claim or
		// requiring a resolution for evidence the model did not receive.
		size := encodedTextBytes(renderGroup(group)) + 2
		if used+size > limit-1024 {
			omitted++
			continue
		}
		selected = append(selected, group)
		used += size
	}
	return selected, omitted
}

func boundVerificationSections(sections []agent.PromptSection) []agent.PromptSection {
	for i, section := range sections {
		switch section.Name {
		case "working_memory_snapshot", "core_memory_snapshot", "verification_review_target":
			sections[i] = boundVerificationSection(section, verificationSnapshotBytes)
		case "transcript_artifact":
			sections[i] = boundVerificationSection(section, MaxTranscriptWindowBytes)
		}
	}
	return sections
}
