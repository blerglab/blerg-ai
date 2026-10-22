import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Card, Column } from "./api";
import {
  activeFilters, searchHits, chipsFor, countLabel, emptyFilter, forgetTab, isFiltering, loadFilter, loadTab, matchesFilter, pickColumn,
  saveFilter, saveTab, swipeStep, toggleChip,
} from "./boardFilter";

const card = (n: number, over: Partial<Card> = {}): Card => ({
  id: `c${n}`, board_id: "b1", number: n, rank: String(n), column_id: "col-0", type: "feature",
  title: `Card ${n}`, body: null, priority: "medium", size: null, model: "", fields: {}, dedup_key: null,
  external_id: null, archived_at: null, version: 1, gate_flag: null, auto_merge: false, merged_sha: null,
  merged_at: null, run_attempts: 0, stuck_at: null, stale_at: null, created_at: "", updated_at: "",
  repos: [], tags: [], links: [], depends_on: [], blockers: [], ...over,
});

const cards = [
  card(1, { title: "Phone layout for the board", type: "feature", priority: "high", tags: ["0.4.5", "mobile"] }),
  card(12, { title: "Login fails after wake", type: "bug", priority: "urgent", tags: ["0.4.5"] }),
  card(120, { title: "Docs: the board filter", type: "chore", priority: "low", tags: ["docs"] }),
  card(7, { title: "Card number 12 in a title", type: "bug", priority: "medium", tags: [] }),
];
const numbers = (f = emptyFilter) => cards.filter((c) => matchesFilter(c, f)).map((c) => c.number);

describe("board filter matching", () => {
  it("matches every word against the title or a tag, in any case", () => {
    expect(numbers({ ...emptyFilter, q: "BOARD" })).toEqual([1, 120]);
    expect(numbers({ ...emptyFilter, q: "board phone" })).toEqual([1]);
    expect(numbers({ ...emptyFilter, q: "0.4" })).toEqual([1, 12]);
    expect(numbers({ ...emptyFilter, q: "mobi" })).toEqual([1]);
  });
  it("matches #123 to the card number only, and a bare number to the number or the title", () => {
    expect(numbers({ ...emptyFilter, q: "#12" })).toEqual([12]);
    expect(numbers({ ...emptyFilter, q: "12" })).toEqual([12, 7]);
    expect(numbers({ ...emptyFilter, q: "#99" })).toEqual([]);
  });
  it("matches #tag to a tag that starts with it, not to the title", () => {
    expect(numbers({ ...emptyFilter, q: "#0.4" })).toEqual([1, 12]);
    expect(numbers({ ...emptyFilter, q: "#MOB" })).toEqual([1]);
    expect(numbers({ ...emptyFilter, q: "#board" })).toEqual([]); // "board" is only in titles
    expect(numbers({ ...emptyFilter, q: "#" })).toEqual([]);
  });
  it("ignores accents on both sides", () => {
    const cafe = [card(3, { title: "Café menu", tags: ["Résumé"] }), card(4, { title: "cafe\u0301 decomposed" })];
    const hit = (q: string) => cafe.filter((c) => matchesFilter(c, { ...emptyFilter, q })).map((c) => c.number);
    expect(hit("cafe")).toEqual([3, 4]);
    expect(hit("CAFÉ")).toEqual([3, 4]);
    expect(hit("#resume")).toEqual([3]);
  });
  it("treats chips in a group as alternatives and groups as narrowing each other", () => {
    expect(numbers({ ...emptyFilter, type: ["bug", "chore"] })).toEqual([12, 120, 7]);
    expect(numbers({ ...emptyFilter, type: ["bug"], tag: ["0.4.5"] })).toEqual([12]);
    expect(numbers({ ...emptyFilter, priority: ["high", "urgent"], q: "login" })).toEqual([12]);
  });
  it("shows everything when nothing is set", () => {
    expect(isFiltering(emptyFilter)).toBe(false);
    expect(isFiltering({ ...emptyFilter, q: "  " })).toBe(false);
    expect(numbers()).toEqual([1, 12, 120, 7]);
  });
});

describe("board filter chips", () => {
  const chip = (f: typeof emptyFilter, facet: string, value: string) =>
    chipsFor(cards, f).find((c) => c.facet === facet && c.value === value);
  it("lists each type, priority and tag with its count, priorities in order and tags by count", () => {
    const list = chipsFor(cards, emptyFilter);
    expect(list.filter((c) => c.facet === "type").map((c) => [c.value, c.count])).toEqual([["bug", 2], ["chore", 1], ["feature", 1]]);
    expect(list.filter((c) => c.facet === "priority").map((c) => c.value)).toEqual(["urgent", "high", "medium", "low"]);
    expect(list.filter((c) => c.facet === "tag").map((c) => [c.value, c.count])).toEqual([["0.4.5", 2], ["docs", 1], ["mobile", 1]]);
  });
  it("counts each chip against the search and the other groups, not its own", () => {
    const f = toggleChip(emptyFilter, "type", "bug");
    expect(chip(f, "type", "feature")?.count).toBe(1); // picking bug does not zero the other types
    expect(chip(f, "tag", "0.4.5")?.count).toBe(1); // only the bug #12 carries it
    expect(chip(f, "type", "bug")?.on).toBe(true);
    expect(chip({ ...emptyFilter, q: "board" }, "type", "bug")?.count).toBe(0);
  });
  it("lists the tags that are on first, so one row of chips never hides them", () => {
    const tags = chipsFor(cards, { ...emptyFilter, tag: ["mobile"] }).filter((c) => c.facet === "tag").map((c) => c.value);
    expect(tags[0]).toBe("mobile");
  });
  it("keeps a chip that is on even when no card carries it", () => {
    expect(chip({ ...emptyFilter, tag: ["gone"] }, "tag", "gone")).toMatchObject({ count: 0, on: true });
  });
  it("toggles a chip on and off", () => {
    const on = toggleChip(emptyFilter, "tag", "docs");
    expect(on.tag).toEqual(["docs"]);
    expect(toggleChip(on, "tag", "docs").tag).toEqual([]);
  });
  it("labels a filtered count as shown/total", () => {
    expect(countLabel(4, 37, true)).toBe("4/37");
    expect(countLabel(37, 37, false)).toBe("37");
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

describe("board filter memory", () => {
  beforeEach(() => { sessionStorage.clear(); vi.stubGlobal("localStorage", memoryStorage()); });
  afterEach(() => { vi.unstubAllGlobals(); });
  it("keeps the filter per board for the session", () => {
    const f = { ...emptyFilter, q: "login", tag: ["0.4.5"] };
    saveFilter("b1", f);
    expect(loadFilter("b1")).toEqual(f);
    expect(loadFilter("b2")).toEqual(emptyFilter);
    saveFilter("b1", emptyFilter);
    expect(sessionStorage.length).toBe(0);
    sessionStorage.setItem("blerg-board:filter:b3", "{not json");
    expect(loadFilter("b3")).toEqual(emptyFilter);
  });
  it("remembers the picked column per board and falls back to the first", () => {
    const cols = ["Inbox", "Ready"].map((name, i) => ({ id: `col-${i}`, name } as Column));
    expect(pickColumn(cols, loadTab("b1"))).toBe("col-0");
    saveTab("b1", "col-1");
    expect(pickColumn(cols, loadTab("b1"))).toBe("col-1");
    expect(pickColumn(cols, "deleted-column")).toBe("col-0");
    expect(pickColumn([], null)).toBeNull();
    forgetTab("b1");
    expect(loadTab("b1")).toBeNull();
  });
});

describe("swipe between columns", () => {
  it("steps only on a clear sideways swipe", () => {
    expect(swipeStep(-120, 10)).toBe(1);
    expect(swipeStep(120, -20)).toBe(-1);
    expect(swipeStep(-40, 0)).toBe(0); // too short: a tap or a wobble
    expect(swipeStep(-90, 200)).toBe(0); // mostly vertical: scrolling
  });
});

describe("toolbar helpers (#141)", () => {
  it("names the active filters as the toolbar shows them, a tag by its name alone", () => {
    const f = { ...emptyFilter, q: "login", type: ["bug"], priority: ["high"], tag: ["0.4.5"] };
    expect(activeFilters(f).map((a) => a.label)).toEqual(["type: bug", "priority: high", "0.4.5"]);
    expect(activeFilters({ ...emptyFilter, q: "only text" })).toEqual([]);
  });
  it("lists the search's hits, a typed number first, and nothing for an empty search", () => {
    expect(searchHits(cards, "12").map((c) => c.number)).toEqual([12, 7]);
    expect(searchHits(cards, "#12").map((c) => c.number)).toEqual([12]);
    expect(searchHits(cards, "board", 1).map((c) => c.number)).toEqual([1]);
    expect(searchHits(cards, "  ")).toEqual([]);
  });
});
