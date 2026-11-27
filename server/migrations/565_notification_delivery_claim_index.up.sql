-- Single-statement migration: CREATE INDEX CONCURRENTLY cannot run inside a
-- transaction and must not block Inbox inserts during deployment.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_notification_delivery_claim
    ON notification_delivery_outbox (available_at, updated_at)
    WHERE status IN ('pending', 'sending');
