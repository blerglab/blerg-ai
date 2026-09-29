// apiFetch.ts — the one place runner's frontend calls its own gated API from.
//
// Task 7 put every one of runner's browser-facing endpoints behind a bearer
// token; runner has no single central fetch wrapper (unlike board/web's
// api.ts), so this small helper is the seam every call site routes through
// instead: it attaches `Authorization: Bearer <token>` (via authClient.ts,
// copied verbatim from core/web — see Task 18) and, on a 401, sends the
// whole page to core's /auth/refresh to silently renew the session rather
// than handing the caller a failed response to improvise with.
//
// Only for calls to runner's OWN API (same-origin, /api/...). A genuinely
// external fetch has no business carrying this token and should keep using
// the bare global fetch.
import { getAccessToken, redirectToRefresh } from './authClient'
import { refreshAlreadyAttempted, markRefreshAttempted } from './refreshGuard'

export function apiFetch(url: string, init?: RequestInit): Promise<Response> {
  const token = getAccessToken()
  // Only attach the header when a token actually exists — an unconditional
  // template literal sends the literal string "Bearer null", which the server
  // then has to reject as a malformed token rather than as a missing one.
  const headers = token
    ? { ...init?.headers, Authorization: `Bearer ${token}` }
    : { ...init?.headers }
  return fetch(url, { ...init, headers }).then((resp) => {
    if (resp.status === 401) {
      if (!refreshAlreadyAttempted()) {
        markRefreshAttempted()
        redirectToRefresh(location.href)
        // The page is navigating away — never hand the 401 back to the
        // caller to be parsed as a real response. The navigation above wins
        // the race; this just stops any .then()/.json() chain from running
        // against a body callers never expected to see.
        return new Promise<Response>(() => {})
      }
      // A refresh was already attempted once this page load and we're STILL
      // getting 401s — refreshing again would just loop forever (e.g. core
      // itself is down, or the session is genuinely revoked). Hand the 401
      // back so the caller/UI can show an error instead of hanging.
      return resp
    }
    return resp
  })
}
