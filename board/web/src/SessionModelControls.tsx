import { modelDisplayNames } from "@blerglab/chat";
import { useState } from "react";
import { api } from "./api";
import { useMe } from "./me";
import { useBoardLaunchOptions } from "./launchOptions";
import type { RunnerSession } from "./SessionChat";

/** Native engine identity is fixed. These controls request settings for its next
 * turn; polled/native metadata continues to display the effective values. */
export function SessionModelControls({ session }: { session: RunnerSession }) {
  const me = useMe();
  const enabled = !!me?.is_human && !!session.board_id && !["ended", "stopped", "error", "disconnected"].includes(session.lifecycle);
  const catalog = useBoardLaunchOptions(session.board_id, enabled, session.id);
  const [draft, setDraft] = useState({ model: session.model ?? "", effort: session.effort ?? "" });
  const [seen, setSeen] = useState(`${session.id}\0${session.model}\0${session.effort}`);
  const current = `${session.id}\0${session.model}\0${session.effort}`;
  if (seen !== current) {
    setSeen(current);
    setDraft({ model: session.model ?? "", effort: session.effort ?? "" });
  }
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  if (!enabled) return null;
  const engine = catalog.engines.find(e => e.id === session.engine);
  const models = engine?.models.models ?? [];
  const modelNames = modelDisplayNames(draft.model && !models.some(m => m.id === draft.model) ? [...models, { id: draft.model }] : models);
  const selected = models.find(m => m.id === draft.model);
  const efforts = selected?.efforts ?? [];
  const apply = async () => {
    setBusy(true); setNotice("");
    try {
      await api(`/api/runner-sessions/${encodeURIComponent(session.id)}/model`, { json: { model: draft.model, effort: draft.effort } });
      setNotice("Settings requested for the next turn.");
    } catch (error) { setNotice(error instanceof Error ? error.message : "Could not change session settings."); }
    finally { setBusy(false); }
  };
  return <div className="row" aria-label="Session model settings">
    {catalog.state === "loading" && <span role="status">Loading models…</span>}
    {catalog.error && <span role="alert">{catalog.error} <button type="button" className="btn ghost small" onClick={catalog.retry}>Retry models</button></span>}
    {catalog.state === "known" && models.length === 0 && <span>No models are available for this session's engine.</span>}
    {models.length > 0 && <>
      <label>Model <select aria-label="Session model" value={draft.model} disabled={busy} onChange={e => { setDraft({ model: e.target.value, effort: "" }); setNotice(""); }}>
        {!selected && <option value={draft.model} title={draft.model}>{draft.model ? `${modelNames.get(draft.model)} (current)` : "Choose model"}</option>}
        {models.map(model => <option key={model.id} value={model.id} title={model.id}>{modelNames.get(model.id)}</option>)}
      </select></label>
      {efforts.length > 0 && <label>Effort <select aria-label="Session effort" value={draft.effort} disabled={busy} onChange={e => { setDraft({ ...draft, effort: e.target.value }); setNotice(""); }}>
        <option value="">{draft.model === session.model ? "Keep current effort" : "Engine chooses effort"}</option>
        {efforts.map(effort => <option key={effort} value={effort}>{effort}</option>)}
      </select></label>}
      <button type="button" className="btn ghost small" disabled={busy || !selected || (!!draft.effort && !efforts.includes(draft.effort))} onClick={() => { void apply(); }}>Apply settings</button>
    </>}
    {notice && <span role="status">{notice}</span>}
  </div>;
}
