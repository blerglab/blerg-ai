// refreshGuard bounds how often a page may bounce to core's /auth/refresh.
// Kept textually identical in runner/frontend/src/refreshGuard.ts and board/web/src/refreshGuard.ts.
//
// Two layers:
//   1. "attempted" — set right before a redirect, cleared after a token was genuinely
//      obtained (fragment consumed) or a socket opened, AND at the start of every page
//      load except the one return leg from /auth/refresh that came back without a token
//      (beginPageLoad below). So it means "once per page load": a flag left by an earlier
//      load (a bounce that failed during a restart, say) can't silence this one.
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
// RETURN_MARKER is the fragment this page puts on its own return_to. Core appends
// "#access_token=..." to return_to, so a successful return reads
// "#blerg_refresh_return#access_token=..." and a return that carried no token reads
// "#blerg_refresh_return" alone.
export const RETURN_MARKER = "blerg_refresh_return";
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

// resetRefreshGuard drops both keys: for a deliberate retry by the person, where the
// counter's job (stopping an automatic loop) no longer applies.
export function resetRefreshGuard(): void {
  try {
    sessionStorage.removeItem(ATTEMPTED_KEY);
    sessionStorage.removeItem(COUNT_KEY);
  } catch {
    /* ignore */
  }
}

// refreshReturnUrl is the return_to to hand /auth/refresh: this page, marked as a return leg.
export function refreshReturnUrl(href: string = location.href): string {
  const u = new URL(href);
  u.hash = RETURN_MARKER;
  return u.toString();
}

// beginPageLoad runs once, when this module is first evaluated — before the code that imports it
// (runner's ws.ts, board's main.tsx) consumes the token fragment and asks for anything. A fresh load clears "attempted"; the
// return leg from /auth/refresh that carried no token keeps it, so that load does not bounce
// again (redirect → no token → redirect is still bounded; the counter is never cleared here).
// Returns true for that tokenless return leg. The marker is stripped from the address so a
// later reload counts as a fresh load.
export function beginPageLoad(): boolean {
  const hash = location.hash;
  if (hash.includes(RETURN_MARKER) && !/access_token=/.test(hash)) {
    try {
      history.replaceState(history.state, "", location.pathname + location.search);
    } catch {
      /* ignore */
    }
    return true;
  }
  clearRefreshAttempted();
  return false;
}

if (typeof location !== "undefined") beginPageLoad();
