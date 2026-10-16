"use client";

import { useEffect, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useWorkspaceId } from "@multica/core/hooks";
import { api } from "@multica/core/api";
import { notificationPreferenceOptions } from "@multica/core/notification-preferences/queries";
import { useUpdateNotificationPreferences } from "@multica/core/notification-preferences/mutations";
import type { NotificationGroupKey, NotificationPreferences } from "@multica/core/types";
import { agentListOptions } from "@multica/core/workspace/queries";
import { TimezoneSelect } from "../../common/timezone-select";
import { Input } from "@multica/ui/components/ui/input";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Button } from "@multica/ui/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Switch } from "@multica/ui/components/ui/switch";
import { toast } from "sonner";
import { useT } from "../../i18n";
import { BrowserNotificationSetting } from "./browser-notification-setting";
import {
  SettingsCard,
  SettingsRow,
  SettingsSection,
  SettingsTab,
} from "./settings-layout";

// Inbox event groups rendered in the per-event toggle list. `system_notifications`
// is a sibling preference key but lives in its own section below.
const INBOX_GROUP_KEYS = [
  "assignments",
  "status_changes",
  "comments",
  "mentions",
  "updates",
  "agent_activity",
] as const;
type InboxGroupKey = (typeof INBOX_GROUP_KEYS)[number];

const CHANNEL_EVENT_KEYS = [
  "channel_event_assigned",
  "channel_event_mentioned",
  "channel_event_mention_all",
  "channel_event_status_changed",
  "channel_event_task_failed",
  "channel_event_task_completed",
] as const;
type ChannelEventKey = (typeof CHANNEL_EVENT_KEYS)[number];
const CHANNEL_EVENT_DEFAULTS: Record<ChannelEventKey, "true" | "false"> = {
  channel_event_assigned: "true",
  channel_event_mentioned: "true",
  channel_event_mention_all: "false",
  channel_event_status_changed: "false",
  channel_event_task_failed: "true",
  channel_event_task_completed: "false",
};

export function NotificationsTab() {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const { data } = useQuery(notificationPreferenceOptions(wsId));
  const { data: agents = [] } = useQuery(agentListOptions(wsId));
  const mutation = useUpdateNotificationPreferences();
  const reportNow = useMutation({
    mutationFn: () => api.createNotificationDailyReportNow(),
    onSuccess: () => toast.success(t(($) => $.notifications.daily_report.queued)),
    onError: (err) =>
      toast.error(
        err instanceof Error && err.message
          ? err.message
          : t(($) => $.notifications.toast_failed),
      ),
  });

  const preferences = data?.preferences ?? {};
  const [emailAddress, setEmailAddress] = useState(preferences.email_address ?? "");
  const [reportInstructions, setReportInstructions] = useState(
    preferences.daily_report_instructions ?? "",
  );
  const [reportTime, setReportTime] = useState(preferences.daily_report_time ?? "18:00");

  useEffect(() => setEmailAddress(preferences.email_address ?? ""), [preferences.email_address]);
  useEffect(
    () => setReportInstructions(preferences.daily_report_instructions ?? ""),
    [preferences.daily_report_instructions],
  );
  useEffect(
    () => setReportTime(preferences.daily_report_time ?? "18:00"),
    [preferences.daily_report_time],
  );

  const handleToggle = (key: NotificationGroupKey, enabled: boolean) => {
    const updated: NotificationPreferences = {
      ...preferences,
      [key]: enabled ? "all" : "muted",
    };
    // Remove keys set to "all" (default) to keep the object clean
    if (enabled) {
      delete updated[key];
    }
    mutation.mutate(updated, {
      onError: (err) =>
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t(($) => $.notifications.toast_failed),
        ),
    });
  };

  const savePreference = <K extends keyof NotificationPreferences>(
    key: K,
    value: NonNullable<NotificationPreferences[K]>,
  ) => {
    mutation.mutate(
      { ...preferences, [key]: value },
      {
        onError: (err) =>
          toast.error(
            err instanceof Error && err.message
              ? err.message
              : t(($) => $.notifications.toast_failed),
          ),
      },
    );
  };

  const activeAgents = agents.filter((agent) => !agent.archived_at);
  const emailEnabled = preferences.email_enabled === "true";
  const telegramEnabled = preferences.telegram_enabled === "true";
  const dailyReportEnabled = preferences.daily_report_enabled === "true";
  const reportAgentId = preferences.daily_report_agent_id ?? "";
  const hasActiveReportAgent = activeAgents.some((agent) => agent.id === reportAgentId);

  const systemEnabled = preferences.system_notifications !== "muted";

  return (
    <SettingsTab title={t(($) => $.page.tabs.notifications)} scope="workspace-only">
      <SettingsSection
        title={t(($) => $.notifications.title)}
        description={t(($) => $.notifications.description)}
        anchor="inbox"
      >
        <SettingsCard>
            {INBOX_GROUP_KEYS.map((key: InboxGroupKey) => {
              const enabled = preferences[key] !== "muted";
              return (
                <SettingsRow
                  key={key}
                  anchor={key}
                  label={t(($) => $.notifications.groups[key].label)}
                  description={t(($) => $.notifications.groups[key].description)}
                >
                  <Switch
                    checked={enabled}
                    aria-label={t(($) => $.notifications.groups[key].label)}
                    onCheckedChange={(checked) => handleToggle(key, checked)}
                  />
                </SettingsRow>
              );
            })}
        </SettingsCard>
      </SettingsSection>

      <SettingsSection
        title={t(($) => $.notifications.system.title)}
        description={t(($) => $.notifications.system.description)}
      >
        <SettingsCard>
          <SettingsRow
            anchor="system"
            label={t(($) => $.notifications.system.label)}
          >
              <Switch
                checked={systemEnabled}
                aria-label={t(($) => $.notifications.system.label)}
                onCheckedChange={(checked) => handleToggle("system_notifications", checked)}
              />
          </SettingsRow>
        </SettingsCard>

        {/* Web-only: the browser permission banners require. Renders nothing on
            desktop (OS-native delivery) or where the Notification API is absent. */}
        <BrowserNotificationSetting />
      </SettingsSection>

      <SettingsSection
        title={t(($) => $.notifications.delivery.title)}
        description={t(($) => $.notifications.delivery.description)}
        anchor="delivery"
      >
        <SettingsCard>
          <SettingsRow
            anchor="email"
            label={t(($) => $.notifications.delivery.email)}
            description={t(($) => $.notifications.delivery.email_description)}
          >
            <Switch
              checked={emailEnabled}
              aria-label={t(($) => $.notifications.delivery.email)}
              onCheckedChange={(checked) => savePreference("email_enabled", checked ? "true" : "false")}
            />
          </SettingsRow>
          {(data?.delivery_status?.email?.failed_count ?? 0) > 0 && (
            <p role="status" className="border-t py-3 text-xs text-destructive">
              {t(($) => $.notifications.delivery.delivery_failed)}
            </p>
          )}
          <div className="space-y-2 border-t py-3">
            <label htmlFor="notification-email" className="text-sm font-medium">
              {t(($) => $.notifications.delivery.email_address)}
            </label>
            <Input
              id="notification-email"
              type="email"
              autoComplete="email"
              value={emailAddress}
              placeholder="name@example.com"
              onChange={(event) => setEmailAddress(event.target.value)}
              onBlur={() => {
                if (emailAddress !== (preferences.email_address ?? "")) {
                  savePreference("email_address", emailAddress.trim().toLowerCase());
                }
              }}
            />
          </div>
          <SettingsRow
            anchor="telegram"
            label={t(($) => $.notifications.delivery.telegram)}
            description={
              data?.telegram_connected
                ? t(($) => $.notifications.delivery.telegram_connected)
                : t(($) => $.notifications.delivery.telegram_unconnected)
            }
          >
            <Switch
              checked={telegramEnabled}
              aria-label={t(($) => $.notifications.delivery.telegram)}
              onCheckedChange={(checked) => savePreference("telegram_enabled", checked ? "true" : "false")}
            />
          </SettingsRow>
          {(data?.delivery_status?.telegram?.failed_count ?? 0) > 0 && (
            <p role="status" className="border-t py-3 text-xs text-destructive">
              {t(($) => $.notifications.delivery.delivery_failed)}
            </p>
          )}
          <SettingsRow
            anchor="digest"
            label={t(($) => $.notifications.delivery.digest)}
            description={t(($) => $.notifications.delivery.digest_description)}
          >
            <Select
              value={preferences.digest_minutes ?? "60"}
              items={{
                "15": t(($) => $.notifications.delivery.every_15_minutes),
                "30": t(($) => $.notifications.delivery.every_30_minutes),
                "60": t(($) => $.notifications.delivery.every_hour),
                "240": t(($) => $.notifications.delivery.every_4_hours),
              }}
              onValueChange={(value) => {
                if (value === "15" || value === "30" || value === "60" || value === "240") savePreference("digest_minutes", value);
              }}
            >
              <SelectTrigger aria-label={t(($) => $.notifications.delivery.digest)} className="w-40">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="15">{t(($) => $.notifications.delivery.every_15_minutes)}</SelectItem>
                <SelectItem value="30">{t(($) => $.notifications.delivery.every_30_minutes)}</SelectItem>
                <SelectItem value="60">{t(($) => $.notifications.delivery.every_hour)}</SelectItem>
                <SelectItem value="240">{t(($) => $.notifications.delivery.every_4_hours)}</SelectItem>
              </SelectContent>
            </Select>
          </SettingsRow>
          <div className="border-t pt-3">
            <p className="mb-2 text-sm font-medium">{t(($) => $.notifications.delivery.events.title)}</p>
            <p className="mb-2 text-xs text-muted-foreground">{t(($) => $.notifications.delivery.events.description)}</p>
          </div>
          {CHANNEL_EVENT_KEYS.map((key: ChannelEventKey) => {
            const eventKey = key.replace("channel_event_", "") as
              | "assigned"
              | "mentioned"
              | "mention_all"
              | "status_changed"
              | "task_failed"
              | "task_completed";
            return (
              <SettingsRow
                key={key}
                anchor={key}
                label={t(($) => $.notifications.delivery.events[eventKey])}
              >
                <Switch
                  checked={(preferences[key] ?? CHANNEL_EVENT_DEFAULTS[key]) === "true"}
                  aria-label={t(($) => $.notifications.delivery.events[eventKey])}
                  onCheckedChange={(checked) => savePreference(key, checked ? "true" : "false")}
                />
              </SettingsRow>
            );
          })}
        </SettingsCard>
      </SettingsSection>

      <SettingsSection
        title={t(($) => $.notifications.daily_report.title)}
        description={t(($) => $.notifications.daily_report.description)}
        anchor="daily-report"
      >
        <SettingsCard>
          <SettingsRow
            anchor="daily-report-enabled"
            label={t(($) => $.notifications.daily_report.enabled)}
            description={t(($) => $.notifications.daily_report.enabled_description)}
          >
            <Switch
              checked={dailyReportEnabled}
              disabled={!dailyReportEnabled && !hasActiveReportAgent}
              aria-label={t(($) => $.notifications.daily_report.enabled)}
              onCheckedChange={(checked) => {
                if (checked && !hasActiveReportAgent) {
                  toast.error(t(($) => $.notifications.daily_report.choose_agent_first));
                  return;
                }
                savePreference("daily_report_enabled", checked ? "true" : "false");
              }}
            />
          </SettingsRow>
          <SettingsRow
            anchor="daily-report-agent"
            label={t(($) => $.notifications.daily_report.agent)}
            description={t(($) => $.notifications.daily_report.agent_description)}
          >
            <Select
              value={hasActiveReportAgent ? reportAgentId : "none"}
              items={[
                { label: t(($) => $.notifications.daily_report.choose_agent), value: "none" },
                ...activeAgents.map((agent) => ({ label: agent.name, value: agent.id })),
              ]}
              onValueChange={(value) => {
                if (value !== null) savePreference("daily_report_agent_id", value === "none" ? "" : value);
              }}
            >
              <SelectTrigger aria-label={t(($) => $.notifications.daily_report.agent)} className="w-56">
                <SelectValue placeholder={t(($) => $.notifications.daily_report.choose_agent)} />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="none">{t(($) => $.notifications.daily_report.choose_agent)}</SelectItem>
                {activeAgents.map((agent) => (
                  <SelectItem key={agent.id} value={agent.id}>{agent.name}</SelectItem>
                ))}
              </SelectContent>
            </Select>
          </SettingsRow>
          <SettingsRow
            anchor="daily-report-time"
            label={t(($) => $.notifications.daily_report.time)}
            description={t(($) => $.notifications.daily_report.time_description)}
          >
            <Input
              type="time"
              aria-label={t(($) => $.notifications.daily_report.time)}
              className="w-36"
              value={reportTime}
              onChange={(event) => setReportTime(event.target.value)}
              onBlur={() => {
                if (reportTime !== (preferences.daily_report_time ?? "18:00")) {
                  savePreference("daily_report_time", reportTime);
                }
              }}
            />
          </SettingsRow>
          <SettingsRow
            anchor="daily-report-timezone"
            label={t(($) => $.notifications.daily_report.timezone)}
            description={t(($) => $.notifications.daily_report.timezone_description)}
          >
            <TimezoneSelect
              value={preferences.daily_report_timezone ?? "Europe/Berlin"}
              browserSuffix={t(($) => $.preferences.timezone.browser_suffix)}
              triggerClassName="w-56"
              onValueChange={(value) => savePreference("daily_report_timezone", value)}
            />
          </SettingsRow>
          <SettingsRow
            anchor="daily-report-format"
            label={t(($) => $.notifications.daily_report.format)}
            description={t(($) => $.notifications.daily_report.format_description)}
          >
            <Select
              value={preferences.daily_report_format ?? "standard"}
              items={{
                dev: t(($) => $.notifications.daily_report.dev_format),
                standard: t(($) => $.notifications.daily_report.standard_format),
              }}
              onValueChange={(value) => {
                if (value === "dev" || value === "standard") savePreference("daily_report_format", value);
              }}
            >
              <SelectTrigger aria-label={t(($) => $.notifications.daily_report.format)} className="w-56">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="dev">{t(($) => $.notifications.daily_report.dev_format)}</SelectItem>
                <SelectItem value="standard">{t(($) => $.notifications.daily_report.standard_format)}</SelectItem>
              </SelectContent>
            </Select>
          </SettingsRow>
          <div className="space-y-2 border-t py-3">
            <label htmlFor="daily-report-instructions" className="text-sm font-medium">
              {t(($) => $.notifications.daily_report.instructions)}
            </label>
            <Textarea
              id="daily-report-instructions"
              value={reportInstructions}
              maxLength={4000}
              rows={4}
              placeholder={t(($) => $.notifications.daily_report.instructions_placeholder)}
              onChange={(event) => setReportInstructions(event.target.value)}
              onBlur={() => {
                if (reportInstructions !== (preferences.daily_report_instructions ?? "")) {
                  savePreference("daily_report_instructions", reportInstructions);
                }
              }}
            />
          </div>
          <div className="flex justify-end border-t pt-3">
            <Button
              type="button"
              variant="outline"
              disabled={!hasActiveReportAgent || reportNow.isPending}
              onClick={() => reportNow.mutate()}
            >
              {reportNow.isPending
                ? t(($) => $.notifications.daily_report.queuing)
                : t(($) => $.notifications.daily_report.create_now)}
            </Button>
          </div>
        </SettingsCard>
      </SettingsSection>
    </SettingsTab>
  );
}
