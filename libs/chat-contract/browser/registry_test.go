package browser

import (
	"testing"
	"time"

	"github.com/q15co/q15/libs/chat-contract/browser/protocol"
)

var fixtureTime = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

type fakeConnection struct {
	frames  []protocol.Frame
	slow    bool
	stopped bool
}

func (f *fakeConnection) enqueue(frame protocol.Frame) bool {
	if f.slow {
		return false
	}
	f.frames = append(f.frames, frame)
	return true
}

func (f *fakeConnection) stop() { f.stopped = true }

func TestRegistryFanoutIsolationAndSlowDevice(t *testing.T) {
	r := newRegistry()
	first, second, other, slow := &fakeConnection{}, &fakeConnection{}, &fakeConnection{}, &fakeConnection{
		slow: true,
	}
	for _, device := range []*fakeConnection{first, second, slow} {
		if !r.add("owner", device) {
			t.Fatal("registration failed")
		}
	}
	r.add("another-owner", other)
	r.broadcast("owner", protocol.New(protocol.Pong, "ping", 0, fixtureTime, struct{}{}))
	if len(first.frames) != 1 || len(second.frames) != 1 || len(other.frames) != 0 ||
		!slow.stopped {
		t.Fatal("fanout isolation or slow-device eviction failed")
	}
	r.broadcast("owner", protocol.New(protocol.Pong, "ping", 0, fixtureTime, struct{}{}))
	if len(first.frames) != 2 || len(second.frames) != 2 {
		t.Fatal("slow device blocked healthy devices")
	}
	r.close()
	if !first.stopped || !second.stopped || !other.stopped {
		t.Fatal("shutdown left devices open")
	}
}
