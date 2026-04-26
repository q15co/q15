package cognition

import "strings"

const cognitionSummaryLimit = 200

func accessIncludesPath(paths []string, target string) bool {
	for _, path := range paths {
		if path == target {
			return true
		}
	}
	return false
}

func compactCognitionText(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= cognitionSummaryLimit {
		return text
	}
	return string(runes[:cognitionSummaryLimit-3]) + "..."
}
