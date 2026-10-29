import { useEffect, useState } from "react";
import {
  BrowserRouter, Routes, Route, Link, NavLink, useLocation, useNavigate, useParams, useSearchParams,
} from "react-router-dom";
import { api, ApiError, Board } from "./api";
import { coreOrigin } from "./authClient";
import { formatBuild, useBuildInfo } from "./buildInfo";
import { MeProvider, useMe } from "./me";
import ThemeToggle from "./ThemeToggle";
import { useCodeColours } from "./codeColours";
import SignInBanner from "./SignInBanner";
import { useSignInEpoch } from "./signIn";
import { BoardView } from "./BoardView";
import { SearchModal } from "./SearchModal";
import { TimeAgo } from "./TimeAgo";
import { GateLog } from "./GateLog";
import { Tokens } from "./Tokens";
import { NotFound } from "./NotFound";
import { SessionPage } from "./SessionPage";
import { DiffPage } from "./DiffPage";
import { NewBoardRepos } from "./NewBoardRepos";


// CORE_URL: the control-plane landing — coreOrigin() (shared across core,
// board and runner's authClient.ts, see Task 18) derives it correctly for
// both the k8s subdomain shape and desktop's bare-host+port shape. The
// previous `${protocol}//${hostname}` here dropped the port, so on desktop
// it pointed at a dead http://localhost/ instead of core's actual port.
const CORE_URL = coreOrigin();

interface BoardOverview {
  board_id: string; inbox: number; ready: number; in_progress: number; review: number;
  working: number; tokens_today: number; token_week: number[];
  sessions_week: number; agent_secs_week: number; activity_week: number[];
}

function durShort(secs: number): string {
  if (secs < 90) return `${secs}s`;
  if (secs < 5400) return `${Math.round(secs / 60)}m`;
  return `${(secs / 3600).toFixed(1)}h`;
}

function tokShort(n: number): string {
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${Math.round(n / 1e3)}k`;
  return String(n);
}

// Spark: a 7-day token sparkline, 60×16 inline SVG.
function Spark({ week }: { week: number[] }) {
  const max = Math.max(...week, 1);
  if (week.every((v) => v === 0)) return null;
  const pts = week.map((v, i) => `${i * 10},${15 - Math.round((v / max) * 13)}`).join(" ");
  return (
    <svg className="spark" viewBox="0 0 60 16" width="60" height="16" aria-label="tokens this week">
      <polyline points={pts} fill="none" stroke="var(--blaze)" strokeWidth="1.5" />
    </svg>
  );
}

// useNewVersion: the deploy loop is fast and stale tabs cause ghost bugs —
// poll index.html for a changed bundle hash and surface a reload nudge.
function useNewVersion(): boolean {
  const [stale, setStale] = useState(false);
  useEffect(() => {
    let current: string | null = null;
    const check = async () => {
      try {
        const html = await fetch("/", { cache: "no-store" }).then((r) => r.text());
        const m = html.match(/assets\/index-([\w-]+)\.js/);
        if (!m) return;
        if (current == null) current = m[1];
        else if (m[1] !== current) setStale(true);
      } catch { /* offline — never nag */ }
    };
    check();
    const iv = setInterval(check, 60000);
    return () => clearInterval(iv);
  }, []);
  return stale;
}

interface SessRow {
  id: string; board_id: string; board_name: string; role: string; lifecycle: string;
  external_session_id: string; created_at: string; last_active_at: string;
  card_number: number | null; card_title: string | null;
}

// SessionsTable: the fleet at a glance — every live session, its board,
// role, card, phase, and timing. Rows navigate to the work.
function SessionsTable() {
  const [rows, setRows] = useState<SessRow[]>([]);
  const nav = useNavigate();
  useEffect(() => {
    const load = () => api<SessRow[]>("/api/sessions").then((r) => setRows(r ?? [])).catch(() => {});
    load();
    const iv = setInterval(load, 15000);
    return () => clearInterval(iv);
  }, []);
  if (rows.length === 0) return null;
  const go = (r: SessRow) => {
    const isCard = r.card_number != null;
    nav(isCard ? `/boards/${r.board_id}/cards/${r.card_number}` : `/boards/${r.board_id}`);
    // the target composer mounts async — retry the focus request briefly
    const ev = isCard ? "blerg-board:card-chat-focus" : "blerg-board:chat-focus";
    let tries = 0;
    const t = setInterval(() => {
      window.dispatchEvent(new Event(ev));
      if (++tries >= 5) clearInterval(t);
    }, 300);
  };
  return (
    <section className="fleet">
      <h4 className="fleet-title">Active sessions</h4>
      <table>
        <thead>
          <tr><th>board</th><th>session</th><th>card</th><th>state</th><th>running</th><th>last active</th></tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.id} onClick={() => go(r)}>
              <td className="fleet-board">{r.board_name}</td>
              <td>{r.role === "board" ? "💬 board session" : r.role === "reviewer" ? "🛡 reviewer" : r.role}</td>
              <td className="fleet-card">{r.card_number != null ? `#${r.card_number} ${r.card_title ?? ""}` : "—"}</td>
              <td><span className={`convo-state ${r.lifecycle}`}>{r.lifecycle}</span></td>
              <td><TimeAgo iso={r.created_at} /></td>
              <td><TimeAgo iso={r.last_active_at} /></td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}

// BoardIcon: the project's favicon when it has a deploy_url, else a
// letter tile colored by a stable hash of the name — no faceless boards.
function BoardIcon({ board }: { board: Board }) {
  const [failed, setFailed] = useState(false);
  const hue = [...board.name].reduce((a, ch) => (a * 31 + ch.charCodeAt(0)) % 360, 7);
  if (!board.deploy_url || failed) {
    return (
      <span className="tile-icon letter" style={{ background: `hsl(${hue} 45% 28%)` }}>
        {board.name.slice(0, 1).toUpperCase()}
      </span>
    );
  }
  return (
    <img className="tile-icon" src={`/api/boards/${board.id}/icon`} alt=""
      onError={() => setFailed(true)} />
  );
}

// Board templates the create form offers. Kept in step with the backend
// registry (board/internal/templates) by hand: there is no listing route.
const BOARD_TEMPLATES = [
  {
    id: "focus-board",
    name: "Focus board",
    description: "Inbox, Today, This week, Waiting on, Someday, Proposed and Done, with source, due and tracking fields.",
  },
];

function Boards() {
  const me = useMe();
  const [boards, setBoards] = useState<Board[] | null>(null);
  const [overview, setOverview] = useState<Record<string, BoardOverview>>({});
  // ?new=<template id> (a link from elsewhere) opens the create form with
  // that template already chosen.
  const [search] = useSearchParams();
  const linked = BOARD_TEMPLATES.some((t) => t.id === search.get("new")) ? search.get("new")! : null;
  const [creating, setCreating] = useState(linked !== null);
  const [name, setName] = useState("");
  const [suggestion, setSuggestion] = useState("");
  const [boardKey, setBoardKey] = useState<string | null>(null);
  const [template, setTemplate] = useState(linked ?? "");
  const [repos, setRepos] = useState<string[]>([]);
  const [createError, setCreateError] = useState<string | null>(null);
  const [createNote, setCreateNote] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  // "Run this board's cards as me": checked by default unless the operator turned it off
  // (BLERG_BOARD_DEFAULT_RUN_AS_CREATOR). null = not touched yet, so it follows that default.
  const offerRunAsMe = !!me?.is_human && !!me?.run_as_me_available;
  const [runAsMeChoice, setRunAsMeChoice] = useState<boolean | null>(null);
  const runAsMe = runAsMeChoice ?? (me?.run_as_creator_default ?? true);
  const load = () => Promise.all([
    api<Board[]>("/api/boards").then((b) => setBoards(b ?? [])),
    api<BoardOverview[]>("/api/overview").then((os) => {
      const m: Record<string, BoardOverview> = {};
      for (const o of os ?? []) m[o.board_id] = o;
      setOverview(m);
    }).catch(() => {}),
  ]);
  useEffect(() => {
    load().catch(() => {});
    const iv = setInterval(() => load().catch(() => {}), 15000);
    return () => clearInterval(iv);
  }, []);

  useEffect(() => {
    if (!creating || boardKey !== null || !name.trim()) return;
    let active = true;
    const timer = setTimeout(() => {
      api<{ key: string }>(`/api/board-key-suggestion?name=${encodeURIComponent(name.trim())}`)
        .then(r => { if (active) setSuggestion(r.key ?? ""); }).catch(() => { if (active) setSuggestion(""); });
    }, 150);
    return () => { active = false; clearTimeout(timer); };
  }, [creating, name, boardKey]);

  const create = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!name.trim() || saving) return;
    setCreateError(null);
    setCreateNote(null);
    setSaving(true);
    try {
      const created = await api<{ name?: string; automation_error?: string }>("/api/boards", {
        json: {
          name: name.trim(), key: boardKey ?? suggestion, require_repo: false, ...(template ? { template } : {}), ...(repos.length ? { repos } : {}),
          ...(offerRunAsMe ? { run_as_me: runAsMe } : {}),
        },
      });
      // The board exists either way; if Run as me did not take, say so and how to finish.
      if (created?.automation_error) {
        setCreateNote(`“${created.name ?? name.trim()}” was created, but it can't start sessions yet: ${created.automation_error}. ` +
          "Open its settings and choose Run as me to try again.");
      }
    } catch (err) {
      // Keep the form and what was typed; say why the server refused.
      setCreateError(err instanceof Error ? err.message : "Could not create the board.");
      setSaving(false);
      return;
    }
    setSaving(false);
    setName(""); setBoardKey(null); setTemplate(""); setRepos([]); setRunAsMeChoice(null); setCreating(false); load().catch(() => {});
  };

  if (!boards) return null;
  return (
    <main>
      <div className="row">
        <h1 className="page">Boards</h1>
        <span className="spacer" />
        <button className="btn small" onClick={() => { setCreating(!creating); setCreateError(null); }}>
          {creating ? "Cancel" : "New board"}
        </button>
      </div>
      {creating && (
        <form className="stack new-board" onSubmit={create} style={{ marginBottom: 16 }}>
          <input type="text" placeholder="board name" value={name} autoFocus
            onChange={(e) => setName(e.target.value)} />
          <label htmlFor="new-board-key">Board key</label>
          <input id="new-board-key" value={boardKey ?? suggestion} required pattern="[A-Z][A-Z0-9]{1,4}" minLength={2} maxLength={5}
            onChange={e => setBoardKey(e.target.value.toUpperCase())} />
          <p className="settings-description">2–5 uppercase letters or digits, starting with a letter, unique across boards. Cards use this key, like BLG-205.</p>
          <label htmlFor="board-template">Template</label>
          <select id="board-template" value={template} onChange={(e) => setTemplate(e.target.value)}>
            <option value="">None</option>
            {BOARD_TEMPLATES.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
          </select>
          <p style={{ margin: 0, color: "var(--fog-dim)", fontSize: 13 }}>
            {BOARD_TEMPLATES.find((t) => t.id === template)?.description
              ?? "The default columns (inbox, ready, in progress, done) and no custom fields."}
          </p>
          <NewBoardRepos value={repos} onChange={setRepos} />
          {offerRunAsMe && (
            <label className="run-as-creator" style={{ display: "flex", gap: 8, fontSize: 13, alignItems: "center" }}>
              <input type="checkbox" checked={runAsMe} onChange={(e) => setRunAsMeChoice(e.target.checked)} />
              Run this board's cards as me
            </label>
          )}
          {offerRunAsMe && (
            <p style={{ margin: 0, color: "var(--fog-dim)", fontSize: 13 }}>
              {runAsMe
                ? "Sessions this board starts run as you, on your own credentials: a run-sessions token is created for you in blerg-core (revocable in its Settings → Agent tokens)."
                : "The board starts no sessions until someone sets who they run as, in the board's settings."}
            </p>
          )}
          {createError && <p role="alert" style={{ color: "var(--danger)", fontSize: 13, margin: 0 }}>{createError}</p>}
          <button className="btn" type="submit" disabled={saving}>Create board</button>
        </form>
      )}
      {createNote && <p role="status" className="warn" style={{ fontSize: 13 }}>{createNote}</p>}
      {boards.length === 0 && !creating ? (
        <div className="empty">
          <p className="sign">No boards yet</p>
          <p>
            {me?.is_admin
              ? "Create one, then mint an agent token so sessions can start stacking cards."
              : "No boards yet — create one. To let agents work here, an admin mints a token on the Tokens page."}
          </p>
        </div>
      ) : (
        <div className="boards">
          {boards.map((b) => {
            const o = overview[b.id];
            return (
              <Link className="board-tile" key={b.id} to={`/boards/${b.id}`}>
                {b.gate_enabled && <span className="gate-on" title="Admission gate on: an LLM curator reviews agent writes (dedup + quality) before they land. Hover the 'gated' chip on the board for details." />}
                <div className="row" style={{ gap: 8 }}>
                  <BoardIcon board={b} />
                  <h2 style={{ margin: 0 }}>{b.name}{b.key && <span className="mono"> · {b.key}</span>}</h2>
                  {(o?.working ?? 0) > 0 && (
                    <span className="churn" title={`${o!.working} session${o!.working === 1 ? "" : "s"} working right now`} />
                  )}
                  <span className="spacer" />
                  {o && <Spark week={o.token_week.some((v) => v > 0) ? o.token_week : o.activity_week} />}
                </div>
                <div className="desc">{b.description ?? " "}</div>
                {o && (o.inbox + o.ready + o.in_progress + o.review + o.tokens_today) > 0 && (
                  <div className="tile-tidbits">
                    {o.inbox > 0 && <span className="tid">inbox <b>{o.inbox}</b></span>}
                    {o.ready > 0 && <span className="tid">ready <b>{o.ready}</b></span>}
                    {o.in_progress > 0 && <span className="tid blaze-t">in&nbsp;progress <b>{o.in_progress}</b></span>}
                    {o.review > 0 && <span className="tid amber-t">review <b>{o.review}</b></span>}
                    {o.sessions_week > 0 && (
                      <span className="tid">7d <b>{o.sessions_week}</b> sessions · <b>{durShort(o.agent_secs_week)}</b></span>
                    )}
                    {o.tokens_today > 0 && <span className="tid">today <b>{tokShort(o.tokens_today)}</b> tok</span>}
                  </div>
                )}
              </Link>
            );
          })}
        </div>
      )}
      <SessionsTable />
    </main>
  );
}

// overlayOpen: whether anything is open over the page (every one of them is a dialog).
function overlayOpen(): boolean {
  return document.querySelector('[role="dialog"], [aria-modal="true"]') != null;
}

function BoardRoute() {
  const { id } = useParams();
  return id ? <BoardView boardId={id} /> : null;
}

// Shell wraps every route in the MeProvider (so useMe() works anywhere below
// it, including in the routed page components like Boards) and the shared
// chrome. It is exported so tests can render it standalone.
function Shell({ children }: { children: React.ReactNode }) {
  // A token the sign-in banner's quiet retry obtained (signIn.ts) is a fresh start: everything under
  // here mounts again and asks for its data, as a load with a token would, without reloading the page.
  const signInEpoch = useSignInEpoch();
  return (
    <MeProvider key={signInEpoch}>
      <ShellChrome>{children}</ShellChrome>
    </MeProvider>
  );
}

function ShellChrome({ children }: { children: React.ReactNode }) {
  useCodeColours();
  const me = useMe();
  const stale = useNewVersion();
  const build = formatBuild(useBuildInfo());
  const [searchOpen, setSearchOpen] = useState(false);
  const loc = useLocation();
  const boardId = loc.pathname.match(/^\/boards\/([0-9a-f-]{36})/)?.[1];
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const el = document.activeElement;
      const typing = el && (el.tagName === "INPUT" || el.tagName === "TEXTAREA");
      if (!typing && (e.key === "/" || ((e.ctrlKey || e.metaKey) && e.key === "k"))) {
        // The one guard for the search shortcut: while any dialog, popover or drawer is open
        // (a card, Metrics, Board settings, the board chat, the Filter choices, the search
        // dialog itself), it does nothing, rather than reach behind the overlay.
        if (overlayOpen()) return;
        e.preventDefault();
        // An open board takes the search into its own toolbar field (it cancels this event);
        // everywhere else the search dialog opens.
        if (window.dispatchEvent(new Event("blerg-board:search-inline", { cancelable: true }))) setSearchOpen(true);
      }
    };
    const onOpen = () => setSearchOpen(true);
    window.addEventListener("keydown", onKey);
    window.addEventListener("blerg-board:search", onOpen);
    return () => { window.removeEventListener("keydown", onKey); window.removeEventListener("blerg-board:search", onOpen); };
  }, []);

  return (
    <>
    <SignInBanner />
    {stale && (
      <button className="stale-banner" onClick={() => location.reload()}>
        blerg-board updated — click to reload
      </button>
    )}
    <>
      <header className="topbar">
        <a href={CORE_URL} className="logo" title="blerg control plane">
          <span className="wordmark" aria-label="blerg">blerg<i className="cur" aria-hidden="true" /></span>
          <span className="divider" aria-hidden="true" />
          <span className="component">Board</span>
        </a>
        {build.main && (
          <span className="build-version" title={build.title || undefined}>
            {build.main}
            {build.commit && <span className="build-commit"> · {build.commit}</span>}
          </span>
        )}
        {build.main && build.update && (
          <>
            <a className="build-update" href={build.update.href} target="_blank" rel="noopener noreferrer" title={build.update.title}>
              {build.update.label} ↗
            </a>
            <a className="build-update" href={`${CORE_URL}/upgrade`}>How to upgrade</a>
          </>
        )}
        <nav>
          <NavLink to="/" end className={({ isActive }) => (isActive ? "active" : "")}>Boards</NavLink>
          <NavLink to="/gate" className={({ isActive }) => (isActive ? "active" : "")}>Gate</NavLink>
          {me?.is_admin && (
            <NavLink to="/tokens" className={({ isActive }) => (isActive ? "active" : "")}>Tokens</NavLink>
          )}
          <a href="/onboard" title="the doc to point an agent at">Onboard</a>
          <ThemeToggle />
        </nav>
      </header>
      {children}
      {searchOpen && <SearchModal boardId={boardId} onClose={() => setSearchOpen(false)} key={boardId ?? "all"} />}
    </>
    </>
  );
}

export default function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/" element={<Shell><Boards /></Shell>} />
        <Route path="/boards/:id" element={<Shell><BoardRoute /></Shell>} />
        <Route path="/boards/:id/cards/:number" element={<Shell><BoardRoute /></Shell>} />
        <Route path="/boards/:id/cards/:number/session" element={<Shell><SessionPage /></Shell>} />
        <Route path="/boards/:id/cards/:number/diff" element={<Shell><DiffPage /></Shell>} />
        <Route path="/boards/:id/metrics" element={<Shell><BoardRoute /></Shell>} />
        <Route path="/gate" element={<Shell><GateLog /></Shell>} />
        <Route path="/tokens" element={<Shell><Tokens /></Shell>} />
        <Route path="*" element={<Shell><NotFound /></Shell>} />
      </Routes>
    </BrowserRouter>
  );
}

export { ApiError, Shell, Boards };
