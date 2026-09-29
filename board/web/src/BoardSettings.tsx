import { FormEvent, Fragment, useEffect, useLayoutEffect, useRef, useState } from "react";
import { api, ApiError, Board } from "./api";
import { coreOrigin } from "./authClient";
import { useMe } from "./me";

const ENGINES = [
  { id: "claude", label: "Claude" },
  { id: "codex", label: "Codex" },
  { id: "hermes", label: "Hermes" },
] as const;

function engineLabel(id: string): string {
  return ENGINES.find((e) => e.id === id)?.label ?? id;
}

// expiryNote: "expires in 12 days" / "expired" — the one thing a human needs
// to know about a token they can never read back.
function expiryNote(iso: string | null): string {
  if (!iso) return "";
  const ms = new Date(iso).getTime() - Date.now();
  if (ms <= 0) return "expired";
  const days = Math.floor(ms / 86_400_000);
  if (days >= 1) return `expires in ${days} day${days === 1 ? "" : "s"}`;
  return "expires within a day";
}

// AutomationSection: WHO this board's sessions run as. Every session the
// board starts — Run board, respawns, reviewers, standing agents, and the
// Run/Discuss/chat buttons — goes to the runner under this token, so it runs
// on the token owner's own engine credential. The token is write-only: the
// server never sends it back, so this only ever shows whether one is set.
function AutomationSection({ board, onChanged }: { board: Board; onChanged?: () => void }) {
  const me = useMe();
  const [token, setToken] = useState("");
  const [engine, setEngine] = useState(board.automation_engine || "claude");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  // follow the server's value when it changes (after a save, a refetch): the
  // "adjust state during render" pattern, not an effect
  const [seenEngine, setSeenEngine] = useState(board.automation_engine);
  if (seenEngine !== board.automation_engine) {
    setSeenEngine(board.automation_engine);
    setEngine(board.automation_engine || "claude");
  }

  const canEdit = !!me?.is_admin && !!me?.is_human;
  const expiry = expiryNote(board.automation_token_expires_at);

  const save = async (body: Record<string, string>) => {
    setError("");
    setBusy(true);
    try {
      await api(`/api/boards/${board.id}`, { method: "PATCH", json: body });
      setToken("");
      onChanged?.();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "save failed");
    } finally {
      setBusy(false);
    }
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const body: Record<string, string> = { automation_engine: engine };
    if (token.trim()) body.automation_token = token.trim();
    void save(body);
  };

  return (
    <>
      <p className="sign pop-head">Automation</p>
      <dl className="settings-grid">
        <dt title="the blerg-core agent token every session this board starts runs under">runs as</dt>
        <dd>
          {board.automation_token_set ? (
            <>
              <span className="mono" title="the blerg-core account this token acts for">
                {board.automation_account_id ? `account ${board.automation_account_id}` : "token set"}
              </span>
              {board.automation_account_id && me?.account_id === board.automation_account_id && (
                <span className="inherit"> (you)</span>
              )}
            </>
          ) : (
            <span className="unset warn">no token — this board cannot start sessions</span>
          )}
          {expiry && <span className={`inherit${expiry === "expired" ? " warn" : ""}`}> ({expiry})</span>}
        </dd>
        <dt title="the engine every session this board starts runs">engine</dt>
        <dd><span className="mono">{engineLabel(board.automation_engine || "claude")}</span></dd>
      </dl>
      <p className="pop-foot warn">
        Anyone who can write cards on this board can spend this credential and could get an
        agent to reveal it. Only connect a token you're comfortable with every card-writer
        effectively having.
      </p>
      {canEdit && (
        <form className="automation-form" onSubmit={submit}>
          <p className="pop-foot">
            Sessions this board starts run as the person whose token this is, on their
            own {engineLabel(engine)} credential. To set it: in{" "}
            <a href={`${coreOrigin()}/settings`} target="_blank" rel="noopener noreferrer">
              blerg-core Settings → Agent tokens
            </a>
            , create a token with preset <code className="mono">run-sessions</code>, copy it
            and paste it here. It must be your own; it is never shown again. Make sure your{" "}
            {engineLabel(engine)} credential is connected on the same Settings page.
          </p>
          <label className="field">
            Engine
            <select value={engine} onChange={(e) => setEngine(e.target.value)} disabled={busy}>
              {ENGINES.map((e) => <option key={e.id} value={e.id}>{e.label}</option>)}
            </select>
          </label>
          <label className="field">
            Automation token {board.automation_token_set && "(leave empty to keep the current one)"}
            <input type="password" autoComplete="off" spellCheck={false} value={token}
              placeholder="paste a run-sessions agent token" disabled={busy}
              onChange={(e) => setToken(e.target.value)} />
          </label>
          <div className="row">
            <button className="btn small" type="submit" disabled={busy}>Save</button>
            {board.automation_token_set && (
              <button className="btn ghost small" type="button" disabled={busy}
                onClick={() => void save({ automation_token: "" })}>
                Remove token
              </button>
            )}
          </div>
          {error && <p className="form-error">{error}</p>}
        </form>
      )}
    </>
  );
}

// shortModel: "claude-opus-5" → "opus-5". Only for the header chip, which has
// one line of space; the panel and the tooltip always show the full id.
function shortModel(m: string): string {
  return m.replace(/^claude-/, "");
}

// Model precedence, mirrored from internal/api/runnerapi.go (boardRoleModel +
// spawnSession): per-spawn override > the card's own model > the board's role
// override > board.model > empty, which leaves the choice to the runner. An
// empty role override is NOT "no model" — it means "inherit the board model",
// so the panel resolves it rather than showing a blank.
interface RoleModel { key: string; label: string; set: string; what: string }

function roleModels(board: Board): RoleModel[] {
  return [
    { key: "worker", label: "worker", set: board.model, what: "sessions that work a card" },
    { key: "reviewer", label: "reviewer", set: board.reviewer_model, what: "adversarial review sessions" },
    { key: "discuss", label: "discuss", set: board.discuss_model, what: "per-card discussion sessions" },
    { key: "chat", label: "board chat", set: board.chat_model, what: "the board session in the header" },
  ];
}

// BoardSettings: the board's configured models, the repo wiring sessions clone
// from, and the run dispatcher's concurrency — read-only; board admin for
// those is REST/MCP-only. This exists so a human can answer "is this board on
// Opus?" or "why is this board only working one card?" without curl. The one
// thing editable here is the automation identity, because it can only be set
// by a person, for themselves.
export function BoardSettings({ board, onChanged }: { board: Board; onChanged?: () => void }) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const popRef = useRef<HTMLDivElement>(null);
  // The panel hangs off the chip's left edge, which on a phone-width viewport
  // (or a board with a long name pushing the chip right) puts its right edge
  // past the screen. Measure once on open and slide it back inside, never
  // further than the left margin.
  const [shift, setShift] = useState(0);
  useLayoutEffect(() => {
    if (!open) return;
    const el = popRef.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    const over = r.right - (window.innerWidth - 12);
    if (over > 0) setShift(-Math.min(over, Math.max(0, r.left - 12)));
  }, [open]);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => { if (e.key === "Escape") setOpen(false); };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open]);

  const roles = roleModels(board);
  const worker = board.model;
  const reviewer = board.reviewer_model || worker;
  // The chip carries the worker model; the reviewer only earns space when it
  // actually differs (the common case is one model for the whole board).
  const chipLabel = worker ? shortModel(worker) : "runner default";
  const reviewerDiffers = reviewer !== worker;

  return (
    <div className="board-settings" ref={ref}>
      <button
        className={`chip model-chip${open ? " open" : ""}`}
        aria-expanded={open}
        onClick={() => {
          setShift(0); // measure a fresh opening from the unshifted position
          setOpen((o) => !o);
        }}
        title={`Board models — worker: ${worker || "runner default"} · reviewer: ${reviewer || "runner default"}. Click for all four${board.automation_token_set ? "" : " — and to set the automation token this board needs to start sessions"}.`}
      >
        <span className="lede">model</span>
        <span className="mono">{chipLabel}</span>
        {!board.automation_token_set && <span className="warn-dot" aria-label="no automation token" />}
        {reviewerDiffers && (
          <span className="mono rev">rev {shortModel(reviewer) || "runner default"}</span>
        )}
      </button>
      {open && (
        <div className="settings-pop" role="dialog" aria-label="Board settings"
          ref={popRef} style={shift ? { marginLeft: shift } : undefined}>
          <p className="sign pop-head">Board models</p>
          <dl className="settings-grid">
            {roles.map((r) => {
              const effective = r.set || worker;
              return (
                <Fragment key={r.key}>
                  <dt title={r.what}>{r.label}</dt>
                  <dd>
                    <span className={`mono${effective ? "" : " unset"}`}
                      title={effective ? effective : "no model configured anywhere — the runner picks"}>
                      {effective || "runner default"}
                    </span>
                    {!r.set && r.key !== "worker" && (
                      <span className="inherit"> (inherited)</span>
                    )}
                  </dd>
                </Fragment>
              );
            })}
          </dl>
          <p className="sign pop-head">Repos</p>
          <dl className="settings-grid">
            <dt title="sessions clone the first repo as their working directory">repos</dt>
            <dd className="mono">{board.repos?.length ? board.repos.join(", ") : <span className="unset">none</span>}</dd>
            <dt title="sessions clone from git_base/<repo>.git">git base</dt>
            <dd className="mono">{board.git_base || <span className="unset">server default</span>}</dd>
          </dl>
          <p className="sign pop-head">Board run</p>
          <dl className="settings-grid">
            <dt title="how many cards the run dispatcher keeps in flight on this board at once — the cross-board priority dial. Applies to the run already going.">
              concurrency
            </dt>
            <dd>
              <span className="mono">{board.concurrency}</span>
              {board.concurrency === 0 && (
                <span className="inherit"> (parked — run stays on, nothing dispatched)</span>
              )}
            </dd>
            <dt title="what auto-merge does when a pull request head reports no CI at all. Only ever about ABSENT checks: a red or still-running check blocks under either setting.">
              ci policy
            </dt>
            <dd>
              <span className="mono">{board.ci_policy}</span>
              <span className="inherit">
                {board.ci_policy === "if_present"
                  ? " (no checks reported = nothing to fail; the review is the gate)"
                  : " (a green check is required, so a repo with no CI never auto-merges)"}
              </span>
            </dd>
          </dl>
          <AutomationSection board={board} onChanged={onChanged} />
          <p className="pop-foot">
            Models, repos and the run settings are read-only here. A card can
            override the worker model for its own sessions; board settings change
            through the REST/MCP API or a board session.
          </p>
        </div>
      )}
    </div>
  );
}
