package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/logsafe"
	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"github.com/blerglab/blerg-ai/contracts/secrets"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/authprovider"
	"github.com/blerglab/blerg-ai/core/internal/credentials"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/discovery"
	"github.com/blerglab/blerg-ai/core/internal/identity"
	"github.com/blerglab/blerg-ai/core/internal/keybackend"
	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
	"github.com/blerglab/blerg-ai/core/internal/plugins"
	"github.com/blerglab/blerg-ai/core/internal/projects"
)

// validateSecrets refuses to boot on placeholder or too-short shared secrets (R4) — a copied
// .env.example must never become a live credential. BLERG_CORE_LOCAL_KEY is validated
// separately by buildKeyBackend (it's base64 key material, not a bearer-style secret).
func validateSecrets() error {
	for _, c := range []struct {
		name     string
		required bool
	}{
		{"BLERG_CORE_REGISTER_KEY", false},
		{"BLERG_CORE_INTERNAL_KEY", false},
	} {
		v := os.Getenv(c.name)
		var err error
		if c.required {
			err = secrets.Require(c.name, v)
		} else {
			err = secrets.Check(c.name, v)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

const componentTTL = 5 * time.Minute

// spaHandler serves static files from dir, falling back to index.html for any path that
// doesn't correspond to a real file — standard SPA routing support. Copied verbatim from
// runner/cmd/server/main.go's spaHandler: core and runner are separate Go modules, so sharing
// this ~15-line function via an internal package isn't a clean option without a bigger
// cross-module refactor that's out of scope here.
func spaHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(dir, filepath.Clean(r.URL.Path))
		_, err := os.Stat(path)
		if os.IsNotExist(err) {
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		if filepath.Base(path) == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fs.ServeHTTP(w, r)
	})
}

// webDistDir returns the directory core/web's built assets are served from — overridable via
// BLERG_CORE_WEB_DIST for deployments that place the build somewhere other than the default
// path relative to the process's working directory (e.g. the Docker image).
func webDistDir() string {
	if v := os.Getenv("BLERG_CORE_WEB_DIST"); v != "" {
		return v
	}
	return "web/dist"
}

// withSPAFallback serves the SPA fallback for any request that doesn't match one of next's
// registered patterns, and runs next's own handler unmodified for any request that does.
//
// core's existing routes (landing page, /healthz, /.well-known/jwks, /auth/*, /api/*, ...) all
// live inside api.NewRouter's own *http.ServeMux (core/internal/api/router.go), which main.go
// has no way to add patterns to from the outside — so instead of duplicating that route table
// here, this asks the mux itself which pattern (if any) matches the request, via
// (*http.ServeMux).Handler (stdlib since Go 1.22), and only falls through to the SPA when no
// pattern matched (an empty pattern).
//
// This must NOT be done by running next's handler and checking whether it *responded* 404:
// several of core's real, registered handlers legitimately return 404 for business reasons —
// e.g. POST /auth/login when the local provider isn't active, and GET /auth/callback whenever
// it is (core/internal/api/auth_handlers.go) — and both are covered by tests asserting exactly
// that 404. Treating "the handler said 404" as "no route matched" would silently replace those
// real 404s with a 200 SPA page, which is what an earlier version of this function did wrong.
// apiishPrefixes are path prefixes that belong to core's non-SPA surface: component-to-
// component, machine, and well-known endpoints. A request under one of these must NEVER see the
// SPA's 200 index.html, even on a method the mux has no handler for — audit finding: (*http.
// ServeMux).Handler reports an EMPTY pattern (see below) for a path/method combination that
// matches no registered route, which is indistinguishable, from the caller's signature alone,
// from a path that matches no route at ALL. Without this list, "POST /api/credentials" is a
// perfectly well-known path (a GET/DELETE handler IS registered there) — but GET on it used to
// fall through to the SPA fallback and answer 200 with an HTML page instead of the 404/405 an
// API client expects and would otherwise correctly handle.
var apiishPrefixes = []string{
	"/api/", "/auth/", "/internal/", "/.well-known/", "/revocations", "/agents", "/components", "/healthz",
}

func isAPIishPath(path string) bool {
	for _, p := range apiishPrefixes {
		if path == p || strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

func withSPAFallback(next http.Handler, fallback http.Handler) http.Handler {
	mux, ok := next.(*http.ServeMux)
	if !ok {
		// api.NewRouter (router.go) returns its own *http.ServeMux directly. If that ever
		// changes, fail safe by serving next as-is rather than guessing at route matching.
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern == "" && !isAPIishPath(r.URL.Path) {
			// A genuinely unmatched path outside the API-ish surface: fall through to the SPA
			// so client-side routing (e.g. /app/some/client-route) works.
			fallback.ServeHTTP(w, r)
			return
		}
		// Either a pattern matched, or the path is under an API-ish prefix with no matching
		// method (the mux's own 404/405). Either way, hand the request to the MUX, not to the
		// handler mux.Handler returned: only (*http.ServeMux).ServeHTTP binds the pattern's
		// wildcards onto the request, so calling h directly leaves r.PathValue("id") empty for
		// every `/api/tokens/{id}`-style route — which is how DELETE /api/tokens/{id} and
		// DELETE /api/credentials/{engine} answered 404 in production while passing every
		// router-level test (those serve api.NewRouter's mux directly). mux.Handler above is
		// used only to decide whether the SPA should answer; h itself is deliberately unused.
		_ = h
		mux.ServeHTTP(w, r)
	})
}

// newServer builds a minimal server exposing only /healthz, independent of any store —
// used directly by TestHealthz, which has no DATABASE_URL requirement. The real service
// (main, below) serves the full api.NewRouter, which also exposes GET /healthz.
func newServer(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}
}

// Server timeouts. A client that opens a connection and trickles its request headers would
// otherwise hold the connection open indefinitely. Only the header read and idle keep-alive
// are bounded: a whole-request or write deadline would cut off legitimately slow responses.
const (
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 120 * time.Second
)

// authProviderName returns BLERG_CORE_AUTH_PROVIDER, defaulting to "local" when unset.
// "local", "github" and "oidc" are all wired in buildAuthProvider below.
func authProviderName() string {
	if v := os.Getenv("BLERG_CORE_AUTH_PROVIDER"); v != "" {
		return v
	}
	return "local"
}

// buildAuthProvider constructs the single active authprovider.Provider named by
// BLERG_CORE_AUTH_PROVIDER (default "local"). publicURL is BLERG_CORE_PUBLIC_URL
// (api.Deps.PublicURL) — the github/oidc cases require it to build the OAuth redirect_uri
// (Task 11): "<publicURL>/auth/callback", registered with the IdP as this app's callback URL.
//
// ctx is used by the "oidc" case, whose constructor performs OpenID discovery against the
// issuer up front so a misconfigured/unreachable issuer fails fast at boot rather than on the
// first login attempt.
func buildAuthProvider(ctx context.Context, name string, st *db.PgStore, publicURL string) (authprovider.Provider, error) {
	switch name {
	case "local":
		return authprovider.NewLocal(st), nil
	case "github":
		clientID := os.Getenv("BLERG_CORE_GITHUB_CLIENT_ID")
		clientSecret := os.Getenv("BLERG_CORE_GITHUB_CLIENT_SECRET")
		org := os.Getenv("BLERG_CORE_GITHUB_ORG")
		if clientID == "" || clientSecret == "" || org == "" {
			return nil, fmt.Errorf("BLERG_CORE_AUTH_PROVIDER=github requires BLERG_CORE_GITHUB_CLIENT_ID, BLERG_CORE_GITHUB_CLIENT_SECRET, and BLERG_CORE_GITHUB_ORG")
		}
		if publicURL == "" {
			return nil, fmt.Errorf("BLERG_CORE_AUTH_PROVIDER=%s requires BLERG_CORE_PUBLIC_URL (core's browser-facing origin, e.g. https://core.example.com) to build the OAuth redirect URL", name)
		}
		p := authprovider.NewGitHub(st, clientID, clientSecret, org, http.DefaultClient)
		p.SetRedirectURL(strings.TrimRight(publicURL, "/") + "/auth/callback")
		return p, nil
	case "oidc":
		issuerURL := os.Getenv("BLERG_CORE_OIDC_ISSUER_URL")
		clientID := os.Getenv("BLERG_CORE_OIDC_CLIENT_ID")
		clientSecret := os.Getenv("BLERG_CORE_OIDC_CLIENT_SECRET")
		if issuerURL == "" || clientID == "" || clientSecret == "" {
			return nil, fmt.Errorf("BLERG_CORE_AUTH_PROVIDER=oidc requires BLERG_CORE_OIDC_ISSUER_URL, BLERG_CORE_OIDC_CLIENT_ID, and BLERG_CORE_OIDC_CLIENT_SECRET")
		}
		if publicURL == "" {
			return nil, fmt.Errorf("BLERG_CORE_AUTH_PROVIDER=%s requires BLERG_CORE_PUBLIC_URL (core's browser-facing origin, e.g. https://core.example.com) to build the OAuth redirect URL", name)
		}
		p, err := authprovider.NewOIDC(ctx, st, issuerURL, clientID, clientSecret)
		if err != nil {
			return nil, err
		}
		p.SetRedirectURL(strings.TrimRight(publicURL, "/") + "/auth/callback")
		return p, nil
	default:
		return nil, fmt.Errorf("unknown BLERG_CORE_AUTH_PROVIDER %q (only \"local\", \"github\" and \"oidc\" are implemented)", name)
	}
}

// keyBackendName returns BLERG_CORE_KEYBACKEND, defaulting to "local" when unset. "local" reads
// its AES-256 key from BLERG_CORE_LOCAL_KEY (base64 of 32 bytes) — see keybackend.NewLocal;
// "vault-transit" delegates key material to Vault's Transit engine via Kubernetes auth.
func keyBackendName() string {
	if v := os.Getenv("BLERG_CORE_KEYBACKEND"); v != "" {
		return v
	}
	return "local"
}

// buildKeyBackend constructs the single active keybackend.Backend named by
// BLERG_CORE_KEYBACKEND (default "local"), used to encrypt/decrypt the per-account credential
// vault (core/internal/credentials).
func buildKeyBackend(ctx context.Context) (keybackend.Backend, error) {
	switch name := keyBackendName(); name {
	case "local":
		raw := os.Getenv("BLERG_CORE_LOCAL_KEY")
		if raw == "" {
			return nil, fmt.Errorf("BLERG_CORE_KEYBACKEND=local requires BLERG_CORE_LOCAL_KEY (base64 of 32 random bytes; generate with: openssl rand -base64 32). The key is never stored in the database")
		}
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("BLERG_CORE_LOCAL_KEY is not valid base64: %w", err)
		}
		return keybackend.NewLocal(key)
	case "vault-transit":
		addr := os.Getenv("BLERG_CORE_VAULT_ADDR")
		role := os.Getenv("BLERG_CORE_VAULT_K8S_ROLE")
		transitKey := os.Getenv("BLERG_CORE_VAULT_TRANSIT_KEY")
		if addr == "" || role == "" || transitKey == "" {
			return nil, fmt.Errorf("BLERG_CORE_KEYBACKEND=vault-transit requires BLERG_CORE_VAULT_ADDR, BLERG_CORE_VAULT_K8S_ROLE, and BLERG_CORE_VAULT_TRANSIT_KEY")
		}
		return keybackend.NewVaultTransitK8sAuth(ctx, addr, role, transitKey)
	default:
		return nil, fmt.Errorf("unknown BLERG_CORE_KEYBACKEND %q (only \"local\" and \"vault-transit\" are implemented)", name)
	}
}

// githubReconcileInterval reads BLERG_CORE_GITHUB_RECONCILE_INTERVAL (a
// time.ParseDuration string, e.g. "5m"), defaulting to 5 minutes. An
// unparsable value falls back to the default rather than failing startup,
// since a misconfigured interval shouldn't take down the whole process.
func githubReconcileInterval() time.Duration {
	const defaultInterval = 5 * time.Minute
	v := os.Getenv("BLERG_CORE_GITHUB_RECONCILE_INTERVAL")
	if v == "" {
		return defaultInterval
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Printf("ignoring malformed BLERG_CORE_GITHUB_RECONCILE_INTERVAL %q: %v (using default %s)", v, err, defaultInterval)
		return defaultInterval
	}
	return d
}

// parseAllowedReturnOrigins reads a comma-separated list of origins (scheme://host) from
// BLERG_CORE_ALLOWED_RETURN_ORIGINS — the GET /auth/refresh return_to allowlist (spec §1).
func parseAllowedReturnOrigins() []string {
	v := os.Getenv("BLERG_CORE_ALLOWED_RETURN_ORIGINS")
	if v == "" {
		return nil
	}
	var out []string
	for _, o := range strings.Split(v, ",") {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, o)
		}
	}
	return out
}

// parseOriginAudiences reads BLERG_CORE_ORIGIN_AUDIENCES, a comma-separated list of
// "origin=audience" pairs (e.g. "http://board.example.com=blerg-board,http://runner.example.com=blerg-runner"),
// into the Deps.OriginAudiences map audienceForReturnTo consults.
func parseOriginAudiences() map[string]string {
	v := os.Getenv("BLERG_CORE_ORIGIN_AUDIENCES")
	if v == "" {
		return nil
	}
	out := map[string]string{}
	for _, pair := range strings.Split(v, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		origin, aud, ok := strings.Cut(pair, "=")
		if !ok || origin == "" || aud == "" {
			log.Printf("ignoring malformed BLERG_CORE_ORIGIN_AUDIENCES entry %q (want origin=audience)", pair)
			continue
		}
		out[origin] = aud
	}
	return out
}

// pluginAllowlist reads BLERG_CORE_PLUGIN_MARKETPLACES: the comma-separated GitHub owner/repo
// marketplaces an account may register always-on plugins from ("*" = any valid source).
// Defaults to the official Anthropic marketplace only; malformed entries are dropped, never
// widened into something else.
func pluginAllowlist() pluginspec.Allowlist {
	a, dropped := pluginspec.ParseAllowlist(os.Getenv("BLERG_CORE_PLUGIN_MARKETPLACES"))
	for _, d := range dropped {
		log.Printf("ignoring invalid BLERG_CORE_PLUGIN_MARKETPLACES entry %q (want GitHub owner/repo)", d)
	}
	return a
}

// mcpNetPolicy is the outbound policy for user-supplied MCP server URLs: https only and no
// private addresses, except for the hosts the operator lists in BLERG_CORE_MCP_ALLOW_HTTP_HOSTS
// and BLERG_CORE_MCP_ALLOW_PRIVATE_HOSTS (comma-separated hostnames).
func mcpNetPolicy() netguard.Policy {
	return netguard.Policy{
		AllowHTTPHosts:    netguard.ParseHostList(os.Getenv("BLERG_CORE_MCP_ALLOW_HTTP_HOSTS")),
		AllowPrivateHosts: netguard.ParseHostList(os.Getenv("BLERG_CORE_MCP_ALLOW_PRIVATE_HOSTS")),
	}
}

// sessionTTL reads BLERG_CORE_SESSION_TTL (a time.ParseDuration string, e.g. "720h"),
// defaulting to 720h (30 days) — the same default identity.Service falls back to when
// SetSessionTTL is never called. An unparsable value logs and falls back to the default rather
// than failing boot, matching githubReconcileInterval's convention above.
func sessionTTL() time.Duration {
	const defaultTTL = 720 * time.Hour
	v := os.Getenv("BLERG_CORE_SESSION_TTL")
	if v == "" {
		return defaultTTL
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Printf("ignoring malformed BLERG_CORE_SESSION_TTL %q: %v (using default %s)", v, err, defaultTTL)
		return defaultTTL
	}
	return d
}

// cookieSecure reads BLERG_CORE_COOKIE_SECURE: "false"/"0"/"no" turn the Secure
// attribute off (desktop http://localhost only); anything else keeps it on.
func cookieSecure() bool {
	switch strings.ToLower(os.Getenv("BLERG_CORE_COOKIE_SECURE")) {
	case "false", "0", "no":
		log.Printf("WARNING: BLERG_CORE_COOKIE_SECURE=false — cookies are sent over plain HTTP; only ever use this for http://localhost")
		return false
	}
	return true
}

// behindProxy reads BLERG_CORE_BEHIND_PROXY ("true"/"false"), defaulting to false — see
// api.Deps.BehindProxy / auth_handlers.go's clientIP for why this must default closed (a
// caller-supplied X-Forwarded-For must never be trusted unless the operator says there really
// is a proxy in front of core stripping/setting it).
func behindProxy() bool {
	v := os.Getenv("BLERG_CORE_BEHIND_PROXY")
	return v == "true"
}

// publicURL reads BLERG_CORE_PUBLIC_URL (api.Deps.PublicURL) — core's own browser-facing
// origin, e.g. "https://core.example.com". It's optional for the local provider (publicOrigin
// falls back to deriving the origin from each request when unset), but Task 11's non-local
// providers (github/oidc) need a stable, correct redirect URL that can't be derived
// request-by-request, so it's required — and boot fails fast rather than silently minting
// broken redirect URLs — whenever a non-local provider is active.
func publicURL() string {
	v := os.Getenv("BLERG_CORE_PUBLIC_URL")
	if v == "" && authProviderName() != "local" {
		log.Fatalf("BLERG_CORE_PUBLIC_URL is required when BLERG_CORE_AUTH_PROVIDER=%q", authProviderName())
	}
	return v
}

// loginLimiter reads BLERG_CORE_LOGIN_RATE_LIMIT (default "10/5m"; "0/0" disables).
func loginLimiter() *api.LoginLimiter {
	v := os.Getenv("BLERG_CORE_LOGIN_RATE_LIMIT")
	if v == "" {
		v = "10/5m"
	}
	limit, window, err := api.ParseLoginRateLimit(v)
	if err != nil {
		log.Printf("ignoring malformed BLERG_CORE_LOGIN_RATE_LIMIT %q: %v (using 10/5m)", v, err)
		limit, window = 10, 5*time.Minute
	}
	return api.NewLoginLimiter(limit, window)
}

// buildDeps opens the store, loads (or generates and persists, on first boot) the signing
// key so the kid is stable across restarts (M3, §4.1), and wires the four services. It also
// returns the concrete *identity.Service (not just the narrower api.Deps.Identity interface
// the HTTP layer needs) since main() also passes it directly to
// identity.StartReconcileLoop, which needs RevokeAccountEverywhere — not part of that
// narrower interface.
func buildDeps(ctx context.Context, st *db.PgStore) (api.Deps, *identity.Service, error) {
	kp, err := identity.LoadOrGenerateSigningKey(ctx, st)
	if err != nil {
		return api.Deps{}, nil, err
	}
	pubURL := publicURL()
	authProvider, err := buildAuthProvider(ctx, authProviderName(), st, pubURL)
	if err != nil {
		return api.Deps{}, nil, err
	}
	keyBackend, err := buildKeyBackend(ctx)
	if err != nil {
		return api.Deps{}, nil, err
	}
	identitySvc := identity.NewService(st, kp)
	ttl := sessionTTL()
	identitySvc.SetSessionTTL(ttl)
	return api.Deps{
		Identity:             identitySvc,
		Projects:             projects.NewService(st),
		Registry:             discovery.NewRegistry(st, componentTTL),
		Credentials:          credentials.NewService(st, keyBackend),
		Plugins:              plugins.NewService(st, pluginAllowlist()),
		MCPConnections:       mcpconn.NewService(st, keyBackend, mcpNetPolicy()),
		Audience:             "blerg-core",
		RegisterKey:          os.Getenv("BLERG_CORE_REGISTER_KEY"),
		InternalKey:          os.Getenv("BLERG_CORE_INTERNAL_KEY"),
		Store:                st,
		AuthProvider:         authProvider,
		AllowedReturnOrigins: parseAllowedReturnOrigins(),
		OriginAudiences:      parseOriginAudiences(),
		SessionTTL:           ttl,
		BehindProxy:          behindProxy(),
		PublicURL:            pubURL,
		RevCache:             &api.RevocationCache{},
		LoginLimiter:         loginLimiter(),
		CookieSecure:         cookieSecure(),
	}, identitySvc, nil
}

func main() {
	logsafe.Install()

	if len(os.Args) > 1 && os.Args[1] == "mint" {
		runMint(os.Args[2:])
		return
	}

	// users (create/set-password) dispatches before validateSecrets, same as mint above: it
	// only needs DATABASE_URL to open the store, not the full set of server-boot secrets
	// (BLERG_CORE_REGISTER_KEY / BLERG_CORE_INTERNAL_KEY) validateSecrets checks — those gate
	// HTTP-facing capabilities this CLI path never touches, so requiring them here would block
	// admin recovery (R8) on secrets that may be unset or still placeholders in exactly the
	// "the server won't boot, I need to reset a password" situation this subcommand exists for.
	if len(os.Args) > 1 && os.Args[1] == "users" {
		os.Exit(runUsers(os.Args[2:], os.Stdout))
	}

	if err := validateSecrets(); err != nil {
		log.Fatalf("%v", err)
	}

	if err := serve(context.Background()); err != nil {
		log.Fatal(err)
	}
}

// serve opens the store, wires the service and serves HTTP until the listener fails. It
// returns rather than exiting so the deferred store close runs on every failure path.
func serve(ctx context.Context) error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	addr := os.Getenv("BLERG_CORE_LISTEN")
	if addr == "" {
		addr = ":8080"
	}

	st, err := db.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	deps, identitySvc, err := buildDeps(ctx, st)
	if err != nil {
		return fmt.Errorf("build deps: %w", err)
	}

	// Seed the bootstrap admin account (idempotent no-op once accounts exist) only when the
	// local password provider is actually active — an OIDC/GitHub deployment (Tasks 10/11)
	// has no use for a local password account and must not get one seeded underneath it.
	if authProviderName() == "local" {
		if local, ok := deps.AuthProvider.(*authprovider.Local); ok {
			if err := local.EnsureBootstrapAdmin(ctx); err != nil {
				return fmt.Errorf("ensure bootstrap admin: %w", err)
			}
		}
	}

	// Start the periodic github-provider membership reconcile job only when the github
	// provider is actually active — it's a no-op (and would error trying to list members
	// of an unconfigured org) under any other provider.
	if authProviderName() == "github" {
		if _, ok := deps.AuthProvider.(*authprovider.GitHub); ok {
			org := os.Getenv("BLERG_CORE_GITHUB_ORG")
			token := os.Getenv("BLERG_CORE_GITHUB_TOKEN")
			if token == "" {
				log.Printf("WARNING: BLERG_CORE_GITHUB_TOKEN is unset — the org-membership reconcile loop is NOT running; people who leave org %q keep their access until it is set (needs read:org)", org)
			} else {
				interval := githubReconcileInterval()
				log.Printf("starting github org-membership reconcile loop for org %q every %s", org, interval)
				identity.StartReconcileLoop(ctx, st, identitySvc, org, token, http.DefaultClient, interval)
			}
		}
	}

	mcpconn.StartPruneLoop(ctx, deps.MCPConnections, 24*time.Hour)
	identity.StartExchangePruneLoop(ctx, identitySvc, time.Hour)

	log.Printf("blerg-core listening on %s", addr)
	handler := withSPAFallback(api.NewRouter(deps), spaHandler(webDistDir()))
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}
	return srv.ListenAndServe()
}

// runMint is the `blerg-core mint` CLI subcommand: it mints a core-signed agent token against
// the same store/signing-key path the HTTP server uses (so the token verifies against the
// server's live JWKS), and prints it to stdout. This is an operator/bootstrap tool for testing
// that other components (board, runner) correctly accept core-issued tokens, without needing
// to go through a full session-start flow.
func runMint(args []string) {
	fs := flag.NewFlagSet("mint", flag.ExitOnError)
	aud := fs.String("aud", "", "audience the token is minted for, e.g. blerg-board (required)")
	project := fs.String("project", "", "project the token is scoped to")
	sub := fs.String("sub", "", "subject claim")
	caps := fs.String("caps", "", "comma-separated capability list, e.g. card.read,card.write")
	onBehalfOf := fs.String("on-behalf-of", "", "human/agent this token acts on behalf of")
	lineage := fs.String("lineage", "", "lineage id, for revocation-by-lineage")
	if err := fs.Parse(args); err != nil {
		log.Fatalf("parse flags: %v", err)
	}
	if *aud == "" {
		fmt.Fprintln(os.Stderr, "mint: --aud is required")
		fs.Usage()
		os.Exit(2)
	}

	var capList []string
	if *caps != "" {
		capList = strings.Split(*caps, ",")
	}

	tok, err := mintToken(context.Background(), identity.AgentTokenInput{
		Sub:        *sub,
		Aud:        *aud,
		Project:    *project,
		OnBehalfOf: *onBehalfOf,
		Lineage:    *lineage,
		Caps:       capList,
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(tok)
}

// mintToken opens the store, loads the signing key and mints one token. It returns rather
// than exiting so the deferred store close runs on every failure path.
func mintToken(ctx context.Context, in identity.AgentTokenInput) (string, error) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return "", errors.New("DATABASE_URL is required")
	}
	st, err := db.Open(ctx, dsn)
	if err != nil {
		return "", fmt.Errorf("open store: %w", err)
	}
	defer st.Close()

	kp, err := identity.LoadOrGenerateSigningKey(ctx, st)
	if err != nil {
		return "", fmt.Errorf("load signing key: %w", err)
	}
	tok, err := identity.NewService(st, kp).MintAgentToken(ctx, in)
	if err != nil {
		return "", fmt.Errorf("mint token: %w", err)
	}
	return tok, nil
}
