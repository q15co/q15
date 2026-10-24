package history

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestAcknowledgedDeliveryIsConcurrentAndIdempotent(t *testing.T) {
	store := testStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	event := conversation.DeliveredAssistantEvent{
		Text: " exact delivered text\n",
		Metadata: conversation.ExternalEventMetadata{
			Source:      conversation.ExternalEventSourceScheduledJob,
			JobID:       "job",
			RunID:       "run",
			Channel:     "telegram",
			ChatID:      "chat",
			DeliveredAt: time.Date(2026, 10, 24, 10, 0, 0, 0, time.UTC),
		},
	}
	for range 2 {
		var workers sync.WaitGroup
		failures := make(chan error, 20)
		for range 20 {
			workers.Go(func() { failures <- store.RecordDeliveredAssistantEvent(ctx, event) })
		}
		workers.Wait()
		close(failures)
		for err := range failures {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	page, err := store.ListTurns(ctx, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Turns) != 1 || len(page.Turns[0].Messages) != 1 {
		t.Fatalf("duplicate deliveries: %+v", page)
	}
	message := page.Turns[0].Messages[0]
	if conversation.TextValue(message) != event.Text || message.ExternalEvent == nil ||
		message.ExternalEvent.JobID != "job" {
		t.Fatal("delivery text or provenance changed")
	}
	event.Text = "different delivered text"
	if err := store.RecordDeliveredAssistantEvent(ctx, event); err == nil {
		t.Fatal("same delivery identity accepted different text")
	}
}
