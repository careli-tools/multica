-- name: ClaimNotificationDeliveryOutbox :many
WITH candidates AS (
    SELECT id
    FROM notification_delivery_outbox
    WHERE (status = 'pending' AND available_at <= now())
       OR (status = 'sending' AND updated_at <= now() - interval '5 minutes')
    ORDER BY available_at, created_at, id
    FOR UPDATE SKIP LOCKED
    LIMIT sqlc.arg('row_limit')
)
UPDATE notification_delivery_outbox d
SET status = 'sending', attempts = d.attempts + 1, updated_at = now()
FROM candidates c
WHERE d.id = c.id
RETURNING d.*;

-- name: GetNotificationDeliveryContext :one
SELECT d.id AS delivery_id,
       d.channel_type,
       d.workspace_id,
       d.recipient_id,
       i.id AS inbox_item_id,
       i.type AS inbox_type,
       i.severity,
       i.title AS inbox_title,
       COALESCE(i.body, '') AS inbox_body,
       i.details,
       i.created_at AS inbox_created_at,
       i.read AS inbox_read,
       i.archived AS inbox_archived,
       i.issue_id,
       COALESCE(iss.title, '') AS issue_title,
       COALESCE(iss.description, '') AS issue_description,
       w.slug AS workspace_slug,
       w.name AS workspace_name,
       COALESCE(p.preferences, '{}'::jsonb) AS preferences,
       EXISTS (
           SELECT 1 FROM member m
           WHERE m.workspace_id = d.workspace_id AND m.user_id = d.recipient_id
       ) AS member_active
FROM notification_delivery_outbox d
JOIN inbox_item i ON i.id = d.inbox_item_id AND i.workspace_id = d.workspace_id
JOIN workspace w ON w.id = d.workspace_id
LEFT JOIN issue iss ON iss.id = i.issue_id AND iss.workspace_id = i.workspace_id
LEFT JOIN notification_preference p
  ON p.workspace_id = d.workspace_id AND p.user_id = d.recipient_id
WHERE d.id = sqlc.arg('delivery_id');

-- name: MarkNotificationDeliveryFinished :execrows
UPDATE notification_delivery_outbox
SET status = sqlc.arg('status'),
    sent_at = CASE WHEN sqlc.arg('status') = 'sent' THEN now() ELSE sent_at END,
    last_error_code = sqlc.arg('error_code'),
    updated_at = now()
WHERE id = sqlc.arg('id') AND status = 'sending';

-- name: RetryNotificationDelivery :execrows
UPDATE notification_delivery_outbox
SET status = CASE WHEN attempts >= sqlc.arg('max_attempts') THEN 'failed' ELSE 'pending' END,
    available_at = now() + make_interval(mins => sqlc.arg('backoff_minutes')),
    last_error_code = sqlc.arg('error_code'),
    updated_at = now()
WHERE id = sqlc.arg('id') AND status = 'sending';

-- name: CancelPendingNotificationDeliveries :execrows
UPDATE notification_delivery_outbox
SET status = 'skipped', last_error_code = 'channel_disabled', updated_at = now()
WHERE workspace_id = sqlc.arg('workspace_id')
  AND recipient_id = sqlc.arg('user_id')
  AND channel_type = sqlc.arg('channel_type')
  AND status = 'pending';

-- name: PruneNotificationDeliveryOutbox :execrows
DELETE FROM notification_delivery_outbox
WHERE id IN (
    SELECT id FROM notification_delivery_outbox
    WHERE status IN ('sent', 'skipped', 'failed')
      AND notification_delivery_outbox.updated_at < sqlc.arg('cutoff')
    ORDER BY notification_delivery_outbox.updated_at, notification_delivery_outbox.id
    LIMIT sqlc.arg('row_limit')
);

-- name: ListNotificationDeliveryStatus :many
SELECT channel_type,
       count(*) FILTER (WHERE status = 'failed')::bigint AS failed_count
FROM notification_delivery_outbox
WHERE workspace_id = sqlc.arg('workspace_id')
  AND recipient_id = sqlc.arg('user_id')
  AND created_at >= now() - interval '30 days'
GROUP BY channel_type;

-- name: ListNotificationDailyReportPreferences :many
SELECT p.workspace_id, p.user_id, p.preferences, w.slug AS workspace_slug, w.name AS workspace_name
FROM notification_preference p
JOIN member m ON m.workspace_id = p.workspace_id AND m.user_id = p.user_id
JOIN workspace w ON w.id = p.workspace_id
WHERE p.preferences->>'daily_report_enabled' = 'true'
ORDER BY p.workspace_id, p.user_id
LIMIT sqlc.arg('row_limit');

-- name: InsertNotificationDailyReportRun :one
INSERT INTO notification_daily_report_run (
    workspace_id, user_id, report_date, agent_id, is_manual, format, instructions
) VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (workspace_id, user_id, report_date) DO NOTHING
RETURNING *;

-- name: ListActiveNotificationDailyReportRuns :many
SELECT * FROM notification_daily_report_run
WHERE status IN ('creating', 'running')
ORDER BY created_at, id
LIMIT sqlc.arg('row_limit');

-- name: FindNotificationDailyReportIssue :one
SELECT * FROM issue
WHERE workspace_id = sqlc.arg('workspace_id')
  AND description LIKE '%' || sqlc.arg('marker') || '%'
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: GetLatestNotificationDailyReport :one
SELECT * FROM notification_daily_report_run
WHERE workspace_id = sqlc.arg('workspace_id')
  AND user_id = sqlc.arg('user_id')
  AND report_date = sqlc.arg('previous_date')
  AND status = 'completed'
  AND report_body <> ''
LIMIT 1;

-- name: FindLegacyNotificationDailyReportIssue :one
SELECT i.id,
       i.workspace_id,
       i.assignee_id AS agent_id,
       i.description
FROM issue i
WHERE i.workspace_id = sqlc.arg('workspace_id')
  AND i.description LIKE ('notify-daily-report-request: ' || sqlc.arg('report_date')::text || '/' || sqlc.arg('user_id')::text || '%')
ORDER BY i.created_at DESC
LIMIT 1;

-- name: ImportLegacyNotificationDailyReport :execrows
INSERT INTO notification_daily_report_run (
    workspace_id, user_id, report_date, agent_id, issue_id, status, format, instructions, report_body
) VALUES (
    sqlc.arg('workspace_id'), sqlc.arg('user_id'), sqlc.arg('report_date')::date,
    sqlc.arg('agent_id'), sqlc.arg('issue_id'), 'completed', 'standard', '', sqlc.arg('report_body')
)
ON CONFLICT (workspace_id, user_id, report_date) DO NOTHING;

-- name: SetNotificationDailyReportIssue :execrows
UPDATE notification_daily_report_run
SET issue_id = sqlc.arg('issue_id'), status = 'running', updated_at = now()
WHERE id = sqlc.arg('id') AND status = 'creating';

-- name: SetNotificationDailyReportCompleted :execrows
UPDATE notification_daily_report_run
SET status = 'completed', report_body = sqlc.arg('report_body'), error_code = '', updated_at = now()
WHERE id = sqlc.arg('id') AND status = 'running';

-- name: SetNotificationDailyReportFailed :execrows
UPDATE notification_daily_report_run
SET status = 'failed', error_code = sqlc.arg('error_code'), updated_at = now()
WHERE id = sqlc.arg('id') AND status IN ('creating', 'running');

-- name: SetNotificationDailyReportCancelled :execrows
UPDATE notification_daily_report_run
SET status = 'cancelled', updated_at = now()
WHERE id = sqlc.arg('id') AND status IN ('creating', 'running');

-- name: ListNotificationDailyReportComments :many
SELECT * FROM comment
WHERE workspace_id = sqlc.arg('workspace_id')
  AND issue_id = sqlc.arg('issue_id')
  AND content LIKE '<!-- notify-daily-report -->%'
ORDER BY created_at DESC, id DESC
LIMIT 10;
