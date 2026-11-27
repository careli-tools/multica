export type NotificationGroupKey =
  | "assignments"
  | "status_changes"
  | "comments"
  | "squad_handoffs"
  | "chat_replies"
  | "mentions"
  | "reactions"
  | "updates"
  | "new_issues"
  | "agent_activity"
  | "system_notifications"
  | AutoArchiveKey;

/**
 * Each action you take on an issue that can archive your own notifications on
 * it. `auto_archive_completed` is moving it to a done or closed status;
 * `auto_archive_on_agent_trigger` is any action that starts, merges into,
 * steers or reruns an agent on the issue; `auto_archive_on_status` is moving
 * it to a started status (unstarted statuses like backlog/todo archive nothing
 * unless they trigger an agent).
 */
export type AutoArchiveKey =
  | "auto_archive_completed"
  | "auto_archive_on_agent_trigger"
  | "auto_archive_on_comment"
  | "auto_archive_on_status"
  | "auto_archive_on_assignee"
  | "auto_archive_on_priority"
  | "auto_archive_on_project"
  | "auto_archive_on_dates"
  | "auto_archive_on_labels";

export type NotificationGroupValue = "all" | "muted";

/**
 * How much of someone else's issue reaches you when you are only passively
 * subscribed (you filed it, commented, or were mentioned). Missing means
 * "key_updates": mentions, replies in your threads and handoffs.
 */
export type OthersIssuesValue = "all" | "key_updates";

export type NotificationBooleanValue = "true" | "false";
export type NotificationDigestMinutes = "15" | "30" | "60" | "240";
export type DailyReportFormat = "dev" | "standard";

export type NotificationPreferences = Partial<Record<NotificationGroupKey, NotificationGroupValue>> & {
  others_issues?: OthersIssuesValue;
  email_enabled?: NotificationBooleanValue;
  email_address?: string;
  telegram_enabled?: NotificationBooleanValue;
  digest_minutes?: NotificationDigestMinutes;
  daily_report_enabled?: NotificationBooleanValue;
  daily_report_time?: string;
  daily_report_timezone?: string;
  daily_report_agent_id?: string;
  daily_report_format?: DailyReportFormat;
  daily_report_instructions?: string;
  channel_event_assigned?: NotificationBooleanValue;
  channel_event_mentioned?: NotificationBooleanValue;
  channel_event_mention_all?: NotificationBooleanValue;
  channel_event_status_changed?: NotificationBooleanValue;
  channel_event_task_failed?: NotificationBooleanValue;
  channel_event_task_completed?: NotificationBooleanValue;
};

export interface NotificationPreferenceResponse {
  workspace_id: string;
  preferences: NotificationPreferences;
  /**
   * The member has not yet chosen which of their actions archive notifications
   * since replying, changing status and reassigning started doing so by
   * default. Their auto-archive keys keep the old behavior until they answer.
   */
  auto_archive_prompt?: boolean;
  telegram_connected?: boolean;
  delivery_status?: Partial<Record<"email" | "telegram", {
    failed_count: number;
  }>>;
}
