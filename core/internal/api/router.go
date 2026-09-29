package api

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/agentsmanifest"
	cid "github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/core/internal/authprovider"
	"github.com/blerglab/blerg-ai/core/internal/credentials"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/discovery"
	"github.com/blerglab/blerg-ai/core/internal/identity"
	"github.com/blerglab/blerg-ai/core/internal/landing"
	"github.com/blerglab/blerg-ai/core/internal/plugins"
	"github.com/blerglab/blerg-ai/core/internal/projects"
)

// identityProvider is the narrow surface of *identity.Service the API layer needs. Declaring
// it as an interface (rather than depending on *identity.Service directly) lets tests exercise
// error paths — e.g. a revocation-snapshot failure independent of a JWKS failure — with a
// lightweight stub instead of forcing a real Postgres outage.
type identityProvider interface {
	JWKS(ctx context.Context) (cid.KeySet, error)
	RevocationSnapshot(ctx context.Context) ([]identity.Revocation, error)
}

type Deps struct {
	Identity identityProvider
	Projects *projects.Service
	Registry *discovery.Registry
	Audience string // "blerg-core"

	// Credentials backs POST/GET/DELETE /api/credentials (§5) — the per-account encrypted
	// credential vault. A nil Credentials makes those three endpoints fail closed (503)
	// rather than panic, matching the identitySvc()-style fail-closed convention used
	// elsewhere in this package (see auth_handlers.go's identitySvc comment).
	Credentials *credentials.Service

	// Plugins backs GET/PUT /api/plugins/{engine} and POST /internal/plugins/list — each
	// account's always-on plugins, under the operator's marketplace allow-list. Nil fails those
	// endpoints closed (503).
	Plugins *plugins.Service

	// RegisterKey is the bootstrap shared secret components (board, runner) present to
	// POST /components to self-register. It is intentionally a simple shared secret, not a
	// full identity token — component registration happens before a component has any way
	// to obtain a core-signed token. When empty, registration is disabled (503): we never
	// allow unauthenticated registration into the component directory.
	RegisterKey string

	// InternalKey is the shared secret an already-trusted component (blerg-runner) presents to
	// POST /internal/credentials/fetch. This is DELIBERATELY a separate secret from
	// RegisterKey, not a reuse of it: registering into the discovery directory and fetching a
	// logged-in human's decrypted personal API credential have very different blast radii if
	// the secret leaks, so they must not share one. Checked the same way RegisterKey is
	// (constant-time compare, see validInternalKey in internal_handlers.go) for consistency of
	// style only. When empty, the endpoint is disabled (503) — see handleInternalFetchCredential.
	InternalKey string

	// Store gives the internal-only /internal/credentials/fetch handler direct Postgres access
	// for the human_sessions live-session check and the credential_access_log audit write — the
	// only handlers in this package that need raw pool access rather than going through a
	// narrower service seam. Nil disables the endpoint (fails closed, 503), matching the
	// nil-Credentials convention used elsewhere in this Deps.
	Store db.Store

	// AuthProvider is the single active human-login mechanism, selected at process startup by
	// BLERG_CORE_AUTH_PROVIDER: *authprovider.Local, *authprovider.GitHub, or *authprovider.OIDC.
	// GET /auth/start and GET /auth/callback 404 under the local provider, which logs in via a
	// direct form POST to /auth/login instead.
	AuthProvider authprovider.Provider

	// AllowedReturnOrigins is the operator-configured allowlist of origins (scheme://host,
	// e.g. "http://board.example.com") GET /auth/refresh is permitted to redirect back to via
	// its return_to query parameter. Any return_to whose origin isn't in this list is rejected
	// — this is the open-redirect guard from spec §1.
	AllowedReturnOrigins []string

	// OriginAudiences maps an allowed return_to origin (scheme://host) to the access-token
	// audience minted for a refresh that returns there, e.g.
	// {"http://board.example.com": "blerg-board", "http://runner.example.com": "blerg-runner"}.
	// A return_to whose origin has no entry here falls back to Audience (core's own,
	// "blerg-core") — see audienceForReturnTo.
	OriginAudiences map[string]string

	// SessionTTL is how long a refresh session lives (BLERG_CORE_SESSION_TTL); it sizes the
	// refresh cookie's Max-Age. Zero means the identity.Service default (720h) — see
	// sessionCookieMaxAge in auth_handlers.go.
	SessionTTL time.Duration

	// BehindProxy makes clientIP trust the first X-Forwarded-For entry (BLERG_CORE_BEHIND_PROXY)
	// — see clientIP in auth_handlers.go.
	BehindProxy bool

	// PublicURL is core's own browser-facing origin (BLERG_CORE_PUBLIC_URL), e.g.
	// "https://core.example.com". When set, it is the sole source of truth for publicOrigin
	// (auth_handlers.go) — the CSRF same-origin check and, from Task 11 on, OAuth redirect
	// URLs. When empty, publicOrigin falls back to deriving the origin from the request
	// itself (X-Forwarded-Proto/TLS + Host); see publicOrigin's own doc comment for why that
	// fallback is safe for the CSRF check specifically but not a substitute for setting this
	// in production behind a provider that needs a stable redirect URL.
	PublicURL string

	// RevCache memoizes the revocation snapshot (see RevocationCache below, audit M-6) so
	// verifyBearer's per-request revocation check isn't a full-table read on every single
	// authenticated request. A nil RevCache (the zero value) disables the cache entirely and
	// falls back to querying on every call, matching pre-Task-14 behaviour — this is what
	// every existing test that builds a bare Deps{} literal (without setting RevCache) keeps
	// getting. main.go's buildDeps always sets one for the real server.
	RevCache *RevocationCache

	// LoginLimiter guards POST /auth/login against online brute force, per client IP and per
	// claimed subject (BLERG_CORE_LOGIN_RATE_LIMIT; desktop-security R2/C1). nil disables it —
	// see login_limiter.go.
	LoginLimiter *LoginLimiter

	// CookieSecure is the Secure attribute on every cookie core sets; true everywhere except a
	// plain-http desktop install — see BLERG_CORE_COOKIE_SECURE (desktop-security R6). WebKit
	// does not treat http://localhost as a secure context for cookies, so a hardcoded
	// Secure: true would silently drop the refresh cookie in Safari on the desktop install.
	CookieSecure bool
}

// componentLinks inverts OriginAudiences (origin→audience) into the browser-facing URLs the
// landing page's tiles need. Missing entries leave the page's desktop fallback in charge.
func (d Deps) componentLinks() landing.Links {
	var l landing.Links
	for origin, aud := range d.OriginAudiences {
		switch aud {
		case "blerg-board":
			l.BoardURL = origin
		case "blerg-runner":
			l.RunnerURL = origin
		}
	}
	return l
}

// RevocationCache memoizes the revocation snapshot for revCacheTTL so the per-request check in
// verifyBearer is not a full-table read on every authenticated request (audit M-6). A revocation
// therefore takes up to revCacheTTL to bite on core's own endpoints; board/runner already poll
// /revocations on their own interval, so this only affects how fast a revocation is enforced
// against core's OWN endpoints (/agents, /api/*, POST /components's downstream effects, etc),
// never how fast board/runner learn about it. The zero value is ready to use.
type RevocationCache struct {
	mu      sync.Mutex
	at      time.Time
	checker cid.RevocationChecker

	// now defaults to time.Now when nil (the zero value). Tests override it to advance the
	// clock past revCacheTTL without a real sleep — see TestRevocationCacheExpiresAfterTTL.
	now func() time.Time
}

func (c *RevocationCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// revCacheTTL bounds how stale the cached "not revoked" view can be: a revocation is never
// invisible for longer than this on core's own endpoints. Chosen short enough that "just
// revoked, still works for a moment" is not a meaningfully different security posture from
// today's live-query behaviour, while still turning most request bursts into a single DB read.
const revCacheTTL = 2 * time.Second

// revChecker builds the revocation checker from the DB snapshot. Core is authoritative against
// its own DB, so a *successful* query is never "stale" — StaleBeyondCeiling only reports true
// when the snapshot query itself failed, and requirePrincipal treats that failure as fatal
// (503) rather than proceeding with an empty revocation view (§4.1: fail closed on DB error,
// never silently treat every revoked token as valid).
func (d Deps) revChecker(ctx context.Context) (cid.RevocationChecker, error) {
	if d.RevCache != nil {
		d.RevCache.mu.Lock()
		if d.RevCache.checker != nil && d.RevCache.clock().Sub(d.RevCache.at) < revCacheTTL {
			checker := d.RevCache.checker
			d.RevCache.mu.Unlock()
			return checker, nil
		}
		d.RevCache.mu.Unlock()
	}
	// Captured BEFORE the query runs, not after: the cache's staleness bound is "at most
	// revCacheTTL since the snapshot could have started reflecting a revocation", not "at most
	// revCacheTTL since the query happened to finish". Stamping after RevocationSnapshot
	// returns would silently widen the effective bound to TTL + query latency on every refill.
	var start time.Time
	if d.RevCache != nil {
		start = d.RevCache.clock()
	}
	snap, err := d.Identity.RevocationSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	set := cid.RevocationSet{}
	for _, r := range snap {
		set.Add(r.Kind, r.Value, r.RevokedAt)
	}
	checker := snapChecker{set}
	if d.RevCache != nil {
		d.RevCache.mu.Lock()
		d.RevCache.checker = checker
		d.RevCache.at = start
		d.RevCache.mu.Unlock()
	}
	return checker, nil
}

// snapChecker is the shared RevocationSet (Revoked + RevokedFor, so sub/lineage entries are
// scoped to tokens issued at or before revoked_at) over one DB snapshot; never stale because
// core is authoritative against its own table.
type snapChecker struct{ cid.RevocationSet }

func (snapChecker) StaleBeyondCeiling() bool { return false }

func NewRouter(deps Deps) http.Handler {
	mux := http.NewServeMux()

	// GET /{$} matches only the exact root path (Go 1.22+ ServeMux pattern syntax) — it
	// does not swallow every other route the way a bare "/" registration would. The root is
	// the SPA's home: a signed-out visitor is bounced to the login form by the app itself, so
	// nobody lands on a page that merely offers a "Sign in" button to miss.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/app", http.StatusFound)
	})
	mux.Handle("GET /brand/", http.StripPrefix("/brand/", landing.AssetHandler()))

	// GET /api/site is the one unauthenticated /api route: the build version and the
	// browser-facing component URLs (BLERG_CORE_ORIGIN_AUDIENCES, inverted) the SPA's home
	// tiles and footer render. Nothing here is secret — it is exactly what the old
	// server-rendered landing page put in its HTML for anyone to read.
	mux.HandleFunc("GET /api/site", func(w http.ResponseWriter, _ *http.Request) {
		l := deps.componentLinks()
		writeJSON(w, map[string]string{
			"version":    landing.Version(),
			"board_url":  l.BoardURL,
			"runner_url": l.RunnerURL,
		})
	})

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /.well-known/jwks", func(w http.ResponseWriter, r *http.Request) {
		keys, err := deps.Identity.JWKS(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := map[string]string{}
		for kid, pub := range keys {
			out[kid] = base64.RawURLEncoding.EncodeToString(pub)
		}
		writeJSON(w, out)
	})

	mux.HandleFunc("GET /revocations", func(w http.ResponseWriter, r *http.Request) {
		snap, err := deps.Identity.RevocationSnapshot(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, snap)
	})

	// Public (no principal): discovery must work before a caller has any credential — see
	// handleAgents' own comment. It used to require card.read.
	mux.HandleFunc("GET /agents", deps.handleAgents)
	mux.HandleFunc("GET /openapi.json", handleOpenAPI)

	mux.HandleFunc("POST /components", func(w http.ResponseWriter, r *http.Request) {
		if deps.RegisterKey == "" {
			http.Error(w, "registration disabled", http.StatusServiceUnavailable)
			return
		}
		if !validRegisterKey(r, deps.RegisterKey) {
			http.Error(w, "invalid register key", http.StatusUnauthorized)
			return
		}
		// Cap the body BEFORE decoding (audit M-4) — 16 KiB is generous for a component
		// manifest entry, which since the agent contract also carries the component's
		// description, docs/openapi/mcp URLs, auth block and full operation table (the
		// runner's is the largest at a few KiB).
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		var e agentsmanifest.ComponentEntry
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if e.Name == "" || e.BaseURL == "" {
			http.Error(w, "name and base_url are required", http.StatusBadRequest)
			return
		}
		if err := deps.Registry.Register(r.Context(), e); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	mux.HandleFunc("POST /auth/login", deps.handleLogin)
	mux.HandleFunc("GET /auth/start", deps.handleStart)
	mux.HandleFunc("GET /auth/provider", deps.handleProvider)
	mux.HandleFunc("GET /auth/callback", deps.handleCallback)
	mux.HandleFunc("GET /auth/refresh", deps.handleRefresh)
	mux.HandleFunc("POST /auth/logout", deps.handleLogout)
	mux.HandleFunc("POST /auth/logout-all", requireHumanPrincipal(deps.handleLogoutAll, deps, ""))
	// No capability required: a password-change-only token (caps = ["password.change"]) MUST
	// be able to reach this endpoint (R7/I-7) — it's the only USEFUL thing such a token can
	// do; GET /api/me and POST /auth/logout-all are also reachable (no capability gate
	// either) but neither leaks or changes anything the flag needs to withhold.
	mux.HandleFunc("POST /auth/password", requireHumanPrincipal(deps.handleChangePassword, deps, ""))

	// requirePrincipal, not requireHumanPrincipal: /api/me is the introspection endpoint an
	// external tool calls to confirm its agent token works and learn what it may do (spec §2),
	// so it accepts a `platform` agent token as well as a human session. handleMe itself
	// branches on the principal's kind and refuses anything else — no capability is required
	// here, so a password-change-only token can still see who it is.
	mux.HandleFunc("GET /api/me", requirePrincipal(deps.handleMe, deps, ""))

	// Agent tokens (spec §2). Human-only, on purpose: an agent token must not be able to mint
	// another one, or revoking the parent would leave the child alive. "card.read" is the same
	// gate /api/credentials uses — it is what a must-change-password bootstrap token lacks.
	mux.HandleFunc("POST /api/tokens", requireHumanPrincipal(deps.handleCreateToken, deps, "card.read"))
	mux.HandleFunc("GET /api/tokens", requireHumanPrincipal(deps.handleListTokens, deps, "card.read"))
	mux.HandleFunc("DELETE /api/tokens/{id}", requireHumanPrincipal(deps.handleRevokeToken, deps, "card.read"))

	mux.HandleFunc("POST /api/projects/{id}/members", requirePrincipal(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := principalFromCtx(r.Context())
		if !ok {
			http.Error(w, "missing principal", http.StatusUnauthorized)
			return
		}
		projectID := r.PathValue("id")
		// The middleware only checked the "membership.write" capability name; it never
		// checked *which* project the token is scoped to. A token scoped to project A must
		// not be able to modify project B's membership (CRITICAL 1).
		if principal.Project != projectID {
			http.Error(w, "token not scoped to this project", http.StatusForbidden)
			return
		}
		// Cap the body BEFORE decoding (audit M-4) — 16 KiB is generous for {Sub, Role}.
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		var body struct{ Sub, Role string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
				return
			}
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if _, ok := projects.RoleCaps[body.Role]; !ok {
			http.Error(w, "unknown role", http.StatusBadRequest)
			return
		}
		if err := deps.Projects.AddMember(r.Context(), projectID, body.Sub, body.Role); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}, deps, "membership.write"))

	// "card.read" gates all three: it's the capability every normal role (member, admin) has
	// and a password-change-only token (R7/I-7) deliberately lacks, so this is what actually
	// stops such a token from touching the credential vault, not just the SPA's own routing.
	mux.HandleFunc("POST /api/credentials", requireHumanPrincipal(deps.handleStoreCredential, deps, "card.read"))
	mux.HandleFunc("GET /api/credentials", requireHumanPrincipal(deps.handleListCredentials, deps, "card.read"))
	mux.HandleFunc("DELETE /api/credentials/{engine}", requireHumanPrincipal(deps.handleDeleteCredential, deps, "card.read"))

	// Always-on plugins. Reads and writes are human-only (requireHumanPrincipal): see the
	// SECURITY comment on handlePutPlugins for why an agent token must never be able to write.
	mux.HandleFunc("GET /api/plugins/{engine}", requireHumanPrincipal(deps.handleListPlugins, deps, "card.read"))
	mux.HandleFunc("PUT /api/plugins/{engine}", requireHumanPrincipal(deps.handlePutPlugins, deps, "card.read"))

	// /internal/... is a deliberately distinct path prefix from /api/... (the public API
	// surface): everything under it is component-to-component only, never browser-facing.
	// This handler sets no CORS headers (nothing in this package ever does), so a browser
	// attempting to call it cross-origin is blocked by the browser itself even if it somehow
	// obtained a valid internal key. See internal_handlers.go's header comment for the full
	// rationale.
	mux.HandleFunc("POST /internal/credentials/fetch", deps.handleInternalFetchCredential)
	// Same gates as fetch (internal key + live human session), names only, no plaintext.
	mux.HandleFunc("POST /internal/credentials/list", deps.handleInternalListCredentials)
	// The runner reads an account's plugin list (no secrets) with a mandatory liveness proof.
	mux.HandleFunc("POST /internal/plugins/list", deps.handleInternalListPlugins)

	return mux
}

// validRegisterKey constant-time-compares the caller-supplied registration key against the
// configured bootstrap secret. Accepts either "Authorization: Bearer <key>" or
// "X-Register-Key: <key>" so callers can use whichever is more convenient. A missing or
// empty supplied key never matches, even against an empty want (callers must not reach here
// with an empty deps.RegisterKey — that case is rejected earlier as 503).
func validRegisterKey(r *http.Request, want string) bool {
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if got == "" || got == r.Header.Get("Authorization") {
		got = r.Header.Get("X-Register-Key")
	}
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
