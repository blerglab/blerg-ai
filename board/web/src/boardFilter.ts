import type { Card, Column } from "./api";

// The board's in-place filter: a search box and chips for tag, type and priority. It narrows the
// cards a board shows without leaving it (the 🔍 search modal still jumps to any card anywhere).
//
// Matching: every word of the search must match the card. A word matches the title (substring,
// any case, accents ignored), a tag (substring), or, written as "#123" or "123", the card's number
// exactly. "#ops" means a tag that starts with "ops". Chips:
// several chips in one group are alternatives (type bug OR feature); groups narrow each other (a
// type AND a tag).

export type Facet = "tag" | "type" | "priority";
export interface BoardFilter { q: string; tag: string[]; type: string[]; priority: string[] }
export const emptyFilter: BoardFilter = { q: "", tag: [], type: [], priority: [] };
export const facets: Facet[] = ["type", "priority", "tag"];

export function isFiltering(f: BoardFilter): boolean {
  return f.q.trim() !== "" || f.tag.length > 0 || f.type.length > 0 || f.priority.length > 0;
}

function values(card: Card, facet: Facet): string[] {
  if (facet === "tag") return card.tags ?? [];
  const v = facet === "type" ? card.type : card.priority;
  return v ? [v] : [];
}

// fold: the form both sides are compared in: lower case, accents removed ("Café" → "cafe").
export function fold(s: string): string {
  return s.normalize("NFD").replace(/\p{M}+/gu, "").toLowerCase();
}

// words: a search split into folded words, once per search rather than once per card.
export function words(q: string): string[] {
  return fold(q).trim().split(/\s+/).filter(Boolean);
}

function matchesWord(card: Card, word: string): boolean {
  const num = /^#?(\d+)$/.exec(word);
  if (num && card.number === Number(num[1])) return true;
  if (word.startsWith("#")) {
    if (num) return false; // "#12" means the number, nothing else
    const tag = word.slice(1);
    return tag !== "" && (card.tags ?? []).some((t) => fold(t).startsWith(tag));
  }
  if (fold(card.title).includes(word)) return true;
  return (card.tags ?? []).some((t) => fold(t).includes(word));
}

export function matchesSearch(card: Card, q: string | string[]): boolean {
  return (typeof q === "string" ? words(q) : q).every((w) => matchesWord(card, w));
}

function matchesFacets(card: Card, f: BoardFilter, skip?: Facet): boolean {
  for (const facet of facets) {
    if (facet === skip || f[facet].length === 0) continue;
    const have = values(card, facet);
    if (!f[facet].some((v) => have.includes(v))) return false;
  }
  return true;
}

export function matchesFilter(card: Card, f: BoardFilter, q: string[] = words(f.q)): boolean {
  return matchesSearch(card, q) && matchesFacets(card, f);
}

export interface Chip { facet: Facet; value: string; count: number; on: boolean }

const priorityOrder = ["urgent", "high", "medium", "low"];

// chipsFor: every value the board's cards carry, per group, with how many cards the chip would
// show: the cards that match the search and the OTHER groups' chips (a chip's own group is left
// out, or picking one type would zero every other type's count). A chip that is on is always
// listed, even at 0, so it can be switched off.
export function chipsFor(cards: Card[], f: BoardFilter): Chip[] {
  const out: Chip[] = [];
  const q = words(f.q);
  const searched = cards.filter((card) => matchesSearch(card, q));
  for (const facet of facets) {
    const counts = new Map<string, number>();
    for (const card of cards) for (const v of values(card, facet)) if (!counts.has(v)) counts.set(v, 0);
    for (const card of searched) {
      if (!matchesFacets(card, f, facet)) continue;
      for (const v of new Set(values(card, facet))) counts.set(v, (counts.get(v) ?? 0) + 1);
    }
    for (const v of f[facet]) if (!counts.has(v)) counts.set(v, 0);
    const list = [...counts].map(([value, count]) => ({ facet, value, count, on: f[facet].includes(value) }));
    list.sort((a, b) => {
      if (facet === "priority") {
        const ai = priorityOrder.indexOf(a.value), bi = priorityOrder.indexOf(b.value);
        return (ai < 0 ? 99 : ai) - (bi < 0 ? 99 : bi) || a.value.localeCompare(b.value);
      }
      // tags: the ones that are on first (so a collapsed strip never hides them), then by count
      if (facet === "tag") return Number(b.on) - Number(a.on) || b.count - a.count || a.value.localeCompare(b.value);
      return a.value.localeCompare(b.value);
    });
    out.push(...list);
  }
  return out;
}

export function toggleChip(f: BoardFilter, facet: Facet, value: string): BoardFilter {
  const cur = f[facet];
  return { ...f, [facet]: cur.includes(value) ? cur.filter((v) => v !== value) : [...cur, value] };
}

// countLabel: a column's count. Filtered, it reads "shown/total" ("4/37") so a column the filter
// empties does not look like an empty column, and the size of the whole column stays in sight.
export function countLabel(shown: number, total: number, filtering: boolean): string {
  return filtering ? `${shown}/${total}` : String(shown);
}

// The filter is kept per board for the browser session (sessionStorage): it survives opening a
// card and coming back, or a reload, and a new tab starts clean. The phone's picked column is kept
// per board for good (localStorage).
const filterKey = (boardId: string) => `blerg-board:filter:${boardId}`;
const tabKey = (boardId: string) => `blerg-board:column:${boardId}`;

function strings(v: unknown): string[] {
  return Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : [];
}

export function loadFilter(boardId: string): BoardFilter {
  try {
    const raw = JSON.parse(sessionStorage.getItem(filterKey(boardId)) ?? "null");
    if (!raw || typeof raw !== "object") return emptyFilter;
    return { q: typeof raw.q === "string" ? raw.q : "", tag: strings(raw.tag), type: strings(raw.type), priority: strings(raw.priority) };
  } catch { return emptyFilter; }
}

export function saveFilter(boardId: string, f: BoardFilter) {
  try {
    if (isFiltering(f)) sessionStorage.setItem(filterKey(boardId), JSON.stringify(f));
    else sessionStorage.removeItem(filterKey(boardId));
  } catch { /* storage unavailable: the filter just isn't kept */ }
}

export function loadTab(boardId: string): string | null {
  try { return localStorage.getItem(tabKey(boardId)); } catch { return null; }
}

export function saveTab(boardId: string, columnId: string) {
  try { localStorage.setItem(tabKey(boardId), columnId); } catch { /* not kept */ }
}

export function forgetTab(boardId: string) {
  try { localStorage.removeItem(tabKey(boardId)); } catch { /* nothing kept */ }
}

// pickColumn: the column the phone shows: the remembered one while it exists, else the first.
export function pickColumn(columns: Column[], remembered: string | null): string | null {
  if (remembered && columns.some((c) => c.id === remembered)) return remembered;
  return columns[0]?.id ?? null;
}

// swipeStep: a finished touch's horizontal swipe as a column step (+1 next, -1 previous, 0 none).
// It must be clearly sideways (far enough, and mostly horizontal) so vertical scrolling and taps
// never switch columns.
export function swipeStep(dx: number, dy: number): number {
  if (Math.abs(dx) < 60 || Math.abs(dx) < Math.abs(dy) * 1.5) return 0;
  return dx < 0 ? 1 : -1;
}

// activeFilters: the chips that are on, as the toolbar lists them ("type: bug", "priority: high",
// and a tag by its name alone, "0.4.5"). The search text is not one: it is in the search field.
export interface ActiveFilter { facet: Facet; value: string; label: string }
export function activeFilters(f: BoardFilter): ActiveFilter[] {
  return facets.flatMap((facet) => f[facet].map((value) => ({
    facet, value, label: facet === "tag" ? value : `${facet}: ${value}`,
  })));
}

// searchHits: this board's cards that match the search alone (chips aside), for the search
// field's list: a card whose number was typed first, then board order. Empty for an empty search.
export function searchHits(cards: Card[], q: string, limit = 8): Card[] {
  const w = words(q);
  if (w.length === 0) return [];
  const num = /^#?(\d+)$/.exec(q.trim());
  const hits = cards.filter((c) => matchesSearch(c, w));
  if (num) hits.sort((a, b) => Number(b.number === Number(num[1])) - Number(a.number === Number(num[1])));
  return hits.slice(0, limit);
}
