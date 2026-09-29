// apiFetch.ts — the one place core's own frontend calls core's gated API from.
//
// Same shape as runner/frontend/src/apiFetch.ts and board/web/src/api.ts
// (Task 18): attach `Authorization: Bearer <token>` when a token is actually
// in memory, and on a 401 send the whole page to /auth/refresh rather than
// handing the caller a dead response it can only render as an error.
//
// core/web had no such wrapper, which is why Settings.tsx used to render a
// bare "Failed to load credential status (401)" with no way out.
//
// Only for calls to core's OWN API (same-origin, /api/...). A genuinely
// external fetch has no business carrying this token.
import { getAccessToken, redirectToRefresh } from "./authClient";
import { refreshAlreadyAttempted, markRefreshAttempted } from "./refreshGuard";

export type ApiFetchOptions = {
  // passthrough401 opts a single call out of the blanket "401 -> redirect to /auth/refresh"
  // behavior below, handing the 401 response back to the caller instead. This exists for
  // exactly one caller today: ChangePassword.tsx's POST /auth/password, where a 401 means
  // "you typed the wrong current password" (handleChangePassword in
  // core/internal/api/auth_handlers.go), NOT "your session/token is stale". Redirecting that
  // to /auth/refresh would silently swallow the real error, bounce the user through a
  // refresh (itself likely to fail — a password-change-only token's refresh session is
  // exactly the kind of thing this flow is revoking), and leave the page's `submitting`
  // state stuck true forever. Every other caller of apiFetch wants the default behavior:
  // core's normal API 401s really do mean "your token needs refreshing."
  passthrough401?: boolean;
};

export function apiFetch(
  url: string,
  init?: RequestInit,
  options?: ApiFetchOptions,
): Promise<Response> {
  const token = getAccessToken();
  // Only attach the header when a token actually exists — an unconditional
  // template literal sends the literal string "Bearer null".
  const headers = token
    ? { ...init?.headers, Authorization: `Bearer ${token}` }
    : { ...init?.headers };

  return fetch(url, { ...init, headers }).then((resp) => {
    if (resp.status === 401 && !options?.passthrough401) {
      // Only navigate to /auth/refresh once per page load (and at most
      // MAX_ATTEMPTS per WINDOW_MS across loads — see refreshGuard.ts). A
      // second consecutive 401 must not re-trigger the redirect, or a refresh
      // that mints a token core's own API then rejects (revocation-cache
      // window, key rotation glitch, clock skew) loops /app ↔ /auth/refresh
      // forever. Same guard as board/web's api.ts and runner's apiFetch.ts.
      if (!refreshAlreadyAttempted()) {
        markRefreshAttempted();
        redirectToRefresh(location.href);
        // The page is navigating away — never hand the 401 back to the caller
        // to be parsed as a real response. The navigation wins the race; this
        // just stops any .then()/.json() chain from running against a body the
        // caller never expected to see.
        return new Promise<Response>(() => {});
      }
      // A refresh was already attempted this page load and we're STILL getting
      // 401s — hand the 401 back so the caller/UI can show an error instead of
      // hanging or looping.
      return resp;
    }
    return resp;
  });
}
