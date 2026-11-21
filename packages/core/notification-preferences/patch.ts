import type { NotificationPreferences } from "../types";

// Careli: channel and daily-report settings share the preference object but
// are plain values without a default, so they are diffed and applied as-is.
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

type DeliveryPreferenceKey = (typeof DELIVERY_PREFERENCE_KEYS)[number];
type PreferenceKey = Exclude<keyof NotificationPreferences, DeliveryPreferenceKey>;
type PreferenceValues = Record<string, string | undefined>;

/**
 * The value each key means when it is missing. Every key the settings UI can
 * write must be listed, or its change is silently never sent. Must match the
 * server's defaults (`mutedByDefault` in server/internal/inbox/registry.go).
 */
const PREFERENCE_DEFAULTS: { [K in PreferenceKey]-?: NonNullable<NotificationPreferences[K]> } = {
  assignments: "all",
  status_changes: "muted",
  comments: "all",
  chat_replies: "all",
  mentions: "all",
  reactions: "all",
  updates: "all",
  new_issues: "all",
  agent_activity: "all",
  system_notifications: "all",
  auto_archive_completed: "all",
  others_issues: "key_updates",
};

const PREFERENCE_KEYS = Object.keys(PREFERENCE_DEFAULTS) as PreferenceKey[];

/** The effective value of a preference, filling in its default. */
export function notificationPreferenceValue<K extends PreferenceKey>(
  preferences: NotificationPreferences,
  key: K,
): NonNullable<NotificationPreferences[K]> {
  return (preferences[key] ?? PREFERENCE_DEFAULTS[key]) as NonNullable<NotificationPreferences[K]>;
}

/** Set a value on a copy, dropping the key when it is the default. */
function withValue(
  preferences: NotificationPreferences,
  key: PreferenceKey,
  value: string,
): NotificationPreferences {
  const next = { ...preferences } as PreferenceValues;
  if (value === PREFERENCE_DEFAULTS[key]) {
    delete next[key];
  } else {
    next[key] = value;
  }
  return next as NotificationPreferences;
}

/**
 * Convert the full preference object produced by the settings UI into the
 * smallest atomic patch. Missing keys mean their default value.
 */
export function deriveNotificationPreferencePatch(
  previous: NotificationPreferences,
  next: NotificationPreferences,
): NotificationPreferences {
  const patch = {} as PreferenceValues;
  const previousValues = previous as unknown as PreferenceValues;
  const nextValues = next as unknown as PreferenceValues;

  for (const key of PREFERENCE_KEYS) {
    const previousValue = notificationPreferenceValue(previous, key);
    const nextValue = notificationPreferenceValue(next, key);
    if (previousValue !== nextValue) {
      patch[key] = nextValue;
    }
  }

  for (const key of DELIVERY_PREFERENCE_KEYS) {
    if (previousValues[key] !== nextValues[key] && nextValues[key] !== undefined) {
      patch[key] = nextValues[key];
    }
  }

  return patch as NotificationPreferences;
}

/** Apply a preference patch while keeping default values sparse. */
export function applyNotificationPreferencePatch(
  current: NotificationPreferences,
  patch: NotificationPreferences,
): NotificationPreferences {
  let next = { ...current };
  const patchValues = patch as unknown as PreferenceValues;

  for (const key of PREFERENCE_KEYS) {
    const value = patch[key];
    if (value !== undefined) {
      next = withValue(next, key, value);
    }
  }

  const nextValues = next as unknown as PreferenceValues;
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
  let next = { ...current };
  const currentValues = current as unknown as PreferenceValues;
  const patchValues = patch as unknown as PreferenceValues;
  const previousValues = previous as unknown as PreferenceValues;

  for (const key of PREFERENCE_KEYS) {
    const patchedValue = patch[key];
    if (
      patchedValue === undefined ||
      notificationPreferenceValue(current, key) !== patchedValue
    ) {
      continue;
    }
    next = withValue(next, key, notificationPreferenceValue(previous, key));
  }

  const nextValues = next as unknown as PreferenceValues;
  for (const key of DELIVERY_PREFERENCE_KEYS) {
    const patchedValue = patchValues[key];
    if (patchedValue === undefined || currentValues[key] !== patchedValue) continue;
    const previousValue = previousValues[key];
    if (previousValue === undefined) delete nextValues[key];
    else nextValues[key] = previousValue;
  }

  return next;
}
