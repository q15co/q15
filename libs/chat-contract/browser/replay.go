package browser

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/q15co/q15/libs/chat-contract/browser/protocol"
	"github.com/q15co/q15/libs/chat-contract/chatpb"
)

// MaxReplayTurns counts readable records, not sequence distance (seqs have gaps).
const MaxReplayTurns = 500

func (s *Endpoint) replay(
	ctx context.Context,
	c *socketConn,
	device, ref string,
	cursor int64,
) bool {
	page, err := s.service.ListTurns(ctx, &chatpb.ListTurnsRequest{Limit: MaxReplayTurns})
	if err != nil {
		s.socketError(c, "bridge_unavailable", ref)
		return false
	}
	if cursor > page.GetHeadSeq() {
		s.resync(c, ref, page.GetHeadSeq())
		return false
	}
	turns := page.GetTurns()
	// A full page whose oldest turn is above the cursor needs one boundary
	// probe to distinguish exactly 500 missed records from 501 (including gaps).
	if len(turns) == MaxReplayTurns && turns[len(turns)-1].GetSeq() > cursor && page.GetHasMore() {
		older, err := s.service.ListTurns(
			ctx,
			&chatpb.ListTurnsRequest{AfterSeq: turns[len(turns)-1].GetSeq(), Limit: 1},
		)
		if err != nil {
			s.socketError(c, "bridge_unavailable", ref)
			return false
		}
		if len(older.GetTurns()) > 0 && older.GetTurns()[0].GetSeq() > cursor {
			s.resync(c, ref, page.GetHeadSeq())
			return false
		}
	}
	completedCursor := cursor
	if len(turns) > 0 && turns[0].GetSeq() > completedCursor {
		completedCursor = turns[0].GetSeq()
	}
	frames := make([][]byte, 0)
	replayBytes := 0
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].GetSeq() <= cursor {
			continue
		}
		turn := protocol.FromPage(&chatpb.ListTurnsResponse{Turns: []*chatpb.Turn{turns[i]}}).Turns[0]
		for _, message := range turn.Messages {
			var text strings.Builder
			for _, part := range message.Parts {
				if part.PartType == "text" {
					text.WriteString(part.Text)
				}
			}
			payload := protocol.FinalPayload{
				Msg:      protocol.MessageID{Turn: turn.Seq, Ordinal: message.Ordinal},
				FullText: text.String(),
				Status:   "completed",
				Message:  &message,
			}
			data, err := c.encode(
				protocol.New(
					protocol.Final,
					fmt.Sprintf("turn:%d:msg:%d", turn.Seq, message.Ordinal),
					0,
					turn.CreatedAt,
					payload,
				),
			)
			if err != nil || replayBytes+len(data) > maxQueueBytes/2 ||
				len(frames) >= maxQueueFrames/2 {
				s.resync(c, ref, page.GetHeadSeq())
				return false
			}
			replayBytes += len(data)
			frames = append(frames, data)
		}
	}
	for _, data := range frames {
		if !c.enqueueBytes(data) {
			c.stop()
			return false
		}
	}
	// Announce the completed cursor after replay, so a disconnect cannot persist
	// a cursor ahead of history frames still waiting to be delivered.
	if !c.enqueue(protocol.New(protocol.Ready, ref, 0, time.Now(), protocol.ReadyPayload{
		HeadSeq: page.GetHeadSeq(), Cursor: completedCursor, DeviceID: device})) {
		c.stop()
		return false
	}
	return true
}

func (s *Endpoint) resync(c *socketConn, ref string, head int64) {
	if !c.enqueue(protocol.New(protocol.Error, ref, 0, time.Now(),
		protocol.ErrorPayload{Code: "resync_from_head", Ref: ref, HeadSeq: head})) {
		c.stop()
	}
}
