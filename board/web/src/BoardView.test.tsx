import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { act, render, screen, fireEvent, within } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router-dom";
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
  it("retries the newest failed reload rather than remaining stale", async () => {
    const { api, boardSocket } = await import("./api");
    let ping = () => {};
    vi.mocked(boardSocket).mockImplementation((_id, onPing) => { ping = onPing; return () => {}; });
    const base = vi.mocked(api).getMockImplementation()!;
    renderBoard();
    await screen.findByText("Board One");
    vi.useFakeTimers();
    let older!: (v: unknown) => void;
    let failLatest!: (e: Error) => void;
    let calls = 0;
    vi.mocked(api).mockImplementation(((path: string) => {
      if (path === "/api/boards/b1") {
        calls++;
        if (calls === 1) return new Promise(resolve => { older = resolve as (v: unknown) => void; });
        if (calls === 2) return new Promise((_resolve, reject) => { failLatest = reject; });
      }
      return base(path);
    }) as typeof api);
    try {
      act(() => { ping(); ping(); });
      responses["/api/boards/b1"] = { id: "b1", name: "Recovered board", repos: ["app"] };
      await act(async () => { failLatest(new Error("temporary failure")); older({ id: "b1", name: "Old board", repos: ["app"] }); });
      await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
      expect(screen.queryByText("Recovered board")).not.toBeNull();
      expect(screen.queryByText("Old board")).toBeNull();
    } finally { vi.useRealTimers(); vi.mocked(api).mockImplementation(base); }
  });

  it("backs off between failed reloads and stops on a refusal that will not change", async () => {
    const { api, boardSocket } = await import("./api");
    let ping = () => {};
    vi.mocked(boardSocket).mockImplementation((_id, onPing) => { ping = onPing; return () => {}; });
    const base = vi.mocked(api).getMockImplementation()!;
    renderBoard();
    await screen.findByText("Board One");
    vi.useFakeTimers();
    let boardCalls = 0;
    const count = (status: number) => vi.mocked(api).mockImplementation(((path: string) => {
      if (path === "/api/boards/b1") {
        boardCalls++;
        return Promise.reject(new ApiError(status, { error: "x" }));
      }
      return base(path);
    }) as typeof api);
    try {
      // A server error: retried after 3 s, then 6 s (not 3 s again).
      count(503);
      await act(async () => { ping(); });
      expect(boardCalls).toBe(1);
      await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
      expect(boardCalls).toBe(2);
      await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
      expect(boardCalls).toBe(2);
      await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
      expect(boardCalls).toBe(3);
      // The board is gone: no retry at all.
      count(404);
      boardCalls = 0;
      await act(async () => { ping(); });
      expect(boardCalls).toBe(1);
      await act(async () => { await vi.advanceTimersByTimeAsync(120000); });
      expect(boardCalls).toBe(1);
    } finally { vi.useRealTimers(); vi.mocked(api).mockImplementation(base); }
  });

});


// Node's own (file-less) localStorage can shadow jsdom's in this runner: use a plain in-memory one.
function memoryStorage(): Storage {
  const m = new Map<string, string>();
  return {
    get length() { return m.size; },
    clear: () => m.clear(),
    getItem: (k) => m.get(k) ?? null,
    key: (i) => [...m.keys()][i] ?? null,
    removeItem: (k) => void m.delete(k),
    setItem: (k, v) => void m.set(k, String(v)),
  };
}

describe("BoardView in-place filter (#124)", () => {
  afterEach(() => { vi.unstubAllGlobals(); });
  const card = (n: number, column: string, over: Record<string, unknown> = {}) => ({
    id: `k${n}`, board_id: "b1", number: n, rank: `r${String(n).padStart(3, "0")}`, column_id: column, type: "task",
    title: `Card ${n}`, body: null, priority: "medium", size: null, model: "", fields: {}, dedup_key: null,
    external_id: null, archived_at: null, version: 1, gate_flag: null, auto_merge: false, merged_sha: null,
    merged_at: null, run_attempts: 0, stuck_at: null, stale_at: null, created_at: "", updated_at: "", repos: null,
    tags: null, links: null, depends_on: null, blockers: null, ...over,
  });
  beforeEach(() => {
    for (const k of Object.keys(responses)) delete responses[k];
    for (const k of Object.keys(failures)) delete failures[k];
    sessionStorage.clear(); vi.stubGlobal("localStorage", memoryStorage());
    Element.prototype.getAnimations ??= () => [];
    Element.prototype.animate ??= (() => ({ cancel() {} })) as unknown as Element["animate"];
    responses["/api/boards/b1"] = { id: "b1", name: "Board One", repos: ["app"] };
    responses["/api/boards/b1/columns"] = [
      { id: "in", board_id: "b1", rank: "a", name: "Inbox", is_terminal: false },
      { id: "done", board_id: "b1", rank: "b", name: "Done", is_terminal: true },
    ];
    responses["/api/boards/b1/run"] = { state: "stopped", ready: 0, blocked: 0, in_flight: 0 };
    responses["/api/boards/b1/cards"] = [
      ...Array.from({ length: 37 }, (_, i) => card(i + 1, "in", i < 16 && i % 4 === 0 ? { tags: ["0.4.5"], type: "bug" } : {})),
      card(50, "done", { tags: ["0.4.5"], title: "Shipped thing" }),
    ];
  });
  const Where = () => <span data-testid="where">{useLocation().pathname}</span>;
  const mount = (where = false) => render(
    <MemoryRouter><MeProvider><BoardView boardId="b1" />{where && <Where />}</MeProvider></MemoryRouter>,
  );
  const tab = (name: RegExp) => screen.getByRole("tab", { name });
  const shown = () => [...document.querySelectorAll(".card .num")].map((n) => n.textContent);
  // the toolbar's 🔍 opens into the search field
  const searchField = () => {
    const open = screen.queryByRole("button", { name: "Search cards" });
    if (open) fireEvent.click(open);
    return screen.getByRole("combobox", { name: "Search cards" }) as HTMLInputElement;
  };
  const type = (value: string) => fireEvent.change(searchField(), { target: { value } });
  const openFilter = () => fireEvent.click(screen.getByRole("button", { name: /^Filter/ }));

  it("narrows the cards in place as you type and shows filtered counts", async () => {
    mount();
    await screen.findByText("Card 37");
    expect(tab(/Inbox/).textContent).toBe("Inbox37");
    type("#12");
    expect(shown()).toEqual(["#12"]);
    expect(tab(/Inbox/).textContent).toBe("Inbox1/37");
    expect(tab(/Done/).textContent).toBe("Done0/1");
    expect(screen.getByText("No match among 1")).toBeTruthy();
  });

  it("filters by a tag chip with counts, and Clear brings every card back", async () => {
    mount();
    await screen.findByText("Card 37");
    openFilter();
    const chip = screen.getByRole("button", { name: "Tag 0.4.5, 5 cards" });
    fireEvent.click(chip);
    expect(chip.getAttribute("aria-pressed")).toBe("true");
    expect(shown()).toEqual(["#1", "#5", "#9", "#13", "#50"]);
    expect(tab(/Inbox/).textContent).toBe("Inbox4/37");
    // the type chips now count within the tag: 4 bugs, 1 task
    expect(screen.getByRole("button", { name: "Type bug, 4 cards" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Type task, 1 card" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Clear" }));
    expect(shown()).toHaveLength(38);
  });

  it("keeps the filter for the session when the board is opened again", async () => {
    const first = mount();
    await screen.findByText("Card 37");
    type("shipped");
    first.unmount();
    mount();
    await screen.findByText("Shipped thing");
    expect(searchField().value).toBe("shipped");
    expect(shown()).toEqual(["#50"]);
  });

  it("drops after the last card shown, not at the end of the hidden ones", async () => {
    const { api } = await import("./api");
    mount();
    await screen.findByText("Card 37");
    openFilter();
    fireEvent.click(screen.getByRole("button", { name: "Tag 0.4.5, 5 cards" }));
    // Inbox shows #1, #5, #9 and #13 of 37. Drag #1 below #13 (jsdom has no layout: the drop lands
    // at the end of the shown cards): it goes right after #13, before the hidden #14.
    vi.useFakeTimers();
    try {
      const src = screen.getByText("Card 1").closest("button")!;
      fireEvent.dragStart(src, { dataTransfer: { effectAllowed: "" } });
      await act(async () => { await vi.advanceTimersByTimeAsync(1); });
      const col = src.closest(".column")!;
      fireEvent.dragOver(col, { clientX: 100, clientY: 500 });
      await act(async () => { fireEvent.drop(col); });
    } finally { vi.useRealTimers(); }
    await vi.waitFor(() => expect(vi.mocked(api)).toHaveBeenCalledWith("/api/cards/k1/move",
      { json: { column_id: "in", before_card_id: "k14" } }));
  });

  it("follows the tabs pattern: one Tab stop, arrows, Home and End pick a column", async () => {
    mount();
    await screen.findByText("Card 37");
    const [inbox, done] = [tab(/Inbox/), tab(/Done/)];
    expect(inbox.tabIndex).toBe(0); expect(done.tabIndex).toBe(-1);
    inbox.focus();
    fireEvent.keyDown(inbox, { key: "ArrowRight" });
    expect(done.getAttribute("aria-selected")).toBe("true");
    expect(document.activeElement).toBe(done);
    expect(done.tabIndex).toBe(0); expect(inbox.tabIndex).toBe(-1);
    fireEvent.keyDown(done, { key: "ArrowRight" }); // wraps
    expect(inbox.getAttribute("aria-selected")).toBe("true");
    fireEvent.keyDown(inbox, { key: "End" });
    expect(done.getAttribute("aria-selected")).toBe("true");
    fireEvent.keyDown(done, { key: "Home" });
    expect(document.activeElement).toBe(inbox);
  });

  it("tells a screen reader what the filter left, once typing settles", async () => {
    mount();
    await screen.findByText("Card 37");
    const status = screen.getByRole("status");
    type("#1");
    type("#12");
    expect(status.textContent).toBe("");
    expect(status.getAttribute("aria-live")).toBe("polite");
    await vi.waitFor(() => expect(status.textContent).toBe("1 of 38 cards shown"), { timeout: 3000 });
    fireEvent.click(screen.getByRole("button", { name: "Clear" }));
    await vi.waitFor(() => expect(status.textContent).toBe("All 38 cards shown"), { timeout: 3000 });
  });

  it("forgets a remembered column that was deleted", async () => {
    localStorage.setItem("blerg-board:column:b1", "gone");
    mount();
    await screen.findByText("Card 37");
    expect(tab(/Inbox/).getAttribute("aria-selected")).toBe("true");
    await vi.waitFor(() => expect(localStorage.getItem("blerg-board:column:b1")).toBeNull());
  });

  it("keeps the toolbar to one row of controls: no chips until a filter is on, then only those", async () => {
    mount();
    await screen.findByText("Card 37");
    // the 🔍 and Filter buttons, nothing else: no strip of type/priority/tag chips, no Clear
    expect(screen.getByRole("button", { name: "Search cards" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Filter" }).getAttribute("aria-expanded")).toBe("false");
    expect(screen.queryByRole("button", { name: /^Tag / })).toBeNull();
    expect(screen.queryByRole("button", { name: "Clear" })).toBeNull();
    expect(screen.queryByRole("group", { name: "Active filters" })).toBeNull();
    openFilter();
    fireEvent.click(screen.getByRole("button", { name: "Tag 0.4.5, 5 cards" }));
    fireEvent.click(screen.getByRole("button", { name: "Type bug, 4 cards" }));
    fireEvent.keyDown(document.body, { key: "Escape" });
    expect(screen.queryByRole("dialog", { name: "Filter cards" })).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole("button", { name: "Filter, 2 on" }));
    const active = screen.getByRole("group", { name: "Active filters" });
    expect([...active.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["type: bug ×", "0.4.5 ×", "Clear"]);
    fireEvent.click(screen.getByRole("button", { name: "Remove type: bug" }));
    expect(shown()).toEqual(["#1", "#5", "#9", "#13", "#50"]);
    expect(screen.getByRole("button", { name: "Filter, 1 on" })).toBeTruthy();
  });

  it("sorts the popover's tags by count and finds one by name", async () => {
    responses["/api/boards/b1/cards"] = [
      card(1, "in", { tags: ["rare", "common"] }), card(2, "in", { tags: ["common", "middle"] }),
      card(3, "in", { tags: ["common", "middle", "Écran"] }),
    ];
    mount();
    await screen.findByText("Card 3");
    openFilter();
    const tags = within(screen.getByRole("group", { name: "Tag" }));
    expect(tags.getAllByRole("button").map((b) => b.getAttribute("aria-label"))).toEqual([
      "Tag common, 3 cards", "Tag middle, 2 cards", "Tag Écran, 1 card", "Tag rare, 1 card",
    ]);
    fireEvent.change(screen.getByRole("searchbox", { name: "Find a tag" }), { target: { value: "ecr" } });
    expect(tags.getAllByRole("button").map((b) => b.textContent)).toEqual(["Écran 1"]);
    // a picked tag is drawn selected and stays where it was
    fireEvent.click(tags.getByRole("button", { name: "Tag Écran, 1 card" }));
    expect(tags.getByRole("button", { name: "Tag Écran, 1 card" }).className).toContain("on");
  });

  it("lists matching cards under the search and opens one with ↓ and Enter", async () => {
    mount(true);
    await screen.findByText("Card 37");
    type("#12");
    const list = screen.getByRole("listbox");
    expect(within(list).getAllByRole("option").map((o) => o.textContent)).toEqual(["#12Card 12"]);
    const field = searchField();
    expect(field.getAttribute("aria-expanded")).toBe("true");
    fireEvent.keyDown(field, { key: "ArrowDown" });
    fireEvent.keyDown(field, { key: "Enter" });
    expect(screen.getByTestId("where").textContent).toBe("/boards/b1/cards/12");
    // opening a hit clears the search: the board is no longer cut down to it
    expect(shown()).toHaveLength(38);
    expect(screen.queryByRole("group", { name: "Active filters" })).toBeNull();
    expect(screen.getByRole("button", { name: "Search cards" })).toBeTruthy();
  });

  it("adds the server's hits and searches every board after Backspace twice, as the 🔍 search did", async () => {
    responses["/api/boards"] = [{ id: "b1", name: "Board One" }, { id: "b2", name: "Other board" }];
    responses["/api/search?q=deploy&board=b1"] = [card(90, "in", { title: "Deploy notes (archived)", archived_at: "2026-01-01" })];
    responses["/api/search?q=deploy"] = [card(7, "x", { board_id: "b2", title: "Deploy the other thing" })];
    mount(true);
    await screen.findByText("Card 37");
    const field = searchField();
    fireEvent.focus(field);
    // empty: a board switcher, as before
    await vi.waitFor(() => expect(within(screen.getByRole("listbox")).getByText("Other board")).toBeTruthy());
    // this board: the server adds what the cards on screen cannot answer (an archived card)
    fireEvent.change(field, { target: { value: "deploy" } });
    const archived = await screen.findByText("Deploy notes (archived)", {}, { timeout: 3000 });
    expect(archived.closest("[role=option]")!.textContent).toContain("archived");
    // Backspace twice on an empty field: every board
    fireEvent.change(field, { target: { value: "" } });
    fireEvent.keyDown(field, { key: "Backspace" });
    fireEvent.keyDown(field, { key: "Backspace" });
    expect(screen.getByRole("button", { name: "All boards" })).toBeTruthy();
    fireEvent.change(field, { target: { value: "deploy" } });
    await vi.waitFor(() => expect(screen.getByText("Deploy the other thing")).toBeTruthy(), { timeout: 3000 });
    const hit = screen.getByText("Deploy the other thing").closest("[role=option]")!;
    expect(hit.textContent).toContain("Other board");
    fireEvent.click(hit);
    expect(screen.getByTestId("where").textContent).toBe("/boards/b2/cards/7");
  });

  it("shows only the newest search's server hits, cancelling the older request", async () => {
    const { api } = await import("./api");
    const base = vi.mocked(api).getMockImplementation()!;
    const pending: { path: string; signal?: AbortSignal; resolve: (v: unknown) => void }[] = [];
    vi.mocked(api).mockImplementation(((path: string, init?: RequestInit) => {
      if (path.startsWith("/api/search")) return new Promise((resolve) => pending.push({ path, signal: init?.signal ?? undefined, resolve: resolve as (v: unknown) => void }));
      return base(path);
    }) as typeof api);
    try {
      mount();
      await screen.findByText("Card 37");
      type("zeta");
      await vi.waitFor(() => expect(pending).toHaveLength(1), { timeout: 3000 });
      type("zetas");
      await vi.waitFor(() => expect(pending).toHaveLength(2), { timeout: 3000 });
      expect(pending[0].signal?.aborted).toBe(true); // the older request is cancelled
      act(() => pending[1].resolve([card(80, "in", { title: "Newest zetas hit" })]));
      await screen.findByText("Newest zetas hit");
      // the older one answers late (a server that ignored the abort): it must not show
      await act(async () => { pending[0].resolve([card(81, "in", { title: "Stale zeta hit" })]); });
      expect(screen.queryByText("Stale zeta hit")).toBeNull();
      expect(screen.getByText("Newest zetas hit")).toBeTruthy();
      // a new search hides the last one's hits at once, before its own answer
      type("zetas2");
      expect(screen.queryByText("Newest zetas hit")).toBeNull();
      // Esc-clear and typing the old text again does not bring back stale hits without a request
      fireEvent.keyDown(searchField(), { key: "Escape" });
      fireEvent.keyDown(searchField(), { key: "Escape" });
      expect(searchField().value).toBe("");
    } finally { vi.mocked(api).mockImplementation(base); }
  });

  it("lists the search text as a removable chip while it filters the board, reload included", async () => {
    sessionStorage.setItem("blerg-board:filter:b1", JSON.stringify({ q: "shipped", tag: [], type: [], priority: [] }));
    mount();
    await screen.findByText("Shipped thing");
    const chip = screen.getByRole("button", { name: "Remove search: shipped" });
    expect(chip.textContent).toBe("search: shipped ×");
    fireEvent.click(chip);
    expect(shown()).toHaveLength(38);
    expect(screen.queryByRole("group", { name: "Active filters" })).toBeNull();
  });

  it("names the list only while it is open and announces how many cards it found", async () => {
    mount();
    await screen.findByText("Card 37");
    const field = searchField();
    expect(field.getAttribute("aria-controls")).toBeNull();
    type("Card 3");
    expect(field.getAttribute("aria-controls")).toBe(screen.getByRole("listbox").id);
    const status = field.closest(".tb-search")!.querySelector("[role=status]")!;
    expect(status.getAttribute("aria-live")).toBe("polite");
    expect(status.textContent).toBe("8 cards found");
    type("nothing like this");
    expect(status.textContent).toBe("Nothing matches");
  });

  it("does not filter this board while searching every board", async () => {
    responses["/api/boards"] = [{ id: "b1", name: "Board One" }, { id: "b2", name: "Other board" }];
    mount();
    await screen.findByText("Card 37");
    type("Card 1");
    expect(shown().length).toBeLessThan(38);
    const field = searchField();
    fireEvent.click(await screen.findByRole("button", { name: /Board One ✕/ }));
    // the text moves to the all-boards search; the board is whole again
    expect(field.value).toBe("Card 1");
    expect(shown()).toHaveLength(38);
    expect(screen.queryByRole("group", { name: "Active filters" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "All boards" }));
    expect(shown().length).toBeLessThan(38);
  });

  it("opens a card with Enter, and a board only when it is the one chosen", async () => {
    responses["/api/boards"] = [{ id: "b1", name: "Board One" }, { id: "b2", name: "Other board" }];
    mount(true);
    await screen.findByText("Card 37");
    type("other");
    await vi.waitFor(() => expect(within(screen.getByRole("listbox")).getByText("Other board")).toBeTruthy());
    const field = searchField();
    expect(field.getAttribute("aria-activedescendant")).toBeNull(); // no card: nothing chosen yet
    fireEvent.keyDown(field, { key: "Enter" });
    expect(screen.getByTestId("where").textContent).toBe("/");
    fireEvent.keyDown(field, { key: "ArrowDown" });
    fireEvent.keyDown(field, { key: "Enter" });
    expect(screen.getByTestId("where").textContent).toBe("/boards/b2");
  });

  it("closes the Filter popover when focus leaves it", async () => {
    mount();
    await screen.findByText("Card 37");
    openFilter();
    expect(screen.getByRole("dialog", { name: "Filter cards" })).toBeTruthy();
    act(() => screen.getByRole("button", { name: /Metrics/ }).focus());
    expect(screen.queryByRole("dialog", { name: "Filter cards" })).toBeNull();
  });

  it("takes / into the toolbar field instead of the search dialog", async () => {
    mount();
    await screen.findByText("Card 37");
    const ask = new Event("blerg-board:search-inline", { cancelable: true });
    act(() => { window.dispatchEvent(ask); });
    expect(ask.defaultPrevented).toBe(true);
    await vi.waitFor(() => expect(document.activeElement).toBe(screen.getByRole("combobox", { name: "Search cards" })));
    fireEvent.keyDown(document.activeElement!, { key: "Escape" });
    expect(screen.getByRole("button", { name: "Search cards" })).toBeTruthy();
  });

  it("remembers the picked column per board", async () => {
    const first = mount();
    await screen.findByText("Card 37");
    expect(tab(/Inbox/).getAttribute("aria-selected")).toBe("true");
    fireEvent.click(tab(/Done/));
    expect(tab(/Done/).getAttribute("aria-selected")).toBe("true");
    expect(document.querySelector(".column.picked h3")?.textContent).toBe("Done");
    first.unmount();
    mount();
    await screen.findByText("Card 37");
    expect(tab(/Done/).getAttribute("aria-selected")).toBe("true");
  });
});
