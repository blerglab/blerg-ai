import { useEffect, useState } from "react";
import { apiFetch } from "./apiFetch";

type Window = { enabled: boolean; start: string; end: string };
type Clock = { timezone: string; quiet_hours?: Window };
const timeZones = ["UTC", ...Intl.supportedValuesOf("timeZone")];
const defaultWindow: Window = { enabled: false, start: "22:00", end: "08:00" };

export default function PersonTimeSettings() {
  const [clock, setClock] = useState<Clock | null>(null);
  const [zone, setZone] = useState("");
  const [quiet, setQuiet] = useState(defaultWindow);
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        let res = await apiFetch("/api/me/time");
        if (!res.ok) throw new Error();
        if (cancelled) return;
        let value = await res.json() as Clock;
        if (!value.timezone) {
          res = await apiFetch("/api/me/time", {
            method: "PUT", headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ timezone: Intl.DateTimeFormat().resolvedOptions().timeZone, initialize: true }),
          });
          if (!res.ok) throw new Error();
          value = await res.json() as Clock;
        }
        if (!cancelled) {
          setClock(value); setZone(value.timezone); setQuiet(value.quiet_hours ?? defaultWindow);
        }
      } catch {
        if (!cancelled) setError("Could not load your time settings. Try reopening Settings.");
      }
    })();
    return () => { cancelled = true; };
  }, []);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    if (!timeZones.includes(zone)) return;
    setSaving(true); setError(""); setSaved(false);
    try {
      const res = await apiFetch("/api/me/time", {
        method: "PUT", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ timezone: zone, quiet_hours: quiet }),
      });
      if (!res.ok) {
        setError("Could not save. Check the IANA time zone and use different start and end times.");
        return;
      }
      const value = await res.json() as Clock;
      setClock(value); setZone(value.timezone); setQuiet(value.quiet_hours ?? defaultWindow); setSaved(true);
    } catch {
      setError("Could not reach the server. Please try again.");
    } finally { setSaving(false); }
  }

  function changeQuiet(value: Partial<Window>) { setQuiet({ ...quiet, ...value }); setSaved(false); }
  const previous = clock?.quiet_hours ?? defaultWindow;
  const unchanged = clock && zone === clock.timezone && quiet.enabled === previous.enabled && quiet.start === previous.start && quiet.end === previous.end;
  return <form onSubmit={save} className="card" data-testid="person-time-settings">
    <label className="settings-field field" htmlFor="person-timezone">
      <span className="field-label">Time zone</span>
      <input className="settings-field" id="person-timezone" value={zone}
        onChange={e => { setZone(e.target.value); setSaved(false); }} disabled={!clock || saving}
        required list="person-timezones" autoComplete="off" maxLength={128} placeholder="Search for a location" aria-describedby="person-timezone-help"
        style={{ width: "min(100%, 22rem)" }} />
    </label>
    <datalist id="person-timezones">{timeZones.map(value => <option key={value} value={value} />)}</datalist>
    <p className="field-help" id="person-timezone-help">Search for a location, such as America/New_York, or choose UTC. Defaults to your browser’s zone; agents use it to plan around your local time.</p>
    <label className="settings-field">
      <input className="settings-toggle-control" type="checkbox" checked={quiet.enabled}
        disabled={!clock || saving} onChange={e => changeQuiet({ enabled: e.target.checked })} /> Quiet hours
    </label>
    <p className="field-help">Hold push notifications during this window. Urgent questions and destructive approvals still notify you. Questions remain in Needs you.</p>
    {quiet.enabled && <div className="card-actions" style={{ flexWrap: "wrap" }}>
      <label className="settings-field field" htmlFor="quiet-start"><span className="field-label">From</span>
        <input className="settings-field" id="quiet-start" type="time" value={quiet.start} required
          disabled={saving} onChange={e => changeQuiet({ start: e.target.value })} style={{ width: "9rem" }} />
      </label>
      <label className="settings-field field" htmlFor="quiet-end"><span className="field-label">Until</span>
        <input className="settings-field" id="quiet-end" type="time" value={quiet.end} required
          disabled={saving} onChange={e => changeQuiet({ end: e.target.value })} style={{ width: "9rem" }} />
      </label>
      <p className="field-help">Every day in {zone}.</p>
    </div>}
    <div className="card-actions">
      <button className="settings-action btn btn-primary" type="submit" disabled={!clock || saving || !timeZones.includes(zone) || !!unchanged || (quiet.enabled && quiet.start === quiet.end)}>
        {saving ? "Saving…" : "Save time settings"}
      </button>
    </div>
    {saved && <p className="field-help" role="status">Time settings saved.</p>}
    {error && <p className="field-error" role="alert">{error}</p>}
  </form>;
}
