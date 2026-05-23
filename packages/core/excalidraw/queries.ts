import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { Attachment } from "../types";

// The canonical MIME for `.excalidraw` files registered server-side in
// CAR-707.2 / CAR-711. Used by the preview dispatcher (CAR-707.1) and the
// editor save path so both ends agree on a single string.
export const EXCALIDRAW_MIME = "application/vnd.excalidraw+json";
export const EXCALIDRAW_EXTENSION = ".excalidraw";

export const excalidrawKeys = {
  scene: (attachmentId: string) =>
    ["excalidraw", "scene", attachmentId] as const,
  config: () => ["excalidraw", "config"] as const,
};

export function excalidrawSceneOptions(attachmentId: string | null | undefined) {
  return queryOptions({
    queryKey: excalidrawKeys.scene(attachmentId ?? ""),
    queryFn: () => api.getExcalidrawScene(attachmentId as string),
    enabled: !!attachmentId,
    // Scenes are persisted on debounced save (2 s). React Query's default
    // 5 min staleness is plenty — the editor owns the live in-memory copy
    // while the drawer is open and the cache is only read when reopening.
  });
}

// Server may either label the file with the registered MIME or rely on the
// extension fallback (older uploads from before MIME registration land with
// `application/octet-stream`). Treat both paths as Excalidraw to avoid a
// dead "click does nothing" affordance on legacy attachments.
export function isExcalidrawAttachment(attachment: Attachment | null | undefined): boolean {
  if (!attachment) return false;
  if (attachment.content_type === EXCALIDRAW_MIME) return true;
  return attachment.filename.toLowerCase().endsWith(EXCALIDRAW_EXTENSION);
}

/** Fetch the app config to get the excalidraw-room sidecar URL. */
export function excalidrawRoomOptions() {
  return queryOptions({
    queryKey: excalidrawKeys.config(),
    queryFn: () => api.getConfig(),
    staleTime: 5 * 60 * 1000, // 5 min — config rarely changes at runtime.
  });
}

// Filename for a brand-new diagram. Includes issue identifier and a UTC
// timestamp so two diagrams created back-to-back can't collide and the
// download name stays meaningful when detached from the issue context.
export function defaultExcalidrawFilename(issueIdentifier?: string | null): string {
  const stamp = new Date()
    .toISOString()
    .replace(/[:.]/g, "-")
    .replace("T", "_")
    .slice(0, 19);
  const base = issueIdentifier ? `${issueIdentifier}-${stamp}` : `diagram-${stamp}`;
  return `${base}${EXCALIDRAW_EXTENSION}`;
}
