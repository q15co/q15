package browser

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/q15co/q15/libs/chat-contract/browser/protocol"
	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestHistoryByteBudgetPreservesCompleteTurnsAndCursor(t *testing.T) {
	turns := []*chatpb.Turn{}
	for seq := int64(3); seq > 0; seq-- {
		turns = append(turns, &chatpb.Turn{Seq: seq, Messages: []*chatpb.Message{
			{
				Role:  "assistant",
				Parts: []*chatpb.MessagePart{{PartType: "text", Text: strings.Repeat("\x00", 100)}},
			},
		}})
	}
	first, err := historyPayload(&chatpb.ListTurnsResponse{HeadSeq: 4, Turns: turns[:1]}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	budget := len(first)
	for _, more := range []bool{false, true} {
		remaining := turns
		for len(remaining) > 0 {
			data, err := historyPayload(
				&chatpb.ListTurnsResponse{HeadSeq: 4, Turns: remaining, HasMore: more},
				budget,
			)
			if err != nil || len(data) > budget {
				t.Fatal("page exceeded byte budget", len(data), budget, err)
			}
			var page protocol.Page
			if err := json.Unmarshal(data, &page); err != nil {
				t.Fatal(err)
			}
			if len(page.Turns) != 1 || page.Turns[0].Seq != remaining[0].GetSeq() ||
				page.HeadSeq != 4 || page.HasMore != (len(remaining) > 1 || more) ||
				page.Turns[0].Messages[0].Parts[0].Text != remaining[0].GetMessages()[0].GetParts()[0].GetText() {
				t.Fatal("history lost a complete record or continuation", page)
			}
			remaining = remaining[1:]
		}
	}
	if _, err := historyPayload(&chatpb.ListTurnsResponse{Turns: turns}, budget-2); status.Code(
		err,
	) != codes.ResourceExhausted {
		t.Fatal("oversized first turn did not report exhaustion", err)
	}
}
