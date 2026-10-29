import { RestrictedMarker } from "./CardOrigin";
import { useCallback, useDeferredValue, useEffect, useLayoutEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import { useLocation, useNavigate, useParams } from "react-router-dom";
import { api, ApiError, Board, Card, Column, boardSocket, pendingBlockers } from "./api";
import { CardSheet } from "./CardSheet";
import { BoardChat } from "./BoardChat";
import { MetricsModal } from "./MetricsPage";
import { BoardSettings } from "./BoardSettings";
import { BoardFilterBar } from "./BoardFilterBar";
import { BoardSearch } from "./BoardSearch";
import { ActiveFilters, FilterMenu } from "./FilterMenu";
import {
  BoardFilter, chipsFor, countLabel, forgetTab, isFiltering, loadFilter, loadTab, matchesFilter, pickColumn,
  saveFilter, saveTab, swipeStep, words,
} from "./boardFilter";

// indicatorFor: what a card's top-right glyph means.
//   spinner  — a session is actively churning (starting/running)
//   ?        — the session asked something and is waiting on you
//   ✓        — the card is at the review gate: your accept/reject verdict
interface ActiveInfo { lifecycle: string; since: string }

function ageShort(iso: string): string {
  const secs = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (secs < 90) return `${Math.round(secs)}s`;
  if (secs < 5400) return `${Math.round(secs / 60)}m`;
  if (secs < 129600) return `${(secs / 3600).toFixed(1)}h`;
  return `${Math.round(secs / 86400)}d`;
}

function indicatorFor(card: Card, active: Record<string, ActiveInfo>, reviewCols: Set<string>) {
  const info = active[card.id];
  const lifecycle = info?.lifecycle;
  const age = info ? ageShort(info.since) : "";
  const inReview = card.column_id != null && reviewCols.has(card.column_id);
  if (lifecycle === "running" || lifecycle === "starting") {
    return inReview ? (
      <span className="ind-live" title={`adversarial review running · active ${age} ago`}>
        <span className="shield">🛡</span><span className="churn review" />
      </span>
    ) : (
      <span className="ind-live" title={`session working · active ${age} ago`}>
        <span className="churn" />
      </span>
    );
  }
  if (lifecycle === "waiting") {
    return (
      <span className="ind-live" title={`waiting on you for ${age}`}>
        <span className="churn-q">?</span><span className="ind-age">{age}</span>
      </span>
    );
  }
  if (lifecycle === "idle" || lifecycle === "disconnected") {
    // session attached but between turns — show a quiet pulse so "moving
    // along" is distinguishable from "abandoned"
    return (
      <span className="ind-live" title={`session attached, between turns · last active ${age} ago`}>
        <span className="idle-dot" /><span className="ind-age idle">{age}</span>
      </span>
    );
  }
  if (inReview) {
    return <span className="verdict-badge" title="ready for review — accept or reject" aria-label="awaiting verdict">✓</span>;
  }
  return null;
}

interface Deployment { env: string; sha: string; status: string; detail: string; created_at: string }
function byRank(a: Card, b: Card): number {
  return a.rank < b.rank ? -1 : a.rank > b.rank ? 1 : 0;
}
// staleFirst: a stale-flagged card (claimed but nothing moving) sorts ahead
// of everything else, falling back to rank order. Only ever compared within
// ONE column's cards (see columnCards) — a global sort with this rule would
// be non-transitive (Array.sort requires a total order across the whole
// slice, and "same column" isn't transitive across the whole board) and can
// silently scramble unrelated columns.
function staleFirst(a: Card, b: Card): number {
  const stale = Number(!!b.stale_at) - Number(!!a.stale_at);
  return stale !== 0 ? stale : byRank(a, b);
}
// columnCards: a column's cards, stale-first. Shared by render and
// commitDrop so drag-and-drop's index math (built from this exact ordering)
// can never desync from what's on screen.
function columnCards(cards: Card[], colId: string): Card[] {
  return cards.filter((c) => c.column_id === colId).sort(staleFirst);
}

// blocked: ready-column cards the dispatcher is skipping because a hard
// dependency has not landed. Without it a board waiting on its own dependency
// graph reads as "N ready, 0 in flight" forever, with nothing saying why.
interface RunStatus {
  state: string; ready?: number; in_flight?: number;
  cards_done?: number; cards_stuck?: number; blocked?: number;
  // the board's dispatch limit, echoed here so the chip can tell a PARKED run
  // (running, concurrency 0) from a stopped one without a second request
  concurrency?: number;
  // the runner has no session slots left: ready cards are left untouched
  // until one frees, so a run with work queued can sit still and be healthy
  runner_full?: boolean;
}

// retryableLoadError says whether a failed board reload may succeed if tried again: anything but
// a client error, except a timeout (408) or a rate limit (429).
function retryableLoadError(e: unknown): boolean {
  if (!(e instanceof ApiError)) return true;
  return e.status >= 500 || e.status === 408 || e.status === 429 || e.status < 400;
}

export function BoardView({ boardId }: { boardId: string }) {
  const [board, setBoard] = useState<Board | null>(null);
  const [deploys, setDeploys] = useState<Deployment[]>([]);
  const [chatOpen, setChatOpen] = useState(false);
  // "c" opens the board session and puts the cursor in its composer
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = document.activeElement;
      const typing = el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA");
      if (!typing && !e.ctrlKey && !e.metaKey && !e.altKey && e.key === "c") {
        e.preventDefault();
        setChatOpen(true);
        setTimeout(() => window.dispatchEvent(new Event("blerg-board:chat-focus")), 60);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const [run, setRun] = useState<RunStatus | null>(null);
  const [startError, setStartError] = useState("");

  const loadRun = useCallback(() => {
    api<RunStatus>(`/api/boards/${boardId}/run`).then(setRun).catch(() => {});
  }, [boardId]);
  useEffect(() => {
    loadRun();
    const iv = setInterval(loadRun, 10000);
    return () => clearInterval(iv);
  }, [loadRun]);

  // returning to a board with a live session reopens its chat
  useEffect(() => {
    let stop = false;
    api<{ id: string }[]>(`/api/boards/${boardId}/sessions`)
      .then((s) => { if (!stop && (s?.length ?? 0) > 0) setChatOpen(true); })
      .catch(() => {});
    return () => { stop = true; };
  }, [boardId]);
  const [columns, setColumns] = useState<Column[]>([]);
  const [cards, setCards] = useState<Card[]>([]);
  const [openCard, setOpenCard] = useState<Card | null>(null);
  const nav = useNavigate();
  const metricsOpen = useLocation().pathname.endsWith("/metrics");
  const { number: urlNumber } = useParams();
  const [active, setActive] = useState<Record<string, ActiveInfo>>({});
  const [dragging, setDragging] = useState<Card | null>(null);
  const [dragOver, setDragOver] = useState<{ colId: string; index: number } | null>(null);
  const [dragSrc, setDragSrc] = useState<{ colId: string; index: number } | null>(null);
  const [dragHeight, setDragHeight] = useState(72);

  // The in-place filter, kept per board for the browser session, and the column a phone shows,
  // remembered per board. Both follow a switch to another board (state adjusted during render).
  const [filterBoard, setFilterBoard] = useState(boardId);
  const [filter, setFilterState] = useState<BoardFilter>(() => loadFilter(boardId));
  const [tabPick, setTabPick] = useState<string | null>(() => loadTab(boardId));
  if (filterBoard !== boardId) {
    setFilterBoard(boardId); setFilterState(loadFilter(boardId)); setTabPick(loadTab(boardId));
  }
  const setFilter = (f: BoardFilter) => { setFilterState(f); saveFilter(boardId, f); };
  // The box shows every keystroke at once; the cards follow at a lower priority, so typing stays
  // smooth on a big board.
  const applied = useDeferredValue(filter);
  const filtering = isFiltering(applied);
  // Each column's shown cards (stale-first, narrowed by the filter) and size, and the chips:
  // worked out once per board change or filter change, not once per render.
  const view = useMemo(() => {
    const q = words(applied.q);
    const on = isFiltering(applied);
    const byColumn = new Map<string, { shown: Card[]; total: number }>();
    for (const col of columns) {
      const all = columnCards(cards, col.id);
      byColumn.set(col.id, { shown: on ? all.filter((c) => matchesFilter(c, applied, q)) : all, total: all.length });
    }
    return { byColumn, chips: chipsFor(cards, applied) };
  }, [cards, columns, applied]);
  // shownCards: a column's cards as the board shows them. Render and commitDrop both use it, so a
  // drop's index means the same card on screen and here.
  const shownCards = (colId: string) => view.byColumn.get(colId)?.shown ?? [];
  const activeColumn = pickColumn(columns, tabPick);
  // a remembered column that was deleted is forgotten, not kept forever
  // (pickColumn already shows the first column in its place)
  useEffect(() => {
    if (tabPick && columns.length > 0 && !columns.some((c) => c.id === tabPick)) forgetTab(boardId);
  }, [boardId, columns, tabPick]);
  const isPhone = useSyncExternalStore(subscribePhone, phoneNow, () => false);
  // The search field: "/" (or Ctrl/⌘-K) on a board opens it here instead of the search dialog the
  // other pages use; on a phone it is a full-width row under the column tabs.
  const [searchSignal, setSearchSignal] = useState(0);
  const [phoneSearch, setPhoneSearch] = useState(false);
  useEffect(() => {
    // App asks only while no dialog or popover is open (see App's shortcut guard)
    const onAsk = (e: Event) => {
      e.preventDefault();
      if (phoneNow()) setPhoneSearch(true);
      setSearchSignal((n) => n + 1);
    };
    window.addEventListener("blerg-board:search-inline", onAsk);
    return () => window.removeEventListener("blerg-board:search-inline", onAsk);
  }, []);
  const sentinelRef = useRef<HTMLDivElement>(null);
  const pickColumnTab = (id: string) => {
    setTabPick(id); saveTab(boardId, id);
    // a new column starts at its top, just under the sticky strip, not mid-way down
    const top = sentinelRef.current?.getBoundingClientRect().top;
    if (top != null && top < 0) window.scrollTo({ top: window.scrollY + top });
  };
  const swipeRef = useRef<{ x: number; y: number } | null>(null);
  const onSwipeEnd = (x: number, y: number) => {
    const start = swipeRef.current; swipeRef.current = null;
    if (!start || dragging || !phoneNow()) return;
    // pinch-zoomed in, a sideways drag pans the zoomed page: it is not a swipe
    if ((window.visualViewport?.scale ?? 1) > 1.01) return;
    const step = swipeStep(x - start.x, y - start.y);
    const i = columns.findIndex((c) => c.id === activeColumn);
    const next = columns[i + step];
    if (step !== 0 && next) pickColumnTab(next.id);
  };

  // FLIP: whenever the layout changes (drag projection, drops, reloads),
  // cards animate from their previous position instead of teleporting.
  const rectsRef = useRef(new Map<string, { x: number; y: number }>());
  const flipFilterRef = useRef(applied);
  useLayoutEffect(() => {
    const prev = rectsRef.current;
    // A new filter re-lays the board at once: sliding every card on each keystroke is noise.
    const still = flipFilterRef.current !== applied;
    flipFilterRef.current = applied;
    const next = new Map<string, { x: number; y: number }>();
    const boardEl = document.querySelector<HTMLElement>(".board");
    document.querySelectorAll<HTMLElement>("[data-card-id]").forEach((el) => {
      const id = el.dataset.cardId!;
      // cancel in-flight FLIP transforms BEFORE measuring — measuring a
      // mid-animation rect re-triggers the animation forever (the flicker)
      el.getAnimations().forEach((a) => a.cancel());
      const r = el.getBoundingClientRect();
      // A card that is not laid out (a phone's hidden columns) has no position: neither record nor
      // animate it, so a column a tab brings into view appears in place instead of flying in.
      if (r.width === 0 && r.height === 0) return;
      // CONTENT-space coordinates: add back every scroll offset. Client
      // rects are viewport-relative, and scrolling doesn't re-render — so
      // a refresh after a scroll would see every card "moved" by the
      // scroll delta and animate the whole column (fake-scroll artifact).
      const cardsEl = el.closest<HTMLElement>(".cards");
      const pos = {
        x: r.left + (boardEl?.scrollLeft ?? 0),
        y: r.top + (cardsEl?.scrollTop ?? 0) + window.scrollY,
      };
      next.set(id, pos);
      const old = prev.get(id);
      if (!still && old && (Math.abs(old.y - pos.y) > 1 || Math.abs(old.x - pos.x) > 1)) {
        el.animate(
          [{ transform: `translate(${old.x - pos.x}px, ${old.y - pos.y}px)` }, { transform: "translate(0, 0)" }],
          { duration: 280, easing: "cubic-bezier(0.25, 0.1, 0.25, 1)" },
        );
      }
    });
    rectsRef.current = next;
  });

  const endDrag = () => { setDragging(null); setDragOver(null); setDragSrc(null); };

  // commit the projected drop: card → dragOver column at dragOver index
  const commitDrop = async () => {
    if (!dragging || !dragOver) { endDrag(); return; }
    if (dragSrc && dragOver.colId === dragSrc.colId &&
        dragOver.index === dragSrc.index) { endDrag(); return; }
    // The drop index counts the cards on screen. With a filter on, cards are hidden in between:
    // dropping after the last shown card lands right after it, not at the column's very end.
    const all = columnCards(cards, dragOver.colId).filter((c) => c.id !== dragging.id);
    const target = shownCards(dragOver.colId).filter((c) => c.id !== dragging.id);
    let before = target[dragOver.index]?.id ?? null;
    if (!before && target.length > 0) before = all[all.indexOf(target[target.length - 1]) + 1]?.id ?? null;
    const card = dragging;
    // optimistic: place it locally so the slide happens now
    setCards((cs) => {
      const rest = cs.filter((c) => c.id !== card.id);
      const moved = { ...card, column_id: dragOver.colId };
      if (!before) return [...rest, moved];
      const i = rest.findIndex((c) => c.id === before);
      return [...rest.slice(0, i), moved, ...rest.slice(i)];
    });
    endDrag();
    try {
      await api(`/api/cards/${card.id}/move`, {
        json: { column_id: dragOver.colId, before_card_id: before },
      });
    } finally { load(); }
  };

  // Change pings arrive in bursts (a card created and moved, a session reporting), and each one
  // starts a reload. Only the newest reload may land: an older one that answers late would put back
  // the board as it was before the change, and a card a script created would vanish again.
  const [loadTicket] = useState(latestOnly);
  // A failed reload is retried after 3 s, then 6, 12, ... up to a minute between tries, while it
  // can still succeed: a network error, a timeout or a server error. A refusal that will not
  // change stops retrying: 401 (signed out: api() and the sign-in banner handle that, and its
  // recovery remounts the board), 403 (access lost), 404 (the board is gone).
  const [reloadRetry] = useState(() => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    let delay = 3000;
    return {
      clear: () => clearTimeout(timer),
      reset: () => { delay = 3000; },
      schedule: (callback: () => void) => {
        timer = setTimeout(callback, delay);
        delay = Math.min(delay * 2, 60000);
      },
    };
  });
  const load = useCallback(async () => {
    reloadRetry.clear();
    reloadRetry.reset();
    const isLatest = loadTicket();
    const attempt = async () => {
      try {
        const [b, cols, cs, act, deps] = await Promise.all([
          api<Board>(`/api/boards/${boardId}`),
          api<Column[]>(`/api/boards/${boardId}/columns`),
          api<Card[]>(`/api/boards/${boardId}/cards`),
          api<Record<string, ActiveInfo>>(`/api/boards/${boardId}/active-sessions`).catch(() => ({})),
          api<Deployment[]>(`/api/boards/${boardId}/deployments`).catch(() => []),
        ]);
        if (!isLatest()) return;
        setBoard(b); setColumns(cols ?? []);
        setCards((cs ?? []).slice().sort(byRank));
        setActive(act ?? {}); setDeploys(deps ?? []);
      } catch (e) {
        if (isLatest() && retryableLoadError(e)) reloadRetry.schedule(() => { void attempt(); });
      }
    };
    await attempt();
  }, [boardId, loadTicket, reloadRetry]);

  // A board_changed ping refreshes the run status too, not just the cards:
  // concurrency is a board setting the chip renders, so without this a PATCH
  // that parks a board leaves the chip claiming the board is running until the
  // 10s poll catches up.
  useEffect(() => {
    load().catch(() => {});
    const disconnect = boardSocket(boardId, () => { load().catch(() => {}); loadRun(); });
    return () => {
      disconnect();
      loadTicket(); // Retire pending replies and retries for the previous board.
      reloadRetry.clear();
    };
  }, [boardId, load, loadRun, loadTicket, reloadRetry]);

  // Deep link: /boards/:id/cards/:number opens that card's sheet.
  // (state adjusted during render, not in an effect: the sheet follows the URL)
  if (urlNumber && cards.length > 0 && !(openCard && openCard.number === Number(urlNumber))) {
    const target = cards.find((c) => c.number === Number(urlNumber));
    if (target) setOpenCard(target);
  }

  const openSheet = (card: Card) => {
    setOpenCard(card);
    nav(`/boards/${boardId}/cards/${card.number}`, { replace: false });
  };
  const closeSheet = () => {
    setOpenCard(null);
    nav(`/boards/${boardId}`, { replace: false });
    load();
  };


  // run-the-board controls. A failed start is shown verbatim: blerg-runner's
  // message is the actionable half ("no connected daemon has repo …"), and
  // swallowing it left ▶ Run board silently doing nothing on desktop.
  const startRun = async () => {
    setStartError("");
    try {
      await api(`/api/boards/${boardId}/run`, { method: "POST" });
    } catch (e) {
      setStartError(e instanceof ApiError ? e.message : "start failed");
      loadRun();
      return;
    }
    load(); loadRun();
  };
  const stopRun = async () => {
    setStartError("");
    try {
      await api(`/api/boards/${boardId}/run/stop`, { method: "POST" });
    } catch (e) {
      setStartError(e instanceof ApiError ? e.message : "stop failed");
    }
    loadRun();
  };

  if (!board) return null;
  const isMirroredBoard = !!board.driven_by;
  const reviewCols = new Set(columns.filter((c) => c.name.toLowerCase().includes("review")).map((c) => c.id));

  return (
    <>
      <div className="row board-toolbar" style={{ padding: "12px 16px 0" }}>
        <h1 className="page" style={{ margin: 0 }}>{board.name}</h1>
        {board.gate_enabled && (
          <span
            className="chip"
            style={{ color: "var(--blaze)", borderColor: "var(--blaze-soft)", cursor: "help" }}
            title={`Admission gate: an LLM curator reviews every agent write on this board before it lands. Creates are checked for semantic duplicates and vague scope (deny/revise with a reason the agent can act on); moves, updates and archives are allowed unless clearly destructive. Human edits skip the gate. If the curator is unreachable: ${board.gate_on_unavailable === "hold" ? "writes are held for your review" : "writes land flagged ungated"}. Disputed denials: ${board.gate_on_dispute === "tiebreak" ? "adjudicated by a stronger model" : board.gate_on_dispute === "hold" ? "held for you" : "accepted but flagged"}. Every verdict is in the Gate log.`}
          >
            gated
          </span>
        )}
        <BoardSettings board={board} onChanged={() => { load().catch(() => {}); }} />
        {deploys.map((d) => (
          <a className={`env-chip ${d.status}`} key={d.env}
            href={board.deploy_url ?? undefined} target="_blank" rel="noreferrer"
            title={`${d.env}: ${d.status}${d.detail ? " — " + d.detail : ""} · ${d.created_at}`}>
            {d.status === "ok" ? "●" : d.status === "deploying" ? "◌" : "✗"} {d.env}
            {d.sha && <span className="mono sha7"> {d.sha.slice(0, 7)}</span>}
          </a>
        ))}
        {!isMirroredBoard && (() => {
          const running = run?.state === "running";
          const ready = run?.ready ?? 0, blocked = run?.blocked ?? 0;
          const inFlight = run?.in_flight ?? 0;
          const limit = run?.concurrency ?? 1;
          // Parked (concurrency 0) is a run that is ON and deliberately
          // dispatching nothing. It has to be readable as that and not as a
          // hung board, so it takes priority over "idle" — a parked board with
          // an empty ready column is parked, not merely waiting for work. It
          // also outranks "gridlocked": at concurrency 0 the dial is why
          // nothing is moving, whatever the dependency graph says.
          const parked = running && limit === 0;
          const idle = running && !parked && ready === 0 && inFlight === 0;
          // gridlocked: ready has cards, none can start, nothing is in flight.
          // The board is doing nothing and that is *explicable* — say so, or it
          // reads exactly like a board that is quietly ignoring its queue.
          const gridlocked = running && !parked && !idle && inFlight === 0 && blocked >= ready && blocked > 0;
          // The runner is out of session slots: ready cards stay in ready,
          // untouched, until one frees. Say so — a run with work queued and
          // nothing moving otherwise looks broken. Ranks below parked (blerg-board's
          // own standing decision) and below gridlocked, which is the stronger
          // answer: a board whose whole queue is blocked would not dispatch
          // even with every slot in the world free.
          const waiting = running && !parked && !idle && !gridlocked && (run?.runner_full ?? false);
          const stateClass = parked ? " parked" : idle ? " idle" : gridlocked ? " gridlocked" : "";
          return (
            <label className={`run-toggle${stateClass}`}
              title={running
                ? parked
                  ? `auto-advance is on but this board is PARKED: its concurrency is 0, so nothing new is dispatched.${inFlight > 0 ? ` ${inFlight} card(s) already in flight finish normally and still auto-merge.` : ""} The run stays active and keeps its counters. Set the board's concurrency above 0 (PATCH /api/boards/{id}, or ask a board session) to resume.`
                  : idle
                  ? "auto-advance is on, idle — nothing in ready right now; a card dropped into ready is picked up automatically. Uncheck to stop."
                  : gridlocked
                  ? "auto-advance is on but every ready card is waiting on a hard dependency that has not reached a done column yet. Nothing is wrong — open a card to see which blocker it is waiting on. The board starts moving the moment a blocker lands."
                  : waiting
                  ? `auto-advance is on (up to ${limit} card(s) at a time), but the runner has no free session slots — ready cards are left where they are and the first tick after a slot frees picks one up.`
                  : `auto-advance is on — draining ready top-down, up to ${limit} card(s) at a time, through adversarial review and (if auto-merge is on) merge. Uncheck to stop feeding new cards; in-flight sessions finish and still auto-merge.`
                : "check to auto-advance the ready column — cards move to in progress as they become claimable, with no manual click per card"}>
              <input type="checkbox" checked={running}
                onChange={(e) => (e.target.checked ? startRun() : stopRun())} />
              {running ? (
                <span className="run-chip">
                  {parked ? (
                    <>⏸ parked · dispatching nothing{inFlight > 0 ? ` · ${inFlight} finishing` : ""}</>
                  ) : idle ? "⏸ idle · waiting for ready work"
                    : gridlocked ? `⛔ ${blocked} ready · all blocked on dependencies` : (
                    <>▶ running · {ready} ready · {inFlight}
                    {limit > 1 ? `/${limit}` : ""} in flight
                    {blocked > 0 ? ` · ${blocked} blocked` : ""}
                    {(run?.cards_stuck ?? 0) > 0 ? ` · ${run?.cards_stuck} stuck` : ""}
                    {waiting ? " · runner full" : ""}</>

                  )}
                </span>
              ) : (
                <span className="run-label">▶ Run board</span>
              )}
            </label>
          );
        })()}
        <button className="btn ghost small" onClick={() => { setChatOpen(true); setTimeout(() => window.dispatchEvent(new Event("blerg-board:chat-focus")), 60); }} title="board session (c)">
          💬 Board session
        </button>
        <button className="btn ghost small" onClick={() => nav(`/boards/${boardId}/metrics`)}>📊 Metrics</button>
        {/* search, Filter and the active filters stay together: they never split across rows */}
        <div className="tb-find">
        {isPhone ? (
          <button type="button" className="btn ghost small tb-search-btn" aria-label="Search cards"
            aria-expanded={phoneSearch || filter.q !== ""}
            onClick={() => { setPhoneSearch(true); setSearchSignal((n) => n + 1); }}>🔍</button>
        ) : (
          <BoardSearch boardId={boardId} value={filter.q} onChange={(q) => setFilter({ ...filter, q })}
            cards={cards} onOpenCard={openSheet} collapsible focusSignal={searchSignal} />
        )}
        <FilterMenu chips={view.chips} filter={filter} onFilter={setFilter} sheet={isPhone} />
        {!isPhone && <ActiveFilters filter={filter} onFilter={setFilter} />}
        </div>
        {board.deploy_url && (
          <a className="live-chip" href={board.deploy_url} target="_blank" rel="noreferrer"
            title="this project is deployed — open it">
            ● LIVE ↗
          </a>
        )}
        {isMirroredBoard && <span className="chip">{board.driven_by}-driven — read-only cards</span>}
      </div>
      {startError && (
        <p role="alert" style={{ color: "var(--danger)", fontSize: 13, padding: "6px 16px 0", margin: 0 }}>
          {startError}
        </p>
      )}
      {cards.length === 0 && (board.repos?.length ?? 0) === 0 && (
        <div className="bootstrap-hint">
          <p className="sign">New project?</p>
          <p>
            Open <b>💬 Board session</b> — the agent interviews you, creates the
            GitHub repo, scaffolds it, wires this board (repos, git base, model),
            and files your first cards. Then drag them to ready and press
            ▶ Run board.
          </p>
        </div>
      )}
      <div ref={sentinelRef} aria-hidden="true" />
      <BoardFilterBar
        counts={columns.map((column) => ({
          column, shown: shownCards(column.id).length, total: view.byColumn.get(column.id)?.total ?? 0,
        }))}
        activeColumn={activeColumn} onPickColumn={pickColumnTab} applied={applied}
      >
        {isPhone && (phoneSearch || filter.q !== "") && (
          <div className="phone-search">
            <BoardSearch boardId={boardId} value={filter.q} onChange={(q) => setFilter({ ...filter, q })}
              cards={cards} onOpenCard={openSheet} collapsible={false} focusSignal={searchSignal}
              onDismiss={() => setPhoneSearch(false)} />
          </div>
        )}
        {isPhone && <ActiveFilters filter={filter} onFilter={setFilter} />}
      </BoardFilterBar>
      <div className={`board${dragging ? " drag-active" : ""}`}
        onTouchStart={(e) => { const t = e.touches[0]; swipeRef.current = e.touches.length === 1 && t ? { x: t.clientX, y: t.clientY } : null; }}
        onTouchEnd={(e) => { const t = e.changedTouches[0]; if (t) onSwipeEnd(t.clientX, t.clientY); }}
        onTouchCancel={() => { swipeRef.current = null; }}
      >
        {columns.map((col) => {
          const colCards = shownCards(col.id);
          const total = view.byColumn.get(col.id)?.total ?? 0;
          const visible = colCards.filter((c) => c.id !== dragging?.id);
          const indicatorIdx = !dragging ? null
            : dragOver?.colId === col.id ? dragOver.index
            : dragSrc?.colId === col.id ? dragSrc.index
            : null;
          const srcHome = dragging != null && dragSrc?.colId === col.id &&
            indicatorIdx === dragSrc.index;
          return (
            <section
              className={`column${col.is_terminal ? " terminal" : ""}${col.id === activeColumn ? " picked" : ""}`}
              key={col.id} id={`column-${col.id}`}
              // on a phone the picked column is the tab panel its tab names; on a desktop each
              // column is a region named by its heading
              {...(isPhone ? { role: "tabpanel", tabIndex: 0, "aria-labelledby": `tab-${col.id}` } : { "aria-labelledby": `colname-${col.id}` })}
              onDragOver={(e) => {
                e.preventDefault();
                if (!dragging) return;
                const colEl = e.currentTarget as HTMLElement;
                // hysteresis: entering a NEW column needs the cursor well
                // inside it — grazing an edge while pulling out of the old
                // column must not flip the slot over
                if (dragOver && dragOver.colId !== col.id) {
                  const cr = colEl.getBoundingClientRect();
                  if (e.clientX < cr.left + 28 || e.clientX > cr.right - 28) return;
                }
                const listEl = colEl.querySelector<HTMLElement>(".cards");
                const els = [...colEl.querySelectorAll<HTMLElement>("[data-card-id]")];
                // layout-space hit test: offsetTop ignores in-flight FLIP
                // transforms, so animations can't perturb the index
                const lr = (listEl ?? colEl).getBoundingClientRect();
                const yy = e.clientY - lr.top + ((listEl ?? colEl).scrollTop ?? 0);
                let idx = els.length;
                for (let i = 0; i < els.length; i++) {
                  if (yy < els[i].offsetTop + els[i].offsetHeight / 2) { idx = i; break; }
                }
                setDragOver((cur) =>
                  cur?.colId === col.id && cur.index === idx ? cur : { colId: col.id, index: idx });
              }}
              onDrop={(e) => { e.preventDefault(); commitDrop(); }}
            >
              <header>
                <span className="blaze" />
                <h3 id={`colname-${col.id}`}>{col.name}</h3>
                <span className="count">{countLabel(visible.length, total, filtering)}</span>
              </header>
              <div className="cards">
                {(() => { let vi = 0; return colCards.flatMap((card) => {
                  const isSrc = card.id === dragging?.id;
                  const idx = vi;
                  if (!isSrc) vi++;
                  const slot = !isSrc && !srcHome && indicatorIdx === idx ? (
                    <div className="card ghost-slot" key="ghost" style={{ height: dragHeight }} />
                  ) : null;
                  return [slot, (
                  <button
                    className={`card${isSrc ? (srcHome ? " ghost-inplace" : " drag-src") : ""}`} key={card.id} draggable
                    {...(isSrc ? {} : { "data-card-id": card.id })}
                    onDragStart={(e) => {
                      e.dataTransfer.effectAllowed = "move";
                      const height = e.currentTarget.getBoundingClientRect().height;
                      const c = card;
                      // the slot starts in the card's own spot: nothing else
                      // moves until the drag crosses a card boundary
                      // the card's index in the rendered list IS its visible
                      // slot position (everything before it stays visible)
                      const at = colCards.findIndex((x) => x.id === c.id);
                      // defer so the browser captures the drag image first
                      setTimeout(() => {
                        setDragHeight(height);
                        setDragSrc({ colId: col.id, index: at });
                        setDragging(c);
                        setDragOver({ colId: col.id, index: at });
                      }, 0);
                    }}
                    onDragEnd={endDrag}
                    onClick={() => openSheet(card)}
                  >
                    <div className="head">
                      <span className="num">{card.reference || `#${card.number}`}</span>
                      <span className="title">{card.title}</span>
                      <RestrictedMarker sensitive={card.sensitive} />
                      {indicatorFor(card, active, reviewCols)}
                    </div>
                    <div className="meta">
                      <span className="chip type">{card.type}</span>
                      {(card.priority === "high" || card.priority === "urgent") && (
                        <span className={`chip priority-${card.priority}`}>{card.priority}</span>
                      )}
                      {card.size && <span className="chip">{card.size}</span>}
                      {card.gate_flag && <span className={`dot ${card.gate_flag}`} title={`gate: ${card.gate_flag}`} />}
                      {card.merged_sha && (
                        <span className={`chip merged${(() => {
                          const newest = deploys.reduce((a, d) => Math.max(a, new Date(d.created_at).getTime()), 0);
                          return card.merged_at && newest > new Date(card.merged_at).getTime() ? " live" : "";
                        })()}`}
                          title={card.merged_at && deploys.length > 0
                            ? (deploys.some((d) => new Date(d.created_at).getTime() > new Date(card.merged_at!).getTime())
                              ? "merged and included in the current deployment"
                              : "merged — not yet in a deployment")
                            : "merged"}>
                          {(() => {
                            const newest = deploys.reduce((a, d) => Math.max(a, new Date(d.created_at).getTime()), 0);
                            const live = card.merged_at && newest > new Date(card.merged_at).getTime();
                            return <>{live ? "● " : ""}{card.merged_sha.slice(0, 7)}</>;
                          })()}
                        </span>
                      )}
                      {(() => {
                        const bs = pendingBlockers(card);
                        if (bs.length === 0) return null;
                        return (
                          <span className="chip blocked"
                            title={`waiting on ${bs.map((b) => `#${b.number} ${b.title}`).join(", ")} — the dispatcher skips this card until every blocker reaches a done column. Not stuck: nothing is wrong with it and no human action is needed.`}>
                            ⛔ blocked by {bs.map((b) => `#${b.number}`).join(", ")}
                          </span>
                        );
                      })()}
                      {card.stuck_at && <span className="chip stuck" title="blerg-board stopped working this card and wants a human — open it, the newest comment says why and what to do; Run clears the flag and retries">⏸ stuck {ageShort(card.stuck_at)}</span>}
                      {card.stale_at && <span className="chip stale" title="claimed but idle — no active session and no card activity for a while; update it or release it back to ready">💤 stale {ageShort(card.stale_at)}</span>}
                      {card.auto_merge && reviewCols.has(card.column_id ?? "") && (
                        <span className="chip automerge" title="auto-merges when the adversarial review approves">⚡ auto</span>
                      )}
                      {(card.tags ?? []).slice(0, 3).map((t) => <span className="chip" key={t}>{t}</span>)}
                    </div>
                  </button>
                  )];
                }); })()}
                {indicatorIdx != null && !srcHome && indicatorIdx >= visible.length && (
                  <div className="card ghost-slot" style={{ height: dragHeight }} />
                )}
                {visible.length === 0 && indicatorIdx == null && (
                  <div className="empty" style={{ padding: "18px 8px", fontSize: 13 }}>
                    {filtering && total > 0 ? `No match among ${total}` : "—"}
                  </div>
                )}
              </div>
            </section>
          );
        })}
      </div>
      {chatOpen && <BoardChat boardId={boardId} onClosed={() => setChatOpen(false)} />}
      {metricsOpen && <MetricsModal boardId={boardId} onClose={() => nav(`/boards/${boardId}`)} />}
      {openCard && (
        <CardSheet
          card={openCard} board={board} columns={columns}
          onClose={closeSheet}
        />
      )}
    </>
  );
}

// The phone layout's breakpoint, the same as the stylesheet's.
const phoneQuery = "(max-width: 640px)";
function phoneNow(): boolean { return window.matchMedia?.(phoneQuery).matches ?? false; }
function subscribePhone(onChange: () => void): () => void {
  const mq = window.matchMedia?.(phoneQuery);
  mq?.addEventListener?.("change", onChange);
  return () => mq?.removeEventListener?.("change", onChange);
}

// latestOnly hands out one ticket per call; a ticket says whether a newer one has been handed out
// since. A reload holds a ticket and lands only while it is still the newest.
function latestOnly(): () => () => boolean {
  let seq = 0;
  return () => {
    const mine = ++seq;
    return () => mine === seq;
  };
}
