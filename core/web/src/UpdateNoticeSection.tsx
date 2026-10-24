import { useEffect, useState } from "react";
import { apiFetch } from "./apiFetch";

export default function UpdateNoticeSection() {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  useEffect(() => {
    let cancelled = false;
    apiFetch("/api/settings/update-notice").then(async r => {
      if (!r.ok) throw new Error();
      const value = await r.json();
      if (typeof value.enabled !== "boolean") throw new Error();
      if (!cancelled) setEnabled(value.enabled);
    }).catch(() => { if (!cancelled) setError("Could not load update notice settings."); });
    return () => { cancelled = true; };
  }, []);
  return <section className="section"><h2 className="section-title">Release notices</h2>
    <p className="section-help">Check GitHub for public releases every six hours and show upgrade notices across this install. Turning this off stops the network check. Board and runner cache notices for up to ten minutes. After that interval, reload open pages; expired reads wait up to five seconds for core and show no notice on timeout.</p>
    {enabled !== null && <form onSubmit={async e => {
      e.preventDefault(); setSaving(true); setError(""); setSaved(false);
      try {
        const r = await apiFetch("/api/settings/update-notice", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled }) });
        if (!r.ok) throw new Error();
        setSaved(true);
      } catch { setError("Could not save update notice settings."); }
      finally { setSaving(false); }
    }}>
      <label><input type="checkbox" checked={enabled} disabled={saving} onChange={e => { setEnabled(e.target.checked); setSaved(false); }} /> Check for newer releases</label>
      <div className="card-actions"><button className="btn" disabled={saving}>Save release notices</button></div>
    </form>}
    {saved && <p role="status">Release notice settings saved.</p>}
    {error && <p role="alert">{error}</p>}
  </section>;
}
