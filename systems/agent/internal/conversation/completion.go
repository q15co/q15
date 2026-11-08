package conversation

import "strings"

// SubagentCompletionName identifies runtime-pushed results encoded as a tool
// exchange so providers keep delegated output in the untrusted tool role.
const SubagentCompletionName = "subagent_completion"

// IsSubagentCompletion distinguishes runtime notifications from model tool use,
// including notifications persisted before they had a dedicated name.
func IsSubagentCompletion(part Part) bool {
	return part.Type == ToolCallPartType &&
		strings.HasPrefix(part.ID, "completion-") &&
		(part.Name == SubagentCompletionName || part.Name == "subagent_read")
}
