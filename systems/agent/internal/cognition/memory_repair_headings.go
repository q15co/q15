package cognition

import (
	"fmt"
	"strings"
)

func checkRepairHeadings(relative string, pre, post []byte) error {
	var headings []string
	switch relative {
	case semanticFactsRelativePath:
		headings = []string{"Confirmed Facts", "Grounded Inferences"}
	case semanticPreferencesRelativePath:
		headings = []string{"User Preferences", "Collaboration Preferences"}
	case semanticProjectsRelativePath:
		headings = []string{"Active Projects", "Durable Project Knowledge"}
	case workingMemoryRelativePath:
		headings = []string{
			"Current Priorities",
			"Active Tasks",
			"Open Threads",
			"Recent Progress",
			"Pending Checks",
			"Temporary Context",
		}
	}
	if len(headings) == 0 {
		return nil
	}
	lines := func(raw []byte) map[string]bool {
		found := make(map[string]bool)
		for _, line := range strings.Split(string(raw), "\n") {
			found[strings.TrimSpace(line)] = true
		}
		return found
	}
	before, after := lines(pre), lines(post)
	for _, heading := range headings {
		line := "## " + heading
		if before[line] && !after[line] {
			return fmt.Errorf(
				"memory repair removed canonical heading %q from %s",
				heading,
				relative,
			)
		}
	}
	return nil
}
