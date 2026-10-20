import { modelDisplayName } from "@blerglab/chat";
import { FormEvent, Fragment, useEffect, useLayoutEffect, useRef, useState } from "react";
import { api, ApiError, Board } from "./api";
import { coreOrigin } from "./authClient";
import { useMe } from "./me";
import { useBoardLaunchOptions } from "./launchOptions";

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
//
// "Run as me" is the one-click way to set it: the board asks blerg-core to
// mint a run-sessions token for the signed-in person and stores it, with no
// copy and paste. Pasting stays available ("Use a different token…") for
// service accounts, and is the only way on an install that cannot mint.
function AutomationSection({ board, onChanged }: { board: Board; onChanged?: () => void }) {
  const me = useMe();
  const canEdit = !!me?.is_admin && !!me?.is_human;
  const canRunAsMe = canEdit && !!me?.run_as_me_available;
  const discovery = useBoardLaunchOptions(board.id, canEdit && board.automation_token_set);
  const label = (id: string) => discovery.engines.find(e => e.id === id)?.name ?? id;

  const [token, setToken] = useState("");
  const [engine, setEngine] = useState(board.automation_engine || "claude");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [pasting, setPasting] = useState(false);
  // follow the server's value when it changes (after a save, a refetch): the
  // "adjust state during render" pattern, not an effect
  const [seenEngine, setSeenEngine] = useState(board.automation_engine);
  if (seenEngine !== board.automation_engine) {
    setSeenEngine(board.automation_engine);
    setEngine(board.automation_engine || "claude");
  }

  const selected = discovery.state === "known" ? (discovery.engines.find(e => e.id === engine)?.id ?? discovery.engines[0]?.id ?? "") : "";
  const expiry = expiryNote(board.automation_token_expires_at);
  const mine = !!board.automation_account_id && me?.account_id === board.automation_account_id;
  // Run as me is offered unless the board already runs on a live token Blerg minted for you.
  const offerRunAsMe = canRunAsMe && !(board.automation_token_set && mine && board.automation_token_minted && expiry !== "expired");
  // With Run as me on offer, pasting is the secondary path, behind "Use a different token…".
  const showPaste = !canRunAsMe || pasting;

  const save = async (body: Record<string, string>) => {
    setError("");
    setBusy(true);
    try {
      await api(`/api/boards/${board.id}`, { method: "PATCH", json: body });
      setToken("");
      if ("automation_token" in body) discovery.retry();
      onChanged?.();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "save failed");
    } finally {
      setBusy(false);
    }
  };
  const runAsMe = async () => {
    setError("");
    setBusy(true);
    try {
      await api(`/api/boards/${board.id}/automation/run-as-me`, { method: "POST" });
      setPasting(false);
      discovery.retry();
      onChanged?.();
    } catch (e) {
      setError(e instanceof ApiError ? e.message : "Run as me failed");
    } finally {
      setBusy(false);
    }
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const body: Record<string, string> = {};
    if (selected && !token.trim()) body.automation_engine = selected;
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
              {mine && <span className="inherit"> (you)</span>}
              {board.automation_token_minted && (
                <span className="inherit" title="minted by Run as me; listed in blerg-core Settings → Agent tokens"> · Run as me</span>
              )}
            </>
          ) : (
            <span className="unset warn">no token — this board cannot start sessions</span>
          )}
          {expiry && <span className={`inherit${expiry === "expired" ? " warn" : ""}`}> ({expiry})</span>}
        </dd>
        <dt title="the engine every session this board starts runs">engine</dt>
        <dd><span className="mono">{label(board.automation_engine || "claude")}</span></dd>
      </dl>
      {offerRunAsMe && (
        <div className={`run-as-me-cta${board.automation_token_set ? "" : " prominent"}`}>
          <button className="btn small" type="button" disabled={busy} onClick={() => void runAsMe()}>
            {busy && !pasting ? "Setting up…" : "Run as me"}
          </button>
          <p className="pop-foot">
            {board.automation_token_set
              ? "Make this board's sessions run as you instead, on your own credentials."
              : "One click: sessions this board starts run as you, on your own credentials."}{" "}
            A run-sessions token is created for you in blerg-core and saved here; you can revoke it in{" "}
            <a href={`${coreOrigin()}/settings`} target="_blank" rel="noopener noreferrer">Settings → Agent tokens</a>.
          </p>
        </div>
      )}
      <p className="pop-foot warn">
        Anyone who can write cards on this board can spend this credential and could get an
        agent to reveal it. Only connect a token you're comfortable with every card-writer
        effectively having.
      </p>
      {canEdit && (
        <form className="automation-form" onSubmit={submit}>
          {showPaste && (
            <p className="pop-foot">
              Sessions this board starts run as the person whose token this is, on their
              own {label(selected || engine)} credential. To set it: in{" "}
              <a href={`${coreOrigin()}/settings`} target="_blank" rel="noopener noreferrer">
                blerg-core Settings → Agent tokens
              </a>
              , create a token with preset <code className="mono">run-sessions</code>, copy it
              and paste it here. It must be your own; it is never shown again. Make sure your{" "}
              {label(selected || engine)} credential is connected on the same Settings page.
            </p>
          )}
          {(discovery.state === "loading" || (discovery.refreshing && discovery.state !== "known")) && <p role="status">Loading available engines…</p>}
          {discovery.error && <p role="alert">{discovery.error} <button type="button" onClick={discovery.retry}>Retry engine discovery</button></p>}
          {board.automation_token_set && discovery.state === "known" && discovery.engines.length === 0 && <p role="status">No engines are available for this board's automation account and target.</p>}
          {discovery.engines.length > 1 && (
            <label className="field">Engine
              <select value={selected} onChange={(e) => setEngine(e.target.value)} disabled={busy}>
                {discovery.engines.map(e => <option key={e.id} value={e.id}>{e.name}</option>)}
              </select>
            </label>
          )}
          {discovery.engines.length === 1 && <p>Available engine: {discovery.engines[0].name}</p>}
          {selected && selected !== (board.automation_engine || "claude") && <p role="status">Save to use {label(selected)} for new sessions. Changing engines clears model and effort overrides for this board and its cards. Existing sessions keep their engine.</p>}
          {token.trim() && <p>Save the token first, then choose from its available engines.</p>}
          {showPaste && (
            <label className="field">
              Automation token {board.automation_token_set && "(leave empty to keep the current one)"}
              <input type="password" autoComplete="off" spellCheck={false} value={token}
                placeholder="paste a run-sessions agent token" disabled={busy}
                onChange={(e) => setToken(e.target.value)} />
            </label>
          )}
          <div className="row">
            {(showPaste || selected) && (
              <button className="btn small" type="submit" disabled={busy || (!selected && !token.trim())}>Save</button>
            )}
            {canRunAsMe && !pasting && (
              <button className="btn ghost small" type="button" disabled={busy} onClick={() => setPasting(true)}>
                Use a different token…
              </button>
            )}
            {board.automation_token_set && (
              <button className="btn ghost small" type="button" disabled={busy}
                onClick={() => void save({ automation_token: "" })}>
                Remove token
              </button>
            )}
          </div>
          {error && <p className="form-error" role="alert">{error}</p>}
        </form>
      )}
    </>
  );
}

function ComponentVersionSetting({ board, onChanged }: { board: Board; onChanged?: () => void }) {
  const me = useMe();
  const [enabled, setEnabled] = useState(board.include_component_version === true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function change(next: boolean) {
    setBusy(true); setError("");
    try {
      await api(`/api/boards/${board.id}`, { method: "PATCH", json: { include_component_version: next } });
      setEnabled(next);
      onChanged?.();
    } catch (e) { setError(e instanceof Error ? e.message : "Couldn't save version collection."); }
    finally { setBusy(false); }
  }
  return <section>
    <p className="sign pop-head">Filed ticket context</p>
    {me?.is_admin ? <label style={{ display: "flex", gap: 8, fontSize: 13 }}>
      <input type="checkbox" checked={enabled} disabled={busy} onChange={e => void change(e.target.checked)} />
      Include component version on filed tickets
    </label> : <p>Component version collection: {board.include_component_version ? "on" : "off"}</p>}
    <p style={{ fontSize: 12 }}>When available, attach the originating component's version and commit to tickets filed on this board.</p>
    {error && <p role="alert">{error}</p>}
  </section>;
}

// Model precedence, mirrored from internal/api/runnerapi.go (boardRoleModel +
// spawnSession): per-spawn override > the card's own model > the board's role
// override > board.model > empty, which leaves the choice to the runner. An
// empty role override is NOT "no model" — it means "inherit the board model",
// so the panel resolves it rather than showing a blank.
interface RoleModel { key: string; label: string; set: string; effort?: string; what: string }

function roleModels(board: Board): RoleModel[] {
  return [
    { key: "worker", label: "worker", set: board.model, effort: board.effort, what: "sessions that work a card" },
    { key: "reviewer", label: "reviewer", set: board.reviewer_model, effort: board.reviewer_effort || (board.reviewer_model ? "" : board.effort), what: "adversarial review sessions" },
    { key: "discuss", label: "discuss", set: board.discuss_model, effort: board.discuss_effort || (board.discuss_model ? "" : board.effort), what: "per-card discussion sessions" },
    { key: "chat", label: "board chat", set: board.chat_model, effort: board.chat_effort || (board.chat_model ? "" : board.effort), what: "the board session in the header" },
  ];
}

// BoardSettings: the board's configured models, the repo wiring sessions clone
// from, and the run dispatcher's concurrency — read-only; board admin for
// those is REST/MCP-only. This exists so a human can answer "is this board on
// Opus?" or "why is this board only working one card?" without curl. The
// editable controls are the automation identity and the per-board opt-in for
// recording component versions on filed tickets.
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
  const chipLabel = worker ? modelDisplayName(worker) : "runner default";
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
          <span className="mono rev">rev {modelDisplayName(reviewer) || "runner default"}</span>
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
                      {effective || "runner default"}{r.effort ? ` · ${r.effort}` : ""}
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
          <ComponentVersionSetting key={`${board.id}:${board.include_component_version}`} board={board} onChanged={onChanged} />
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
