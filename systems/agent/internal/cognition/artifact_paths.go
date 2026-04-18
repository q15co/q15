package cognition

const (
	// VerificationReviewPath is the cognition-relative path for the
	// persisted verification review artifact.
	VerificationReviewPath = "state/verification_review.md"

	// VerificationReviewRuntimePath is the runtime-visible path for the
	// persisted verification review artifact.
	VerificationReviewRuntimePath = "/memory/cognition/" + VerificationReviewPath
)
