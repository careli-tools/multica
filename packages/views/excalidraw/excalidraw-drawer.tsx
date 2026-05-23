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
import { Button } from "@multica/ui/components/ui/button";
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
  /** When non-null the drawer renders an inline error state instead of the
   *  editor. The editor bundle is never loaded in this state. */
  error?: Error | null;
  /** Called when the user clicks the "retry" button inside the error state.
   *  Typically invalidates the scene query so the load is re-attempted. */
  onRetry?: () => void;
}

export function ExcalidrawDrawer({
  open,
  onOpenChange,
  title,
  description,
  className,
  error,
  onRetry,
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
          <div className="flex h-full flex-col items-center justify-center gap-4 px-6 text-center">
            <AlertTriangle className="size-10 text-destructive" />
            <div>
              <h3 className="text-sm font-semibold">
                Failed to load diagram
              </h3>
              <p className="mt-1 text-sm text-muted-foreground">
                {error.message}
              </p>
            </div>
            {onRetry && (
              <Button variant="outline" size="sm" onClick={onRetry}>
                Retry
              </Button>
            )}
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
