package claims

import (
	"fmt"
	"strings"
)

// ReplaceClaim replaces one claim and its metadata without rendering neighboring
// claims or unparsed text. An empty replacement removes that claim's byte range.
func ReplaceClaim(markdown, id, replacement string) (string, error) {
	lines := strings.SplitAfter(markdown, "\n")
	text := make([]string, len(lines))
	offsets := make([]int, len(lines)+1)
	for i, line := range lines {
		text[i] = strings.TrimSuffix(line, "\n")
		offsets[i+1] = offsets[i] + len(line)
	}
	start, end := -1, -1
	for i := range text {
		match := claimLinePattern.FindStringSubmatch(text[i])
		if match == nil || match[1] != id {
			continue
		}
		if start >= 0 {
			return "", fmt.Errorf("duplicate claim ID %s", id)
		}
		_, last := consumeMetadata(text, i+1)
		start, end = offsets[i], offsets[last+1]
	}
	if start < 0 {
		return "", fmt.Errorf("claim %s not found", id)
	}
	return markdown[:start] + replacement + markdown[end:], nil
}
