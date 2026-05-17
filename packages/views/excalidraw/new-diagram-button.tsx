"use client";

import { PencilRuler } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";

export interface NewDiagramButtonProps {
  onClick: () => void;
  /** Accessible label / tooltip — injected by the host (i18n stays in the
   *  consuming view file so this package doesn't need its own namespace). */
  label: string;
  disabled?: boolean;
  className?: string;
  size?: "sm" | "default";
}

// Matches the visual contract of `FileUploadButton` so the two sit
// side-by-side in the issue toolbar without extra layout overrides.
export function NewDiagramButton({
  onClick,
  label,
  disabled,
  className,
  size = "default",
}: NewDiagramButtonProps) {
  const iconSize = size === "sm" ? "h-3.5 w-3.5" : "h-4 w-4";
  const btnSize = size === "sm" ? "h-6 w-6" : "h-7 w-7";

  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled}
      aria-label={label}
      title={label}
      data-slot="new-diagram-button"
      className={cn(
        "inline-flex items-center justify-center rounded-full text-muted-foreground hover:bg-accent hover:text-foreground transition-colors disabled:opacity-50 disabled:pointer-events-none",
        btnSize,
        className,
      )}
    >
      <PencilRuler className={iconSize} />
    </button>
  );
}
