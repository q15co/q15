package cognition

import "strings"

const (
	// VerificationReviewPath is the cognition-relative path for the
	// persisted verification review artifact.
	VerificationReviewPath = "state/verification_review.md"

	// VerificationReviewRuntimePath is the runtime-visible path for the
	// persisted verification review artifact.
	VerificationReviewRuntimePath = "/memory/cognition/" + VerificationReviewPath

	// cognitionAlertsDirPath is the cognition-relative directory holding one
	// stall alert artifact per struggling checkpoint-advancing job type.
	cognitionAlertsDirPath = "alerts"
)

// StallAlertPath returns the cognition-relative artifact path under which the
// controller persists the stall alert for one cognition job type.
func StallAlertPath(jobType string) string {
	jobType = strings.ReplaceAll(strings.TrimSpace(jobType), "/", "_")
	if jobType == "" {
		jobType = "job"
	}
	return cognitionAlertsDirPath + "/" + jobType + ".md"
}
