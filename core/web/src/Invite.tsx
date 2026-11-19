import { useEffect, useState } from "react";
import { redirectToRefresh } from "./authClient";
import { Wordmark } from "./Shell";
export default function Invite() {
    const [code] = useState(() => new URLSearchParams(location.hash.slice(1)).get('code') ?? '');
    const [username, setUsername] = useState('');
    const [password, setPassword] = useState('');
    const [confirm, setConfirm] = useState('');
    const [busy, setBusy] = useState(false);
    const [error, setError] = useState('');
    useEffect(() => { history.replaceState(history.state, '', location.pathname + location.search); }, []);
    async function submit(e: React.FormEvent) {
        e.preventDefault();
        if (password !== confirm) {
            setError("Passwords don't match.");
            return;
        }
        setBusy(true);
        setError('');
        try {
            const res = await fetch('/auth/invite', { method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ code, provider_subject: username, password }) });
            if (!res.ok) {
                setError(res.status === 429 ? 'Too many attempts. Try again later.' : 'Could not accept the invite. Check the username, or ask your administrator for help.');
                return;
            }
            redirectToRefresh(`${location.origin}/settings?setup=desktop`);
        }
        catch {
            setError('Could not reach the server. Try again.');
        }
        finally {
            setBusy(false);
        }
    }
    return <main className="auth"><form className="auth-card" onSubmit={e => void submit(e)}><Wordmark as="span"/><h1 className="auth-title">Welcome to Blerg</h1><p className="auth-notice">Choose your sign-in details, then set up your desktop.</p>
 <label className="field"><span className="field-label">Choose your username</span><input autoComplete="username" autoCapitalize="none" required maxLength={64} value={username} onChange={e => setUsername(e.target.value)}/></label>
 <label className="field"><span className="field-label">Password</span><input type="password" autoComplete="new-password" required minLength={12} maxLength={72} value={password} onChange={e => setPassword(e.target.value)}/></label>
 <label className="field"><span className="field-label">Confirm password</span><input type="password" autoComplete="new-password" required value={confirm} onChange={e => setConfirm(e.target.value)}/></label>
 <p className="field-help">Use 12 to 72 characters for your password.</p>{!code && <p className="field-error">Open the invite link your administrator sent you.</p>}{error && <p className="field-error" role="alert">{error}</p>}
 <button className="btn btn-primary" disabled={busy || !code}>Set up your desktop</button></form></main>;
}
