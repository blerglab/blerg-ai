import { useEffect, useState } from "react";
import { apiFetch } from "./apiFetch";
import "./LiveDataSettingsSection.css";

type Binding = { binding_id: string; revision: number; digest: string; connection_name: string; tool: string; mode: string; status: string; hash?: string; tool_definition?: unknown };
type View = { unsafe_host_opt_in: boolean; polling_enabled: boolean; bindings: Binding[] };
const warning = "Agents on this machine can reach the polling credentials.";

export default function LiveDataSettingsSection() {
  const [view, setView] = useState<View | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [saved, setSaved] = useState(false);
  async function load() {
    const res = await apiFetch("/api/settings/live-data");
    if (!res.ok) throw new Error("load");
    const next = await res.json() as View;
    if (typeof next.unsafe_host_opt_in !== "boolean" || !Array.isArray(next.bindings)) throw new Error("shape");
    return next;
  }
  useEffect(() => {
    let active = true;
    load().then(next => { if (active) setView(next); }).catch(() => {
      console.warn("live_data_settings:load_failed");
      if (active) setError("Could not load live data settings.");
    });
    return () => { active = false; };
  }, []);
  async function write(path: string, body: object) {
    if (busy) return;
    setBusy(true); setError(""); setSaved(false);
    try {
      const res = await apiFetch(path, { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      if (!res.ok) throw new Error("write");
      setView(await load()); setSaved(true);
    } catch {
      console.warn("live_data_settings:save_failed");
      setError("Could not save live data settings. Your change was not confirmed.");
    } finally { setBusy(false); }
  }
  return <section className="settings-section section" data-testid="live-data-settings">
    <h2 className="settings-heading section-title">Live data</h2>
    <p className="settings-description">Polling is not available yet. These settings do not start calls or approve a binding.</p>
    {error && <p className="field-error" role="alert">{error}</p>}
    {!view && !error && <p role="status">Loading live data settings…</p>}
    {view && <>
      <p className="settings-description" id="live-data-warning">{warning} On machines where agents have administrator or container access, polling stays off unless you enable it here. Turning this off revokes approvals that depend on this permission.</p>
      <label className="settings-field live-data-consent">
        <input className="settings-toggle-control" type="checkbox" checked={view.unsafe_host_opt_in} disabled={busy} aria-describedby="live-data-warning"
          onChange={e => void write("/api/settings/live-data", { unsafe_host_opt_in: e.target.checked, acknowledgement: e.target.checked ? warning : "" })} />
        Allow polling on this machine
      </label>
      {view.bindings.map(binding => <div className="settings-field field live-data-policy" key={binding.binding_id}>
        <label className="field-label" htmlFor={`live-policy-${binding.binding_id}`}>{binding.connection_name} · {binding.tool}</label>
        <p className="field-help">Revision {binding.revision} · {binding.status}. This policy is separate from approval of the exact binding.</p>
        {binding.hash && <p className="field-help live-data-hash">Tool hash: <code>{binding.hash}</code></p>}
        {binding.tool_definition!=null && <details><summary>Tool definition</summary><pre className="live-data-definition">{JSON.stringify(binding.tool_definition,null,2)}</pre></details>}
        <select className="settings-field" id={`live-policy-${binding.binding_id}`} value={binding.mode} disabled={busy}
          onChange={e => void write("/api/settings/live-data/policy", { binding_id: binding.binding_id, revision: binding.revision, digest: binding.digest, mode: e.target.value })}>
          <option value="off">Off</option><option value="propose">Ask first</option><option value="allow">Allow</option>
        </select>
      </div>)}
      {saved && <p className="field-help" role="status">Saved.</p>}
    </>}
  </section>;
}
