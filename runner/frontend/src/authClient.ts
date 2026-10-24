// authClient.ts — the in-memory access-token module shared by every blerg frontend (Task 18
// copies this exact module into board's and runner's own web/ trees).
//
// The access token lives in a module-level variable ONLY — never localStorage/sessionStorage.
// Web Storage is JS-readable by any XSS on this origin with no TTL bound beyond the token's
// own short lifetime; an in-memory value is lost on tab close/reload, which is the correct
// tradeoff given redirectToRefresh below makes reacquiring it cheap (spec §1).
let accessToken: string | null = null;

// This runner copy also notifies local desktop caches when the signed-in
// subject changes. The comparison is advisory; the server verifies authority.
function receiveAccessToken(token: string): void {
  const subject = (raw: string | null): string => {
    try { const data = JSON.parse(atob((raw ?? '').split('.')[1].replace(/-/g, '+').replace(/_/g, '/'))) as { sub?: string }; return data.sub ?? ''; } catch { return ''; }
  };
  const before = subject(accessToken);
  accessToken = token;
  if (before !== subject(token)) window.dispatchEvent(new Event('blerg-identity-changed'));
}

export function getAccessToken(): string | null {
  return accessToken;
}

// consumeAccessTokenFromFragment reads "#access_token=..." out of the URL fragment left by a
// GET /auth/refresh (or /auth/callback) redirect, stores it in memory, and strips it from the
// URL via history.replaceState — the fragment is never sent to a server (it's client-only), but
// it's still visible in browser history/devtools, so we scrub it immediately after reading it.
export function consumeAccessTokenFromFragment(): void {
  const match = location.hash.match(/access_token=([^&]+)/);
  if (match) {
    receiveAccessToken(decodeURIComponent(match[1]));
    history.replaceState(null, "", location.pathname + location.search);
  }
}

// coreOrigin derives the origin blerg-core is served from, for whichever frontend this copy of
// the module is running inside (it is shared verbatim across core, board and runner). It is the
// one piece of cross-component wiring the browser can't be told at runtime, so it is derived —
// with a build-time escape hatch for any topology the derivation doesn't cover.
//
// VITE_CORE_URL (a build-time Vite env var, e.g. `VITE_CORE_URL=https://core.example.com npm run
// build`) always wins. Otherwise the two deployment shapes this repo actually ships are matched:
//
//   k8s (install/k8s/ingress.yaml): core is the APEX domain, ${DOMAIN}; board and runner are the
//   board.${DOMAIN} / runner.${DOMAIN} subdomains of it, all on the ingress's own port. So from
//   board/runner, strip that one leading label; from core's own frontend (already at the apex)
//   nothing matches and the current origin is used.
//
//   desktop (install/desktop/docker-compose.yml): every service is on localhost, distinguished
//   only by a published port — core on BLERG_PORT_CORE (default 8081). A bare hostname (no dots)
//   or a raw IP means this shape, so keep the host and swap in core's port. Override the port
//   with VITE_CORE_PORT when BLERG_PORT_CORE is changed.
//
// The previous implementation was wrong for both shapes: it rewrote the first hostname label to
// "core" (a core.<domain> convention this repo's ingress does not use — core is the apex) and
// dropped the port entirely (so on desktop it pointed at localhost:80, where nothing listens).
export function coreOrigin(): string {
  const configured = import.meta.env.VITE_CORE_URL;
  if (configured) return configured.replace(/\/+$/, "");

  const { protocol, hostname } = location;
  const subdomain = hostname.match(/^(?:board|runner)\.(.+)$/);
  if (subdomain) return `${protocol}//${subdomain[1]}`;

  const isBareHost = !hostname.includes(".") || /^\d+(?:\.\d+){3}$/.test(hostname);
  if (isBareHost) {
    return `${protocol}//${hostname}:${import.meta.env.VITE_CORE_PORT || "8081"}`;
  }

  return location.origin;
}

// redirectToRefresh sends the whole page to core's own origin to refresh — never a
// cross-origin fetch, which SameSite=Strict would silently drop the refresh cookie on
// (spec §1). returnTo is the URL (on the calling component's own origin) to send the user
// back to, with a fresh #access_token=... fragment, once the refresh completes.
export function redirectToRefresh(returnTo: string): void {
  location.href = `${coreOrigin()}/auth/refresh?return_to=${encodeURIComponent(returnTo)}`;
}

// ─── Silent renewal ───────────────────────────────────────────────────────────────────────────────
// An access token lasts about ten minutes. Renewing it with redirectToRefresh sends the whole page to
// core and back, which reloads the app and throws away what the person was looking at. So before that
// is ever needed, the page asks core for a new token in a hidden frame: core's /auth/refresh answers
// the frame with a redirect to THIS origin's /silent-refresh.html carrying the token in the URL
// fragment, and since the frame is same-origin again by then the page can read it, store it and throw
// the frame away. Nothing on screen changes.
//
// The refresh cookie is SameSite=Strict, which a frame to a different SITE would not carry. core, board
// and runner are always one site (subdomains of one domain, or one host on different ports), so it is
// carried. When the frame gets no token (signed out, cookie not sent, core unreachable) the caller
// falls back to the full-page redirect, exactly as before.
//
// What the frame can tell apart (renewSilently): "token"; "signed-out", when core answered that the
// sign-in is gone (no refresh cookie, or its session expired or was revoked) — core sends the frame
// back to the silent page with "#signin_error=<reason>" for exactly that; and "no-answer" for
// everything else: core down or unreachable (the frame shows the browser's error page, which is
// cross-origin and unreadable, or never loads), core's own page (a password change it insists on, an
// error), or no load within SILENT_TIMEOUT_MS. Only the positive signal counts as signed out.
//
// Every refresh ROTATES the cookie, so the browser must end up holding the cookie core last issued.
// Two rules keep it that way. One refresh at a time across the tabs of this origin: a Web Lock is
// held for the frame's whole life, so a second tab waits instead of racing (core heals a race, but
// it should not have to). And a frame is never torn down while its request is in flight: a caller
// that waited long enough gives up and falls back, but the frame stays until it loads, so the
// cookie core set on its way back is taken up rather than lost. A rotation whose response is lost
// is what makes a browser present a stale cookie later.

/** Renew this long before the token expires. */
const RENEW_BEFORE_MS = 90_000;
/** How long a caller waits on one frame before falling back. */
const SILENT_TIMEOUT_MS = 15_000;
/** How long a frame nobody is waiting on may stay, so a slow answer can still land its cookie. */
const SILENT_LINGER_MS = 120_000;
/** Retry a failed scheduled renewal after this long, while the token is still good. */
const RENEW_RETRY_MS = 30_000;
/** The page core redirects the frame to: a static page of this origin that does nothing. */
export const SILENT_REFRESH_PAGE = "/silent-refresh.html";
/** The Web Lock every tab of this origin takes to refresh. */
export const REFRESH_LOCK = "blerg.auth.refresh";

// tokenExpiryMs is a cheap, UNVERIFIED read of the token's exp claim, for scheduling only.
function tokenExpiryMs(token: string): number | null {
  try {
    const parts = token.split(".");
    if (parts.length < 2) return null;
    const payload = JSON.parse(atob(parts[1].replace(/-/g, "+").replace(/_/g, "/"))) as { exp?: unknown };
    return typeof payload.exp === "number" ? payload.exp * 1000 : null;
  } catch {
    return null;
  }
}

/** What one silent renewal came back with; see the block comment above. */
export type SilentOutcome = "token" | "signed-out" | "no-answer";

// silentFrame loads core's refresh in a hidden frame. `answered` settles when the frame has loaded
// (with the outcome; a token is stored first); `done` settles when the frame is gone — after its
// answer, or after SILENT_LINGER_MS if it never loads. A caller waits on `answered` for at most
// SILENT_TIMEOUT_MS; the frame itself is never cut short, so a rotation core committed is not lost.
function silentFrame(): { answered: Promise<SilentOutcome>; done: Promise<void> } {
  let settle!: (o: SilentOutcome) => void;
  let gone!: () => void;
  const answered = new Promise<SilentOutcome>((resolve) => { settle = resolve; });
  const done = new Promise<void>((resolve) => { gone = resolve; });
  const frame = document.createElement("iframe");
  frame.setAttribute("aria-hidden", "true");
  frame.tabIndex = -1;
  frame.style.cssText = "position:absolute;width:0;height:0;border:0;visibility:hidden";
  let settled = false;
  const finish = (o: SilentOutcome) => {
    if (settled) return;
    settled = true;
    clearTimeout(linger);
    frame.remove();
    settle(o);
    gone();
  };
  const linger = setTimeout(() => finish("no-answer"), SILENT_LINGER_MS);
  frame.addEventListener("load", () => {
    try {
      // Reading the frame's location throws while it is still on core's origin (core answered with
      // its sign-in page or an error): that is "no token", not a failure to report.
      const loc = frame.contentWindow!.location;
      const sameOrigin = loc.origin === location.origin;
      const match = sameOrigin ? loc.hash.match(/access_token=([^&]+)/) : null;
      if (match) {
        receiveAccessToken(decodeURIComponent(match[1]));
        finish("token");
        return;
      }
      if (sameOrigin && /(?:^#|&)signin_error=/.test(loc.hash)) {
        finish("signed-out");
        return;
      }
    } catch {
      /* cross-origin: no token */
    }
    finish("no-answer");
  });
  const returnTo = location.origin + SILENT_REFRESH_PAGE;
  frame.src = `${coreOrigin()}/auth/refresh?return_to=${encodeURIComponent(returnTo)}`;
  document.body.appendChild(frame);
  return { answered, done };
}

// withRefreshLock runs fn while holding this origin's refresh lock, so tabs refresh one after
// another. Without Web Locks (an old browser, a test) fn just runs.
function withRefreshLock<T>(fn: () => Promise<T>): Promise<T> {
  const locks = typeof navigator !== "undefined" ? navigator.locks : undefined;
  if (!locks || typeof locks.request !== "function") return fn();
  return locks.request(REFRESH_LOCK, fn);
}

// silentRefresh: one frame, the lock held until the frame is gone, and the caller's answer no later
// than SILENT_TIMEOUT_MS.
function silentRefresh(): Promise<SilentOutcome> {
  return new Promise<SilentOutcome>((resolve) => {
    let resolved = false;
    const report = (o: SilentOutcome) => {
      if (resolved) return;
      resolved = true;
      resolve(o);
    };
    void withRefreshLock(async () => {
      const { answered, done } = silentFrame();
      const timer = setTimeout(() => report("no-answer"), SILENT_TIMEOUT_MS);
      answered.then(report, () => report("no-answer")).finally(() => clearTimeout(timer));
      await done;
    }).catch(() => report("no-answer"));
  });
}

let inflight: Promise<SilentOutcome> | null = null;

/** renewSilently gets a new access token without leaving the page, with or without one held now,
 *  and says what came back (SilentOutcome). Concurrent calls in this tab share one frame; other tabs
 *  of this origin wait their turn on the refresh lock. */
export function renewSilently(): Promise<SilentOutcome> {
  if (!inflight) {
    inflight = silentRefresh().finally(() => {
      inflight = null;
    });
  }
  return inflight;
}

/** ensureFreshToken is renewSilently as a yes or no: true when a token was stored, false when the
 *  caller should fall back to redirectToRefresh. */
export function ensureFreshToken(): Promise<boolean> {
  return renewSilently().then((o) => o === "token");
}

let renewTimer: ReturnType<typeof setTimeout> | null = null;

function scheduleRenewal(delayOverride?: number): void {
  if (renewTimer) clearTimeout(renewTimer);
  renewTimer = null;
  const exp = accessToken ? tokenExpiryMs(accessToken) : null;
  if (exp === null) return;
  const delay = delayOverride ?? Math.max(5_000, exp - Date.now() - RENEW_BEFORE_MS);
  renewTimer = setTimeout(() => void renewNow(), delay);
}

async function renewNow(): Promise<void> {
  const ok = await ensureFreshToken();
  if (ok) {
    scheduleRenewal();
    return;
  }
  // No new token. While the current one is still good, try again shortly; once it has expired the
  // next request's 401 takes the visible route (and a hidden tab renews when it is shown again).
  const exp = accessToken ? tokenExpiryMs(accessToken) : null;
  if (exp !== null && exp > Date.now()) scheduleRenewal(RENEW_RETRY_MS);
}

let renewalStarted = false;

/** startTokenRenewal keeps the token fresh in the background: it renews shortly before expiry and
 *  again when a tab that was hidden is shown (timers in a hidden tab can be late). Call after
 *  consumeAccessTokenFromFragment, or once a token was first obtained some other way; a second call
 *  only reschedules. */
export function startTokenRenewal(): void {
  scheduleRenewal();
  if (renewalStarted) return;
  renewalStarted = true;
  document.addEventListener("visibilitychange", () => {
    if (document.hidden || !accessToken) return;
    const exp = tokenExpiryMs(accessToken);
    if (exp !== null && exp - Date.now() <= RENEW_BEFORE_MS) void renewNow();
  });
}
