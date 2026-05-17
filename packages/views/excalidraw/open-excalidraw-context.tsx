"use client";

// Cross-component dispatch surface for "open this `.excalidraw` attachment
// in the editor drawer." The host (issue-detail page) installs the
// provider with the openers from `useIssueExcalidraw`; downstream nodes
// — the description editor's attachment NodeView (CAR-707.1) or a chat
// message's diagram preview — call `useOpenExcalidraw()` without having
// to thread the controller through every prop layer.
//
// Kept deliberately tiny: the controller still owns state, this context
// only exposes the imperative "open" call. Consumers tolerate the
// no-provider case by checking the return value (null = host has no
// drawer wired up; the preview is still a download link).
import { createContext, useContext, type ReactNode } from "react";

export interface OpenExcalidrawApi {
  openNew: () => void;
  openExisting: (attachmentId: string, opts?: { viewMode?: boolean }) => void;
}

const OpenExcalidrawContext = createContext<OpenExcalidrawApi | null>(null);

export function OpenExcalidrawProvider({
  value,
  children,
}: {
  value: OpenExcalidrawApi;
  children: ReactNode;
}) {
  return (
    <OpenExcalidrawContext.Provider value={value}>
      {children}
    </OpenExcalidrawContext.Provider>
  );
}

export function useOpenExcalidraw(): OpenExcalidrawApi | null {
  return useContext(OpenExcalidrawContext);
}
