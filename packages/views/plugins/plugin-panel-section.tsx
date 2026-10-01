"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { pluginInstallationsOptions } from "@multica/core/plugins";
import { useCurrentWorkspace } from "@multica/core/paths";
import { useFeatureEnabled } from "@multica/core/config";
import { PLUGINS_V1_FLAG } from "@multica/core/feature-flags";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "../i18n";
import { isDesktopShell } from "../platform/local-directory";
import { PluginSurfaceFrame } from "./plugin-surface-frame";

/**
 * Renders every enabled `issue_panel` surface on an issue.
 *
 * Disabled installations are filtered here AND refused by the Action API — the
 * client filter is presentation, the server check is the control. A surface
 * left open in a stale tab stops working even though this component never
 * re-rendered.
 */
export function PluginPanelSection({ issueId, projectId, type = "issue_panel" }: { issueId?: string; projectId?: string; type?: "issue_panel" | "project_panel" | "workspace_panel" }) {
  const [open, setOpen] = useState(false);
  const { t } = useT("issues");
  const platform = isDesktopShell() ? "desktop" : "web";
  const workspace = useCurrentWorkspace();
  const wsId = workspace?.id ?? "";
  const pluginsEnabled = useFeatureEnabled(PLUGINS_V1_FLAG, false);

  const { data } = useQuery({ ...pluginInstallationsOptions(wsId), enabled: pluginsEnabled && wsId.length > 0 });

  const panels = useMemo(() => {
    const installations = data?.plugins ?? [];
    return installations
      .filter((installation) => installation.enabled === true)
      .flatMap((installation) =>
        installation.surfaces
          .filter((surface) => surface.type === type)
          // An empty list means "anywhere"; a declared list is a filter, not a
          // hint, or a desktop-only panel renders on web anyway.
          .filter((surface) => (surface.platforms ?? []).length === 0 || (surface.platforms ?? []).includes(platform))
          .map((surface) => ({ installation, surface })),
      );
  }, [data, platform, type]);

  if (!pluginsEnabled || panels.length === 0) return null;

  return (
    <section className="space-y-3">
      <h3 className="px-0.5 text-caption font-medium text-muted-foreground">{t(($) => $.plugins.panels)}</h3>
      {type !== "issue_panel" && <Button type="button" variant="outline" size="sm" aria-expanded={open} onClick={() => setOpen(!open)}>{panels.map(({ surface }) => surface.name).join(" · ")}</Button>}
      {(type === "issue_panel" || open) && panels.map(({ installation, surface }) => (
        <PluginSurfaceFrame
          key={`${installation.id}:${surface.key}`}
          wsId={wsId}
          installation={installation}
          surface={surface}
          issueId={issueId}
          projectId={projectId}
        />
      ))}
    </section>
  );
}
