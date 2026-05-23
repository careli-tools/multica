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
  /** initialData passed straight to the editor; undefined while loading,
   *  null when an error occurred (so the drawer can show an error UI
   *  instead of an empty canvas). */
  initialData: ExcalidrawSceneData | null | undefined;
  isLoading: boolean;
  /** Non-null when the scene query failed. Consumers render an inline error
   *  state rather than a blank canvas. */
  error: Error | null;
  /** Refetches the scene query so the host can offer a "retry"
   *  affordance. */
  retry: () => void;
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

  const editingAttachmentId =
    mode.kind === "edit" ? mode.attachmentId : null;
  const sceneQuery = useQuery(excalidrawSceneOptions(editingAttachmentId));

  const retry = useCallback(() => {
    if (editingAttachmentId) {
      queryClient.refetchQueries({
        queryKey: excalidrawKeys.scene(editingAttachmentId),
      });
    }
  }, [editingAttachmentId, queryClient]);

  const sceneError: Error | null =
    sceneQuery.error instanceof Error
      ? sceneQuery.error
      : sceneQuery.error
        ? new Error("Failed to load diagram")
        : null;

  // Once we create an attachment in "new" mode, subsequent saves must
  // overwrite that same id (not POST again). The id is held in a ref so
  // the in-flight save callback closes over the live value without
  // re-rendering.
  const createdIdRef = useRef<string | null>(null);
  // Coordinates concurrent saves: a PUT that races with a still-pending
  // POST would orphan the original create. Save calls queue behind the
  // in-flight promise instead of fanning out.
  const inFlightRef = useRef<Promise<unknown> | null>(null);

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
            // Echo back the ETag observed when the scene was loaded (or
            // last saved) so the server can reject a save built on a stale
            // read with 412 instead of clobbering a concurrent edit
            // (CAR-794). The ETag lives alongside the cached scene.
            const cached = queryClient.getQueryData<{ etag?: string | null }>(
              excalidrawKeys.scene(targetIdAtSchedule),
            );
            const att = await api.updateExcalidrawAttachment(
              targetIdAtSchedule,
              scene,
              cached?.etag ?? undefined,
            );
            // Refresh the scene cache — with the fresh ETag so the next
            // save's If-Match reflects this write — and the issue's
            // attachment list (size / updated-at columns).
            queryClient.setQueryData(
              excalidrawKeys.scene(targetIdAtSchedule),
              { ...scene, etag: att.updated_at ?? null },
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
          queryClient.setQueryData(
            excalidrawKeys.scene(att.id),
            { ...scene, etag: att.updated_at ?? null },
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
    // The scene cache also carries the ETag (CAR-794); strip it back down
    // to the scene shape the editor's initialData expects.
    // When sceneQuery has an error we return null so the drawer renders an
    // inline error state rather than a blank canvas.
    initialData: mode.kind === "edit"
      ? sceneQuery.data
        ? {
            elements: sceneQuery.data.elements,
            appState: sceneQuery.data.appState,
            files: sceneQuery.data.files,
          }
        : sceneQuery.error
          ? null
          : sceneQuery.isLoading
            ? undefined
            : { elements: [], appState: {}, files: {} }
      : mode.kind === "new"
        ? { elements: [], appState: {}, files: {} }
        : undefined,
    isLoading: mode.kind === "edit" && sceneQuery.isLoading,
    error: sceneError,
    retry,
    handleChange,
    isSaving,
  };
}
