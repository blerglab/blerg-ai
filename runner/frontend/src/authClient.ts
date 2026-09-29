// authClient.ts — the in-memory access-token module shared by every blerg frontend (Task 18
// copies this exact module into board's and runner's own web/ trees).
//
// The access token lives in a module-level variable ONLY — never localStorage/sessionStorage.
// Web Storage is JS-readable by any XSS on this origin with no TTL bound beyond the token's
// own short lifetime; an in-memory value is lost on tab close/reload, which is the correct
// tradeoff given redirectToRefresh below makes reacquiring it cheap (spec §1).
let accessToken: string | null = null;

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
    accessToken = decodeURIComponent(match[1]);
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
