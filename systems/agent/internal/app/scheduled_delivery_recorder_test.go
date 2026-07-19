package app

import (
	"context"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/schedule"
)

type scheduledDeliveryRecorderTestStore struct {
	event conversation.DeliveredAssistantEvent
}

func (s *scheduledDeliveryRecorderTestStore) RecordDeliveredAssistantEvent(
	_ context.Context,
	event conversation.DeliveredAssistantEvent,
) error {
	s.event = event
	return nil
}

func TestScheduledDeliveryRecorderMapsAcknowledgedNotificationExactly(t *testing.T) {
	store := &scheduledDeliveryRecorderTestStore{}
	recorder := &scheduledDeliveryRecorder{store: store}
	deliveredAt := time.Date(2026, time.July, 19, 12, 34, 56, 789, time.UTC)

	err := recorder.RecordDeliveredNotification(
		context.Background(),
		schedule.DeliveredNotification{
			JobID:       "job-1",
			RunID:       "run-1",
			Channel:     "telegram",
			ChatID:      "chat-1",
			Text:        "exact delivered text\nwith spacing",
			DeliveredAt: deliveredAt,
		},
	)
	if err != nil {
		t.Fatalf("RecordDeliveredNotification() error = %v", err)
	}

	want := conversation.DeliveredAssistantEvent{
		Text: "exact delivered text\nwith spacing",
		Metadata: conversation.ExternalEventMetadata{
			Source:      conversation.ExternalEventSourceScheduledJob,
			JobID:       "job-1",
			RunID:       "run-1",
			Channel:     "telegram",
			ChatID:      "chat-1",
			DeliveredAt: deliveredAt,
		},
	}
	if store.event.Text != want.Text || store.event.Metadata != want.Metadata {
		t.Fatalf("recorded event = %#v, want %#v", store.event, want)
	}
}
