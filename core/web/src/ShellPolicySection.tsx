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
  return <section className="section" aria-label="Owner shells">
    <h2>Owner shells</h2>
    <p>Allow session owners to open shells. Host shells also require consent naming the machine and OS account.</p>
    <button type="button" role="switch" aria-checked={policy?.enabled ?? false} disabled={!policy || busy} onClick={() => { void toggle(); }}>Shells {policy?.enabled ? "enabled" : "disabled"}</button>
    <p>Disabling closes connected shells. Disconnected runtimes lose their grants within 30 seconds. Enabling requires opening new shells.</p>
    {error && <p role="alert">{error}</p>}
  </section>;
}
