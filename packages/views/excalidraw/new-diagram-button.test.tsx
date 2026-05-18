import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { NewDiagramButton } from "./new-diagram-button";

describe("NewDiagramButton", () => {
  it("renders the supplied label and fires onClick", async () => {
    const onClick = vi.fn();
    render(<NewDiagramButton label="New diagram" onClick={onClick} />);
    const btn = screen.getByRole("button", { name: "New diagram" });
    await userEvent.click(btn);
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it("respects disabled", async () => {
    const onClick = vi.fn();
    render(
      <NewDiagramButton label="New diagram" onClick={onClick} disabled />,
    );
    const btn = screen.getByRole("button", { name: "New diagram" });
    expect(btn).toBeDisabled();
    await userEvent.click(btn);
    expect(onClick).not.toHaveBeenCalled();
  });
});
