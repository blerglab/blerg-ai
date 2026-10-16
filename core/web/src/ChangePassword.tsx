import { useEffect, useState } from "react";
import { apiFetch } from "./apiFetch";
import { getAccessToken, redirectToRefresh } from "./authClient";
import { markRefreshAttempted, refreshAlreadyAttempted } from "./refreshGuard";
import { Wordmark } from "./Shell";

// ChangePassword: the screen a must-change-password account's token (caps = ["password.change"]
// only — see claims.ts's mustChangePassword and core/internal/identity/human_session.go's
// MintHumanAccessToken) is routed to (App.tsx). Submits to POST /auth/password
// (core/internal/api/auth_handlers.go's handleChangePassword), the one endpoint such a token
// can do anything useful at server-side — this page is convenience, not the enforcement.
//
// On success the server revokes the account everywhere and clears the refresh cookie, so there
// is nothing left to refresh here: the user is sent to /login?changed=1 to sign in fresh with
// the new password, exactly like a first-time login.
//
// It is also where the person picks their username: the field is prefilled with the current
// one from GET /api/me (a random string for the bootstrap admin), and only a CHANGED value is
// sent, as provider_subject in the same POST — core renames the account in the same
// transaction as the password change, so a refused username changes nothing.
//
// ?return_to=... is set when GET /auth/refresh (handleRefresh's must-change-password branch)
// diverted the user here on their way to board or runner. It is carried through to /login so the
// fresh sign-in sends them on to where they were originally going instead of stranding them on
// core's /app. Not an open redirect: Login.tsx only ever hands it back to /auth/refresh, which
// re-checks it against BLERG_CORE_ALLOWED_RETURN_ORIGINS server-side.

// postChangeLoginURL builds the /login URL a successful change navigates to, preserving a
// return_to from the current page's query string when there is one. user=1 tells Login.tsx the
// username changed too, so its notice says to use the new one.
export function postChangeLoginURL(search: string, usernameChanged = false): string {
  const returnTo = new URLSearchParams(search).get("return_to");
  const base = usernameChanged ? "/login?changed=1&user=1" : "/login?changed=1";
  return returnTo ? `${base}&return_to=${encodeURIComponent(returnTo)}` : base;
}

// errorTag is core's X-Blerg-Error marker on a refusal ("invalid_credentials", "username"), or
// null when there is none.
function errorTag(res: Response): string | null {
  return res.headers?.get?.("X-Blerg-Error") ?? null;
}

// coreMessage is core's own words from a JSON {"error"} body, or null.
async function coreMessage(res: Response): Promise<string | null> {
  try {
    const msg = ((await res.json()) as { error?: unknown } | null)?.error;
    return typeof msg === "string" && msg.trim() !== "" ? msg.trim().slice(0, 200) : null;
  } catch {
    return null;
  }
}

// refreshHere sends the page through GET /auth/refresh and back to this exact URL (return_to
// and all — handleRefresh's must-change-password branch hands a /change-password return_to
// back unchanged). Guarded like apiFetch's own redirect so a refresh that keeps producing
// tokens the server rejects can't loop; returns false when the guard says stop.
function refreshHere(): boolean {
  if (refreshAlreadyAttempted()) return false;
  markRefreshAttempted();
  redirectToRefresh(location.href);
  return true;
}

export default function ChangePassword() {
  const [username, setUsername] = useState("");
  // The account's current username, from GET /api/me: what "left alone" means.
  const [currentUsername, setCurrentUsername] = useState<string | null>(null);
  const [usernameError, setUsernameError] = useState<string | null>(null);
  const [oldPassword, setOldPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  // Signed out with the one refresh bounce already spent: say so from the first render.
  const [error, setError] = useState<string | null>(() =>
    !getAccessToken() && refreshAlreadyAttempted()
      ? "You're not signed in. Reload the page or sign in again."
      : null,
  );
  const [submitting, setSubmitting] = useState(false);

  // No token in memory (a reload, a bookmarked URL, a tab restored after the fragment was
  // consumed) — go get one before the user types anything, rather than letting the submit
  // fail with a 401 that used to be misreported as a wrong password.
  useEffect(() => {
    if (!getAccessToken()) refreshHere();
  }, []);

  // Prefill the username field with the current one. passthrough401: a stale token here is
  // the submit's problem (it refreshes then); this load only fills a field, and must not
  // navigate the page away on its own. Anything the person already typed wins.
  useEffect(() => {
    if (!getAccessToken()) return;
    let cancelled = false;
    apiFetch("/api/me", undefined, { passthrough401: true })
      .then(async (res) => {
        if (!res.ok) return;
        const subject = ((await res.json()) as { provider_subject?: unknown } | null)?.provider_subject;
        if (cancelled || typeof subject !== "string") return;
        setCurrentUsername(subject);
        setUsername((typed) => (typed === "" ? subject : typed));
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
    setUsernameError(null);
    // Only a changed username is sent; an empty field keeps the current one.
    const newUsername = username !== "" && username !== currentUsername ? username : null;
    if (newPassword !== confirm) {
      setError("New password and confirmation don't match.");
      return;
    }
    setSubmitting(true);
    try {
      // passthrough401: apiFetch's default behavior (redirect the whole page to
      // /auth/refresh on any 401) is wrong here — a 401 from POST /auth/password means "the
      // old password you typed is incorrect" (handleChangePassword), not "your token is
      // stale". Without this opt-out the redirect would fire before this component ever saw
      // the 401: "Current password is incorrect." would never render, the page would
      // navigate away mid-submit, and submitting would be stuck true.
      const res = await apiFetch(
        "/auth/password",
        {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            old_password: oldPassword,
            new_password: newPassword,
            ...(newUsername !== null ? { provider_subject: newUsername } : {}),
          }),
        },
        { passthrough401: true },
      );
      // A refused username (invalid 400, taken 409): core's words, by the username field.
      if (res.status === 409 || (res.status === 400 && errorTag(res) === "username")) {
        setUsernameError((await coreMessage(res)) ?? "That username can't be used.");
        return;
      }
      if (res.status === 400) {
        setError("Password must be at least 12 characters.");
        return;
      }
      if (res.status === 401) {
        // handleChangePassword marks a genuinely wrong current password with X-Blerg-Error.
        // Any other 401 on this route is the middleware rejecting the bearer token itself —
        // most often the 10-minute access token expired while this form sat open — and the
        // fix is a refresh, not a retype. Blaming the password here is exactly the trap a
        // first-time admin fell into.
        if (errorTag(res) === "invalid_credentials") {
          setError("Current password is incorrect.");
          return;
        }
        if (refreshHere()) return;
        setError("Your session expired. Reload the page and try again.");
        return;
      }
      if (!res.ok) {
        setError(`Could not change password (${res.status}).`);
        return;
      }
      // The account was just revoked everywhere (every session, plus already-minted access
      // tokens on other devices) and the refresh cookie cleared — the user signs in again
      // with the new password, same as a first login.
      location.href = postChangeLoginURL(location.search, newUsername !== null);
    } catch {
      setError("Could not reach the server. Please try again.");
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <main className="auth">
      <form className="auth-card" onSubmit={submit}>
        <Wordmark as="span" />
        <h1 className="auth-title">Set a new password</h1>
        <p className="auth-notice">This account must set a new password before doing anything else.</p>
        <label className="field">
          <span className="field-label">Choose your username</span>
          <input
            type="text"
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            maxLength={64}
            aria-invalid={usernameError ? true : undefined}
            aria-describedby="username-help"
            autoFocus
          />
        </label>
        <p className="field-help" id="username-help">
          This is the name you sign in with. Lowercase letters, digits, dots, dashes and underscores.
        </p>
        {usernameError && <p className="field-error" role="alert">{usernameError}</p>}
        <label className="field">
          <span className="field-label">Current password</span>
          <input
            type="password"
            autoComplete="current-password"
            value={oldPassword}
            onChange={(e) => setOldPassword(e.target.value)}
            required
          />
        </label>
        <label className="field">
          <span className="field-label">New password</span>
          <input
            type="password"
            autoComplete="new-password"
            value={newPassword}
            onChange={(e) => setNewPassword(e.target.value)}
            minLength={12}
            required
          />
        </label>
        <p className="field-help">12 to 72 characters.</p>
        <label className="field">
          <span className="field-label">Confirm new password</span>
          <input
            type="password"
            autoComplete="new-password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            minLength={12}
            required
          />
        </label>
        {error && <p className="field-error" role="alert">{error}</p>}
        <button type="submit" className="btn btn-primary" disabled={submitting}>
          {submitting ? "Changing password…" : "Change password"}
        </button>
      </form>
    </main>
  );
}
