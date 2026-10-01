package app

import (
	"testing"

	"github.com/q15co/q15/systems/agent/internal/bus"
	"github.com/q15co/q15/systems/agent/internal/schedule"
)

func TestScheduleOwnerAuthorizationByTransport(t *testing.T) {
	for _, tc := range []struct {
		name    string
		allowed []int64
		owner   schedule.Owner
		wantErr bool
	}{
		{"allowed telegram user", []int64{42}, schedule.Owner{Channel: bus.ChannelTelegram, UserID: "42"}, false},
		{"denied telegram user", []int64{42}, schedule.Owner{Channel: bus.ChannelTelegram, UserID: "99"}, true},
		{"disabled telegram", nil, schedule.Owner{Channel: bus.ChannelTelegram, UserID: "42"}, true},
		{"bridge with telegram enabled", []int64{42}, schedule.Owner{Channel: bus.ChannelBridge, UserID: "owner"}, false},
		{"bridge only", nil, schedule.Owner{Channel: bus.ChannelBridge, UserID: "owner"}, false},
		{"unknown transport", []int64{42}, schedule.Owner{Channel: "unknown", UserID: "42"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := scheduleOwnerAuthorizer(tc.allowed)(tc.owner)
			if (err != nil) != tc.wantErr {
				t.Fatalf("authorize(%+v) = %v, want error %v", tc.owner, err, tc.wantErr)
			}
		})
	}
}
