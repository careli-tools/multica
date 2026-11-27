CREATE TABLE notification_delivery_outbox (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    inbox_item_id UUID NOT NULL,
    workspace_id UUID NOT NULL,
    recipient_id UUID NOT NULL,
    channel_type TEXT NOT NULL CHECK (channel_type IN ('email', 'telegram')),
    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'sending', 'sent', 'skipped', 'failed')),
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error_code TEXT NOT NULL DEFAULT '',
    sent_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (inbox_item_id, channel_type)
);

CREATE TABLE notification_daily_report_run (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL,
    user_id UUID NOT NULL,
    report_date DATE NOT NULL,
    agent_id UUID NOT NULL,
    issue_id UUID,
    status TEXT NOT NULL DEFAULT 'creating'
        CHECK (status IN ('creating', 'running', 'completed', 'failed', 'cancelled')),
    is_manual BOOLEAN NOT NULL DEFAULT FALSE,
    format TEXT NOT NULL DEFAULT 'standard'
        CHECK (format IN ('dev', 'standard')),
    instructions TEXT NOT NULL DEFAULT '',
    report_body TEXT NOT NULL DEFAULT '',
    error_code TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, user_id, report_date)
);

-- Keep the older Notify installation's opt-ins, addresses, event filters, and
-- report schedule when moving preferences into Multica's native settings.
-- Malformed or orphaned plugin-storage values are ignored without blocking the
-- application migration. Existing native preferences win on key collisions.
DO $$
DECLARE
    legacy_row RECORD;
    legacy JSONB;
    legacy_events JSONB;
    legacy_channels JSONB;
    legacy_report JSONB;
    email_address TEXT;
    email_enabled BOOLEAN;
    telegram_enabled BOOLEAN;
    report_enabled BOOLEAN;
    report_agent_id TEXT;
    report_time TEXT;
    report_timezone TEXT;
    new_preferences JSONB;
BEGIN
    FOR legacy_row IN
        SELECT DISTINCT ON (pi.workspace_id, substring(ps.key FROM 7)::uuid)
               pi.workspace_id, substring(ps.key FROM 7)::uuid AS user_id, ps.value
        FROM plugin_storage ps
        JOIN plugin_installation pi ON pi.id = ps.installation_id
        JOIN member m ON m.workspace_id = pi.workspace_id AND m.user_id = substring(ps.key FROM 7)::uuid
        WHERE pi.plugin_key = 'de.careli.notify'
          AND pi.enabled = TRUE
          AND ps.scope_type = 'workspace'
          AND ps.scope_id = pi.workspace_id
          AND ps.key ~ '^prefs\.[0-9a-fA-F-]{36}$'
        ORDER BY pi.workspace_id, substring(ps.key FROM 7)::uuid, ps.updated_at DESC
    LOOP
        BEGIN
            legacy := legacy_row.value::jsonb;
        EXCEPTION WHEN OTHERS THEN
            CONTINUE;
        END;
        IF jsonb_typeof(legacy) IS DISTINCT FROM 'object' THEN
            CONTINUE;
        END IF;
        IF COALESCE(NULLIF(legacy->>'user_id', ''), legacy_row.user_id::text) <> legacy_row.user_id::text THEN
            CONTINUE;
        END IF;

        legacy_channels := CASE
            WHEN jsonb_typeof(legacy->'channels') = 'array' THEN legacy->'channels'
            ELSE '[]'::jsonb
        END;
        legacy_events := CASE
            WHEN jsonb_typeof(legacy->'events') = 'object' THEN legacy->'events'
            ELSE '{}'::jsonb
        END;
        legacy_report := CASE
            WHEN jsonb_typeof(legacy->'daily_report') = 'object' THEN legacy->'daily_report'
            ELSE '{}'::jsonb
        END;
        email_address := lower(trim(COALESCE(legacy->>'email', '')));
        email_enabled := COALESCE(legacy->>'enabled', 'true') <> 'false'
            AND legacy_channels ? 'email'
            AND email_address ~ '^[^[:space:]@]+@[^[:space:]@]+[.][^[:space:]@]+$';
        telegram_enabled := COALESCE(legacy->>'enabled', 'true') <> 'false'
            AND legacy_channels ? 'telegram';
        report_agent_id := CASE
            WHEN COALESCE(legacy_report->>'agent_id', '') ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
                THEN lower(legacy_report->>'agent_id')
            ELSE ''
        END;
        report_time := CASE
            WHEN COALESCE(legacy_report->>'time', '') ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'
                THEN legacy_report->>'time'
            ELSE '18:00'
        END;
        report_timezone := CASE
            WHEN EXISTS (SELECT 1 FROM pg_timezone_names WHERE name = legacy_report->>'timezone')
                THEN legacy_report->>'timezone'
            ELSE 'Europe/Berlin'
        END;
        report_enabled := COALESCE(legacy_report->>'enabled', 'false') = 'true'
            AND email_enabled AND report_agent_id <> '';

        new_preferences := jsonb_build_object(
            'email_enabled', email_enabled::text,
            'email_address', CASE WHEN email_enabled THEN email_address ELSE '' END,
            'telegram_enabled', telegram_enabled::text,
            'digest_minutes', '60',
            'channel_event_assigned', COALESCE(legacy_events->>'issue.assigned' = 'true', true)::text,
            'channel_event_mentioned', COALESCE(legacy_events->>'comment.mentioned' = 'true', true)::text,
            'channel_event_mention_all', COALESCE(legacy_events->>'comment.mention_all' = 'true', false)::text,
            'channel_event_status_changed', COALESCE(legacy_events->>'issue.status_changed' = 'true', false)::text,
            'channel_event_task_failed', COALESCE(legacy_events->>'task.failed' = 'true', true)::text,
            'channel_event_task_completed', COALESCE(legacy_events->>'task.completed' = 'true', false)::text,
            'daily_report_enabled', report_enabled::text,
            'daily_report_time', report_time,
            'daily_report_timezone', report_timezone,
            'daily_report_agent_id', report_agent_id,
            'daily_report_format', 'standard',
            'daily_report_instructions', ''
        );

        INSERT INTO notification_preference (workspace_id, user_id, preferences)
        VALUES (legacy_row.workspace_id, legacy_row.user_id, new_preferences)
        ON CONFLICT (workspace_id, user_id) DO UPDATE
        SET preferences = EXCLUDED.preferences || notification_preference.preferences,
            updated_at = now();
    END LOOP;
END $$;

-- Seed report history from successful Notify 0.3.x report issues so the first
-- core-generated report can read the actual previous-day report. The join is
-- limited to current workspace members and agent-authored marker comments.
WITH marked_issues AS (
    SELECT i.id AS issue_id,
           i.workspace_id,
           i.assignee_id AS agent_id,
           i.created_at AS issue_created_at,
           parsed.marker_parts[1]::date AS report_date,
           parsed.marker_parts[2]::uuid AS user_id
    FROM issue i
    CROSS JOIN LATERAL regexp_match(
        i.description,
        '^notify-daily-report-request: ([0-9]{4}-[0-9]{2}-[0-9]{2})/([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12})'
    ) AS parsed(marker_parts)
    WHERE i.description ~ '^notify-daily-report-request: '
      AND i.assignee_type = 'agent'
), successful_reports AS (
    SELECT marked_issues.*,
           ltrim(substring(c.content FROM char_length('<!-- notify-daily-report -->') + 1)) AS report_body
    FROM marked_issues
    JOIN member m ON m.workspace_id = marked_issues.workspace_id AND m.user_id = marked_issues.user_id
    JOIN LATERAL (
        SELECT c.content
        FROM comment c
        WHERE c.workspace_id = marked_issues.workspace_id
          AND c.issue_id = marked_issues.issue_id
          AND c.author_type = 'agent'
          AND c.author_id = marked_issues.agent_id
          AND c.content LIKE '<!-- notify-daily-report -->%'
        ORDER BY c.created_at DESC, c.id DESC
        LIMIT 1
    ) c ON TRUE
)
INSERT INTO notification_daily_report_run (
    workspace_id, user_id, report_date, agent_id, issue_id, status,
    format, instructions, report_body
)
SELECT DISTINCT ON (workspace_id, user_id, report_date)
       workspace_id, user_id, report_date, agent_id, issue_id, 'completed',
       'standard', '', left(report_body, 30000)
FROM successful_reports
ORDER BY workspace_id, user_id, report_date, issue_created_at DESC
ON CONFLICT (workspace_id, user_id, report_date) DO NOTHING;

CREATE FUNCTION enqueue_notification_delivery_for_inbox_item() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    prefs JSONB;
    digest_minutes INTEGER;
    channel_name TEXT;
BEGIN
    IF NEW.recipient_type <> 'member' THEN
        RETURN NEW;
    END IF;

    SELECT preferences INTO prefs
    FROM notification_preference
    WHERE workspace_id = NEW.workspace_id AND user_id = NEW.recipient_id;
    IF prefs IS NULL THEN
        RETURN NEW;
    END IF;

    BEGIN
        digest_minutes := (prefs->>'digest_minutes')::integer;
    EXCEPTION WHEN OTHERS THEN
        digest_minutes := 60;
    END;
    IF digest_minutes IS NULL OR digest_minutes NOT IN (15, 30, 60, 240) THEN
        digest_minutes := 60;
    END IF;

    FOREACH channel_name IN ARRAY ARRAY['email', 'telegram'] LOOP
        IF COALESCE(prefs->>(channel_name || '_enabled'), 'false') <> 'true' THEN
            CONTINUE;
        END IF;
        INSERT INTO notification_delivery_outbox (
            inbox_item_id, workspace_id, recipient_id, channel_type, available_at
        ) VALUES (
            NEW.id, NEW.workspace_id, NEW.recipient_id, channel_name,
            CASE WHEN NEW.severity = 'action_required' THEN now()
                 ELSE to_timestamp(((floor(extract(epoch FROM now()) / (digest_minutes * 60)) + 1) * (digest_minutes * 60))::double precision) END
        ) ON CONFLICT (inbox_item_id, channel_type) DO NOTHING;
    END LOOP;
    RETURN NEW;
END $$;

CREATE TRIGGER inbox_item_enqueue_notification_delivery
AFTER INSERT ON inbox_item
FOR EACH ROW EXECUTE FUNCTION enqueue_notification_delivery_for_inbox_item();
