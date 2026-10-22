import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { useNavigate } from "react-router-dom";
import { api, Board, Card } from "./api";
import { searchHits } from "./boardFilter";
import { useAnchored } from "./anchor";

type Item =
  | { kind: "card"; card: Card; here: boolean }
  | { kind: "board"; id: string; name: string };

// BoardSearch: the board's search field. Scoped to this board (the default), typing filters the
// board in place (the caller owns that text: it is the filter's search) and lists what matches
// underneath, as the 🔍 search did: this board's cards at once, then more from the server (body
// text, archived cards). Switched to all boards (the scope chip, or Backspace twice on an empty
// field) it searches every board and leaves this board unfiltered. ↑/↓ move, Enter opens.
// Opening a hit clears the search text: the board is no longer cut down to what was typed.
//
// On a desktop it sits in the toolbar as a 🔍 button that opens into the field (`collapsible`);
// on a phone it is a full-width row under the column tabs, which `onDismiss` closes.
export function BoardSearch({ boardId, value, onChange, cards, onOpenCard, collapsible, focusSignal, onDismiss }: {
  boardId: string;
  value: string;
  onChange: (q: string) => void;
  cards: Card[];
  onOpenCard: (card: Card) => void;
  collapsible: boolean;
  focusSignal: number; // bumps when "/" (or the phone's 🔍) asks for the field
  onDismiss?: () => void; // the field closed with nothing in it
}) {
  const nav = useNavigate();
  const listId = useId();
  const inputRef = useRef<HTMLInputElement>(null);
  const [focused, setFocused] = useState(false);
  const [listOpen, setListOpen] = useState(false);
  const [opened, setOpened] = useState(false); // the collapsed button was pressed
  const [scoped, setScoped] = useState(true);
  const [allQ, setAllQ] = useState(""); // the text while searching every board: not a filter here
  const [chipArmed, setChipArmed] = useState(false);
  // the server's hits, kept with the search they answer: hits for any other search never show
  const [remote, setRemote] = useState<{ key: string; cards: Card[] }>({ key: "", cards: [] });
  const [boards, setBoards] = useState<Record<string, string>>({});
  const [sel, setSel] = useState<number | null>(null); // null: nothing chosen with the keys or pointer
  const text = scoped ? value : allQ;
  const setText = (t: string) => { if (scoped) onChange(t); else setAllQ(t); };
  const expanded = !collapsible || opened || focused || text !== "";

  // "/" (or the phone's 🔍) opens the field and puts the cursor in it
  const [seenSignal, setSeenSignal] = useState(focusSignal);
  if (seenSignal !== focusSignal) { setSeenSignal(focusSignal); setOpened(true); }
  useEffect(() => {
    if (focusSignal > 0) inputRef.current?.focus();
  }, [focusSignal, expanded]);

  useEffect(() => {
    if (!listOpen) return;
    let stop = false;
    api<Board[]>("/api/boards").then((bs) => {
      if (stop) return;
      const m: Record<string, string> = {};
      for (const b of bs ?? []) m[b.id] = b.name;
      setBoards(m);
    }).catch(() => {});
    return () => { stop = true; };
  }, [listOpen]);

  const q = text.trim();
  const key = `${scoped ? boardId : "*"}\n${q}`;
  // The server's search: debounced, and a newer search or scope cancels the request in flight.
  useEffect(() => {
    if (!q || !listOpen) return;
    const ctl = new AbortController();
    const t = setTimeout(() => {
      const scope = scoped ? `&board=${boardId}` : "";
      api<Card[]>(`/api/search?q=${encodeURIComponent(q)}${scope}`, { signal: ctl.signal })
        .then((cs) => { if (!ctl.signal.aborted) setRemote({ key, cards: cs ?? [] }); })
        .catch(() => {});
    }, 250);
    return () => { clearTimeout(t); ctl.abort(); };
  }, [q, scoped, boardId, listOpen, key]);

  const here = scoped ? searchHits(cards, text) : [];
  const shownIds = new Set(here.map((c) => c.id));
  const items: Item[] = [
    ...here.map((card) => ({ kind: "card" as const, card, here: true })),
    ...(q && remote.key === key ? remote.cards : []).filter((c) => !shownIds.has(c.id)).slice(0, 12)
      .map((card) => ({ kind: "card" as const, card, here: false })),
    // boards by name, a board switcher: every other board while the field is empty
    ...Object.entries(boards)
      .filter(([id, name]) => id !== boardId && (!q || name.toLowerCase().includes(q.toLowerCase())))
      .sort((a, b) => a[1].localeCompare(b[1]))
      .slice(0, 8)
      .map(([id, name]) => ({ kind: "board" as const, id, name })),
  ];
  // Until a key or the pointer picks one, the first card is the one Enter opens: a board (the
  // switcher) is opened only when it is the one chosen.
  const at = sel != null ? Math.min(sel, items.length - 1) : items.findIndex((i) => i.kind === "card");
  const showList = listOpen && (items.length > 0 || q !== "");
  const results = items.filter((i) => i.kind === "card").length;

  const close = () => { setListOpen(false); setSel(null); };
  const dismiss = () => { setOpened(false); onDismiss?.(); };
  const openItem = (item: Item) => {
    close();
    setText(""); setAllQ("");
    inputRef.current?.blur();
    dismiss();
    if (item.kind === "board") nav(`/boards/${item.id}`);
    else if (item.card.board_id === boardId && item.here) onOpenCard(item.card);
    else nav(`/boards/${item.card.board_id}/cards/${item.card.number}`);
  };
  const setScope = (on: boolean) => {
    if (on === scoped) return;
    // the text moves with the scope: on this board it filters, across boards it does not
    if (on) { onChange(allQ); setAllQ(""); } else { setAllQ(value); onChange(""); }
    setScoped(on); setSel(null);
  };

  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Escape") {
      e.stopPropagation();
      if (showList) close();
      else if (text) setText("");
      else { inputRef.current?.blur(); dismiss(); }
      return;
    }
    // Backspace on an already-empty field: the first press arms the scope chip, the second
    // widens the search to all boards (as the 🔍 search did).
    if (e.key === "Backspace" && !e.repeat && text === "" && scoped) {
      e.preventDefault();
      if (chipArmed) { setScope(false); setChipArmed(false); } else setChipArmed(true);
      return;
    }
    if (chipArmed) setChipArmed(false);
    if (e.key === "ArrowDown") {
      e.preventDefault(); setListOpen(true);
      setSel(listOpen ? Math.min(at + 1, items.length - 1) : Math.max(at, 0));
    }
    if (e.key === "ArrowUp") { e.preventDefault(); setSel(Math.max(at - 1, 0)); }
    if (e.key === "Enter" && listOpen && at >= 0 && items[at]) { e.preventDefault(); openItem(items[at]); }
  };

  const listRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    listRef.current?.querySelector(".search-hit.selected")?.scrollIntoView?.({ block: "nearest" });
  }, [at]);

  // the list uses the space: a third of a wide screen, the whole width of a phone
  const wrapRef = useRef<HTMLDivElement>(null);
  const popStyle = useAnchored(wrapRef, showList, (vw) => (vw <= 640 ? vw - 16 : Math.min(Math.max(480, vw * 0.34), 1400)));

  if (!expanded) {
    return (
      <button type="button" className="btn ghost small tb-search-btn" aria-label="Search cards"
        title="Search: filters this board, opens any card (/)"
        onClick={() => { setOpened(true); requestAnimationFrame(() => inputRef.current?.focus()); }}>
        🔍
      </button>
    );
  }

  return (
    <div ref={wrapRef} className={`tb-search${collapsible ? "" : " full"}`}
      onFocus={() => { setFocused(true); setListOpen(true); }}
      onBlur={(e) => {
        if (e.currentTarget.contains(e.relatedTarget as Node | null)) return;
        setFocused(false); close();
        if (!text) dismiss();
      }}>
      <span className="glyph" aria-hidden="true">🔍</span>
      <input
        ref={inputRef} type="search" role="combobox" value={text}
        aria-label="Search cards" aria-autocomplete="list"
        aria-expanded={showList} aria-controls={showList ? listId : undefined}
        aria-activedescendant={showList && at >= 0 ? `${listId}-${at}` : undefined}
        placeholder={scoped ? "Search: title, #number, #tag" : "Search all boards"}
        onChange={(e) => { setText(e.target.value); setListOpen(true); setSel(null); }}
        onKeyDown={onKey}
      />
      {!collapsible && (
        <button type="button" className="tb-search-close" aria-label="Close search"
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => { setText(""); close(); inputRef.current?.blur(); dismiss(); }}>×</button>
      )}
      <span className="sr-only" role="status" aria-live="polite">
        {showList && q ? (results === 0 ? "Nothing matches" : `${results} card${results === 1 ? "" : "s"} found`) : ""}
      </span>
      {showList && (
        <div className="search-pop" ref={listRef} style={popStyle}>
          <div className="search-pop-head">
            <button type="button" className={`scope-chip${scoped ? "" : " off"}${chipArmed ? " armed" : ""}`}
              onMouseDown={(e) => e.preventDefault()}
              onClick={() => setScope(!scoped)}
              title={scoped
                ? "searching this board, and filtering it — click (or Backspace twice) to search all boards"
                : "searching all boards; this board is not filtered — click for this board only"}>
              {scoped ? `${boards[boardId] ?? "This board"} ✕` : "All boards"}
            </button>
            <span className="hint">↑↓ choose · Enter opens · Esc closes</span>
          </div>
          <div role="listbox" id={listId} aria-label="Matching cards and boards">
            {items.map((item, i) => (
              <div
                role="option" id={`${listId}-${i}`} aria-selected={i === at}
                key={item.kind === "board" ? `b:${item.id}` : `c:${item.card.id}`}
                className={`search-hit${i === at ? " selected" : ""}`}
                onMouseDown={(e) => e.preventDefault()}
                onMouseEnter={() => setSel(i)} onClick={() => openItem(item)}
              >
                {item.kind === "board" ? (
                  <>
                    <span className="hit-glyph">▦</span>
                    <span className="hit-title">{item.name}</span>
                    <span className="hit-kind">board</span>
                  </>
                ) : (
                  <>
                    <span className="mono num">#{item.card.number}</span>
                    {item.card.board_id !== boardId && <span className="hit-board">{boards[item.card.board_id] ?? "?"}</span>}
                    <span className="hit-title">{item.card.title}</span>
                    {item.card.archived_at && <span className="chip">archived</span>}
                  </>
                )}
              </div>
            ))}
            {q !== "" && items.length === 0 && <p className="dimtext" style={{ padding: 12, margin: 0 }}>Nothing matches.</p>}
          </div>
        </div>
      )}
    </div>
  );
}
