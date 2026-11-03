DROP TRIGGER IF EXISTS inbox_item_enqueue_notification_delivery ON inbox_item;
DROP FUNCTION IF EXISTS enqueue_notification_delivery_for_inbox_item();
DROP TABLE IF EXISTS notification_daily_report_run;
DROP TABLE IF EXISTS notification_delivery_outbox;
