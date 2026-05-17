import { Skeleton } from "@multica/ui/components/ui/skeleton";

// Rendered inside the Suspense boundary while the @excalidraw/excalidraw
// chunk is fetching. Kept intentionally lightweight — the only reason we
// render anything is so the drawer doesn't flash empty between open and
// the editor's first paint.
export function DrawerSkeleton() {
  return (
    <div
      data-slot="excalidraw-drawer-skeleton"
      className="flex h-full w-full flex-col gap-3 p-6"
    >
      <Skeleton className="h-8 w-1/3" />
      <Skeleton className="h-12 w-full" />
      <Skeleton className="h-full w-full flex-1" />
    </div>
  );
}
