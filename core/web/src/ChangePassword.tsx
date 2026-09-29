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
// ?return_to=... is set when GET /auth/refresh (handleRefresh's must-change-password branch)
// diverted the user here on their way to board or runner. It is carried through to /login so the
// fresh sign-in sends them on to where they were originally going instead of stranding them on
// core's /app. Not an open redirect: Login.tsx only ever hands it back to /auth/refresh, which
// re-checks it against BLERG_CORE_ALLOWED_RETURN_ORIGINS server-side.

// postChangeLoginURL builds the /login URL a successful change navigates to, preserving a
// return_to from the current page's query string when there is one.
export function postChangeLoginURL(search: string): string {
  const returnTo = new URLSearchParams(search).get("return_to");
  return returnTo ? `/login?changed=1&return_to=${encodeURIComponent(returnTo)}` : "/login?changed=1";
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

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(null);
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
          body: JSON.stringify({ old_password: oldPassword, new_password: newPassword }),
        },
        { passthrough401: true },
      );
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
        if (res.headers.get("X-Blerg-Error") === "invalid_credentials") {
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
      location.href = postChangeLoginURL(location.search);
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
          <span className="field-label">Current password</span>
          <input
            type="password"
            autoComplete="current-password"
            value={oldPassword}
            onChange={(e) => setOldPassword(e.target.value)}
            autoFocus
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
