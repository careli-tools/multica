"use client";

// The drawer is the inline editor shell. The editor itself is pulled in via
// `React.lazy` so the @excalidraw/excalidraw bundle (~2 MB) only ships when
// the editor actually opens — issue pages without a diagram never download
// it. The same lazy boundary works in both Next.js and electron-vite;
// framework-specific SSR opt-outs (`next/dynamic`'s `ssr: false`) belong in
// the app shell, not in this shared package.
import { Suspense, lazy, useCallback } from "react";
import { AlertTriangle, X } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import { DrawerSkeleton } from "./drawer-skeleton";
import type {
  ExcalidrawEditorProps,
  ExcalidrawSceneData,
} from "./excalidraw-editor";

const LazyExcalidrawEditor = lazy(() => import("./excalidraw-editor"));

export interface ExcalidrawDrawerProps extends ExcalidrawEditorProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title?: string;
  description?: string;
  className?: string;
  /** When non-null the drawer renders an inline error banner instead of the
   *  editor. Set by `useIssueExcalidraw.error` when the scene load fails. */
  error?: Error | null;
}

export function ExcalidrawDrawer({
  open,
  onOpenChange,
  title,
  description,
  error,
  className,
  ...editorProps
}: ExcalidrawDrawerProps) {
  const handleClose = useCallback(() => onOpenChange(false), [onOpenChange]);

  if (!open) return null;

  return (
    <div
      data-slot="excalidraw-drawer"
      className={cn(
        "flex flex-col overflow-hidden rounded-lg border bg-card",
        className,
      )}
      style={{ height: "70vh", minHeight: "400px" }}
    >
      <div className="flex shrink-0 items-center gap-2 border-b px-4 py-2.5">
        <div className="min-w-0 flex-1">
          <h2 className="truncate text-sm font-medium">
            {title ?? "Diagram"}
          </h2>
          {description && (
            <p className="truncate text-xs text-muted-foreground">
              {description}
            </p>
          )}
        </div>
        <button
          type="button"
          onClick={handleClose}
          className="shrink-0 rounded-md p-1 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
          aria-label="Close editor"
        >
          <X className="size-4" />
        </button>
      </div>
      <div className="min-h-0 flex-1">
        {error ? (
          <div className="flex h-full flex-col items-center justify-center gap-3 p-6 text-center">
            <AlertTriangle className="size-8 text-destructive" />
            <div>
              <p className="text-sm font-medium text-destructive">
                Failed to load diagram
              </p>
              <p className="mt-1 text-xs text-muted-foreground">
                {error.message}
              </p>
            </div>
            <p className="text-xs text-muted-foreground">
              Close the editor and try again. If the issue persists, check your
              connection or try downloading the file directly.
            </p>
          </div>
        ) : (
          <Suspense fallback={<DrawerSkeleton />}>
            <LazyExcalidrawEditor {...editorProps} />
          </Suspense>
        )}
      </div>
    </div>
  );
}

export type { ExcalidrawSceneData, ExcalidrawEditorProps };
