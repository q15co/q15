package cognition

import (
	"fmt"
	"strings"
)

// validateMemoryBuildEvidence rejects interleaved writes before a journal is
// created. The journal then rechecks these same snapshots under its mutation
// lease, so neither an earlier prompt read nor a later snapshot can hide a write.
func validateMemoryBuildEvidence(
	snapshots map[string]FileSnapshot,
	baseline map[string]string,
	prompt map[string]string,
) error {
	for runtimePath, snapshot := range snapshots {
		before, ok := baseline[runtimePath]
		if !ok || before != string(snapshot.Content) {
			return fmt.Errorf(
				"memory changed while building job evidence: baseline differs from recovery snapshot for %s",
				runtimePath,
			)
		}
	}
	for runtimePath := range baseline {
		if _, ok := snapshots[runtimePath]; !ok {
			return fmt.Errorf(
				"memory changed while building job evidence: recovery snapshot is missing %s",
				runtimePath,
			)
		}
	}
	for runtimePath, content := range prompt {
		snapshot, ok := snapshots[runtimePath]
		if !ok || strings.TrimSpace(content) != strings.TrimSpace(string(snapshot.Content)) {
			return fmt.Errorf(
				"memory changed while building job evidence: prompt differs from recovery snapshot for %s",
				runtimePath,
			)
		}
	}
	return nil
}
