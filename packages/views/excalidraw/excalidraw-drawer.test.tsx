import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, act, cleanup } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { ExcalidrawDrawer } from "./excalidraw-drawer";

// The shared theme hook isn't relevant to drawer-shell behavior.
const themeRef = vi.hoisted(() => ({ current: "light" as string }));
vi.mock("@multica/ui/components/common/theme-provider", () => ({
  useTheme: () => ({
    theme: themeRef.current,
    resolvedTheme: themeRef.current,
    setTheme: vi.fn(),
  }),
}));

// Stub @excalidraw/excalidraw so the heavy canvas-backed module never has
// to load in jsdom. The drawer test only cares about its own contract
// (open / close, lazy mount, prop forwarding, debounced onChange).
const editorOnChangeRef = vi.hoisted(() => ({
  current: null as
    | null
    | ((
        elements: readonly unknown[],
        appState: Record<string, unknown>,
        files: Record<string, unknown>,
      ) => void),
}));
const editorPropsRef = vi.hoisted(() => ({
  current: null as null | Record<string, unknown>,
}));
vi.mock("@excalidraw/excalidraw/index.css", () => ({}));
vi.mock("@excalidraw/excalidraw", () => ({
  Excalidraw: (props: Record<string, unknown>) => {
    editorPropsRef.current = props;
    editorOnChangeRef.current = props.onChange as typeof editorOnChangeRef.current;
    return <div data-testid="mock-excalidraw" />;
  },
}));

beforeEach(() => {
  cleanup();
  editorOnChangeRef.current = null;
  editorPropsRef.current = null;
  themeRef.current = "light";
});

function flushLazy() {
  // Two microtask flushes: one for the dynamic import promise, one for the
  // resulting Suspense re-render.
  return act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

describe("ExcalidrawDrawer", () => {
  it("does not mount the editor when closed", async () => {
    render(
      <ExcalidrawDrawer open={false} onOpenChange={() => {}} />,
    );
    expect(screen.queryByTestId("mock-excalidraw")).toBeNull();
  });

  it("lazy-mounts the editor when opened and passes UIOptions/theme/viewMode", async () => {
    const handleChange = vi.fn();
    render(
      <ExcalidrawDrawer
        open
        onOpenChange={() => {}}
        viewModeEnabled
        onChange={handleChange}
      />,
    );

    await flushLazy();

    expect(await screen.findByTestId("mock-excalidraw")).toBeInTheDocument();
    const props = editorPropsRef.current!;
    expect(props.viewModeEnabled).toBe(true);
    expect(props.theme).toBe("light");
    expect(props.UIOptions).toEqual({
      canvasActions: { saveAsImage: false },
    });
  });

  it("debounces onChange and forwards the final scene", async () => {
    vi.useFakeTimers();
    try {
      const handleChange = vi.fn();
      render(
        <ExcalidrawDrawer
          open
          onOpenChange={() => {}}
          onChange={handleChange}
          debounceMs={2000}
        />,
      );

      await act(async () => {
        await Promise.resolve();
        await Promise.resolve();
      });

      expect(editorOnChangeRef.current).toBeTypeOf("function");
      act(() => {
        editorOnChangeRef.current!([{ id: "a" }], { foo: 1 }, {});
        editorOnChangeRef.current!([{ id: "a" }, { id: "b" }], { foo: 2 }, {});
      });
      expect(handleChange).not.toHaveBeenCalled();

      act(() => {
        vi.advanceTimersByTime(2000);
      });
      expect(handleChange).toHaveBeenCalledTimes(1);
      expect(handleChange).toHaveBeenCalledWith({
        elements: [{ id: "a" }, { id: "b" }],
        appState: { foo: 2 },
        files: {},
      });
    } finally {
      vi.useRealTimers();
    }
  });

  it("closes via onOpenChange when the close button is clicked", async () => {
    const onOpenChange = vi.fn();
    function Harness() {
      const [open, setOpen] = useState(true);
      return (
        <ExcalidrawDrawer
          open={open}
          onOpenChange={(next) => {
            onOpenChange(next);
            setOpen(next);
          }}
        />
      );
    }
    render(<Harness />);
    await flushLazy();

    const closeButton = screen.getByRole("button", { name: /close editor/i });
    await userEvent.click(closeButton);
    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});
