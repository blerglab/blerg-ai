import { useEffect, useState } from "react";
import { redirectToRefresh } from "./authClient";
import { Wordmark } from "./Shell";

// Login: provider-aware sign-in page. On mount it asks GET /auth/provider which mechanism is
// active (BLERG_CORE_AUTH_PROVIDER server-side) and renders accordingly: "local" gets the
// username/password form posting to POST /auth/login (relative, same-origin — core serves this
// page itself, so no cross-origin fetch or CORS setup is needed); "github"/"oidc" get a single
// link into GET /auth/start, which redirects the browser to the IdP.
//
// This is the first thing a signed-out visitor sees: "/" and /app both route here (via
// GET /auth/refresh) when there is no live session, and board/runner bounce here the same
// way. So the screen carries the wordmark and a one-line reason when there is one — it IS the
// front door, not a form hidden behind a "Sign in" button.
type ProviderInfo = { id: string };

// noticeFor turns the query string GET /auth/refresh (writeSignInRequired) and
// ChangePassword.tsx leave behind into the single line shown above the form, or null.
export function noticeFor(search: string): string | null {
  const q = new URLSearchParams(search);
  if (q.get("changed") === "1") return "Password changed — sign in again.";
  if (q.get("reason") === "expired") return "Your session expired or was signed out everywhere. Sign in again.";
  return null;
}

export default function Login() {
  const [provider, setProvider] = useState<ProviderInfo | null>(null);
  const [providerSubject, setProviderSubject] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    fetch("/auth/provider")
      .then((r) => r.json())
      .then(setProvider)
      .catch(() => setProvider({ id: "local" }));
  }, []);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      // POST /auth/login (core/internal/authprovider/local.go's Callback) reads the submitted
      // credentials via r.ParseForm()/r.FormValue — a form-urlencoded body, not JSON.
      const res = await fetch("/auth/login", {
        method: "POST",
        body: new URLSearchParams({ provider_subject: providerSubject, password }),
      });
      if (!res.ok) {
        setError(
          res.status === 401
            ? "Incorrect username or password."
            : res.status === 429
              ? "Too many attempts — wait a few minutes and try again."
              : `Login failed (${res.status}).`,
        );
        return;
      }
      // A successful login sets the refresh cookie server-side (Set-Cookie on the response
      // above) but doesn't itself hand back an access token — that's GET /auth/refresh's job.
      // redirectToRefresh sends the whole page there; it redirects back to returnTo with a
      // fresh #access_token=... fragment, which main.tsx's boot path consumes.
      //
      // ?return_to=... is set when the user was bounced here from GET /auth/refresh
      // (core/internal/api/auth_handlers.go's writeSignInRequired) on their way to board,
      // runner, or core's own /app — send them on to where they were going rather than
      // stranding them here. Not an open redirect: the value is handed straight back to
      // /auth/refresh, which re-checks it against BLERG_CORE_ALLOWED_RETURN_ORIGINS server-side.
      const returnTo = new URLSearchParams(location.search).get("return_to");
      redirectToRefresh(returnTo || `${location.origin}/app`);
    } catch {
      setError("Could not reach the server. Please try again.");
    } finally {
      setSubmitting(false);
    }
  };

  if (provider === null) {
    return null;
  }

  const notice = noticeFor(location.search);

  if (provider.id !== "local") {
    return (
      <main className="auth">
        <div className="auth-card">
          <Wordmark as="span" />
          <h1 className="auth-title">Sign in</h1>
          {notice && <p className="auth-notice">{notice}</p>}
          <a className="btn btn-primary" href="/auth/start">
            Sign in with {provider.id === "github" ? "GitHub" : "SSO"}
          </a>
        </div>
      </main>
    );
  }

  return (
    <main className="auth">
      <form className="auth-card" onSubmit={submit}>
        <Wordmark as="span" />
        <h1 className="auth-title">Sign in</h1>
        {notice && <p className="auth-notice">{notice}</p>}
        <label className="field">
          <span className="field-label">Username</span>
          <input
            type="text"
            name="provider_subject"
            autoComplete="username"
            value={providerSubject}
            onChange={(e) => setProviderSubject(e.target.value)}
            autoFocus
            required
          />
        </label>
        <label className="field">
          <span className="field-label">Password</span>
          <input
            type="password"
            name="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
          />
        </label>
        {error && <p className="field-error" role="alert">{error}</p>}
        <button type="submit" className="btn btn-primary" disabled={submitting}>
          {submitting ? "Signing in…" : "Sign in"}
        </button>
        <p className="auth-hint">
          First time here? Your username is the provider_subject printed next to BLERG_BOOTSTRAP_ADMIN_PASSWORD in core's log (first boot only) — or whatever your admin created for you.
        </p>
      </form>
    </main>
  );
}
