package cognition

import (
	"fmt"
	"sort"
	"strings"
)

func renderRepairDestinationSizes(snapshots map[string]FileSnapshot) string {
	var lines []string
	for runtimePath, snapshot := range snapshots {
		if !semanticClaimPath(runtimePath) {
			continue
		}
		relative := relativeMemoryPath(runtimePath)
		capBytes := SemanticCapBytes(relative)
		lines = append(lines, fmt.Sprintf("%s bytes=%d cap=%d remaining=%d",
			relative, len(snapshot.Content), capBytes, max(0, capBytes-len(snapshot.Content))))
	}
	sort.Strings(lines)
	return "Destination sizes at this run's snapshot, including existing headings and metadata:\n" + strings.Join(
		lines,
		"\n",
	)
}

func renderTypedRepairContract(target MemorySurfaceTarget) string {
	contract := renderPromptLines(
		"Return exactly one JSON object, with no Markdown fence or prose: {\"decisions\":[...]}. File mutations are performed only by the runtime after validation; read_file and embed_search are optional evidence tools.",
		"Return exactly one decision for every content line in repair_chunk, keyed by its supplied offset. Headings, blank lines and exact - None placeholders need no decision. Never return a source description, source citation, timestamp, ledger line or replacement file content.",
		"Converted decision: {\"offset\":123,\"outcome\":\"converted\",\"claim_id\":\"c-YYYY-NNNN\",\"claim\":{\"path\":\"semantic/topics/example.md\",\"text\":\"Self-contained statement.\",\"status\":\"active\",\"kind\":\"fact\",\"confidence\":\"confirmed\"}}. Choose valid status, kind and confidence from the shared claim rules. The runtime writes canonical metadata with original source citations and the maintenance date. Several decisions may name one claim; give identical definitions or define it once.",
		"For an existing claim, you may omit claim to keep its statement and destination and add provenance. To revise or relocate it, provide the claim definition and retain its stable ID. The runtime preserves its previous sources and neighboring bytes. Read the existing claim when choosing a merge; do not guess an ID.",
		fmt.Sprintf(
			"Never choose the repair target %s as a claim destination. A destination must finish within its cap; oversized roots must drain into other canonical files or topics.",
			target.Path,
		),
		fmt.Sprintf(
			"A written semantic root must fit %d bytes and a topic file must fit %d bytes, including headings and metadata.",
			semanticRootCapBytes,
			semanticTopicCapBytes,
		),
		"Use repair_destination_sizes to choose destinations with enough remaining bytes for the entire chunk, including rendered claim metadata and added provenance on existing claims. If a destination lacks room, choose a suitable new sibling semantic/topics/<slug>.md file and allow for its heading. The runtime refuses an overshooting plan before writing; retrying the same full destination cannot make progress.",
		"Dropped decision: {\"offset\":123,\"outcome\":\"dropped\",\"reason\":\"Why this transient or obsolete line should leave.\"}. Supply no claim or loop fields. The reason must be nonempty single-line text with balanced parentheses so the existing ledger grammar can represent it.",
		"The runtime derives ledger descriptions from the original cut lines, appends records without changing history, removes only covered occurrences, preserves the uncovered suffix byte-for-byte, and retains canonical headings. It refuses a content line with no decision, naming its offset, and refuses invalid claim, loop or ledger data before writing; a decision for a line this chunk does not drain is ignored and a repeated offset keeps the later one. A structure-only chunk requires an empty decisions array.",
	)
	if target.Path == workingMemoryRelativePath {
		contract += "\n" + renderPromptLines(
			"Preserve unfinished intentions with a moved decision: {\"offset\":123,\"outcome\":\"moved\",\"loop\":{\"id\":\"l-YYYY-NNNN\",\"trigger\":\"next-session\",\"owner\":\"agent\"}}. The runtime takes the full intention from the original line, writes its source citation and appends the new open loop without changing existing loops. Choose a unique new loop ID and canonical trigger/owner metadata; the complete open-loops file must fit its configured budget.",
			OpenLoopsLineFormat,
		)
	}
	return contract
}
