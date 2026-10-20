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

describe("BoardView live refresh", () => {
  beforeEach(() => {
    for (const k of Object.keys(responses)) delete responses[k];
    for (const k of Object.keys(failures)) delete failures[k];
  });

  // Two change pings close together start two reloads. If the older one answers last it must not
  // put back the board as it was before the newer change: a card created by a script would vanish
  // from the open board until the next ping or a reload.
  it("keeps the newest reload when an older one answers last", async () => {
    // jsdom has no Web Animations; the board's FLIP pass calls these on every rendered card.
    Element.prototype.getAnimations ??= () => [];
    Element.prototype.animate ??= (() => ({ cancel() {} })) as unknown as Element["animate"];
    const { api, boardSocket } = await import("./api");
    let ping: () => void = () => {};
    vi.mocked(boardSocket).mockImplementation((_id: string, onPing: () => void) => { ping = onPing; return () => {}; });
    responses["/api/boards/b1"] = { id: "b1", name: "Board One", repos: ["app"] };
    responses["/api/boards/b1/columns"] = [{ id: "c1", board_id: "b1", rank: "a", name: "Todo", is_terminal: false }];
    responses["/api/boards/b1/run"] = { state: "stopped", ready: 0, blocked: 0, in_flight: 0 };
    const card = (n: number, title: string) => ({
      id: `k${n}`, board_id: "b1", number: n, rank: `r${n}`, column_id: "c1", type: "task", title, body: null,
      priority: "p2", size: null, model: "", fields: {}, dedup_key: null, external_id: null, archived_at: null,
      version: 1, gate_flag: null, auto_merge: false, merged_sha: null, merged_at: null, run_attempts: 0,
      stuck_at: null, stale_at: null, created_at: "", updated_at: "", repos: null, tags: null, links: null,
      depends_on: null, blockers: null,
    });
    const answers: ((v: unknown) => void)[] = [];
    const base = vi.mocked(api).getMockImplementation()!;
    vi.mocked(api).mockImplementation(((path: string) => {
      if (path === "/api/boards/b1/cards") return new Promise((resolve) => answers.push(resolve as (v: unknown) => void));
      return base(path);
    }) as typeof api);
    render(
      <MemoryRouter>
        <MeProvider><BoardView boardId="b1" /></MeProvider>
      </MemoryRouter>,
    );
    await vi.waitFor(() => expect(answers.length).toBe(1));
    answers[0]([card(1, "first card")]);
    await screen.findByText("first card");

    ping(); // reload A: started before the script's create
    ping(); // reload B: started after it
    await vi.waitFor(() => expect(answers.length).toBe(3));
    answers[2]([card(1, "first card"), card(2, "made by a script")]);
    await screen.findByText("made by a script");
    answers[1]([card(1, "first card")]); // A answers late, with the older board
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByText("made by a script")).not.toBeNull();
    vi.mocked(api).mockImplementation(base);
  });
});
