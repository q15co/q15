package app

import (
	"context"
	"fmt"

	"github.com/q15co/q15/systems/agent/internal/conversation"
	"github.com/q15co/q15/systems/agent/internal/schedule"
)

type scheduledDeliveryRecorder struct {
	store deliveredAssistantEventStore
}

var _ schedule.DeliveryRecorder = (*scheduledDeliveryRecorder)(nil)

type deliveredAssistantEventStore interface {
	RecordDeliveredAssistantEvent(context.Context, conversation.DeliveredAssistantEvent) error
}

func (r *scheduledDeliveryRecorder) RecordDeliveredNotification(
	ctx context.Context,
	delivery schedule.DeliveredNotification,
) error {
	if r == nil || r.store == nil {
		return fmt.Errorf("scheduled delivery transcript recorder is not configured")
	}
	return r.store.RecordDeliveredAssistantEvent(
		ctx,
		conversation.DeliveredAssistantEvent{
			Text: delivery.Text,
			Metadata: conversation.ExternalEventMetadata{
				Source:      conversation.ExternalEventSourceScheduledJob,
				JobID:       delivery.JobID,
				RunID:       delivery.RunID,
				Channel:     delivery.Channel,
				ChatID:      delivery.ChatID,
				DeliveredAt: delivery.DeliveredAt,
			},
		},
	)
}
