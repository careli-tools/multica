import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Attachment } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

// Mock `@excalidraw/excalidraw` BEFORE the component module loads. The real
// package pulls in a heavy editor + Roughjs and isn't safe to load under
// jsdom; we only care that the component invokes `exportToSvg` with the
// parsed scene and renders the resulting markup.
const exportToSvgMock = vi.fn(async (_options: unknown): Promise<SVGSVGElement> => {
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("viewBox", "0 0 400 200");
  svg.setAttribute("data-testid", "rendered-svg");
  svg.innerHTML = "<rect width=\"400\" height=\"200\" fill=\"#abc\" />";
  return svg;
});

vi.mock("@excalidraw/excalidraw", () => ({
  exportToSvg: (options: unknown) => exportToSvgMock(options),
}));

import { ExcalidrawPreview, isExcalidrawAttachment } from "./excalidraw-preview";

const SCENE_OK = JSON.stringify({
  type: "excalidraw",
  version: 2,
  source: "test",
  elements: [
    {
      type: "rectangle",
      x: 10,
      y: 10,
      width: 100,
      height: 50,
    },
  ],
  appState: { viewBackgroundColor: "#fff" },
  files: {},
});

function makeAttachment(overrides: Partial<Attachment> = {}): Attachment {
  return {
    id: "att-1",
    workspace_id: "ws-1",
    issue_id: "issue-1",
    comment_id: null,
    chat_session_id: null,
    chat_message_id: null,
    uploader_type: "member",
    uploader_id: "user-1",
    filename: "diagram.excalidraw",
    url: "https://cdn.test/diagram.excalidraw",
    download_url: "https://cdn.test/diagram.excalidraw?sig=abc",
    content_type: "application/json",
    size_bytes: 256,
    created_at: "2026-05-17T00:00:00Z",
    ...overrides,
  };
}

function mockFetch(body: string, ok = true, contentLength = `${body.length}`) {
  const headers = new Headers();
  if (contentLength) headers.set("content-length", contentLength);
  globalThis.fetch = vi.fn(async () =>
    new Response(ok ? body : "", { status: ok ? 200 : 500, headers }),
  ) as unknown as typeof fetch;
}

describe("isExcalidrawAttachment", () => {
  it("recognises .excalidraw and .excalidraw.json", () => {
    expect(isExcalidrawAttachment(makeAttachment({ filename: "a.excalidraw" }))).toBe(true);
    expect(isExcalidrawAttachment(makeAttachment({ filename: "B.Excalidraw.JSON" }))).toBe(true);
  });

  it("rejects other files", () => {
    expect(isExcalidrawAttachment(makeAttachment({ filename: "notes.md" }))).toBe(false);
    expect(isExcalidrawAttachment(makeAttachment({ filename: "diagram.png" }))).toBe(false);
  });
});

describe("ExcalidrawPreview", () => {
  beforeEach(() => {
    exportToSvgMock.mockClear();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("fetches and renders the SVG markup", async () => {
    mockFetch(SCENE_OK);
    renderWithI18n(<ExcalidrawPreview attachment={makeAttachment()} />);

    await waitFor(() => {
      expect(screen.getByTestId("rendered-svg")).toBeInTheDocument();
    });

    expect(exportToSvgMock).toHaveBeenCalledTimes(1);
    const args = exportToSvgMock.mock.calls[0]![0] as {
      elements: unknown[];
      appState: { exportWithDarkMode: boolean; exportBackground: boolean };
    };
    expect(Array.isArray(args.elements)).toBe(true);
    expect(args.appState.exportBackground).toBe(false);
  });

  it("shows the loading state before the fetch resolves", () => {
    let release: () => void = () => {};
    const pending = new Promise<Response>((resolve) => {
      release = () => resolve(new Response(SCENE_OK, { status: 200 }));
    });
    globalThis.fetch = vi.fn(() => pending) as unknown as typeof fetch;

    renderWithI18n(<ExcalidrawPreview attachment={makeAttachment()} />);
    expect(screen.getByText(/loading diagram/i)).toBeInTheDocument();
    release();
  });

  it("falls back to the file-link UI when JSON is malformed", async () => {
    mockFetch("not valid json {{{");
    renderWithI18n(<ExcalidrawPreview attachment={makeAttachment()} />);

    await waitFor(() => {
      expect(screen.getByText(/couldn't render/i)).toBeInTheDocument();
    });
    expect(screen.getByRole("button", { name: /open file/i })).toBeInTheDocument();
    expect(exportToSvgMock).not.toHaveBeenCalled();
  });

  it("falls back when the scene is missing required fields", async () => {
    mockFetch(JSON.stringify({ type: "something-else", elements: [] }));
    renderWithI18n(<ExcalidrawPreview attachment={makeAttachment()} />);

    await waitFor(() => {
      expect(screen.getByText(/couldn't render/i)).toBeInTheDocument();
    });
    expect(exportToSvgMock).not.toHaveBeenCalled();
  });

  it("falls back when the network call fails", async () => {
    globalThis.fetch = vi.fn(async () => {
      throw new TypeError("network");
    }) as unknown as typeof fetch;

    renderWithI18n(<ExcalidrawPreview attachment={makeAttachment()} />);

    await waitFor(() => {
      expect(screen.getByText(/couldn't render/i)).toBeInTheDocument();
    });
  });

  it("opens a lightbox when the diagram is taller than the inline cap", async () => {
    exportToSvgMock.mockImplementationOnce(async () => {
      const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
      svg.setAttribute("viewBox", "0 0 800 1200");
      svg.setAttribute("data-testid", "tall-svg");
      return svg as unknown as SVGSVGElement;
    });
    mockFetch(SCENE_OK);

    renderWithI18n(<ExcalidrawPreview attachment={makeAttachment()} />);
    await waitFor(() => {
      expect(screen.getByTestId("tall-svg")).toBeInTheDocument();
    });

    const expandButton = screen.getByRole("button", { name: /expand/i });
    await userEvent.click(expandButton);

    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });
});
