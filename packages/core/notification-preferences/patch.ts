import type {
  NotificationGroupKey,
  NotificationGroupValue,
  NotificationPreferences,
} from "../types";

const NOTIFICATION_GROUP_KEYS: readonly NotificationGroupKey[] = [
  "assignments",
  "status_changes",
  "comments",
  "mentions",
  "updates",
  "agent_activity",
  "system_notifications",
];

const DELIVERY_PREFERENCE_KEYS = [
  "email_enabled",
  "email_address",
  "telegram_enabled",
  "digest_minutes",
  "daily_report_enabled",
  "daily_report_time",
  "daily_report_timezone",
  "daily_report_agent_id",
  "daily_report_format",
  "daily_report_instructions",
  "channel_event_assigned",
  "channel_event_mentioned",
  "channel_event_mention_all",
  "channel_event_status_changed",
  "channel_event_task_failed",
  "channel_event_task_completed",
] as const;

function preferenceValue(
  preferences: NotificationPreferences,
  key: NotificationGroupKey,
): NotificationGroupValue {
  return preferences[key] ?? "all";
}

/**
 * Convert the full preference object produced by the settings UI into the
 * smallest atomic patch. Missing keys mean the default value ("all").
 */
export function deriveNotificationPreferencePatch(
  previous: NotificationPreferences,
  next: NotificationPreferences,
): NotificationPreferences {
  const patch: NotificationPreferences = {};
  const previousValues = previous as unknown as Record<string, string | undefined>;
  const nextValues = next as unknown as Record<string, string | undefined>;
  const patchValues = patch as unknown as Record<string, string | undefined>;

  for (const key of NOTIFICATION_GROUP_KEYS) {
    const previousValue = preferenceValue(previous, key);
    const nextValue = preferenceValue(next, key);
    if (previousValue !== nextValue) {
      patch[key] = nextValue;
    }
  }

  for (const key of DELIVERY_PREFERENCE_KEYS) {
    if (previousValues[key] !== nextValues[key] && nextValues[key] !== undefined) {
      patchValues[key] = nextValues[key];
    }
  }

  return patch;
}

/** Apply a preference patch while keeping default "all" values sparse. */
export function applyNotificationPreferencePatch(
  current: NotificationPreferences,
  patch: NotificationPreferences,
): NotificationPreferences {
  const next = { ...current };
  const patchValues = patch as unknown as Record<string, string | undefined>;
  const nextValues = next as unknown as Record<string, string | undefined>;

  for (const key of NOTIFICATION_GROUP_KEYS) {
    const value = patch[key];
    if (value === "muted") {
      next[key] = value;
    } else if (value === "all") {
      delete next[key];
    }
  }

  for (const key of DELIVERY_PREFERENCE_KEYS) {
    const value = patchValues[key];
    if (value !== undefined) nextValues[key] = value;
  }

  return next;
}

/**
 * Roll back only values that still match this mutation's optimistic patch.
 * A later toggle that touched the same key wins.
 */
export function rollbackNotificationPreferencePatch(
  current: NotificationPreferences,
  patch: NotificationPreferences,
  previous: NotificationPreferences,
): NotificationPreferences {
  const next = { ...current };
  const currentValues = current as unknown as Record<string, string | undefined>;
  const patchValues = patch as unknown as Record<string, string | undefined>;
  const previousValues = previous as unknown as Record<string, string | undefined>;
  const nextValues = next as unknown as Record<string, string | undefined>;

  for (const key of NOTIFICATION_GROUP_KEYS) {
    const patchedValue = patch[key];
    if (
      patchedValue === undefined ||
      preferenceValue(current, key) !== patchedValue
    ) {
      continue;
    }

    const previousValue = preferenceValue(previous, key);
    if (previousValue === "all") {
      delete next[key];
    } else {
      next[key] = previousValue;
    }
  }

  for (const key of DELIVERY_PREFERENCE_KEYS) {
    const patchedValue = patchValues[key];
    if (patchedValue === undefined || currentValues[key] !== patchedValue) continue;
    const previousValue = previousValues[key];
    if (previousValue === undefined) delete nextValues[key];
    else nextValues[key] = previousValue;
  }

  return next;
}
