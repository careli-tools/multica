"use client";

// The Excalidraw editor wraps `@excalidraw/excalidraw` and is the heavy
// payload that must be code-split. It is the default export of this module
// so that consumers (the drawer in this package, or framework-specific
// wrappers in apps/web / apps/desktop) can pull it in via `React.lazy` or
// `next/dynamic` without pulling in any of the surrounding sheet/UI code.
import { useCallback, useEffect, useRef } from "react";
import { Excalidraw } from "@excalidraw/excalidraw";
import "@excalidraw/excalidraw/index.css";
import { useTheme } from "@multica/ui/components/common/theme-provider";

// Loose-typed scene payload. We deliberately do not import the deep
// `@excalidraw/excalidraw/types/...` paths here — they change across minor
// versions and the persistence boundary in CAR-708 stores raw JSON, so
// upstream callers do not benefit from the tighter compile-time types.
export type ExcalidrawSceneData = {
  elements?: readonly unknown[];
  appState?: Record<string, unknown>;
  files?: Record<string, unknown>;
};

export interface ExcalidrawEditorProps {
  initialData?: ExcalidrawSceneData | null;
  // Fires `debounceMs` after the last edit. A pending change is also flushed
  // on unmount so closing the drawer can never drop the most recent edits.
  onChange?: (data: ExcalidrawSceneData) => void;
  viewModeEnabled?: boolean;
  debounceMs?: number;
  /** excalidraw-room WebSocket URL for live collaboration. */
  roomUrl?: string;
  /** Unique room identifier (e.g. attachment or issue id). */
  roomId?: string;
}

export default function ExcalidrawEditor({
  initialData,
  onChange,
  viewModeEnabled = false,
  debounceMs = 2000,
}: ExcalidrawEditorProps) {
  const { resolvedTheme } = useTheme();
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pendingRef = useRef<ExcalidrawSceneData | null>(null);
  const onChangeRef = useRef(onChange);

  useEffect(() => {
    onChangeRef.current = onChange;
  }, [onChange]);

  useEffect(() => {
    return () => {
      if (debounceRef.current) {
        clearTimeout(debounceRef.current);
        debounceRef.current = null;
      }
      if (pendingRef.current && onChangeRef.current) {
        onChangeRef.current(pendingRef.current);
        pendingRef.current = null;
      }
    };
  }, []);

  const handleChange = useCallback(
    (
      elements: readonly unknown[],
      appState: Record<string, unknown>,
      files: Record<string, unknown>,
    ) => {
      pendingRef.current = { elements, appState, files };
      if (debounceRef.current) clearTimeout(debounceRef.current);
      debounceRef.current = setTimeout(() => {
        debounceRef.current = null;
        const scene = pendingRef.current;
        pendingRef.current = null;
        if (scene && onChangeRef.current) onChangeRef.current(scene);
      }, debounceMs);
    },
    [debounceMs],
  );

  const theme = resolvedTheme === "dark" ? "dark" : "light";

  return (
    <div data-slot="excalidraw-editor" className="h-full w-full">
      <Excalidraw
        theme={theme}
        initialData={
          (initialData ?? undefined) as React.ComponentProps<
            typeof Excalidraw
          >["initialData"]
        }
        viewModeEnabled={viewModeEnabled}
        onChange={
          handleChange as unknown as React.ComponentProps<
            typeof Excalidraw
          >["onChange"]
        }
        // Die `collab`-Prop existiert in v0.18.x nicht und UIOptions hat keine
        // collab-Bezogene Einstellung. renderTopRightUI wird explizit auf null
        // gesetzt, damit kein LiveCollaborationTrigger (auch nicht in zukuenftigen
        // Minor-Releases) im Single-User-Inline-Editor erscheint.
        renderTopRightUI={() => null}
        UIOptions={{
          canvasActions: {
            saveAsImage: false,
          },
        }}
      />
    </div>
  );
}
