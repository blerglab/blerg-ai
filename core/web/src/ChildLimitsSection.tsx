import { useEffect, useState } from "react";
import { apiFetch } from "./apiFetch";

type Limits = { parallel: number; starts_per_hour: number };
type View = Limits & { defaults: Limits; ranges: Record<keyof Limits, [number, number]> };
const fields: { key: keyof Limits; label: string; help: string }[] = [
  { key: "parallel", label: "Child sessions at once, per parent", help: "A parent must stop a child before starting another at this limit." },
  { key: "starts_per_hour", label: "Child starts per rolling hour, per parent", help: "Approval requests reserve rate budget too. Refusals say when the next start is possible." },
];

export default function ChildLimitsSection() {
  const [view, setView] = useState<View | null>(null);
  const [draft, setDraft] = useState({ parallel: "", starts_per_hour: "" });
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const take = (v: View) => {
    setView(v);
    setDraft({ parallel: String(v.parallel), starts_per_hour: String(v.starts_per_hour) });
  };
  useEffect(() => {
    let cancelled = false;
    apiFetch("/api/settings/child-limits").then(async res => {
      if (!res.ok) throw new Error("Could not load the child start limits.");
      const v = await res.json() as View;
      if (!v.ranges?.parallel || !v.ranges?.starts_per_hour || !v.defaults || !Number.isInteger(v.parallel) || !Number.isInteger(v.starts_per_hour)) throw new Error("Invalid limits");
      if (!cancelled) take(v);
    }).catch(() => { if (!cancelled) setError("Could not load the child start limits."); });
    return () => { cancelled = true; };
  }, []);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (saving) return;
    setSaving(true); setError(""); setSaved(false);
    try {
      const res = await apiFetch("/api/settings/child-limits", {
        method: "PUT", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ parallel: Number(draft.parallel), starts_per_hour: Number(draft.starts_per_hour) }),
      });
      if (!res.ok) {
        const reason = (await res.text()).trim();
        setError(reason && !reason.startsWith("<") ? reason.slice(0, 200) : "Could not save the child start limits.");
        return;
      }
      take(await res.json() as View); setSaved(true);
    } catch { setError("Could not reach the server. Please try again."); }
    finally { setSaving(false); }
  };
  return <section className="settings-section section" data-testid="child-limits">
    <p className="eyebrow">Administration</p>
    <h2 className="settings-heading section-title">Child session limits</h2>
    <p className="settings-description">Choose how many sessions an agent can start and run at once.</p>
    {error && <p className="field-error" role="alert">{error}</p>}
    {view && <form className="card card-form" onSubmit={submit}>
      {fields.map(f => {
        const id = `child-limit-${f.key}`;
        const [min, max] = view.ranges[f.key];
        return <div key={f.key}>
          <label className="settings-field field" htmlFor={id}><span className="field-label">{f.label}</span>
            <input className="settings-field" id={id} type="number" inputMode="numeric" min={min} max={max} step={1} required disabled={saving}
              value={draft[f.key]} onChange={e => { setDraft({ ...draft, [f.key]: e.target.value }); setSaved(false); }}
              aria-describedby={`${id}-help`} />
          </label>
          <p className="field-help" id={`${id}-help`}>{f.help} Default {view.defaults[f.key]}; {min} to {max}.</p>
        </div>;
      })}
      {saved && <p className="field-help" role="status">Saved. The new limits apply at the next start request.</p>}
      <div className="card-actions">
        <button type="button" className="settings-action btn" disabled={saving} onClick={() => { setDraft({ parallel: String(view.defaults.parallel), starts_per_hour: String(view.defaults.starts_per_hour) }); setSaved(false); }}>Use the defaults</button>
        <button type="submit" className="settings-action btn btn-primary" disabled={saving}>{saving ? "Saving…" : "Save child limits"}</button>
      </div>
    </form>}
  </section>;
}
