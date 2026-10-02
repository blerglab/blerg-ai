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

/**
 * apiUpload sends a file like apiFetch does (same bearer token, same refresh-on-401 rule) but
 * reports upload progress, which fetch cannot. It resolves to an ordinary Response.
 */
export function apiUpload(
  url: string,
  init: { method?: string; headers?: Record<string, string>; body: Blob; signal?: AbortSignal },
  onProgress?: (loaded: number, total: number) => void,
): Promise<Response> {
  return new Promise<Response>((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open(init.method ?? 'POST', url)
    const token = getAccessToken()
    for (const [k, v] of Object.entries(init.headers ?? {})) xhr.setRequestHeader(k, v)
    if (token) xhr.setRequestHeader('Authorization', `Bearer ${token}`)
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) onProgress?.(e.loaded, e.total)
    }
    const onAbort = () => xhr.abort()
    xhr.onload = () => {
      init.signal?.removeEventListener('abort', onAbort)
      if (xhr.status === 401 && !refreshAlreadyAttempted()) {
        markRefreshAttempted()
        redirectToRefresh(location.href)
        return // the page is navigating away; never resolve a 401 the caller would parse
      }
      resolve(new Response(xhr.status === 204 ? null : xhr.responseText, {
        status: xhr.status,
        headers: { 'Content-Type': xhr.getResponseHeader('Content-Type') ?? 'application/json' },
      }))
    }
    xhr.onerror = () => {
      init.signal?.removeEventListener('abort', onAbort)
      reject(new TypeError('Network error'))
    }
    xhr.onabort = () => {
      init.signal?.removeEventListener('abort', onAbort)
      reject(new DOMException('The upload was cancelled.', 'AbortError'))
    }
    if (init.signal?.aborted) {
      xhr.abort()
      return
    }
    init.signal?.addEventListener('abort', onAbort)
    xhr.send(init.body)
  })
}
