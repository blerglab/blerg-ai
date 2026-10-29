import { useEffect, useState } from "react";
import { apiFetch } from "./apiFetch";

type Marketplace = { source: string; enabled: boolean };
type Policy = { revision: number; marketplaces: Marketplace[]; any_marketplace: boolean; updated_by?: string; updated_at?: string };
const DEFAULTS = ["anthropics/claude-plugins-official", "codex:openai-curated", "codex:openai-curated-remote"];
export function validMarketplace(source: string): boolean {
  if (/^codex:[a-z0-9][a-z0-9._-]{0,99}$/.test(source)) return true;
  if (!/^[A-Za-z0-9._-]{1,39}\/[A-Za-z0-9._-]{1,100}$/.test(source)) return false;
  const [owner, repo] = source.split("/");
  return !/^[.-]/.test(owner) && repo !== "." && repo !== "..";
}

export default function PluginMarketplacesSection() {
  const [policy, setPolicy] = useState<Policy | null>(null);
  const [source, setSource] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [status, setStatus] = useState("");
  useEffect(() => {
    let active = true;
    void apiFetch("/api/settings/plugin-marketplaces").then(async r => {
      if (!r.ok) throw new Error();
      const p = await r.json() as Policy;
      if (!Array.isArray(p.marketplaces)) throw new Error();
      if (active) setPolicy(p);
    }).catch(() => { if (active) setError("Plugin marketplace policy is unavailable. Reload to try again."); });
    return () => { active = false; };
  }, []);
  async function save(marketplaces: Marketplace[], anyMarketplace = policy?.any_marketplace ?? false) {
    if (!policy || busy) return;
    setBusy(true); setError(""); setStatus("");
    try {
      const r = await apiFetch("/api/settings/plugin-marketplaces", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ revision: policy.revision, marketplaces, any_marketplace: anyMarketplace }) });
      if (!r.ok) throw new Error(r.status === 409 ? "The marketplace policy changed. Reload before trying again." : "Plugin marketplace policy could not be saved.");
      setPolicy(await r.json() as Policy); setSource("");
      setStatus("Saved. Connected runners and desktops receive this policy within a few seconds.");
      window.dispatchEvent(new Event("plugin-marketplaces-changed"));
    } catch (e) { setError(e instanceof Error ? e.message : "Plugin marketplace policy could not be saved."); }
    finally { setBusy(false); }
  }
  const draft = source.trim();
  const duplicate = policy?.marketplaces.some(m => m.source.toLowerCase() === draft.toLowerCase());
  const invalid = draft && (!validMarketplace(draft) || duplicate);
  return <section className="settings-section section" aria-label="Plugin marketplaces" data-testid="plugin-marketplaces">
    <p className="eyebrow">Administration</p>
    <h2 className="settings-heading section-title">Plugin marketplaces</h2>
    <p className="settings-description">Allow trusted sources for plugins that run code in sessions. Changes apply across runners and desktops. Turning a source off keeps existing plugins and flags them as disallowed.</p>
    {!policy && !error && <p>Loading marketplace policy…</p>}
    {policy && <>
      <ul className="marketplace-policy-list">
        {policy.marketplaces.map(m => <li key={m.source}>
          <span className="marketplace-policy-source mono">{m.source}{DEFAULTS.includes(m.source) && <small>Default</small>}</span>
          <button className="settings-toggle-control" role="switch" type="button" aria-label={`Allow ${m.source}`} aria-checked={m.enabled} disabled={busy || policy.any_marketplace} onClick={() => { void save(policy.marketplaces.map(p => p.source === m.source ? { ...p, enabled: !p.enabled } : p)); }} />
          {!DEFAULTS.includes(m.source) && <button className="settings-action btn btn-quiet" type="button" aria-label={`Remove ${m.source}`} disabled={busy} onClick={() => { void save(policy.marketplaces.filter(p => p.source !== m.source)); }}>Remove</button>}
        </li>)}
      </ul>
      <div className="settings-toggle"><span id="any-marketplace-label">Allow any valid marketplace</span><button type="button" className="settings-toggle-control" role="switch" aria-labelledby="any-marketplace-label" aria-checked={policy.any_marketplace} disabled={busy} onClick={() => { void save(policy.marketplaces, !policy.any_marketplace); }} /></div>
      {policy.any_marketplace && <p className="field-help">All valid sources are allowed. Turn this off to enforce the individual switches.</p>}
      <form className="card-form" onSubmit={e => { e.preventDefault(); if (validMarketplace(draft) && !duplicate) void save([...policy.marketplaces, { source: draft, enabled: true }]); }}>
        <label className="settings-field field" htmlFor="add-plugin-marketplace"><span className="field-label">Add marketplace (owner/repo or codex:&lt;marketplace&gt;)</span><input id="add-plugin-marketplace" value={source} onChange={e => setSource(e.target.value)} maxLength={140} spellCheck={false} autoCapitalize="none" aria-invalid={!!invalid} aria-describedby={invalid ? "marketplace-validation" : undefined} /></label>
        {invalid && <p id="marketplace-validation" className="field-help">{duplicate ? "This marketplace is already listed." : "Enter a GitHub owner/repo or codex:<marketplace>. URLs and paths are not accepted."}</p>}
        <div className="card-actions"><button className="settings-action btn btn-primary" type="submit" disabled={busy || !draft || !!invalid || policy.marketplaces.length >= 100}>Add marketplace</button></div>
      </form>
      <p className="field-help">Official Anthropic and OpenAI sources ship enabled. Every saved change records who changed it and when. Environment settings initialize a new policy and provide fallback if Core cannot be reached.</p>
    </>}
    {status && <p role="status">{status}</p>}
    {error && <p role="alert">{error}</p>}
  </section>;
}
