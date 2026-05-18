"use client";

/**
 * ExcalidrawPreview — viewer-only inline render of an `.excalidraw` attachment.
 *
 * Stufe 1 of the Excalidraw integration (CAR-707): we render diagrams as SVG
 * but do NOT mount the editor. The full `@excalidraw/excalidraw` editor stays
 * out of the issue-detail bundle because we only `import("@excalidraw/excalidraw")`
 * inside an effect, and we only call `exportToSvg`. Tree-shaking + dynamic
 * import keep the editor chunk off the initial page load.
 *
 * The dynamic-import promise is module-scoped so multiple previews on the same
 * page share one network round-trip for the library.
 */

import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type CSSProperties,
} from "react";
import { createPortal } from "react-dom";
import { ExternalLink, FileWarning, Loader2, Maximize2, X } from "lucide-react";
import type { Attachment } from "@multica/core/types";
import { cn } from "@multica/ui/lib/utils";
import { openExternal } from "../../platform";
import { useT } from "../../i18n";

type ExcalidrawModule = typeof import("@excalidraw/excalidraw");

let excalidrawModulePromise: Promise<ExcalidrawModule> | null = null;

function loadExcalidraw(): Promise<ExcalidrawModule> {
  excalidrawModulePromise ??= import("@excalidraw/excalidraw");
  return excalidrawModulePromise;
}

// Visible cap for the inline preview. Above this we offer click-to-expand
// rather than letting a tall diagram push everything else off-screen.
const PREVIEW_MAX_HEIGHT_PX = 600;

// Cap how much of the file body we'll attempt to parse. An accidentally-huge
// upload would otherwise block the main thread inside JSON.parse + exportToSvg.
// 16 MB is well above any real-world Excalidraw scene.
const MAX_FILE_BYTES = 16 * 1024 * 1024;

interface ExcalidrawScene {
  type?: unknown;
  elements?: unknown;
  appState?: Record<string, unknown> | null;
  files?: Record<string, unknown> | null;
}

// Cheap structural guard. The full Excalidraw schema is large and exposed via
// the library's own types, but for the viewer we only need enough confidence
// that `exportToSvg` won't throw on us before we hand it off. Anything missing
// or wrong here means "show file-link fallback", not "crash".
function isValidExcalidrawScene(value: unknown): value is ExcalidrawScene {
  if (value === null || typeof value !== "object") return false;
  const candidate = value as ExcalidrawScene;
  return candidate.type === "excalidraw" && Array.isArray(candidate.elements);
}

function isDarkModeActive(): boolean {
  if (typeof document === "undefined") return false;
  return document.documentElement.classList.contains("dark");
}

interface ExcalidrawPreviewProps {
  attachment: Attachment;
  className?: string;
}

type RenderState =
  | { kind: "loading" }
  | { kind: "ready"; svgMarkup: string; viewBoxHeight: number | null }
  | { kind: "error" };

export function ExcalidrawPreview({ attachment, className }: ExcalidrawPreviewProps) {
  const { t } = useT("editor");
  const [state, setState] = useState<RenderState>({ kind: "loading" });
  const [expanded, setExpanded] = useState(false);
  const [darkVersion, setDarkVersion] = useState(0);

  // Re-render when the user toggles light/dark so the SVG uses the new theme.
  useEffect(() => {
    if (typeof document === "undefined") return;
    const bump = () => setDarkVersion((v) => v + 1);
    const observer = new MutationObserver(bump);
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ["class", "data-theme"],
    });
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    let cancelled = false;
    setState({ kind: "loading" });

    async function render() {
      try {
        const response = await fetch(attachment.download_url, { mode: "cors" });
        if (!response.ok) throw new Error(`status ${response.status}`);

        const contentLength = Number(response.headers.get("content-length") ?? "0");
        if (contentLength > MAX_FILE_BYTES) throw new Error("file too large");

        const raw = await response.text();
        if (raw.length > MAX_FILE_BYTES) throw new Error("file too large");

        const parsed: unknown = JSON.parse(raw);
        if (!isValidExcalidrawScene(parsed)) throw new Error("invalid scene");

        const { exportToSvg } = await loadExcalidraw();
        const darkMode = isDarkModeActive();
        const svgElement = await exportToSvg({
          elements: parsed.elements as Parameters<typeof exportToSvg>[0]["elements"],
          appState: {
            ...(parsed.appState ?? {}),
            exportWithDarkMode: darkMode,
            exportBackground: false,
          } as Parameters<typeof exportToSvg>[0]["appState"],
          files: (parsed.files ?? null) as Parameters<typeof exportToSvg>[0]["files"],
          exportPadding: 16,
        });

        // exportToSvg returns an element with fixed pixel width/height. Strip
        // those so the preview can scale responsively inside its container.
        svgElement.removeAttribute("width");
        svgElement.removeAttribute("height");
        svgElement.setAttribute("style", "width: 100%; height: auto; display: block;");

        // Pull viewBox height for the click-to-expand affordance: we only want
        // to show the Maximize button when the diagram is taller than the cap.
        const viewBox = svgElement.getAttribute("viewBox");
        const viewBoxHeight = viewBox
          ? (() => {
              const parts = viewBox
                .split(/\s+/)
                .map((value: string) => Number.parseFloat(value));
              return parts.length === 4 && Number.isFinite(parts[3]) ? parts[3] : null;
            })()
          : null;

        if (cancelled) return;
        setState({ kind: "ready", svgMarkup: svgElement.outerHTML, viewBoxHeight });
      } catch {
        // We deliberately swallow the underlying error — for the viewer the
        // only useful distinction is "rendered" vs "show fallback". Detailed
        // diagnostics belong in the editor (Stufe 2), not the read-only path.
        if (cancelled) return;
        setState({ kind: "error" });
      }
    }

    void render();
    return () => {
      cancelled = true;
    };
    // darkVersion is intentionally a dependency so a theme toggle re-renders
    // the SVG with the matching exportWithDarkMode flag.
  }, [attachment.download_url, darkVersion]);

  const handleOpenExternal = useCallback(() => {
    openExternal(attachment.download_url);
  }, [attachment.download_url]);

  if (state.kind === "loading") {
    return (
      <PreviewFrame attachment={attachment} className={className}>
        <div className="flex items-center justify-center gap-2 py-12 text-sm text-muted-foreground">
          <Loader2 className="size-4 animate-spin" aria-hidden="true" />
          <span>{t(($) => $.excalidraw.loading)}</span>
        </div>
      </PreviewFrame>
    );
  }

  if (state.kind === "error") {
    return (
      <PreviewFrame attachment={attachment} className={className}>
        <div className="flex flex-col items-center justify-center gap-3 py-10 px-6 text-center">
          <FileWarning className="size-6 text-muted-foreground" aria-hidden="true" />
          <p className="text-sm text-muted-foreground">
            {t(($) => $.excalidraw.render_failed)}
          </p>
          <button
            type="button"
            onClick={handleOpenExternal}
            className="inline-flex items-center gap-1.5 rounded-md border border-border bg-background px-2.5 py-1 text-xs font-medium text-foreground transition-colors hover:bg-muted"
          >
            <ExternalLink className="size-3.5" aria-hidden="true" />
            {t(($) => $.excalidraw.open_file)}
          </button>
        </div>
      </PreviewFrame>
    );
  }

  const showExpand = state.viewBoxHeight === null || state.viewBoxHeight > PREVIEW_MAX_HEIGHT_PX;

  return (
    <>
      <PreviewFrame
        attachment={attachment}
        className={className}
        action={
          showExpand ? (
            <button
              type="button"
              onClick={() => setExpanded(true)}
              className="rounded-md p-1 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
              aria-label={t(($) => $.excalidraw.expand)}
              title={t(($) => $.excalidraw.expand)}
            >
              <Maximize2 className="size-3.5" aria-hidden="true" />
            </button>
          ) : undefined
        }
      >
        <ScrollableSvg
          svgMarkup={state.svgMarkup}
          maxHeight={PREVIEW_MAX_HEIGHT_PX}
          onClick={showExpand ? () => setExpanded(true) : undefined}
        />
      </PreviewFrame>
      {expanded && (
        <ExcalidrawLightbox
          svgMarkup={state.svgMarkup}
          filename={attachment.filename}
          onClose={() => setExpanded(false)}
        />
      )}
    </>
  );
}

function PreviewFrame({
  attachment,
  className,
  action,
  children,
}: {
  attachment: Attachment;
  className?: string;
  action?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <figure
      className={cn(
        "overflow-hidden rounded-lg border border-border bg-card",
        className,
      )}
      aria-label={attachment.filename}
    >
      <figcaption className="flex items-center gap-2 border-b border-border bg-muted/30 px-3 py-1.5 text-xs text-muted-foreground">
        <span className="truncate font-medium text-foreground">{attachment.filename}</span>
        {action ? <span className="ml-auto flex shrink-0 items-center">{action}</span> : null}
      </figcaption>
      {children}
    </figure>
  );
}

function ScrollableSvg({
  svgMarkup,
  maxHeight,
  onClick,
}: {
  svgMarkup: string;
  maxHeight: number;
  onClick?: () => void;
}) {
  const containerStyle: CSSProperties = {
    maxHeight: `${maxHeight}px`,
  };
  return (
    <div
      className={cn(
        "overflow-auto bg-background p-3",
        onClick && "cursor-zoom-in",
      )}
      style={containerStyle}
      onClick={onClick}
      role={onClick ? "button" : undefined}
      tabIndex={onClick ? 0 : undefined}
      onKeyDown={
        onClick
          ? (event) => {
              if (event.key === "Enter" || event.key === " ") {
                event.preventDefault();
                onClick();
              }
            }
          : undefined
      }
      // svgMarkup originates from exportToSvg() (a trusted library call) on
      // JSON we just parsed and structurally validated. We're injecting it
      // raw because there is no JSX equivalent for an opaque <svg> string.
      dangerouslySetInnerHTML={{ __html: svgMarkup }}
    />
  );
}

function ExcalidrawLightbox({
  svgMarkup,
  filename,
  onClose,
}: {
  svgMarkup: string;
  filename: string;
  onClose: () => void;
}) {
  const { t } = useT("editor");

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    document.addEventListener("keydown", handler);
    return () => document.removeEventListener("keydown", handler);
  }, [onClose]);

  const inlineMarkup = useMemo(() => ({ __html: svgMarkup }), [svgMarkup]);

  return createPortal(
    <div
      role="dialog"
      aria-modal="true"
      aria-label={filename}
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/80 p-6"
      onClick={onClose}
    >
      <div
        className="relative flex max-h-full max-w-full flex-col overflow-hidden rounded-lg bg-background shadow-xl"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center gap-2 border-b border-border bg-muted/30 px-4 py-2 text-sm">
          <span className="truncate font-medium">{filename}</span>
          <button
            type="button"
            onClick={onClose}
            className="ml-auto rounded-md p-1 text-muted-foreground transition-colors hover:bg-secondary hover:text-foreground"
            aria-label={t(($) => $.excalidraw.collapse)}
            title={t(($) => $.excalidraw.collapse)}
          >
            <X className="size-4" aria-hidden="true" />
          </button>
        </div>
        <div
          className="overflow-auto bg-background p-6"
          dangerouslySetInnerHTML={inlineMarkup}
        />
      </div>
    </div>,
    document.body,
  );
}

const EXCALIDRAW_EXTENSIONS = [".excalidraw", ".excalidraw.json"] as const;

export function isExcalidrawAttachment(attachment: Attachment): boolean {
  const filename = attachment.filename.toLowerCase();
  return EXCALIDRAW_EXTENSIONS.some((ext) => filename.endsWith(ext));
}
