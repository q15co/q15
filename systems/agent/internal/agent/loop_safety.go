package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	defaultToolLoopHistorySize       = 30
	defaultToolLoopWarningThreshold  = 10
	defaultToolLoopCriticalThreshold = 20
)

type toolLoopRecord struct {
	signature  string
	outputHash string
}

type toolLoopAssessment struct {
	RepeatCount     int
	NoProgressCount int
	Critical        bool
}

type toolLoopDetector struct {
	history            []toolLoopRecord
	historySize        int
	criticalThreshold  int
	maxRepeatCount     int
	maxNoProgressCount int
}

func newToolLoopDetector() *toolLoopDetector {
	criticalThreshold := defaultToolLoopCriticalThreshold
	if criticalThreshold <= defaultToolLoopWarningThreshold {
		criticalThreshold = defaultToolLoopWarningThreshold + 1
	}
	return &toolLoopDetector{
		historySize:       defaultToolLoopHistorySize,
		criticalThreshold: criticalThreshold,
	}
}

func (d *toolLoopDetector) Record(call ToolCall, output string) toolLoopAssessment {
	signature := hashText(call.Name + "\n" + normalizeToolArguments(call.Arguments))
	outputHash := hashText(strings.TrimSpace(output))

	d.history = append(d.history, toolLoopRecord{
		signature:  signature,
		outputHash: outputHash,
	})
	if len(d.history) > d.historySize {
		d.history = d.history[len(d.history)-d.historySize:]
	}

	repeatCount := 0
	for _, rec := range d.history {
		if rec.signature == signature {
			repeatCount++
		}
	}

	noProgressCount := 0
	for i := len(d.history) - 1; i >= 0; i-- {
		rec := d.history[i]
		if rec.signature != signature || rec.outputHash != outputHash {
			break
		}
		noProgressCount++
	}

	if repeatCount > d.maxRepeatCount {
		d.maxRepeatCount = repeatCount
	}
	if noProgressCount > d.maxNoProgressCount {
		d.maxNoProgressCount = noProgressCount
	}

	return toolLoopAssessment{
		RepeatCount:     repeatCount,
		NoProgressCount: noProgressCount,
		Critical: repeatCount >= d.criticalThreshold ||
			noProgressCount >= d.criticalThreshold,
	}
}

func (d *toolLoopDetector) MaxRepeatCount() int {
	return d.maxRepeatCount
}

func (d *toolLoopDetector) MaxNoProgressCount() int {
	return d.maxNoProgressCount
}

func normalizeToolArguments(arguments string) string {
	arguments = strings.TrimSpace(arguments)
	if arguments == "" {
		return ""
	}

	var payload any
	if err := json.Unmarshal([]byte(arguments), &payload); err != nil {
		return arguments
	}

	canonical, err := json.Marshal(payload)
	if err != nil {
		return arguments
	}
	return string(canonical)
}

func hashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func formatStopSummary(
	reason StopReason,
	maxTurns int,
	repeatCount int,
	noProgressCount int,
) string {
	switch reason {
	case StopReasonToolLoopDetected:
		return fmt.Sprintf(
			"Run stopped by internal safety guard: detected repeated tool-call loop (repeat=%d, no_progress=%d). Progress up to this point has been saved.",
			repeatCount,
			noProgressCount,
		)
	case StopReasonToolTurnLimit:
		return fmt.Sprintf(
			"Run stopped by internal safety guard: reached maximum tool-call turns (%d). Observed loop indicators (max_repeat=%d, max_no_progress=%d). Progress up to this point has been saved.",
			maxTurns,
			repeatCount,
			noProgressCount,
		)
	default:
		return "Run stopped by internal safety guard. Progress up to this point has been saved."
	}
}
