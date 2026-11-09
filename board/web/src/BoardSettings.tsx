import { IconButton } from '@blerglab/chat'
import { BoardKeySettings } from "./BoardKeySettings";
import "../../../packages/settings/settings.css";
import BoardRunTarget from "./BoardRunTarget";
import { modelDisplayName } from "@blerglab/chat";
import { FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, Board } from "./api";
import { coreOrigin } from "./authClient";
import { useBoardAdmin, useMe } from "./me";
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
  const boardAdmin = useBoardAdmin(board.id);
  const canEdit = boardAdmin && !!me?.is_human;
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

  const [ownerName, setOwnerName] = useState('');
  useEffect(() => {
    if (!board.automation_token_set) return;
    let live = true;
    void api<{ username?: string }>(`/api/boards/${board.id}/automation/identity`).then(value => { if (live) setOwnerName(value.username || ''); }, () => {});
    return () => { live = false; };
  }, [board.id, board.automation_account_id, board.automation_token_set]);
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
      <h3 className="settings-heading">Automation</h3>
      <p className="settings-description">Choose whose account and which engine this board uses to run workspaces.</p>
      <dl className="settings-grid">
        <dt title="The person whose credentials this board uses">Runs as</dt>
        <dd>
          {board.automation_token_set ? (
            <>
              <span className="mono" title="the blerg-core account this token acts for">
                {board.automation_account_id ? mine ? `You${ownerName ? ` (${ownerName})` : ""}` : ownerName || "Another person (name unavailable)" : "Token owner"}
              </span>

              {board.automation_token_minted && (
                <span className="inherit" title="minted by Run as me; listed in blerg-core Settings → Agent tokens"> · Run as me</span>
              )}
            </>
          ) : (
            <span className="unset warn">no token — this board cannot start workspaces</span>
          )}
          {expiry && <span className={`inherit${expiry === "expired" ? " warn" : ""}`}> ({expiry})</span>}
        </dd>
        <BoardRunTarget board={board} editable={canEdit && mine && board.automation_token_set} onChanged={() => { discovery.retry(); onChanged?.(); }} />
        <dt title="the engine every workspace this board starts runs">engine</dt>
        <dd><span className="mono">{label(board.automation_engine || "claude")}</span></dd>
      </dl>
      {offerRunAsMe && (
        <div className={`run-as-me-cta${board.automation_token_set ? "" : " prominent"}`}>
          <button className="settings-action btn small" type="button" disabled={busy} onClick={() => void runAsMe()}>
            {busy && !pasting ? "Setting up…" : "Run as me"}
          </button>
          <p className="pop-foot">
            {board.automation_token_set
              ? "Make this board's workspaces run as you instead, on your own credentials."
              : "One click: workspaces this board starts run as you, on your own credentials."}{" "}
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
              Workspaces this board starts run as the person whose token this is, on their
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
          {discovery.error && <p role="alert">{discovery.error} <IconButton label="Retry engine discovery" onClick={discovery.retry} /></p>}
          {board.automation_token_set && discovery.state === "known" && discovery.engines.length === 0 && <p role="status">No engines are available for this board's automation account and target.</p>}
          {discovery.engines.length > 1 && (
            <label className="settings-field field">Engine
              <select className="settings-field" value={selected} onChange={(e) => setEngine(e.target.value)} disabled={busy}>
                {discovery.engines.map(e => <option key={e.id} value={e.id}>{e.name}</option>)}
              </select>
            </label>
          )}
          {discovery.engines.length === 1 && <p>Available engine: {discovery.engines[0].name}</p>}
          {selected && selected !== (board.automation_engine || "claude") && <p role="status">Save to use {label(selected)} for new workspaces. Changing engines clears model and effort overrides for this board and its cards. Existing workspaces keep their engine.</p>}
          {token.trim() && <p>Save the token first, then choose from its available engines.</p>}
          {showPaste && (
            <label className="settings-field field">
              Automation token {board.automation_token_set && "(leave empty to keep the current one)"}
              <input className="settings-field" type="password" autoComplete="off" spellCheck={false} value={token}
                placeholder="paste a run-sessions agent token" disabled={busy}
                onChange={(e) => setToken(e.target.value)} />
            </label>
          )}
          <div className="row">
            {(showPaste || selected) && (
              <button className="settings-action btn small" type="submit" disabled={busy || (!selected && !token.trim())}>Save</button>
            )}
            {canRunAsMe && !pasting && (
              <button className="settings-action btn ghost small" type="button" disabled={busy} onClick={() => setPasting(true)}>
                Use a different token…
              </button>
            )}
            {board.automation_token_set && (
              <button className="settings-action btn ghost small" type="button" disabled={busy}
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
  const canEdit = useBoardAdmin(board.id);
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
  return <section className="settings-section">
    <h3 className="settings-heading">Filed tickets</h3>
    {canEdit ? <label className="settings-field" style={{ display: "flex", gap: 8, fontSize: 13 }}>
      <input className="settings-toggle-control" type="checkbox" checked={enabled} disabled={busy} onChange={e => void change(e.target.checked)} />
      Include component version on filed tickets
    </label> : <p>Component version collection: {board.include_component_version ? "on" : "off"}</p>}
    <p className="settings-description">Include the Blerg version when filing tickets on this board.</p>
    {error && <p role="alert">{error}</p>}
  </section>;
}

function ArchiveSetting({ board, onChanged }: { board: Board; onChanged?: () => void }) {
  const canEdit = useBoardAdmin(board.id);
  const [days, setDays] = useState(String(board.auto_archive_done_days ?? 0));
  const [tags, setTags] = useState((board.auto_archive_keep_tags ?? ["to-test"]).join(", "));
  const [preview, setPreview] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function save(e: FormEvent) {
    e.preventDefault(); setBusy(true); setError("");
    const json = { auto_archive_done_days: Number(days), auto_archive_keep_tags: [...new Set(tags.split(",").map(t => t.trim()).filter(Boolean))] };
    try {
      if (Number(days) > 0 && preview === null) {
        const result = await api<{ count: number }>(`/api/boards/${board.id}/archive-preview`, { method: "POST", json });
        setPreview(result?.count ?? 0);
      } else {
        await api(`/api/boards/${board.id}`, { method: "PATCH", json });
        setPreview(null); onChanged?.();
      }
    } catch (e) { setError(e instanceof Error ? e.message : "Couldn't save archiving."); }
    finally { setBusy(false); }
  }
  return <section className="settings-section">
    <h3 className="settings-heading">Done cards</h3>
    <p className="settings-description">The board shows cards from the last 14 days in done. Expand older cards at any time.</p>
    {board.driven_by ? <p>Automatic archiving is unavailable on mirrored boards.</p> : canEdit ? <form onSubmit={e => void save(e)}>
      <label className="settings-field">Auto-archive done after (days)
        <input className="settings-field" type="number" min="0" max="36500" step="1" required value={days} disabled={busy}
          onChange={e => { setDays(e.target.value); setPreview(null); }} />
      </label>
      <label className="settings-field">Keep cards with tags
        <input className="settings-field" value={tags} disabled={busy} onChange={e => { setTags(e.target.value); setPreview(null); }} />
      </label>
      <p className="settings-description">Separate tags with commas. Use 0 to turn off archiving. Time starts when a card enters done. Stuck cards and cards with running sessions are kept. Archived cards stay searchable.</p>
      {preview !== null && <p role="status">{preview} cards eligible now. Future eligible cards will also be archived.</p>}
      <button className="settings-action btn small" type="submit" disabled={busy}>{preview === null ? "Save archiving" : "Confirm archiving"}</button>
      {preview !== null && <button className="settings-action btn small" type="button" disabled={busy} onClick={() => setPreview(null)}>Cancel</button>}
    </form> : <p>Automatic archiving: {board.auto_archive_done_days ? `after ${board.auto_archive_done_days} days in done` : "off"}</p>}
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
    { key: "worker", label: "worker", set: board.model, effort: board.effort, what: "workers that work a card" },
    { key: "reviewer", label: "reviewer", set: board.reviewer_model, effort: board.reviewer_effort || (board.reviewer_model ? "" : board.effort), what: "review workers" },
    { key: "discuss", label: "discuss", set: board.discuss_model, effort: board.discuss_effort || (board.discuss_model ? "" : board.effort), what: "per-card discussion channels" },
    { key: "chat", label: "board chat", set: board.chat_model, effort: board.chat_effort || (board.chat_model ? "" : board.effort), what: "the board channel in the header" },
  ];
}

// Models remain one click away; the rest of the configuration has room to grow.
export function BoardSettings({ board, onChanged }: { board: Board; onChanged?: () => void }) {
  const [open, setOpen] = useState<'models' | 'settings' | null>(null);
  const ref = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const modelTrigger = useRef<HTMLButtonElement>(null);
  const dialog = useRef<HTMLDivElement>(null);
  const canAdmin = useBoardAdmin(board.id);
  const close = useCallback(() => { setOpen(null); (open === 'models' ? modelTrigger : trigger).current?.focus(); }, [open]);
  useEffect(() => {
    const onOpen = () => setOpen('settings');
    window.addEventListener("blerg-board:open-settings", onOpen);
    return () => window.removeEventListener("blerg-board:open-settings", onOpen);
  }, []);
  useEffect(() => {
    if (!open) return;
    dialog.current?.focus();
    const onDown = (e: MouseEvent) => { if (open === 'models' && !ref.current?.contains(e.target as Node)) close(); };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") { e.preventDefault(); close(); }
      if (e.key === 'Tab' && open === 'settings') {
        const controls = dialog.current?.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), select:not(:disabled), textarea:not(:disabled), a[href]');
        if (!controls?.length) return;
        const first = controls[0], last = controls[controls.length - 1];
        if (e.shiftKey && (document.activeElement === first || document.activeElement === dialog.current)) { e.preventDefault(); last.focus(); }
        else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
      }
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => { document.removeEventListener("mousedown", onDown); document.removeEventListener("keydown", onKey); };
  }, [open, close]);
  return <div className="board-settings" ref={ref}>
    <button ref={modelTrigger} className="settings-action chip model-chip" aria-expanded={open === 'models'} onClick={() => setOpen(open === 'models' ? null : 'models')}>
      <span className="lede">model</span><span className="mono">{modelDisplayName(board.model || "") || "runner default"}</span>
    </button>
    <button ref={trigger} className="settings-action chip" onClick={() => setOpen('settings')}>Board settings{!board.automation_token_set && <span className="warn-dot" aria-label="no automation token" />}{!board.key && canAdmin && <span> · confirm board key</span>}</button>
    {open === 'models' && <div className="settings-pop board-model-pop" role="dialog" aria-label="Board models" ref={dialog} tabIndex={-1}>
      <ModelSettings board={board} onChanged={onChanged} />
    </div>}
    {open === 'settings' && <div className="board-settings-overlay">
      <div className="settings-pop board-settings-page" role="dialog" aria-modal="true" aria-label="Board settings" ref={dialog} tabIndex={-1}>
        <header><h2>Board settings</h2><button className="settings-action" onClick={close}>Close settings</button></header>
        {!canAdmin && <p className="settings-description">Only board admins can edit these settings.</p>}
        <section className="settings-section"><h3 className="settings-heading">General</h3>
          <BoardFields board={board} onChanged={onChanged} group="general" />
          <BoardKeySettings board={board} onChanged={onChanged} />
        </section>
        <section className="settings-section"><h3 className="settings-heading">Repos</h3>
          <BoardFields board={board} onChanged={onChanged} group="repos" />
        </section>
        <section className="settings-section"><h3 className="settings-heading">Runs</h3>
          <BoardFields board={board} onChanged={onChanged} group="runs" />
          <AutomationSection board={board} onChanged={onChanged} />
        </section>
        <ComponentVersionSetting key={`version:${board.id}:${board.include_component_version}`} board={board} onChanged={onChanged} />
        <section className="settings-section"><h3 className="settings-heading">Models</h3><ModelSettings board={board} onChanged={onChanged} /></section>
        <ArchiveSetting key={`archive:${board.id}:${board.auto_archive_done_days}`} board={board} onChanged={onChanged} />
      </div>
    </div>}
  </div>;
}

function ModelSettings({ board, onChanged }: { board: Board; onChanged?: () => void }) {
  const canEdit = useBoardAdmin(board.id);
  return <>{!canEdit && <dl className="settings-grid">{roleModels(board).map(r => <div key={r.key}><dt>{r.label}</dt><dd>{r.set || board.model || 'runner default'}{r.effort ? ` · ${r.effort}` : ''}</dd></div>)}</dl>}{canEdit && <BoardFields board={board} onChanged={onChanged} group="models" />}</>;
}

type Group = 'general' | 'repos' | 'runs' | 'models';
function BoardFields({ board, group, onChanged }: { board: Board; group: Group; onChanged?: () => void }) {
  const canEdit = useBoardAdmin(board.id);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const discovery = useBoardLaunchOptions(board.id, group === 'models' && canEdit && board.automation_token_set);
  const roles = roleModels(board);
  const fields = group === 'general' ? [{ key: 'name', label: 'Board name', value: board.name }, { key: 'description', label: 'Description', value: board.description || '' }]
    : group === 'repos' ? [{ key: 'repos', label: 'Repositories (one per line)', value: board.repos?.join('\n') || '' }, { key: 'git_base', label: 'Git base', value: board.git_base || '' }]
    : group === 'runs' ? [{ key: 'concurrency', label: 'Concurrency', value: String(board.concurrency) }, { key: 'ci_policy', label: 'CI policy', value: board.ci_policy }]
    : roles.flatMap(r => [{ key: r.key === 'worker' ? 'model' : `${r.key}_model`, label: `${r.label} model`, value: r.set || '' }, { key: r.key === 'worker' ? 'effort' : `${r.key}_effort`, label: `${r.label} effort`, value: board[(r.key === 'worker' ? 'effort' : `${r.key}_effort`) as keyof Board] as string || '' }]);
  const [modelValues, setModelValues] = useState<Record<string, string>>({});
  const [seenFields, setSeenFields] = useState(JSON.stringify(fields));
  if (seenFields !== JSON.stringify(fields)) {
    setSeenFields(JSON.stringify(fields));
    setModelValues({});
  }
  const valueOf = (key: string) => modelValues[key] ?? fields.find(f => f.key === key)?.value ?? '';
  const models = discovery.engines.find(e => e.id === board.automation_engine)?.models.models ?? [];
  async function save(e: FormEvent<HTMLFormElement>) {
    e.preventDefault(); setBusy(true); setError('');
    const data = new FormData(e.currentTarget);
    const json: Record<string, unknown> = {};
    for (const f of fields) {
      const value = String(data.get(f.key) ?? '');
      json[f.key] = f.key === 'repos' ? value.split('\n').map(s => s.trim()).filter(Boolean) : f.key === 'concurrency' ? Number(value) : value;
    }
    try { await api(`/api/boards/${board.id}`, { method: 'PATCH', json }); onChanged?.(); }
    catch (err) { setError(err instanceof Error ? err.message : "Could not save settings."); }
    finally { setBusy(false); }
  }
  return <form key={JSON.stringify(fields)} className="board-fields stack" onSubmit={e => void save(e)}>
    {fields.map(f => <label className="settings-field" key={f.key}>{f.label}
      {!canEdit ? <span>{f.value || 'Default'}</span> : f.key === 'ci_policy' ? <select className="settings-field" name={f.key} defaultValue={f.value} disabled={busy}><option value="required">Require passing CI</option><option value="if_present">Check CI when present</option></select>
        : group === 'models' && f.key.endsWith('model') ? <select className="settings-field" name={f.key} value={valueOf(f.key)} onChange={e => setModelValues(v => ({ ...v, [f.key]: e.target.value }))} disabled={busy}>
          <option value="">{f.key === 'model' ? 'Runner default' : 'Inherit worker model'}</option>
          {valueOf(f.key) && !models.some(m => m.id === valueOf(f.key)) && <option value={valueOf(f.key)}>{valueOf(f.key)}</option>}
          {models.map(m => <option key={m.id} value={m.id}>{m.name || m.id}</option>)}
        </select> : group === 'models' && f.key.endsWith('effort') ? <select className="settings-field" name={f.key} value={valueOf(f.key)} onChange={e => setModelValues(v => ({ ...v, [f.key]: e.target.value }))} disabled={busy}>
          <option value="">Default</option>
          {Array.from(new Set([...(models.find(m => m.id === (valueOf(f.key.replace('effort', 'model')) || valueOf('model')))?.efforts ?? []), ...(valueOf(f.key) ? [valueOf(f.key)] : [])])).map(effort => <option key={effort} value={effort}>{effort}</option>)}
        </select> : ['description', 'repos'].includes(f.key) ? <textarea className="settings-field" name={f.key} defaultValue={f.value} disabled={busy} />
          : <input className="settings-field" name={f.key} type={f.key === 'concurrency' ? 'number' : 'text'} min={f.key === 'concurrency' ? 0 : undefined} required={f.key === 'name' || f.key === 'concurrency'} defaultValue={f.value} disabled={busy} />}
    </label>)}
    {group === 'models' && discovery.error && <p role="alert">{discovery.error} <IconButton label="Retry models" onClick={discovery.retry} /></p>}
    {group === 'models' && <p className="settings-description">Empty role models inherit the worker model. Existing workspaces keep their models.</p>}
    {canEdit && <button className="settings-action" type="submit" disabled={busy}>Save {group}</button>}
    {error && <p role="alert">{error}</p>}
  </form>;
}
