export type NotificationGroupKey =
  | "assignments"
  | "status_changes"
  | "comments"
  | "chat_replies"
  | "mentions"
  | "reactions"
  | "updates"
  | "new_issues"
  | "agent_activity"
  | "system_notifications"
  | "auto_archive_completed";

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
  telegram_connected?: boolean;
  delivery_status?: Partial<Record<"email" | "telegram", {
    failed_count: number;
  }>>;
}
