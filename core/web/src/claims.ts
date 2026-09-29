// claims.ts — UNVERIFIED, UI-routing-only decoding of the access token's claims. Nothing here
// is ever an authorization decision: the server (core/internal/api/middleware.go's
// requireHumanPrincipal, gated on the "card.read" capability for /api/credentials) is the sole
// enforcement point for R7/I-7 — a client that ignores mustChangePassword entirely and calls
// the API directly still gets nothing but /auth/password to work with. This module only decides
// which screen to show.

// tokenCaps decodes the access token's `caps` claim WITHOUT verifying it — UI routing only;
// the server enforces every capability. Returns [] for anything malformed.
export function tokenCaps(token: string): string[] {
  try {
    const payload = token.split(".")[1] ?? "";
    const json = atob(payload.replace(/-/g, "+").replace(/_/g, "/"));
    const caps = JSON.parse(json).caps;
    return Array.isArray(caps) ? caps.filter((c) => typeof c === "string") : [];
  } catch {
    return [];
  }
}

// mustChangePassword reports whether token looks like one of MintHumanAccessToken's
// password-change-only tokens (caps = ["password.change"], no card.read) — used purely to
// decide whether to route the user to /change-password instead of the normal app.
export function mustChangePassword(token: string | null): boolean {
  if (!token) return false;
  const caps = tokenCaps(token);
  return caps.includes("password.change") && !caps.includes("card.read");
}
