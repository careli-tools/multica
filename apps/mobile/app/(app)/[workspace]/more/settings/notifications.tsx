/**
 * Mobile notification settings. Mirrors the web/desktop Notifications tab:
 * Inbox groups stay independent from email/Telegram event filters, while
 * report style, agent instructions, schedule, and manual creation share the
 * same server preference contract.
 */
import { useEffect, useMemo, useState } from "react";
import { ActivityIndicator, Alert, Pressable, ScrollView, TextInput, View } from "react-native";
import { useMutation, useQuery } from "@tanstack/react-query";
import { router } from "expo-router";
import type {
  DailyReportFormat,
  NotificationGroupKey,
  NotificationPreferences,
} from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { Switch } from "@/components/ui/switch";
import { Separator } from "@/components/ui/separator";
import { api } from "@/data/api";
import { useWorkspaceStore } from "@/data/workspace-store";
import { agentListOptions } from "@/data/queries/agents";
import { notificationPreferenceOptions } from "@/data/queries/notification-preferences";
import { useUpdateNotificationPreferences } from "@/data/mutations/notification-preferences";
import { useT } from "@/lib/i18n";

const INBOX_GROUPS: {
  key: Exclude<NotificationGroupKey, "system_notifications">;
  labelKey: string;
  descriptionKey?: string;
}[] = [
  { key: "assignments", labelKey: "groups.assignments", descriptionKey: "groups.assignments_description" },
  { key: "status_changes", labelKey: "groups.status_changes" },
  { key: "comments", labelKey: "groups.comments", descriptionKey: "groups.comments_description" },
  { key: "mentions", labelKey: "groups.mentions", descriptionKey: "groups.mentions_description" },
  { key: "updates", labelKey: "groups.updates", descriptionKey: "groups.updates_description" },
  { key: "agent_activity", labelKey: "groups.agent_activity", descriptionKey: "groups.agent_activity_description" },
];

const CHANNEL_EVENTS = [
  ["channel_event_assigned", "delivery.events.assigned", true],
  ["channel_event_mentioned", "delivery.events.mentioned", true],
  ["channel_event_mention_all", "delivery.events.mention_all", false],
  ["channel_event_status_changed", "delivery.events.status_changed", false],
  ["channel_event_task_failed", "delivery.events.task_failed", true],
  ["channel_event_task_completed", "delivery.events.task_completed", false],
] as const;

const DIGEST_INTERVALS = ["15", "30", "60", "240"] as const;

export default function NotificationsSettingsScreen() {
  const wsId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const workspaceSlug = useWorkspaceStore((s) => s.currentWorkspaceSlug);
  const { t } = useT("settings");
  const { data, isLoading, error } = useQuery(notificationPreferenceOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const mutation = useUpdateNotificationPreferences();
  const reportNow = useMutation({
    mutationFn: () => api.createNotificationDailyReportNow(),
    onSuccess: () => Alert.alert(t("daily_report.title"), t("daily_report.queued")),
    onError: (err) => Alert.alert(t("errors.save_failed"), err.message),
  });

  const preferences: NotificationPreferences = data?.preferences ?? {};
  const [emailAddress, setEmailAddress] = useState(preferences.email_address ?? "");
  const [reportTime, setReportTime] = useState(preferences.daily_report_time ?? "18:00");
  const [reportTimezone, setReportTimezone] = useState(preferences.daily_report_timezone ?? "Europe/Berlin");
  const [reportInstructions, setReportInstructions] = useState(preferences.daily_report_instructions ?? "");
  useEffect(() => setEmailAddress(preferences.email_address ?? ""), [preferences.email_address]);
  useEffect(() => setReportTime(preferences.daily_report_time ?? "18:00"), [preferences.daily_report_time]);
  useEffect(() => setReportTimezone(preferences.daily_report_timezone ?? "Europe/Berlin"), [preferences.daily_report_timezone]);
  useEffect(() => setReportInstructions(preferences.daily_report_instructions ?? ""), [preferences.daily_report_instructions]);

  const activeAgents = useMemo(() => agents.filter((agent) => !agent.archived_at), [agents]);
  const reportAgentId = preferences.daily_report_agent_id ?? "";
  const reportAgent = activeAgents.find((agent) => agent.id === reportAgentId);
  const reportEnabled = preferences.daily_report_enabled === "true";
  const emailEnabled = preferences.email_enabled === "true";
  const telegramEnabled = preferences.telegram_enabled === "true";
  const systemEnabled = preferences.system_notifications !== "muted";

  const savePreference = <K extends keyof NotificationPreferences>(
    key: K,
    value: NonNullable<NotificationPreferences[K]>,
  ) => mutation.mutate({ ...preferences, [key]: value });

  const onToggle = (key: NotificationGroupKey, enabled: boolean) => {
    const next: NotificationPreferences = { ...preferences };
    if (enabled) delete next[key];
    else next[key] = "muted";
    mutation.mutate(next);
  };

  const chooseAgent = () => {
    if (!workspaceSlug) return;
    router.push(`/${workspaceSlug}/more/settings/notifications-agent`);
  };

  if (isLoading) {
    return (
      <View className="flex-1 items-center justify-center bg-background">
        <ActivityIndicator />
      </View>
    );
  }
  if (error) {
    return (
      <View className="flex-1 items-center justify-center bg-background px-6">
        <Text className="text-sm text-destructive text-center">{t("errors.load_failed")}</Text>
      </View>
    );
  }

  return (
    <ScrollView className="flex-1 bg-background" contentContainerClassName="px-4 py-4 gap-6" keyboardShouldPersistTaps="handled">
      <Section title={t("groups.inbox_notifications")}>
        {INBOX_GROUPS.map((group, idx) => {
          const enabled = preferences[group.key] !== "muted";
          return (
            <View key={group.key}>
              <View className="flex-row items-center px-4 py-3 gap-3">
                <View className="flex-1">
                  <Text className="text-base font-medium text-foreground">{t(group.labelKey)}</Text>
                  {group.descriptionKey ? <Text className="text-xs text-muted-foreground mt-0.5">{t(group.descriptionKey)}</Text> : null}
                </View>
                <Switch checked={enabled} onCheckedChange={(checked) => onToggle(group.key, checked)} />
              </View>
              {idx < INBOX_GROUPS.length - 1 ? <Separator /> : null}
            </View>
          );
        })}
      </Section>

      <Section title={t("delivery.title")} description={t("delivery.description")}>
        <PreferenceToggle label={t("delivery.email")} detail={t("delivery.email_description")} checked={emailEnabled} onToggle={(checked) => savePreference("email_enabled", checked ? "true" : "false")} />
        <View className="px-4 py-3 gap-2">
          <Text className="text-sm font-medium text-foreground">{t("delivery.email_address")}</Text>
          <TextInput
            accessibilityLabel={t("delivery.email_address")}
            className="rounded-md border border-border bg-background px-3 py-2 text-foreground"
            keyboardType="email-address"
            autoCapitalize="none"
            autoCorrect={false}
            value={emailAddress}
            placeholder="name@example.com"
            onChangeText={setEmailAddress}
            onBlur={() => {
              if (emailAddress !== (preferences.email_address ?? "")) {
                savePreference("email_address", emailAddress.trim().toLowerCase());
              }
            }}
          />
        </View>
        {data?.delivery_status?.email?.failed_count ? <StatusNotice text={t("delivery.delivery_failed")} /> : null}
        <Separator />
        <PreferenceToggle
          label={t("delivery.telegram")}
          detail={data?.telegram_connected ? t("delivery.telegram_connected") : t("delivery.telegram_unconnected")}
          checked={telegramEnabled}
          onToggle={(checked) => savePreference("telegram_enabled", checked ? "true" : "false")}
        />
        {data?.delivery_status?.telegram?.failed_count ? <StatusNotice text={t("delivery.delivery_failed")} /> : null}
        <Separator />
        <View className="px-4 py-3 gap-3">
          <View>
            <Text className="text-base font-medium text-foreground">{t("delivery.events.title")}</Text>
            <Text className="text-xs text-muted-foreground mt-0.5">{t("delivery.events.description")}</Text>
          </View>
          {CHANNEL_EVENTS.map(([key, labelKey, defaultEnabled], idx) => {
            const enabled = preferences[key] === undefined ? defaultEnabled : preferences[key] === "true";
            return (
              <View key={key}>
                <View className="flex-row items-center gap-3 py-2">
                  <Text className="flex-1 text-sm text-foreground">{t(labelKey)}</Text>
                  <Switch checked={enabled} onCheckedChange={(checked) => savePreference(key, checked ? "true" : "false")} />
                </View>
                {idx < CHANNEL_EVENTS.length - 1 ? <Separator /> : null}
              </View>
            );
          })}
        </View>
        <View className="border-t border-border px-4 py-3 gap-2">
          <Text className="text-base font-medium text-foreground">{t("delivery.digest")}</Text>
          <Text className="text-xs text-muted-foreground">{t("delivery.digest_description")}</Text>
          <View className="flex-row flex-wrap gap-2">
            {DIGEST_INTERVALS.map((minutes) => {
              const selected = (preferences.digest_minutes ?? "60") === minutes;
              const labels = {
                "15": t("delivery.every_15_minutes"),
                "30": t("delivery.every_30_minutes"),
                "60": t("delivery.every_hour"),
                "240": t("delivery.every_4_hours"),
              } as const;
              return (
                <Pressable
                  key={minutes}
                  accessibilityRole="radio"
                  accessibilityState={{ selected }}
                  className={`rounded-full border px-3 py-2 ${selected ? "border-primary bg-accent" : "border-border"}`}
                  onPress={() => savePreference("digest_minutes", minutes)}
                >
                  <Text className="text-xs text-foreground">{labels[minutes]}</Text>
                </Pressable>
              );
            })}
          </View>
        </View>
      </Section>

      <Section title={t("daily_report.title")} description={t("daily_report.description")}>
        <PreferenceToggle
          label={t("daily_report.enabled")}
          detail={t("daily_report.enabled_description")}
          checked={reportEnabled}
          disabled={!reportAgentId && activeAgents.length === 0}
          onToggle={(checked) => {
            if (checked && !reportAgentId) {
              Alert.alert(t("daily_report.choose_agent_first"));
              return;
            }
            savePreference("daily_report_enabled", checked ? "true" : "false");
          }}
        />
        <Separator />
        <PreferenceRow label={t("daily_report.agent")} detail={reportAgent?.name ?? t("daily_report.choose_agent")} onPress={chooseAgent} />
        <Separator />
        <View className="px-4 py-3 gap-2">
          <Text className="text-sm font-medium text-foreground">{t("daily_report.time")}</Text>
          <Text className="text-xs text-muted-foreground">{t("daily_report.time_description")}</Text>
          <TextInput
            accessibilityLabel={t("daily_report.time")}
            className="rounded-md border border-border bg-background px-3 py-2 text-foreground"
            keyboardType="numbers-and-punctuation"
            value={reportTime}
            placeholder="18:00"
            onChangeText={setReportTime}
            onBlur={() => {
              if (reportTime !== (preferences.daily_report_time ?? "18:00")) savePreference("daily_report_time", reportTime);
            }}
          />
        </View>
        <Separator />
        <View className="px-4 py-3 gap-2">
          <Text className="text-sm font-medium text-foreground">{t("daily_report.timezone")}</Text>
          <Text className="text-xs text-muted-foreground">{t("daily_report.timezone_description")}</Text>
          <TextInput
            accessibilityLabel={t("daily_report.timezone")}
            className="rounded-md border border-border bg-background px-3 py-2 text-foreground"
            autoCapitalize="none"
            autoCorrect={false}
            value={reportTimezone}
            placeholder="Europe/Berlin"
            onChangeText={setReportTimezone}
            onBlur={() => {
              if (reportTimezone !== (preferences.daily_report_timezone ?? "Europe/Berlin")) savePreference("daily_report_timezone", reportTimezone);
            }}
          />
        </View>
        <Separator />
        <View className="px-4 py-3 gap-2">
          <Text className="text-sm font-medium text-foreground">{t("daily_report.format")}</Text>
          <Text className="text-xs text-muted-foreground">{t("daily_report.format_description")}</Text>
          <View className="gap-2">
            {(["dev", "standard"] as const).map((format: DailyReportFormat) => {
              const selected = (preferences.daily_report_format ?? "standard") === format;
              return (
                <Pressable
                  key={format}
                  accessibilityRole="radio"
                  accessibilityState={{ selected }}
                  className={`rounded-md border px-3 py-3 ${selected ? "border-primary bg-accent" : "border-border"}`}
                  onPress={() => savePreference("daily_report_format", format)}
                >
                  <Text className="text-sm font-medium text-foreground">
                    {t(format === "dev" ? "daily_report.dev_format" : "daily_report.standard_format")}
                  </Text>
                </Pressable>
              );
            })}
          </View>
        </View>
        <View className="px-4 py-3 gap-2">
          <Text className="text-sm font-medium text-foreground">{t("daily_report.instructions")}</Text>
          <TextInput
            accessibilityLabel={t("daily_report.instructions")}
            className="min-h-24 rounded-md border border-border bg-background px-3 py-2 text-foreground"
            multiline
            maxLength={4000}
            textAlignVertical="top"
            value={reportInstructions}
            placeholder={t("daily_report.instructions_placeholder")}
            onChangeText={setReportInstructions}
            onBlur={() => {
              if (reportInstructions !== (preferences.daily_report_instructions ?? "")) savePreference("daily_report_instructions", reportInstructions);
            }}
          />
        </View>
        <Separator />
        <Pressable
          accessibilityRole="button"
          accessibilityState={{ disabled: !reportAgentId || reportNow.isPending }}
          className="items-center px-4 py-3"
          disabled={!reportAgentId || reportNow.isPending}
          onPress={() => reportNow.mutate()}
        >
          <Text className="text-sm font-semibold text-primary">
            {reportNow.isPending ? t("daily_report.queuing") : t("daily_report.create_now")}
          </Text>
        </Pressable>
      </Section>

      <Section title={t("groups.system")}>
        <PreferenceToggle
          label={t("groups.system_notifications")}
          detail={t("groups.system_description")}
          checked={systemEnabled}
          onToggle={(checked) => onToggle("system_notifications", checked)}
        />
      </Section>
    </ScrollView>
  );
}

function PreferenceToggle({
  label,
  detail,
  checked,
  disabled,
  onToggle,
}: {
  label: string;
  detail?: string;
  checked: boolean;
  disabled?: boolean;
  onToggle: (checked: boolean) => void;
}) {
  return (
    <View className="flex-row items-center px-4 py-3 gap-3">
      <View className="flex-1">
        <Text className="text-base font-medium text-foreground">{label}</Text>
        {detail ? <Text className="text-xs text-muted-foreground mt-0.5">{detail}</Text> : null}
      </View>
      <Switch checked={checked} disabled={disabled} onCheckedChange={onToggle} />
    </View>
  );
}

function PreferenceRow({ label, detail, onPress }: { label: string; detail: string; onPress: () => void }) {
  return (
    <Pressable accessibilityRole="button" className="flex-row items-center justify-between px-4 py-3 gap-3" onPress={onPress}>
      <Text className="flex-1 text-base font-medium text-foreground">{label}</Text>
      <Text className="max-w-56 text-right text-sm text-muted-foreground">{detail} ›</Text>
    </Pressable>
  );
}

function StatusNotice({ text }: { text: string }) {
  return (
    <View className="border-t border-border px-4 py-2">
      <Text accessibilityRole="alert" className="text-xs text-destructive">{text}</Text>
    </View>
  );
}

function Section({
  title,
  description,
  children,
}: {
  title: string;
  description?: string;
  children: React.ReactNode;
}) {
  return (
    <View className="gap-2">
      <View className="px-1">
        <Text className="text-xs uppercase tracking-wider text-muted-foreground">{title}</Text>
        {description ? <Text className="text-xs text-muted-foreground mt-1">{description}</Text> : null}
      </View>
      <View className="rounded-md border border-border bg-card overflow-hidden">{children}</View>
    </View>
  );
}
