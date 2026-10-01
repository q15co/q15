package bridge

import (
	"context"
	"testing"

	"github.com/q15co/q15/libs/chat-contract/chatpb"
)

func TestCheckVersion(t *testing.T) {
	for _, test := range []struct {
		name  string
		info  *chatpb.GetRuntimeInfoResponse
		valid bool
	}{
		{"equal", &chatpb.GetRuntimeInfoResponse{ProtocolVersion: chatpb.ProtocolVersion}, true},
		{"older", &chatpb.GetRuntimeInfoResponse{ProtocolVersion: chatpb.ProtocolVersion - 1}, false},
		{"newer", &chatpb.GetRuntimeInfoResponse{ProtocolVersion: chatpb.ProtocolVersion + 1}, false},
		{"garbage", &chatpb.GetRuntimeInfoResponse{ProtocolVersion: -42}, false},
		{"missing", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := CheckVersion(test.info); (got == nil) != test.valid {
				t.Fatalf("CheckVersion = %v, valid %v", got, test.valid)
			}
		})
	}
}

func TestClientRejectsNetworkTargets(t *testing.T) {
	for _, target := range []string{"", "localhost:8081", "tcp://localhost:8081", "unix://relative.sock", "/tmp/socket"} {
		if client, err := NewClient(context.Background(), target); err == nil {
			_ = client.Close()
			t.Errorf("accepted %q", target)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewClient(ctx, "unix:///tmp/bridge.sock"); err == nil {
		t.Fatal("accepted canceled context")
	}
}
