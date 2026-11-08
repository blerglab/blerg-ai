import { useEffect, useState } from "react";
import { apiFetch } from "./apiFetch";
type Person = {
    id: string;
    name: string;
    email: string;
    username: string;
    role: string;
    pending: boolean;
    disabled: boolean;
};
export default function PeopleSection() {
    const [people, setPeople] = useState<Person[]>([]);
    const [name, setName] = useState("");
    const [email, setEmail] = useState("");
    const [link, setLink] = useState("");
    const [error, setError] = useState("");
    const [busy, setBusy] = useState(false);
    const [confirm, setConfirm] = useState("");
    async function load() { const res = await apiFetch('/api/people'); if (!res.ok)
        throw new Error(); setPeople((await res.json()).people); }
    useEffect(() => { let cancelled = false; apiFetch('/api/people').then(async (res) => { if (!res.ok)
        throw new Error(); const body = await res.json(); if (!cancelled)
        setPeople(body.people); }).catch(() => { if (!cancelled)
        setError('Could not load people.'); }); return () => { cancelled = true; }; }, []);
    async function invite(e: React.FormEvent) {
        e.preventDefault();
        setBusy(true);
        setError('');
        setLink('');
        try {
            const res = await apiFetch('/api/people', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ name, email }) });
            if (!res.ok)
                throw new Error();
            const body = await res.json();
            setLink(`${location.origin}/invite#code=${encodeURIComponent(body.code)}`);
            setName('');
            setEmail('');
            await load();
        }
        catch {
            setError('Could not add this person. Check the name and email, then try again.');
        }
        finally {
            setBusy(false);
        }
    }
    async function disable(id: string) { setBusy(true); setError(''); try {
        const res = await apiFetch(`/api/people/${id}/disable`, { method: 'POST' });
        if (!res.ok)
            throw new Error();
        setConfirm('');
        await load();
    }
    catch {
        await load().catch(() => {});
        setError('Could not finish this action. Try again. If already disabled, select Disconnect desktops to retry.');
    }
    finally {
        setBusy(false);
    } }
    return <section className="settings-section section people-section" aria-labelledby="people-heading">
  <p className="eyebrow">Admin</p><h2 id="people-heading" className="settings-heading section-title">People</h2>
  <p className="settings-description">Invite someone to this Blerg. New people are members, with no administrator access.</p>
  <form className="card card-form" onSubmit={e => void invite(e)}>
   <label className="settings-field field"><span className="field-label">Name</span><input className="settings-field" required maxLength={200} value={name} onChange={e => setName(e.target.value)}/></label>
   <label className="settings-field field"><span className="field-label">Email</span><input className="settings-field" required type="email" maxLength={254} value={email} onChange={e => setEmail(e.target.value)}/></label>
   <div className="card-actions"><button className="settings-action btn btn-primary" disabled={busy}>Add a person</button></div>
  </form>
  {error && <p role="alert" className="field-error">{error}</p>}
  {link && <div className="card"><p className="card-help">Send this link privately. It works once and expires in seven days. It will not be shown again.</p>
   <label className="settings-field field"><span className="field-label">Invite link</span><input className="settings-field" readOnly value={link} onFocus={e => e.target.select()}/></label>
   <div className="card-actions"><button className="settings-action btn" onClick={() => void navigator.clipboard.writeText(link).catch(() => setError('Select and copy the invite link.'))}>Copy invite link</button><button className="settings-action btn btn-quiet" onClick={() => setLink('')}>Done</button></div>
  </div>}
  <ul className="settings-list">{people.map(p => <li key={p.id} className="card"><h3 className="settings-heading card-title">{p.name || p.username}</h3>
   <p className="card-help">{p.email} · {p.disabled ? 'Disabled' : p.pending ? 'Invited' : p.role === 'admin' ? 'Administrator' : 'Member'}</p>
   {p.disabled && <div className="card-actions"><button className="settings-action btn btn-quiet" disabled={busy} onClick={() => void disable(p.id)}>Disconnect desktops</button></div>}
   {!p.disabled && (confirm === p.id ? <div><p className="card-help">Disable this person? Their sign-ins, tokens, enrollment codes, and desktop credentials will be revoked. Their work is kept.</p><div className="card-actions"><button className="settings-action btn btn-danger" disabled={busy} onClick={() => void disable(p.id)}>Disable person</button><button className="settings-action btn btn-quiet" onClick={() => setConfirm('')}>Cancel</button></div></div> : <div className="card-actions"><button className="settings-action btn btn-quiet" disabled={busy} onClick={() => setConfirm(p.id)}>Disable</button></div>)}
  </li>)}</ul>
 </section>;
}
