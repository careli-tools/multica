import { useMemo, useState } from "react";
import { ActivityIndicator, Alert, Pressable, ScrollView, TextInput, View } from "react-native";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { router } from "expo-router";
import type { Agent } from "@multica/core/types";
import { Text } from "@/components/ui/text";
import { ModalCloseButton } from "@/components/ui/modal-close-button";
import { agentListOptions } from "@/data/queries/agents";
import { notificationPreferenceKeys, notificationPreferenceOptions } from "@/data/queries/notification-preferences";
import { api } from "@/data/api";
import { useWorkspaceStore } from "@/data/workspace-store";
import { useT } from "@/lib/i18n";

function activeAgents(agents: Agent[]): Agent[] {
  return agents.filter((agent) => !agent.archived_at);
}

export default function NotificationReportAgentSheet() {
  const { t } = useT("settings");
  const queryClient = useQueryClient();
  const workspaceId = useWorkspaceStore((s) => s.currentWorkspaceId);
  const workspaceSlug = useWorkspaceStore((s) => s.currentWorkspaceSlug);
  const [search, setSearch] = useState("");
  const { data: agents = [], isLoading } = useQuery(agentListOptions(workspaceId));
  const { data: preferences } = useQuery(notificationPreferenceOptions(workspaceId));
  const saveAgent = useMutation({
    mutationFn: (agentId: string) => {
      if (!workspaceSlug) throw new Error("Workspace context is required");
      return api.updateNotificationPreferences(
        { daily_report_agent_id: agentId },
        workspaceSlug,
      );
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: notificationPreferenceKeys.all(workspaceId) });
      router.back();
    },
    onError: (error) => Alert.alert(t("errors.save_failed"), error.message),
  });

  const currentAgentId = preferences?.preferences.daily_report_agent_id ?? "";
  const filteredAgents = useMemo(() => {
    const normalized = search.trim().toLowerCase();
    return activeAgents(agents).filter((agent) =>
      !normalized || agent.name.toLowerCase().includes(normalized),
    );
  }, [agents, search]);

  return (
    <View className="flex-1 bg-background px-4 pt-3">
      <View className="flex-row items-center gap-3 pb-3">
        <ModalCloseButton />
        <Text className="text-lg font-semibold text-foreground">
          {t("daily_report.agent")}
        </Text>
      </View>
      <TextInput
        accessibilityLabel={t("daily_report.search_agents")}
        className="mb-3 rounded-md border border-border bg-card px-3 py-2 text-foreground"
        value={search}
        onChangeText={setSearch}
        placeholder={t("daily_report.search_agents")}
        placeholderTextColor="#8b8b95"
        autoCorrect={false}
      />
      {isLoading ? (
        <View className="flex-1 items-center justify-center">
          <ActivityIndicator />
        </View>
      ) : filteredAgents.length === 0 ? (
        <View className="flex-1 items-center justify-center px-4">
          <Text className="text-center text-sm text-muted-foreground">
            {t("daily_report.no_agents")}
          </Text>
        </View>
      ) : (
        <ScrollView keyboardShouldPersistTaps="handled" className="flex-1">
          {filteredAgents.map((agent) => {
            const selected = agent.id === currentAgentId;
            return (
              <Pressable
                key={agent.id}
                accessibilityRole="radio"
                accessibilityState={{ selected }}
                className="flex-row items-center justify-between border-b border-border py-4"
                onPress={() => saveAgent.mutate(agent.id)}
                disabled={saveAgent.isPending}
              >
                <Text className="flex-1 text-base text-foreground">{agent.name}</Text>
                {selected ? <Text className="text-sm text-primary">✓</Text> : null}
              </Pressable>
            );
          })}
        </ScrollView>
      )}
    </View>
  );
}
