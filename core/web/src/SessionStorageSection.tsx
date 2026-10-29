import { useEffect, useState } from "react";
import { apiFetch } from "./apiFetch";

// Install-wide attachment storage for sessions (#231): what a session's attachments may weigh
// together, and the most an administrator may raise one session to (its "Increase storage").
type Limits = { default_upload_mb: number; max_upload_mb: number };
type View = Limits & { defaults: Limits; ranges: Record<keyof Limits, [number, number]>; step_mb: number; custom: boolean };
const fields: { key: keyof Limits; label: string; help: string }[] = [
  { key: "default_upload_mb", label: "Attachments per session (MB)", help: "What a new session's attachments may weigh together. Files over it wait until space is freed." },
  { key: "max_upload_mb", label: "Most one session can be raised to (MB)", help: "Administrators raise a session's storage from a waiting file, in steps, up to this." },
];

export default function SessionStorageSection() {
  const [view, setView] = useState<View | null>(null);
  const [draft, setDraft] = useState({ default_upload_mb: "", max_upload_mb: "" });
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const take = (v: View) => {
    setView(v);
    setDraft({ default_upload_mb: String(v.default_upload_mb), max_upload_mb: String(v.max_upload_mb) });
  };
  useEffect(() => {
    let cancelled = false;
    apiFetch("/api/settings/session-storage").then(async res => {
      if (!res.ok) throw new Error("load");
      const v = await res.json() as View;
      if (!v.ranges?.default_upload_mb || !v.ranges?.max_upload_mb || !v.defaults || !Number.isInteger(v.default_upload_mb) || !Number.isInteger(v.max_upload_mb)) throw new Error("invalid");
      if (!cancelled) take(v);
    }).catch(() => { if (!cancelled) setError("Could not load the session storage settings."); });
    return () => { cancelled = true; };
  }, []);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (saving) return;
    setSaving(true); setError(""); setSaved(false);
    try {
      const res = await apiFetch("/api/settings/session-storage", {
        method: "PUT", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ default_upload_mb: Number(draft.default_upload_mb), max_upload_mb: Number(draft.max_upload_mb) }),
      });
      if (!res.ok) {
        const reason = (await res.text()).trim();
        setError(reason && !reason.startsWith("<") ? reason.slice(0, 200) : "Could not save the session storage settings.");
        return;
      }
      take(await res.json() as View); setSaved(true);
    } catch { setError("Could not reach the server. Please try again."); }
    finally { setSaving(false); }
  };
  return <section className="settings-section section" data-testid="session-storage">
    <p className="eyebrow">Administration</p>
    <h2 className="settings-heading section-title">Session storage</h2>
    <p className="settings-description">Choose how much people can attach to a session, and how far an administrator can raise one session.</p>
    {error && <p className="field-error" role="alert">{error}</p>}
    {view && <form className="card card-form" onSubmit={submit}>
      {fields.map(f => {
        const id = `session-storage-${f.key}`;
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
      <p className="field-help">Each “Increase storage” adds {view.step_mb} MB. {view.custom ? "Saved changes are recorded with who made them." : "Not saved yet: the runner’s environment value (BLERG_RUNNER_MAX_SESSION_UPLOADS_MB) applies."}</p>
      {saved && <p className="field-help" role="status">Saved. Sessions use the new values within a minute.</p>}
      <div className="card-actions">
        <button type="button" className="settings-action btn" disabled={saving} onClick={() => { setDraft({ default_upload_mb: String(view.defaults.default_upload_mb), max_upload_mb: String(view.defaults.max_upload_mb) }); setSaved(false); }}>Use the defaults</button>
        <button type="submit" className="settings-action btn btn-primary" disabled={saving}>{saving ? "Saving…" : "Save session storage"}</button>
      </div>
    </form>}
  </section>;
}
