import { describe, expect, it } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { FilterMenu } from "./FilterMenu";
import { chipsFor, emptyFilter, type BoardFilter } from "./boardFilter";
import type { Card } from "./api";

const card = (n: number, tags: string[]) => ({ id: `c${n}`, number: n, title: `Card ${n}`, type: "bug", priority: "high", tags } as unknown as Card);
const cards = [card(1, ["0.4.5"]), card(2, ["docs"])];

function Sheet() {
  const [f, setF] = useState<BoardFilter>(emptyFilter);
  return <><button>Behind</button><FilterMenu chips={chipsFor(cards, f)} filter={f} onFilter={setF} sheet /></>;
}

// The phone's Filter sheet is modal (#141 review I2): focus stays in it until it closes, then
// goes back to the Filter button.
describe("Filter sheet focus", () => {
  it("keeps Tab inside the sheet and returns focus to Filter when it closes", () => {
    render(<Sheet />);
    const filterBtn = screen.getByRole("button", { name: "Filter" });
    fireEvent.click(filterBtn);
    const sheet = screen.getByRole("dialog", { name: "Filter cards" });
    expect(sheet.getAttribute("aria-modal")).toBe("true");
    const focusable = [...sheet.querySelectorAll<HTMLElement>("button:not([disabled]), input")];
    const first = focusable[0], last = focusable[focusable.length - 1];
    act(() => last.focus());
    fireEvent.keyDown(last, { key: "Tab" });
    expect(document.activeElement).toBe(first);
    fireEvent.keyDown(first, { key: "Tab", shiftKey: true });
    expect(document.activeElement).toBe(last);
    // focus that escapes (a screen reader, a script) is brought back
    act(() => screen.getByRole("button", { name: "Behind" }).focus());
    expect(sheet.contains(document.activeElement)).toBe(true);
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.activeElement).toBe(filterBtn);
  });

  it("closes on a tap on the backdrop, focus back on Filter", () => {
    render(<Sheet />);
    fireEvent.click(screen.getByRole("button", { name: "Filter" }));
    fireEvent.pointerDown(document.querySelector(".filter-sheet-backdrop")!);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Filter" }));
  });
});
