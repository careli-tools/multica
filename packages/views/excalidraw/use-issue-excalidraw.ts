"use client";

// Imperative open API for the issue-detail page (CAR-713). Owns the
// drawer's open/edit/new state and translates the editor's debounced
// `onChange` payloads into Multica attachment writes:
//
//   - "new"  → first save calls `createExcalidrawAttachment(issueId)`
//             and remembers the resulting id so subsequent saves PUT in
//             place (last-write-wins). The id is also exposed so a
//             follow-up render can attach the new preview to the issue
//             description.
//   - "edit" → every save PUTs to `/api/attachments/{id}`.
//
// Backend support for PUT lives in CAR-711 (still backlog at time of
// writing). On 4xx/5xx we surface a toast and keep the live scene in
// memory so the user can copy / retry rather than silently lose work.
import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  excalidrawSceneOptions,
  excalidrawKeys,
  defaultExcalidrawFilename,
} from "@multica/core/excalidraw";
import { issueKeys } from "@multica/core/issues/queries";
import { api } from "@multica/core/api";
import type { Attachment } from "@multica/core/types";
import type { ExcalidrawSceneData } from "./excalidraw-editor";

export type ExcalidrawDrawerMode =
  | { kind: "closed" }
  | { kind: "new"; viewMode?: false }
  | { kind: "edit"; attachmentId: string; viewMode: boolean };

export interface UseIssueExcalidrawOptions {
  issueId: string;
  /** Issue identifier (e.g. "MUL-713") used to label new attachments. */
  issueIdentifier?: string | null;
  /** True if the current user has write access on this issue. Controls
   *  whether the "new diagram" entry point is available and whether the
   *  drawer should fall back to viewModeEnabled when opening existing
   *  diagrams from previews. */
  canWrite: boolean;
  /** Called once when a brand-new attachment is created — the host can
   *  e.g. bind the new id to the issue description so the preview shows
   *  up on next render. */
  onAttachmentCreated?: (attachmentId: string) => void;
  /** Reports save failures so the host can render a toast. The
   *  controller stays out of the i18n layer so the toast text matches
   *  whatever locale resolution the host already runs. */
  onSaveError?: (error: Error) => void;
}

export interface UseIssueExcalidraw {
  mode: ExcalidrawDrawerMode;
  open: boolean;
  openNew: () => void;
  openExisting: (attachmentId: string, opts?: { viewMode?: boolean }) => void;
  close: () => void;
  /** initialData passed straight to the editor; undefined while loading. */
  initialData: ExcalidrawSceneData | null | undefined;
  isLoading: boolean;
  /** Wired into ExcalidrawDrawer.onChange. */
  handleChange: (scene: ExcalidrawSceneData) => void;
  /** True if a save is currently in flight. Surfaced for tests / "saving…"
   *  affordances; not currently rendered in the drawer chrome. */
  isSaving: boolean;
}

export function useIssueExcalidraw(
  options: UseIssueExcalidrawOptions,
): UseIssueExcalidraw {
  const { issueId, issueIdentifier, canWrite, onAttachmentCreated, onSaveError } = options;
  const queryClient = useQueryClient();
  const [mode, setMode] = useState<ExcalidrawDrawerMode>({ kind: "closed" });
  const [isSaving, setIsSaving] = useState(false);

  // Once we create an attachment in "new" mode, subsequent saves must
  // overwrite that same id (not POST again). The id is held in a ref so
  // the in-flight save callback closes over the live value without
  // re-rendering.
  const createdIdRef = useRef<string | null>(null);
  // Coordinates concurrent saves: a PUT that races with a still-pending
  // POST would orphan the original create. Save calls queue behind the
  // in-flight promise instead of fanning out.
  const inFlightRef = useRef<Promise<unknown> | null>(null);
  // CAR-794: the attachment's last-seen `updated_at`, sent as the optimistic-
  // locking token on the next PUT so a concurrent save from another tab is
  // rejected instead of silently clobbered. Held in a ref so the in-flight
  // save closure reads the live value. Seeded from the issue's attachment
  // list cache on edit-mode entry and refreshed from every save response;
  // null means "no known version" → the PUT falls back to last-write-wins.
  const knownUpdatedAtRef = useRef<string | null>(null);

  const editingAttachmentId =
    mode.kind === "edit" ? mode.attachmentId : null;
  const sceneQuery = useQuery(excalidrawSceneOptions(editingAttachmentId));

  // Seed (or clear) the optimistic-locking token whenever the edit target
  // changes. The issue's attachment list is almost always already cached —
  // the preview the user clicked to open the editor renders from it — so the
  // freshest known `updated_at` is read straight from the Query cache
  // without an extra round-trip. A cache miss leaves the first save
  // unguarded; subsequent saves use the token from the PUT response.
  useEffect(() => {
    if (!editingAttachmentId) {
      knownUpdatedAtRef.current = null;
      return;
    }
    const cached = queryClient.getQueryData<Attachment[]>(
      issueKeys.attachments(issueId),
    );
    const match = cached?.find((a) => a.id === editingAttachmentId);
    knownUpdatedAtRef.current = match?.updated_at || null;
  }, [editingAttachmentId, issueId, queryClient]);

  const openNew = useCallback(() => {
    if (!canWrite) return;
    createdIdRef.current = null;
    setMode({ kind: "new" });
  }, [canWrite]);

  const openExisting = useCallback<UseIssueExcalidraw["openExisting"]>(
    (attachmentId, opts) => {
      createdIdRef.current = null;
      // Read-only users always land in viewMode; writers honour an explicit
      // opt-in (preview-card "open as viewer" affordance) but default to
      // editable so the common path is one click.
      const viewMode = opts?.viewMode ?? !canWrite;
      setMode({ kind: "edit", attachmentId, viewMode });
    },
    [canWrite],
  );

  const close = useCallback(() => {
    setMode({ kind: "closed" });
  }, []);

  // Reset the in-memory create-id whenever the drawer is closed so
  // reopening "new diagram" doesn't accidentally update the last one.
  useEffect(() => {
    if (mode.kind === "closed") {
      createdIdRef.current = null;
    }
  }, [mode.kind]);

  const handleChange = useCallback(
    (scene: ExcalidrawSceneData) => {
      if (mode.kind === "closed") return;
      // viewMode never reaches `onChange` (Excalidraw suppresses it), but
      // defend against rogue events from custom subclasses / future props.
      if (mode.kind === "edit" && mode.viewMode) return;

      const targetIdAtSchedule = mode.kind === "edit"
        ? mode.attachmentId
        : createdIdRef.current;

      const work = async () => {
        try {
          setIsSaving(true);
          // If a previous save is still racing, wait for it. Errors
          // bubble through the existing handler — we just need the
          // ordering, not the result.
          if (inFlightRef.current) {
            try {
              await inFlightRef.current;
            } catch {
              // Already surfaced; do not block follow-up saves.
            }
          }

          if (targetIdAtSchedule) {
            const att = await api.updateExcalidrawAttachment(
              targetIdAtSchedule,
              scene,
              knownUpdatedAtRef.current ?? undefined,
            );
            // Adopt the server's new updated_at so the next save's If-Match
            // reflects this write. On an AttachmentConflictError this line
            // is never reached — the stale token is left in place and the
            // rejection surfaces through onSaveError for the host to handle.
            if (att.updated_at) knownUpdatedAtRef.current = att.updated_at;
            // Refresh both the scene cache (so a close-then-reopen sees
            // the latest bytes) and the issue's attachment list (size /
            // updated-at columns).
            queryClient.setQueryData(
              excalidrawKeys.scene(targetIdAtSchedule),
              scene,
            );
            queryClient.invalidateQueries({
              queryKey: issueKeys.attachments(issueId),
            });
            return att;
          }

          // First save in "new" mode → POST creates the attachment.
          const filename = defaultExcalidrawFilename(issueIdentifier ?? null);
          const att = await api.createExcalidrawAttachment(issueId, filename, scene);
          createdIdRef.current = att.id;
          // Seed the lock token from the create response so the very next
          // in-place save (same drawer session) already sends an If-Match.
          knownUpdatedAtRef.current = att.updated_at || null;
          queryClient.setQueryData(
            excalidrawKeys.scene(att.id),
            scene,
          );
          queryClient.invalidateQueries({
            queryKey: issueKeys.attachments(issueId),
          });
          onAttachmentCreated?.(att.id);
          return att;
        } catch (err) {
          const error = err instanceof Error ? err : new Error("Save failed");
          onSaveError?.(error);
          throw error;
        } finally {
          setIsSaving(false);
        }
      };

      const promise = work();
      inFlightRef.current = promise;
      // Swallow the rejection at the fire-and-forget call site —
      // `onSaveError` has already surfaced it to the host. Without this
      // the runtime would report an unhandled promise rejection because
      // the editor's `onChange` is sync and never awaits the result.
      promise.catch(() => {}).finally(() => {
        if (inFlightRef.current === promise) {
          inFlightRef.current = null;
        }
      });
    },
    [mode, issueId, issueIdentifier, onAttachmentCreated, onSaveError, queryClient],
  );

  return {
    mode,
    open: mode.kind !== "closed",
    openNew,
    openExisting,
    close,
    initialData: mode.kind === "edit"
      ? sceneQuery.data ?? (sceneQuery.isLoading ? undefined : { elements: [], appState: {}, files: {} })
      : mode.kind === "new"
        ? { elements: [], appState: {}, files: {} }
        : undefined,
    isLoading: mode.kind === "edit" && sceneQuery.isLoading,
    handleChange,
    isSaving,
  };
}
