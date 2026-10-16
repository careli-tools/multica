-- Single-statement migration: CREATE INDEX CONCURRENTLY cannot run inside a
-- transaction and must not block Inbox inserts during deployment.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_notification_daily_report_due
    ON notification_daily_report_run (status, updated_at);
