package browser

import (
	"encoding/json"

	"github.com/q15co/q15/libs/chat-contract/browser/protocol"
	"github.com/q15co/q15/libs/chat-contract/chatpb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func historyPayload(page *chatpb.ListTurnsResponse, budget int) ([]byte, error) {
	out := protocol.Page{Turns: []protocol.Turn{}, HeadSeq: page.GetHeadSeq()}
	empty, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	size := len(empty)
	out.HasMore = true
	for _, record := range page.GetTurns() {
		turn := protocol.FromPage(&chatpb.ListTurnsResponse{Turns: []*chatpb.Turn{record}}).Turns[0]
		data, err := json.Marshal(turn)
		if err != nil {
			return nil, err
		}
		extra := len(data)
		if len(out.Turns) > 0 {
			extra++ // separator between complete records
		}
		if size+extra > budget {
			if len(out.Turns) == 0 {
				return nil, status.Error(
					codes.ResourceExhausted,
					"history turn exceeds envelope budget",
				)
			}
			return json.Marshal(out)
		}
		size += extra
		out.Turns = append(out.Turns, turn)
	}
	out.HasMore = page.GetHasMore()
	return json.Marshal(out)
}
