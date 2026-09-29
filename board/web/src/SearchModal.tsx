import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { api, Board, Card } from "./api";

// SearchModal: card search, opened with / or Ctrl/⌘-K. When opened from a
// board, that board is an active filter chip — click it off to search
// everywhere. Full keyboard flow: type, ↑/↓ to select, Enter to open.
export function SearchModal({ boardId, onClose }: { boardId?: string; onClose: () => void }) {
  const [q, setQ] = useState("");
  const [scoped, setScoped] = useState(!!boardId);
  const [chipArmed, setChipArmed] = useState(false);
  const [results, setResults] = useState<Card[]>([]);
  const [sel, setSel] = useState(0);
  const [boards, setBoards] = useState<Record<string, string>>({});
  const [searched, setSearched] = useState(false);
  const nav = useNavigate();
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    inputRef.current?.focus();
    api<Board[]>("/api/boards").then((bs) => {
      const m: Record<string, string> = {};
      for (const b of bs ?? []) m[b.id] = b.name;
      setBoards(m);
    }).catch(() => {});
  }, []);

  useEffect(() => {
    if (!q.trim()) return;
    const t = setTimeout(() => {
      const scope = scoped && boardId ? `&board=${boardId}` : "";
      api<Card[]>(`/api/search?q=${encodeURIComponent(q.trim())}${scope}`)
        .then((cs) => { setResults(cs ?? []); setSearched(true); setSel(0); })
        .catch(() => {});
    }, 250);
    return () => clearTimeout(t);
  }, [q, scoped, boardId]);

  useEffect(() => {
    listRef.current?.querySelector(".search-hit.selected")
      ?.scrollIntoView({ block: "nearest" });
  }, [sel]);

  // boards are first-class palette hits: empty query lists them all (a board
  // switcher, VS-Code style); a query matches them by name above card hits
  const boardHits = Object.entries(boards)
    .filter(([, name]) => !q.trim() || name.toLowerCase().includes(q.trim().toLowerCase()))
    .sort((a, b) => a[1].localeCompare(b[1]));
  const total = boardHits.length + results.length;

  const openAt = (i: number) => {
    onClose();
    if (i < boardHits.length) nav(`/boards/${boardHits[i][0]}`);
    else {
      const c = results[i - boardHits.length];
      nav(`/boards/${c.board_id}/cards/${c.number}`);
    }
  };

  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") { onClose(); return; }
    // Backspace on an ALREADY-empty input: first press arms the scope chip,
    // second removes it. A backspace that deletes text never arms.
    if (e.key === "Backspace" && !e.repeat && (e.target as HTMLInputElement).value === "" && scoped && boardId) {
      e.preventDefault();
      if (chipArmed) { setScoped(false); setChipArmed(false); }
      else setChipArmed(true);
      return;
    }
    if (chipArmed) setChipArmed(false);
    if (e.key === "ArrowDown") { e.preventDefault(); setSel((s) => Math.min(s + 1, total - 1)); }
    if (e.key === "ArrowUp") { e.preventDefault(); setSel((s) => Math.max(s - 1, 0)); }
    if (e.key === "Enter" && total > 0) openAt(Math.min(sel, total - 1));
  };

  return (
    <div className="sheet-backdrop" onClick={onClose}>
      <div className="search-modal" onClick={(e) => e.stopPropagation()} role="dialog" aria-label="Search cards">
        <div className="search-row">
          {boardId && scoped && (
            <button className={`scope-chip${chipArmed ? " armed" : ""}`} onClick={() => setScoped(false)}
              title="searching this board only — click (or backspace twice) to search all boards">
              {boards[boardId] ?? "board"} ✕
            </button>
          )}
          {boardId && !scoped && (
            <button className="scope-chip off" onClick={() => setScoped(true)}
              title="searching all boards — click to scope to this board">
              all boards
            </button>
          )}
          <input
            ref={inputRef} type="text" value={q}
            placeholder={scoped && boardId ? "search this board…" : "jump to a board or search cards…"}
            onChange={(e) => {
              const v = e.target.value;
              setQ(v);
              // an emptied query drops the old hits at once (the debounced
              // search below only ever runs for a non-empty one)
              if (!v.trim()) { setResults([]); setSearched(false); setSel(0); }
            }}
            onKeyDown={onKey}
          />
        </div>
        <div className="search-results" ref={listRef}>
          {boardHits.map(([id, name], i) => (
            <button
              className={`search-hit${i === sel ? " selected" : ""}`} key={id}
              onMouseEnter={() => setSel(i)} onClick={() => openAt(i)}
            >
              <span className="hit-glyph">▦</span>
              <span className="hit-title">{name}</span>
              <span className="hit-kind">board</span>
            </button>
          ))}
          {results.map((c, i) => {
            const j = boardHits.length + i;
            return (
              <button
                className={`search-hit${j === sel ? " selected" : ""}`} key={c.id}
                onMouseEnter={() => setSel(j)} onClick={() => openAt(j)}
              >
                <span className="mono num">#{c.number}</span>
                {(!scoped || !boardId) && <span className="hit-board">{boards[c.board_id] ?? "?"}</span>}
                <span className="hit-title">{c.title}</span>
                {c.archived_at && <span className="chip">archived</span>}
              </button>
            );
          })}
          {searched && total === 0 && (
            <p className="dimtext" style={{ padding: 12 }}>Nothing matches.</p>
          )}
        </div>
      </div>
    </div>
  );
}
