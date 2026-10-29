import { useEffect, useState } from "react";
import { apiFetch } from "./apiFetch";

type Policy = { version: number; revision: number; enabled: boolean };
export default function ShellPolicySection() {
  const [policy, setPolicy] = useState<Policy | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    void apiFetch("/api/settings/shell-policy").then(async r => {
      if (!r.ok) throw new Error();
      const p = await r.json() as Policy;
      if (active) setPolicy(p);
    }).catch(() => { if (active) setError("Shell policy is unavailable."); });
    return () => { active = false; };
  }, []);
  async function toggle() {
    if (!policy || busy) return;
    setBusy(true); setError("");
    try {
      const r = await apiFetch("/api/settings/shell-policy", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ enabled: !policy.enabled }) });
      if (!r.ok) throw new Error();
      setPolicy(await r.json() as Policy);
    } catch { setError("Shell policy could not be saved."); }
    finally { setBusy(false); }
  }
  return <section className="settings-section section" aria-label="Owner shells">
    <h2 className="settings-heading">Owner shells</h2>
    <p className="settings-description">Let session owners open a shell to work on their sessions.</p>
    <div className="settings-toggle"><span id="owner-shells-label">Allow owner shells</span>
      <button className="settings-toggle-control" type="button" role="switch" aria-labelledby="owner-shells-label" aria-checked={policy?.enabled ?? false} disabled={!policy || busy} onClick={() => { void toggle(); }} />
    </div>
    {error && <p role="alert">{error}</p>}
  </section>;
}
