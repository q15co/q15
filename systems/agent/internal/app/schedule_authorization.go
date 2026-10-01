package app

import (
	"fmt"
	"strconv"

	"github.com/q15co/q15/systems/agent/internal/bus"
	"github.com/q15co/q15/systems/agent/internal/schedule"
)

// scheduleOwnerAuthorizer keeps transport policy at the composition root.
// Telegram authenticates numeric users through its allow-list; the bridge
// authenticates its single owner through access to the privileged Unix socket.
// The scheduler only sees a validated canonical owner and this policy function.
func scheduleOwnerAuthorizer(telegramUserIDs []int64) func(schedule.Owner) error {
	allowed := make(map[string]struct{}, len(telegramUserIDs))
	for _, id := range telegramUserIDs {
		if id > 0 {
			allowed[strconv.FormatInt(id, 10)] = struct{}{}
		}
	}
	return func(owner schedule.Owner) error {
		switch owner.Channel {
		case bus.ChannelBridge:
			return nil
		case bus.ChannelTelegram:
			if _, ok := allowed[owner.UserID]; ok {
				return nil
			}
			return fmt.Errorf("user %q is not allowed to manage scheduled jobs", owner.UserID)
		default:
			return fmt.Errorf("scheduled jobs are not enabled for channel %q", owner.Channel)
		}
	}
}
