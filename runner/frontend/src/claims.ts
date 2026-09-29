// claims.ts — best-effort, UNVERIFIED decode of a JWT access token's `exp`
// claim, for UX only. This is NOT an authoritative auth decision: the token
// is opaque to this frontend (no signature check, no key), it just lets
// ws.ts tell a dead-on-open socket close apart from an unreachable server —
// "the token we already had was expired" vs. "the server refused a live
// token" — without a round trip. The server is, and remains, the only party
// that can actually verify the token; every gated endpoint still enforces
// this for real.
export function tokenExpired(token: string): boolean {
  try {
    const parts = token.split('.')
    if (parts.length < 2) return true
    const base64 = parts[1].replace(/-/g, '+').replace(/_/g, '/')
    const payload = JSON.parse(atob(base64)) as { exp?: unknown }
    if (typeof payload.exp !== 'number') return false
    return Date.now() >= payload.exp * 1000
  } catch {
    // Can't decode it -> can't trust it. Treat as expired so callers fall
    // back to a fresh refresh rather than looping on a token that will
    // never work.
    return true
  }
}
