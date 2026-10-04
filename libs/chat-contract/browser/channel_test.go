package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/q15co/q15/libs/chat-contract/browser/protocol"
	"github.com/q15co/q15/libs/chat-contract/browser/seal"
)

func keyedConnection(t *testing.T) (*socketConn, *seal.Keys) {
	t.Helper()
	browserKey, browserPublic, err := seal.Offer()
	if err != nil {
		t.Fatal(err)
	}
	agentKey, agentPublic, err := seal.Offer()
	if err != nil {
		t.Fatal(err)
	}
	keys, err := seal.Agree(agentKey, browserPublic, "binding", browserPublic, agentPublic, true)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := seal.Agree(browserKey, agentPublic, "binding", browserPublic, agentPublic, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &socketConn{ctx: ctx, cancel: cancel, keys: keys, queue: make(chan []byte, 2)}, peer
}

func TestFrameBudgetCarriesFullEnvelopeAndSealsUnknownKinds(t *testing.T) {
	c, peer := keyedConnection(t)
	frame := protocol.New("future.content", strings.Repeat("\x00", 128), 0, fixtureTime, nil)
	frame.Payload = append([]byte{'"'}, bytes.Repeat([]byte{'x'}, protocol.MaxEnvelopeBytes-2)...)
	frame.Payload = append(frame.Payload, '"')
	if !c.enqueue(frame) {
		t.Fatal("full envelope could not enter queue")
	}
	data := <-c.queue
	if len(data) > protocol.MaxServerFrameBytes || bytes.Contains(data, frame.Payload[:1024]) {
		t.Fatal("wire exceeds derived bound or leaks content")
	}
	var wire protocol.Frame
	var envelope protocol.Sealed
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(wire.Payload, &envelope); err != nil {
		t.Fatal(err)
	}
	var plain bytes.Buffer
	if _, err := peer.Open(envelope, seal.Context(wire), &plain); err != nil ||
		!bytes.Equal(plain.Bytes(), frame.Payload) {
		t.Fatal("full envelope did not round trip", err)
	}
}

func TestOversizedContentReportsErrorWithoutDroppingConnection(t *testing.T) {
	c, _ := keyedConnection(t)
	frame := protocol.New(protocol.Notice, "large", 0, fixtureTime, nil)
	frame.Payload = bytes.Repeat([]byte{'x'}, protocol.MaxEnvelopeBytes+1)
	if !c.enqueue(frame) || c.ctx.Err() != nil {
		t.Fatal("oversized content dropped connection")
	}
	var wire protocol.Frame
	if err := json.Unmarshal(<-c.queue, &wire); err != nil {
		t.Fatal(err)
	}
	var payload protocol.ErrorPayload
	if err := json.Unmarshal(wire.Payload, &payload); err != nil || wire.Type != protocol.Error ||
		payload.Code != "content_too_large" ||
		payload.Ref != "large" {
		t.Fatal("missing safe correlated error", err)
	}
}
