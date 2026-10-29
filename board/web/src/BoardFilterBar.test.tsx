import { afterEach, describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import { BoardFilterBar } from "./BoardFilterBar";
import { emptyFilter } from "./boardFilter";
import type { Column } from "./api";

const column = { id: "todo", name: "To do" } as Column;
const bar = (on: boolean, total: number) => <BoardFilterBar
  counts={[{ column, shown: on ? 1 : total, total }]} activeColumn="todo" onPickColumn={() => {}}
  applied={on ? { ...emptyFilter, q: "one" } : emptyFilter} />;
const settle = () => act(() => vi.advanceTimersByTime(700));
afterEach(() => vi.useRealTimers());

describe("filter announcements", () => {
  it("announces Clear once and ignores subsequent unfiltered card additions and removals", () => {
    vi.useFakeTimers();
    const view = render(bar(false, 38));
    settle();
    expect(screen.getByRole("status").textContent).toBe("");
    view.rerender(bar(true, 38));
    settle();
    expect(screen.getByRole("status").textContent).toBe("1 of 38 cards shown");
    view.rerender(bar(false, 38));
    settle();
    expect(screen.getByRole("status").textContent).toBe("All 38 cards shown");
    for (const n of [39, 37]) {
      view.rerender(bar(false, n));
      settle();
      expect(screen.getByRole("status").textContent).toBe("All 38 cards shown");
    }
    view.rerender(bar(true, 37));
    settle();
    expect(screen.getByRole("status").textContent).toBe("1 of 37 cards shown");
    view.rerender(bar(false, 37));
    // A count update while Clear's announcement is pending must not swallow it.
    view.rerender(bar(false, 40));
    settle();
    expect(screen.getByRole("status").textContent).toBe("All 37 cards shown");
  });
});
