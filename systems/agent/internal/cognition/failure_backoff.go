package cognition

import (
	"regexp"
	"strings"
	"time"
)

// failureBackoffDelays is the retry ladder for consecutive identical failures
// of one cognition job (#379 layer 1): 7m, 30m, 2h, then 24h until the park
// threshold.
var failureBackoffDelays = []time.Duration{
	7 * time.Minute,
	30 * time.Minute,
	2 * time.Hour,
	24 * time.Hour,
}

// failureParkThreshold is the consecutive identical-failure count at which a
// job parks: it stops re-firing on every trigger lane until the streak is
// cleared by a success or by the operator, and the stall alert says so.
const failureParkThreshold = 25

var (
	errorQuotedPayload = regexp.MustCompile(`"[^"]*"`)
	errorDigitRun      = regexp.MustCompile(`\d+`)
	errorWhitespace    = regexp.MustCompile(`\s+`)
)

// normalizeErrorSignature reduces an error string to a low-cardinality
// failure class: quoted payloads and digit runs collapse to placeholders, so
// the same condition repeating across runs shares one fingerprint.
func normalizeErrorSignature(raw string) string {
	signature := strings.ToLower(strings.TrimSpace(raw))
	signature = errorQuotedPayload.ReplaceAllString(signature, `"?"`)
	signature = errorDigitRun.ReplaceAllString(signature, "#")
	signature = errorWhitespace.ReplaceAllString(signature, " ")
	const maxSignatureBytes = 160
	if len(signature) > maxSignatureBytes {
		signature = signature[:maxSignatureBytes]
	}
	return signature
}

// failureFingerprint identifies one job's failure streak by error class.
// #379 named (job, input_seq, error signature) as the key; input_seq is
// deliberately excluded: a job that fails while the transcript advances (the
// 2026-10-20 storm) changes input_seq on every run, which would defeat the
// streak entirely. Work position identity already lives in the dirty/checkpoint
// state; what resets a streak is a changed error class or a success.
func failureFingerprint(jobType string, runErr error) string {
	if runErr == nil {
		return ""
	}
	return jobType + ": " + normalizeErrorSignature(runErr.Error())
}

// updateFailureBackoff advances the failure streak for a failed run. A new
// fingerprint starts the streak over at step one; each identical repeat
// lengthens the retry delay up to the 24h cap; at the park threshold the job
// goes silent (retry timer cleared) until the streak is cleared.
func updateFailureBackoff(
	state JobState,
	jobType string,
	runErr error,
	finished time.Time,
) JobState {
	fingerprint := failureFingerprint(jobType, runErr)
	if state.FailureFingerprint != fingerprint {
		state.FailureFingerprint = fingerprint
		state.FailureBackoffStep = 0
	}
	state.FailureBackoffStep++
	if failureParked(state) {
		state.FailureRetryAt = time.Time{}
		return state
	}
	delay := failureBackoffDelays[min(state.FailureBackoffStep-1, len(failureBackoffDelays)-1)]
	state.FailureRetryAt = finished.Add(delay)
	return state
}

// resetFailureBackoff clears the streak after a run that did not fail with an
// error (success or no-work), so a recovered job resumes at its full cadence.
func resetFailureBackoff(state JobState) JobState {
	state.FailureFingerprint = ""
	state.FailureBackoffStep = 0
	state.FailureRetryAt = time.Time{}
	return state
}

// failureParked reports whether the streak passed the park threshold: the job
// must not fire on any trigger until the streak is cleared.
func failureParked(state JobState) bool {
	return state.FailureFingerprint != "" && state.FailureBackoffStep >= failureParkThreshold
}

// failureBackoffWaiting reports whether the job sits inside an armed backoff
// window whose retry time is still in the future.
func failureBackoffWaiting(state JobState, now time.Time) bool {
	if failureParked(state) {
		return false
	}
	return !state.FailureRetryAt.IsZero() && now.Before(state.FailureRetryAt)
}
