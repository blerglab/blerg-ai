import { useEffect, useState } from "react";
import { redirectToRefresh } from "./authClient";
import { Wordmark } from "./Shell";

// Setup: "Create your account", the first-run page. While no account exists, core is in setup
// mode (core/internal/api/setup_handlers.go) and the first person creates the admin here by
// choosing a username and password. GET /auth/provider says whether this browser needs the setup
// code ("setup": "code" — it comes in the setup link, as #code=) or not ("open" —
// the desktop install, reached on localhost). Once an account exists core redirects /setup to
// /login, and this page only shows that it is done.
//
// On success core has already signed this browser in (POST /auth/setup sets the session cookie,
// like POST /auth/login), so the page goes straight to the app through /auth/refresh.

type SetupMode = "open" | "code" | "done";

// setupCodeFrom reads the setup code from a URL fragment ("#code=…"), or "".
export function setupCodeFrom(hash: string): string {
  return new URLSearchParams(hash.replace(/^#/, "")).get("code") ?? "";
}

// setupErrorFor maps POST /auth/setup's refusal (status, X-Blerg-Error tag, core's own words) to
// the field it belongs to and the line to show there.
export function setupErrorFor(
  status: number,
  tag: string | null,
  message: string | null,
): { field: "username" | "password" | "form"; text: string } {
  if (tag === "username") return { field: "username", text: message ?? "That username can't be used." };
  if (tag === "password") return { field: "password", text: "Password must be 12 to 72 characters." };
  if (tag === "code")
    return {
      field: "form",
      text: "This setup link is not valid: it may be wrong, expired or already used. Get a fresh one from the installer.",
    };
  if (tag === "complete" || status === 409) return { field: "form", text: "This Blerg is already set up. Sign in instead." };
  if (status === 429) return { field: "form", text: "Too many attempts — wait a few minutes and try again." };
  return { field: "form", text: `Could not create the account (${status}).` };
}

export default function Setup() {
  const [mode, setMode] = useState<SetupMode | null>(null);
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [usernameError, setUsernameError] = useState<string | null>(null);
  const [passwordError, setPasswordError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  // The code arrives in the URL fragment (/setup#code=…), which no server ever sees. It is read
  // once into state and then cleared from the address bar and history.
  const [code] = useState(() => setupCodeFrom(location.hash));

  useEffect(() => {
    if (setupCodeFrom(location.hash) !== "") {
      history.replaceState(history.state, "", location.pathname + location.search);
    }
  }, []);

  useEffect(() => {
    fetch("/auth/provider")
      .then((r) => r.json())
      .then((p: { setup?: string }) => setMode(p.setup === "open" || p.setup === "code" ? p.setup : "done"))
      .catch(() => setMode("code"));
  }, []);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setUsernameError(null);
    setPasswordError(null);
    if (password !== confirm) {
      setPasswordError("Password and confirmation don't match.");
      return;
    }
    setSubmitting(true);
    try {
      const res = await fetch("/auth/setup", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ code, provider_subject: username, password }),
      });
      if (!res.ok) {
        let message: string | null = null;
        try {
          const m = ((await res.json()) as { error?: unknown } | null)?.error;
          message = typeof m === "string" && m.trim() !== "" ? m.trim().slice(0, 200) : null;
        } catch {
          message = null;
        }
        const { field, text } = setupErrorFor(res.status, res.headers?.get?.("X-Blerg-Error") ?? null, message);
        if (field === "username") setUsernameError(text);
        else if (field === "password") setPasswordError(text);
        else setError(text);
        return;
      }
      redirectToRefresh(`${location.origin}/app`);
    } catch {
      setError("Could not reach the server. Please try again.");
    } finally {
      setSubmitting(false);
    }
  };

  if (mode === null) return null;

  if (mode === "done") {
    return (
      <main className="auth">
        <div className="auth-card">
          <Wordmark as="span" />
          <h1 className="auth-title">Already set up</h1>
          <p className="auth-notice">This Blerg already has an account.</p>
          <a className="btn btn-primary" href="/login">
            Sign in
          </a>
        </div>
      </main>
    );
  }

  if (mode === "code" && code === "") {
    return (
      <main className="auth">
        <div className="auth-card">
          <Wordmark as="span" />
          <h1 className="auth-title">Create your account</h1>
          <p className="auth-notice">
            Open the setup link from the installer's output to create the first account. An operator can print a fresh one
            with <code>blerg-core setup-link</code>.
          </p>
        </div>
      </main>
    );
  }

  return (
    <main className="auth">
      <form className="auth-card" onSubmit={submit}>
        <Wordmark as="span" />
        <h1 className="auth-title">Create your account</h1>
        <p className="auth-notice">You're the first one here. This account is the administrator.</p>
        <label className="field">
          <span className="field-label">Username</span>
          <input
            type="text"
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            maxLength={64}
            aria-invalid={usernameError ? true : undefined}
            aria-describedby="setup-username-help"
            autoFocus
            required
          />
        </label>
        <p className="field-help" id="setup-username-help">
          This is the name you sign in with. Lowercase letters, digits, dots, dashes and underscores.
        </p>
        {usernameError && <p className="field-error" role="alert">{usernameError}</p>}
        <label className="field">
          <span className="field-label">Password</span>
          <input
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            minLength={12}
            required
          />
        </label>
        <p className="field-help">12 to 72 characters.</p>
        <label className="field">
          <span className="field-label">Confirm password</span>
          <input
            type="password"
            autoComplete="new-password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            minLength={12}
            required
          />
        </label>
        {passwordError && <p className="field-error" role="alert">{passwordError}</p>}
        {error && <p className="field-error" role="alert">{error}</p>}
        <button type="submit" className="btn btn-primary" disabled={submitting}>
          {submitting ? "Creating account…" : "Create account"}
        </button>
      </form>
    </main>
  );
}
