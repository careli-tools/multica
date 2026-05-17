import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import type { Attachment } from "@multica/core/types";

const {
  createMock,
  updateMock,
  getSceneMock,
} = vi.hoisted(() => ({
  createMock: vi.fn(),
  updateMock: vi.fn(),
  getSceneMock: vi.fn(),
}));

vi.mock("@multica/core/api", () => ({
  api: {
    createExcalidrawAttachment: createMock,
    updateExcalidrawAttachment: updateMock,
    getExcalidrawScene: getSceneMock,
  },
}));

import { useIssueExcalidraw } from "./use-issue-excalidraw";

function makeAttachment(id: string): Attachment {
  return {
    id,
    workspace_id: "ws-1",
    issue_id: "issue-1",
    comment_id: null,
    chat_session_id: null,
    chat_message_id: null,
    uploader_type: "agent",
    uploader_id: "agent-1",
    filename: `${id}.excalidraw`,
    url: `https://example.test/${id}.excalidraw`,
    download_url: `https://example.test/${id}.excalidraw?sig=x`,
    content_type: "application/vnd.excalidraw+json",
    size_bytes: 128,
    created_at: "2026-05-17T00:00:00Z",
  };
}

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 } },
  });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

const scene = {
  elements: [{ id: "rect" }],
  appState: { theme: "light" },
  files: {},
} as const;

beforeEach(() => {
  createMock.mockReset();
  updateMock.mockReset();
  getSceneMock.mockReset();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("useIssueExcalidraw", () => {
  it("starts closed and ignores openNew when the user cannot write", () => {
    const { result } = renderHook(
      () =>
        useIssueExcalidraw({
          issueId: "issue-1",
          canWrite: false,
        }),
      { wrapper },
    );
    expect(result.current.open).toBe(false);
    act(() => result.current.openNew());
    expect(result.current.mode.kind).toBe("closed");
  });

  it("openNew transitions to a blank scene for writers", () => {
    const { result } = renderHook(
      () =>
        useIssueExcalidraw({
          issueId: "issue-1",
          canWrite: true,
        }),
      { wrapper },
    );
    act(() => result.current.openNew());
    expect(result.current.mode.kind).toBe("new");
    expect(result.current.initialData).toEqual({
      elements: [],
      appState: {},
      files: {},
    });
  });

  it("openExisting defaults read-only viewers to viewMode and writers to editable", () => {
    const writer = renderHook(
      () => useIssueExcalidraw({ issueId: "issue-1", canWrite: true }),
      { wrapper },
    );
    act(() => writer.result.current.openExisting("att-1"));
    expect(writer.result.current.mode).toEqual({
      kind: "edit",
      attachmentId: "att-1",
      viewMode: false,
    });

    const viewer = renderHook(
      () => useIssueExcalidraw({ issueId: "issue-1", canWrite: false }),
      { wrapper },
    );
    act(() => viewer.result.current.openExisting("att-1"));
    expect(viewer.result.current.mode).toEqual({
      kind: "edit",
      attachmentId: "att-1",
      viewMode: true,
    });
  });

  it("first save in new mode POSTs once and subsequent saves PUT in place", async () => {
    createMock.mockResolvedValue(makeAttachment("att-new"));
    updateMock.mockResolvedValue(makeAttachment("att-new"));
    const onCreated = vi.fn();

    const { result } = renderHook(
      () =>
        useIssueExcalidraw({
          issueId: "issue-1",
          issueIdentifier: "MUL-713",
          canWrite: true,
          onAttachmentCreated: onCreated,
        }),
      { wrapper },
    );
    act(() => result.current.openNew());

    await act(async () => {
      result.current.handleChange(scene);
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(createMock).toHaveBeenCalledTimes(1);
    const [issueIdArg, filenameArg] = createMock.mock.calls[0];
    expect(issueIdArg).toBe("issue-1");
    expect(filenameArg).toMatch(/^MUL-713-.*\.excalidraw$/);
    expect(onCreated).toHaveBeenCalledWith("att-new");

    await act(async () => {
      result.current.handleChange({ ...scene, elements: [{ id: "rect2" }] });
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(createMock).toHaveBeenCalledTimes(1);
    expect(updateMock).toHaveBeenCalledTimes(1);
    expect(updateMock).toHaveBeenCalledWith("att-new", expect.any(Object));
  });

  it("edit mode always PUTs against the provided attachment id", async () => {
    updateMock.mockResolvedValue(makeAttachment("att-existing"));
    const { result } = renderHook(
      () => useIssueExcalidraw({ issueId: "issue-1", canWrite: true }),
      { wrapper },
    );
    act(() => result.current.openExisting("att-existing"));

    await act(async () => {
      result.current.handleChange(scene);
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(createMock).not.toHaveBeenCalled();
    expect(updateMock).toHaveBeenCalledWith("att-existing", expect.any(Object));
  });

  it("viewMode suppresses saves entirely", async () => {
    const { result } = renderHook(
      () => useIssueExcalidraw({ issueId: "issue-1", canWrite: false }),
      { wrapper },
    );
    act(() => result.current.openExisting("att-existing"));

    await act(async () => {
      result.current.handleChange(scene);
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(createMock).not.toHaveBeenCalled();
    expect(updateMock).not.toHaveBeenCalled();
  });

  it("forwards save errors through onSaveError without unhandled rejection", async () => {
    const err = new Error("offline");
    updateMock.mockRejectedValue(err);
    const onSaveError = vi.fn();

    const { result } = renderHook(
      () =>
        useIssueExcalidraw({
          issueId: "issue-1",
          canWrite: true,
          onSaveError,
        }),
      { wrapper },
    );
    act(() => result.current.openExisting("att-1"));

    await act(async () => {
      result.current.handleChange(scene);
      await Promise.resolve();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(onSaveError).toHaveBeenCalledWith(err);
  });

  it("closing resets create-id so the next 'new' diagram POSTs again", async () => {
    createMock
      .mockResolvedValueOnce(makeAttachment("first"))
      .mockResolvedValueOnce(makeAttachment("second"));

    const { result } = renderHook(
      () => useIssueExcalidraw({ issueId: "issue-1", canWrite: true }),
      { wrapper },
    );
    act(() => result.current.openNew());
    await act(async () => {
      result.current.handleChange(scene);
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(createMock).toHaveBeenCalledTimes(1);

    act(() => result.current.close());
    act(() => result.current.openNew());
    await act(async () => {
      result.current.handleChange(scene);
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(createMock).toHaveBeenCalledTimes(2);
    expect(updateMock).not.toHaveBeenCalled();
  });
});
