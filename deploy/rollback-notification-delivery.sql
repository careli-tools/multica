-- Targeted rollback for migrations 564-566 only.
-- Run with psql in autocommit mode; do not wrap this file in a transaction
-- because the index drops use CONCURRENTLY. The old server ignores the new
-- preference keys; they are intentionally kept for a later re-upgrade.
\set ON_ERROR_STOP on

DROP INDEX CONCURRENTLY IF EXISTS idx_notification_daily_report_due;
DROP INDEX CONCURRENTLY IF EXISTS idx_notification_delivery_claim;
DROP TRIGGER IF EXISTS inbox_item_enqueue_notification_delivery ON inbox_item;
DROP FUNCTION IF EXISTS enqueue_notification_delivery_for_inbox_item();
DROP TABLE IF EXISTS notification_daily_report_run;
DROP TABLE IF EXISTS notification_delivery_outbox;
DELETE FROM schema_migrations
WHERE version IN (
    '564_notification_delivery',
    '565_notification_delivery_claim_index',
    '566_notification_daily_report_due_index'
);
