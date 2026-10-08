package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/q15co/q15/libs/chat-contract/browser/protocol"
	"github.com/q15co/q15/libs/chat-contract/browser/seal"
	"github.com/q15co/q15/libs/chat-contract/chatpb"
)

func TestBrowserBoundaryUnsealsInputAndRewrapsCanonicalHistory(t *testing.T) {
	publisher := &fakePublisher{}
	lister := &fakeTurnLister{page: fixtureBridgePage()}
	service := NewService(lister, NewAgentEndpoint(publisher), nil)
	client := startChatService(t, service)
	before, err := json.Marshal(lister.page)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.BrowserChannel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	binding := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := stream.Send(&chatpb.BrowserPacket{Principal: "owner", Binding: binding}); err != nil {
		t.Fatal(err)
	}
	private, public, err := seal.Offer()
	if err != nil {
		t.Fatal(err)
	}
	hello := protocol.New(
		protocol.Hello,
		"hello",
		0,
		time.Now(),
		protocol.HelloPayload{PublicKey: public, Binding: binding},
	)
	data, err := json.Marshal(hello)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&chatpb.BrowserPacket{Frame: data}); err != nil {
		t.Fatal(err)
	}
	packet, err := stream.Recv()
	if err != nil {
		t.Fatal(err)
	}
	var frame protocol.Frame
	if err := json.Unmarshal(packet.Frame, &frame); err != nil || frame.Type != "key" {
		t.Fatal("key exchange failed", err)
	}
	var key protocol.KeyPayload
	if err := json.Unmarshal(frame.Payload, &key); err != nil {
		t.Fatal(err)
	}
	keys, err := seal.Agree(private, key.PublicKey, binding, public, key.PublicKey, false)
	if err != nil {
		t.Fatal(err)
	}
	replayed := 0
	for {
		packet, err = stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(packet.Frame, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type == protocol.Ready {
			break
		}
		if !seal.Content(frame.Type) {
			continue
		}
		var envelope protocol.Sealed
		if err := json.Unmarshal(frame.Payload, &envelope); err != nil {
			t.Fatal(err)
		}
		var payload bytes.Buffer
		if _, err := keys.Open(envelope, seal.Context(frame), &payload); err != nil {
			t.Fatal(err)
		}
		var final protocol.FinalPayload
		if err := json.Unmarshal(payload.Bytes(), &final); err != nil || final.Message == nil {
			t.Fatal("canonical replay lost parts", err)
		}
		replayed++
	}
	if replayed != len(lister.page.Turns[0].Messages) {
		t.Fatal("canonical messages lost")
	}
	request := protocol.New(
		protocol.Send,
		"send",
		0,
		time.Now(),
		protocol.SendRequest{ClientMsgID: "send", Text: "unsealed on agent"},
	)
	envelope, err := keys.Wrap(seal.JSONType, seal.Context(request), request.Payload)
	if err != nil {
		t.Fatal(err)
	}
	request.Payload, err = json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(request)
	if err != nil || bytes.Contains(data, []byte("unsealed on agent")) {
		t.Fatal("input was not sealed", err)
	}
	if err := stream.Send(&chatpb.BrowserPacket{Frame: data}); err != nil {
		t.Fatal(err)
	}
	for {
		packet, err = stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(packet.Frame, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type == protocol.Status && bytes.Contains(frame.Payload, []byte("send")) {
			break
		}
	}
	if messages := publisher.published(); len(messages) != 1 ||
		messages[0].Text != "unsealed on agent" {
		t.Fatal("agent did not process plaintext after unsealing")
	}
	after, err := json.Marshal(lister.page)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("transport changed canonical transcript", err)
	}
}
