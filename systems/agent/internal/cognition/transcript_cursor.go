package cognition

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// TranscriptCursor addresses the next unread byte of one immutable turn.
// InputHash binds positions to the canonical prompt-visible turn, not a file path.
type TranscriptCursor struct {
	TurnSeq    int64  `json:"turn_seq"`
	Message    int    `json:"message"`
	Part       int    `json:"part"`
	ByteOffset int    `json:"byte_offset"`
	InputHash  string `json:"input_hash"`
}

type transcriptProgress struct {
	CompletedSeq   int64
	PartialTurn    *TranscriptCursor
	RemainingTurns int
}

func windowProgress(window TurnWindow) *transcriptProgress {
	if window.LastSeq == 0 && window.LastIncludedSeq == 0 && window.PartialTurn == nil {
		return nil
	}
	return &transcriptProgress{
		CompletedSeq:   window.LastSeq,
		PartialTurn:    window.PartialTurn,
		RemainingTurns: window.RemainingTurns,
	}
}

// CutTranscriptTurn includes every part in order, splitting only portable text
// or reasoning at UTF-8 boundaries. It never advances over an omitted part.
func CutTranscriptTurn(
	messages []conversation.Message,
	start *TranscriptCursor,
	limit int,
) ([]conversation.Message, *TranscriptCursor, error) {
	mi, pi, offset := 0, 0, 0
	if start != nil {
		mi, pi, offset = start.Message, start.Part, start.ByteOffset
	}
	if mi < 0 || mi >= len(messages) || pi < 0 || pi >= len(messages[mi].Parts) || offset < 0 {
		return nil, nil, fmt.Errorf("invalid partial transcript position")
	}
	var out []conversation.Message
	for i := mi; i < len(messages); i++ {
		message := conversation.CloneMessages(messages[i : i+1])[0]
		message.Parts = nil
		out = append(out, message)
		firstPart := 0
		if i == mi {
			firstPart = pi
		}
		for j := firstPart; j < len(messages[i].Parts); j++ {
			part := conversation.CloneParts(messages[i].Parts[j : j+1])[0]
			begin := 0
			if i == mi && j == pi {
				begin = offset
			}
			splittable := part.Type == conversation.TextPartType ||
				part.Type == conversation.ReasoningPartType
			if begin > 0 &&
				(!splittable || begin >= len(part.Text) || !utf8.RuneStart(part.Text[begin])) {
				return nil, nil, fmt.Errorf("invalid partial transcript byte offset")
			}
			if splittable {
				part.Text = part.Text[begin:]
			}
			last := len(out) - 1
			out[last].Parts = append(out[last].Parts, part)
			if TranscriptArtifactBytes(out) <= limit {
				continue
			}
			next := &TranscriptCursor{Message: i, Part: j, ByteOffset: begin}
			if splittable {
				remaining := part.Text
				endAt := func(n int) int {
					for n > 0 && n < len(remaining) && !utf8.RuneStart(remaining[n]) {
						n--
					}
					return n
				}
				firstTooLarge := sort.Search(len(remaining)+1, func(n int) bool {
					out[last].Parts[len(out[last].Parts)-1].Text = remaining[:endAt(n)]
					return TranscriptArtifactBytes(out) > limit
				})
				end := endAt(max(0, firstTooLarge-1))
				if end > 0 {
					if boundary := strings.LastIndexAny(remaining[:end], "\n \t"); boundary > 0 &&
						boundary >= end-256 {
						end = boundary + 1
					}
					out[last].Parts[len(out[last].Parts)-1].Text = remaining[:end]
					next.ByteOffset = begin + end
					return out, next, nil
				}
			}
			out[last].Parts = out[last].Parts[:len(out[last].Parts)-1]
			if len(out[last].Parts) == 0 {
				out = out[:last]
			}
			if len(out) == 0 {
				return nil, nil, fmt.Errorf("transcript part framing exceeds byte budget")
			}
			return out, next, nil
		}
	}
	return out, nil, nil
}
