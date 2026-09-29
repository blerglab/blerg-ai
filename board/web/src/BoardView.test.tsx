import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { BoardView } from "./BoardView";
import { MeProvider } from "./me";
import { ApiError } from "./api";

// Starting or stopping a run used to swallow its error, so ▶ Run board
// silently did nothing. The runner's own message is the actionable half on
// desktop ("no connected daemon has repo …"), so it must reach the user.

const responses: Record<string, unknown> = {};
const failures: Record<string, ApiError> = {};

vi.mock("./api", async (importOriginal) => {
  const actual = await importOriginal<typeof import("./api")>();
  return {
    ...actual,
    boardSocket: vi.fn(() => () => {}),
    api: vi.fn((path: string) => {
      if (failures[path]) return Promise.reject(failures[path]);
      return Promise.resolve(responses[path] ?? []);
    }),
  };
});

function renderBoard(run: unknown = { state: "stopped", ready: 0, blocked: 0, in_flight: 0 }) {
  responses["/api/boards/b1"] = { id: "b1", name: "Board One", repos: ["app"] };
  responses["/api/boards/b1/columns"] = [];
  responses["/api/boards/b1/cards"] = [];
  responses["/api/boards/b1/run"] = run;
  return render(
    <MemoryRouter>
      <MeProvider><BoardView boardId="b1" /></MeProvider>
    </MemoryRouter>,
  );
}

describe("BoardView run controls", () => {
  beforeEach(() => {
    for (const k of Object.keys(responses)) delete responses[k];
    for (const k of Object.keys(failures)) delete failures[k];
  });

  it("shows the server's message when starting a run fails", async () => {
    failures["/api/boards/b1/run"] = new ApiError(503, {
      error: 'no connected daemon has repo "app" — check it out under the daemon\'s repos root, or connect a daemon',
    });
    renderBoard();
    fireEvent.click(await screen.findByRole("checkbox"));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain('no connected daemon has repo "app"');
  });

  it("shows the server's message when stopping a run fails", async () => {
    failures["/api/boards/b1/run/stop"] = new ApiError(500, { error: "could not stop the run" });
    // a running board, so the click UNCHECKS the toggle and calls stopRun
    renderBoard({ state: "running", ready: 2, blocked: 0, in_flight: 1, concurrency: 1 });
    await screen.findByText(/in flight/);
    fireEvent.click(screen.getByRole("checkbox"));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("could not stop the run");
  });
});
