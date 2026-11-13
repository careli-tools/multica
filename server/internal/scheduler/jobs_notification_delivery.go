package scheduler

import (
	"context"
	"time"

	"github.com/multica-ai/multica/server/internal/service"
)

// NotificationDeliveryJob drains the Inbox outbox and starts/finishes daily
// report tasks. Its stable global lease keeps multiple API replicas from
// sending the same batch at once.
func NotificationDeliveryJob(delivery *service.NotificationDeliveryService) JobSpec {
	return JobSpec{
		Name:              "notification_delivery",
		Cadence:           time.Minute,
		CatchUpMode:       CatchUpLatestOnly,
		CatchUpWindow:     10 * time.Minute,
		RunTimeout:        10 * time.Minute,
		StaleTimeout:      15 * time.Minute,
		HeartbeatInterval: 30 * time.Second,
		AllowStaleReentry: true,
		MaxAttempts:       1,
		Scopes:            StaticScopes(ScopeGlobal),
		Handler: func(ctx context.Context, in HandlerInput) (HandlerResult, error) {
			count, err := delivery.RunOnce(ctx, in.PlanTime, in.Heartbeat)
			return HandlerResult{RowsAffected: count}, err
		},
	}
}
