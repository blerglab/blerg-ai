// refreshGuard bounds how often a page may bounce to core's /auth/refresh.
//
// Two layers:
//   1. "attempted" — set right before a redirect, cleared only after a token was
//      genuinely obtained (fragment consumed) or a socket opened. Guarantees at most
//      one redirect per page load in the ordinary case.
//   2. A rolling counter — counts redirects in a 120 s window and is NEVER cleared by
//      success. Once MAX_ATTEMPTS is reached, refreshAlreadyAttempted() stays true for
//      the rest of the window even if the token was consumed, which stops the
//      redirect → mint → 401 → redirect loop that occurs when the resource server
//      keeps rejecting freshly minted tokens (clock skew, key mismatch).
// Both live in sessionStorage (per-tab, never the token itself); every access is
// try/catch'd because storage can be unavailable (private mode, blocked site data).
const ATTEMPTED_KEY = "blerg_refresh_attempted";
const COUNT_KEY = "blerg_refresh_attempts"; // JSON: {"n": number, "since": epochMs}
export const MAX_ATTEMPTS = 3;
export const WINDOW_MS = 120_000;

function readCount(now: number): { n: number; since: number } {
  try {
    const raw = sessionStorage.getItem(COUNT_KEY);
    if (!raw) return { n: 0, since: now };
    const v = JSON.parse(raw) as { n?: unknown; since?: unknown };
    if (typeof v.n !== "number" || typeof v.since !== "number") return { n: 0, since: now };
    if (v.since > now) return { n: 0, since: now };
    if (now - v.since > WINDOW_MS) return { n: 0, since: now };
    return { n: v.n, since: v.since };
  } catch {
    return { n: 0, since: now };
  }
}

export function refreshAlreadyAttempted(now: number = Date.now()): boolean {
  if (readCount(now).n >= MAX_ATTEMPTS) return true;
  try {
    return sessionStorage.getItem(ATTEMPTED_KEY) === "1";
  } catch {
    return false;
  }
}

export function markRefreshAttempted(now: number = Date.now()): void {
  const c = readCount(now);
  try {
    sessionStorage.setItem(ATTEMPTED_KEY, "1");
    sessionStorage.setItem(COUNT_KEY, JSON.stringify({ n: c.n + 1, since: c.since }));
  } catch {
    /* storage unavailable: fall back to once-per-load semantics only */
  }
}

// clearRefreshAttempted resets the per-load flag only; the rolling counter is
// deliberately left alone so a success followed by another 401 still counts.
export function clearRefreshAttempted(): void {
  try {
    sessionStorage.removeItem(ATTEMPTED_KEY);
  } catch {
    /* ignore */
  }
}
