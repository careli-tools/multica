"use client";

// The drawer is the small, always-available shell. The editor itself is
// pulled in via `React.lazy` so the @excalidraw/excalidraw bundle (~2 MB)
// only ships when the drawer actually opens — issue pages without a diagram
// never download it. The same lazy boundary works in both Next.js and
// electron-vite; framework-specific SSR opt-outs (`next/dynamic`'s
// `ssr: false`) belong in the app shell, not in this shared package.
import { Suspense, lazy } from "react";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@multica/ui/components/ui/sheet";
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
}

export function ExcalidrawDrawer({
  open,
  onOpenChange,
  title,
  description,
  ...editorProps
}: ExcalidrawDrawerProps) {
  // Title/description are required by Base UI's Dialog primitive for a11y;
  // the consumer (issue detail page in CAR-713) supplies translated strings.
  // The sr-only fallbacks below ship un-translated only when the consumer
  // passes nothing — a deliberate dev affordance, not user-visible copy.
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        // Override the shared Sheet's default `sm:max-w-sm` cap so the
        // editor gets the full 80vw slide-in panel the spec calls for.
        className="flex h-full w-[80vw] flex-col gap-0 p-0 sm:max-w-none"
      >
        <SheetHeader className={title ? "border-b" : "sr-only"}>
          {/* eslint-disable-next-line i18next/no-literal-string -- sr-only dev fallback when caller omits title */}
          <SheetTitle>{title ?? "Diagram"}</SheetTitle>
          {/* eslint-disable-next-line i18next/no-literal-string -- sr-only dev fallback when caller omits description */}
          <SheetDescription className={description ? undefined : "sr-only"}>
            {description ?? "Excalidraw diagram editor"}
          </SheetDescription>
        </SheetHeader>
        <div className="min-h-0 flex-1">
          {open ? (
            <Suspense fallback={<DrawerSkeleton />}>
              <LazyExcalidrawEditor {...editorProps} />
            </Suspense>
          ) : null}
        </div>
      </SheetContent>
    </Sheet>
  );
}

export type { ExcalidrawSceneData, ExcalidrawEditorProps };
