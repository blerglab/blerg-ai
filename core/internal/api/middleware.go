package api

import (
	"context"
	"log"
	"net/http"
	"strings"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
)

// sensitiveCaps fail closed under stale revocation (§4.1).
func sensitiveCaps(capability string) bool {
	switch capability {
	case "secrets.read", "session.start", "merge", "ops.exec",
		"membership.write", "install.policy", "session.runtime.local_host":
		return true
	}
	return false
}

type ctxKey struct{}

// principalFromCtx reads the authenticated principal that requirePrincipal stores in the
// request context. Handlers that need to make their own authorization decisions beyond the
// blanket capability check (e.g. project-scope checks) should use this instead of re-deriving
// or trusting client-supplied identifiers.
func principalFromCtx(ctx context.Context) (cid.Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(cid.Principal)
	return p, ok
}

func requirePrincipal(next http.HandlerFunc, deps Deps, capability string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := verifyBearer(w, r, deps)
		if !ok {
			return
		}
		if capability != "" && !p.Has(capability) {
			http.Error(w, "missing capability: "+capability, http.StatusForbidden)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	}
}

// verifyBearer does the common bearer-token verification work shared by requirePrincipal and
// requireHumanPrincipal (parse, JWKS lookup, revocation-snapshot fail-closed check,
// cid.Verify). On any failure it has already written the appropriate error response and the
// caller must return immediately without doing anything further.
func verifyBearer(w http.ResponseWriter, r *http.Request, deps Deps) (cid.Principal, bool) {
	raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if raw == "" || raw == r.Header.Get("Authorization") {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return cid.Principal{}, false
	}
	keys, err := deps.Identity.JWKS(r.Context())
	if err != nil {
		http.Error(w, "key lookup failed", http.StatusInternalServerError)
		return cid.Principal{}, false
	}
	// The revocation snapshot query is a live Postgres read. Core is authoritative against its
	// own DB, so a successful query is never "stale" — but a FAILED query must not be treated
	// as "nothing is revoked": that would silently let a revoked token through on every
	// transient DB error (CRITICAL 2). Fail closed with 503 instead of proceeding with an
	// empty revocation view.
	checker, err := deps.revChecker(r.Context())
	if err != nil {
		http.Error(w, "revocation check unavailable", http.StatusServiceUnavailable)
		return cid.Principal{}, false
	}
	p, err := cid.Verify(raw, deps.Audience, keys, checker, sensitiveCaps)
	if err != nil {
		// The real verification error (malformed/expired/wrong-audience/revoked/...) is never
		// echoed to the caller (audit M-7) — that's a distinguishing oracle for someone probing
		// tokens. Log it for operators; the caller gets one generic message regardless of cause.
		log.Printf("verifyBearer: %v", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return cid.Principal{}, false
	}
	return p, true
}

// requireHumanPrincipal gates next behind a valid, non-revoked, core-audience bearer token whose
// Kind is specifically "human" (minted by identity.Service.MintHumanAccessToken via the
// /auth/login -> /auth/refresh flow) — not a service or agent token. This is the gate the
// /api/credentials endpoints use (§5): the caller's own account, derived from the verified
// token's Sub claim, is the only account whose credentials the request may ever touch.
//
// An empty capability means "any human token" (used by /api/me, /auth/logout-all, and
// /auth/password itself — a password-change-only token MUST reach /auth/password). A non-empty
// capability additionally requires p.Has(capability): this is R7/I-7's server-side enforcement
// reaching past the token's Kind check — a must-change-password account's token carries ONLY
// "password.change" (see MintHumanAccessToken), so gating /api/credentials on "card.read" here
// is what actually makes "nothing but the change-password endpoint works" true, rather than
// just true in the SPA's routing.
func requireHumanPrincipal(next http.HandlerFunc, deps Deps, capability string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := verifyBearer(w, r, deps)
		if !ok {
			return
		}
		if p.Kind != string(cid.Human) {
			http.Error(w, "human access token required", http.StatusForbidden)
			return
		}
		if capability != "" && !p.Has(capability) {
			http.Error(w, "missing capability: "+capability, http.StatusForbidden)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)))
	}
}
