package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/authprovider"
	"github.com/blerglab/blerg-ai/core/internal/db"
)

// mainTestSchema is distinct from every other package's test schema name so this package's
// isolated schema never collides with theirs, even though all run against the same shared
// DATABASE_URL (audit M-1: this package used to run TestBuildDepsWiresRegisterKeyFromEnv
// directly against the shared "public" schema with no isolation).
const mainTestSchema = "test_blerg_core_cmd_main"

// mainTestStore gives the caller a fresh, isolated, migrated schema (dropped and recreated,
// then dropped again on cleanup) wrapped in a *db.PgStore (buildDeps's own parameter type) —
// the same ConnConfig.RuntimeParams pattern established in
// internal/api/auth_handlers_test.go's testPool.
func mainTestStore(t *testing.T) *db.PgStore {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	ctx := context.Background()

	setupPool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setupPool.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", mainTestSchema)); err != nil {
		setupPool.Close()
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := setupPool.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s", mainTestSchema)); err != nil {
		setupPool.Close()
		t.Fatalf("create schema: %v", err)
	}
	setupPool.Close()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = mainTestSchema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		pool.Exec(bg, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", mainTestSchema))
		pool.Close()
	})
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	return db.NewPgStore(pool)
}

func TestHealthz(t *testing.T) {
	srv := newServer(":0")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	srv.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("status field = %q, want ok", body["status"])
	}
}

// Regression test for the Critical review finding: withSPAFallback must never turn a real,
// registered handler's legitimate 404 into a 200 SPA page. GET /auth/callback
// (core/internal/api/auth_handlers.go's handleCallback) always 404s while the local provider
// is active — that's a real, tested business-logic response from a registered route, not an
// unmatched path, and this test exercises it through main.go's actual wired handler chain
// (withSPAFallback(api.NewRouter(deps), spaHandler(...))), not api.NewRouter directly — that's
// exactly the layer api's own auth_handlers_test.go can't cover, and where the bug shipped.
func TestWithSPAFallbackPreservesRealNotFound(t *testing.T) {
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html>spa</html>"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}

	deps := api.Deps{AuthProvider: authprovider.NewLocal(nil), Audience: "blerg-core"}
	handler := withSPAFallback(api.NewRouter(deps), spaHandler(dist))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/auth/callback", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /auth/callback status = %d, want 404 (got body %q)", rec.Code, rec.Body.String())
	}
	if rec.Body.String() == "<html>spa</html>" {
		t.Fatal("GET /auth/callback served the SPA fallback instead of the real 404")
	}
}

// TestWithSPAFallbackServesSPAForUnmatchedPath is the companion positive case: a path that
// genuinely matches no registered pattern (not just one whose handler happens to 404) must
// still fall through to the SPA, unchanged from before the fix.
func TestWithSPAFallbackServesSPAForUnmatchedPath(t *testing.T) {
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html>spa</html>"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}

	deps := api.Deps{AuthProvider: authprovider.NewLocal(nil), Audience: "blerg-core"}
	handler := withSPAFallback(api.NewRouter(deps), spaHandler(dist))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/app/some/client-route", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "<html>spa</html>" {
		t.Fatalf("body = %q, want the SPA index.html", rec.Body.String())
	}
}

// TestSPAFallbackNeverShadowsAPIPaths is the companion to the two withSPAFallback tests above,
// covering the specific bug where a METHOD mismatch (not a genuinely unmatched path) on a
// registered API-ish route was treated as "no pattern matched" and served the SPA with 200 —
// audit finding: an API client hitting a route with the wrong verb must see 404/405, never a
// 200 HTML page it would try (and fail) to JSON-decode.
func TestSPAFallbackNeverShadowsAPIPaths(t *testing.T) {
	dist := t.TempDir()
	os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html>spa</html>"), 0o644)
	h := withSPAFallback(api.NewRouter(api.Deps{}), spaHandler(dist))
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"GET", "/auth/login", http.StatusMethodNotAllowed},
		{"GET", "/internal/credentials/fetch", http.StatusMethodNotAllowed},
		{"GET", "/api/does-not-exist", http.StatusNotFound},
		{"GET", "/app", http.StatusOK},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
}

// buildDeps must read BLERG_CORE_REGISTER_KEY from the environment and wire it into
// api.Deps.RegisterKey — that's what the server uses to gate POST /components. This is the
// same env var an operator sets when booting the server for the curl/registration walkthrough.
func TestBuildDepsWiresRegisterKeyFromEnv(t *testing.T) {
	ctx := context.Background()
	st := mainTestStore(t)

	t.Setenv("BLERG_CORE_REGISTER_KEY", "cli-test-key")
	t.Setenv("BLERG_CORE_LOCAL_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	deps, _, err := buildDeps(ctx, st)
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}
	if deps.RegisterKey != "cli-test-key" {
		t.Fatalf("RegisterKey = %q, want %q", deps.RegisterKey, "cli-test-key")
	}
}

// TestBuildKeyBackendRequiresLocalKey: BLERG_CORE_KEYBACKEND defaults to "local", which must
// fail closed at boot with an actionable message when BLERG_CORE_LOCAL_KEY is unset (R1) — never
// fall back to generating or loading a key from the database.
func TestBuildKeyBackendRequiresLocalKey(t *testing.T) {
	t.Setenv("BLERG_CORE_LOCAL_KEY", "")
	if _, err := buildKeyBackend(context.Background()); err == nil {
		t.Fatal("expected an error when BLERG_CORE_LOCAL_KEY is unset")
	} else if !strings.Contains(err.Error(), "BLERG_CORE_LOCAL_KEY") {
		t.Fatalf("error should name the missing env var, got: %v", err)
	}
}

// TestBuildKeyBackendRejectsMalformedLocalKey covers both malformed-base64 and wrong-length
// inputs. Neither error message may echo the offending value (it's key material).
func TestBuildKeyBackendRejectsMalformedLocalKey(t *testing.T) {
	t.Run("not base64", func(t *testing.T) {
		t.Setenv("BLERG_CORE_LOCAL_KEY", "not-valid-base64!!!")
		_, err := buildKeyBackend(context.Background())
		if err == nil {
			t.Fatal("expected an error for malformed base64")
		}
		if strings.Contains(err.Error(), "not-valid-base64!!!") {
			t.Fatalf("error must not echo the raw value: %v", err)
		}
	})
	t.Run("wrong length", func(t *testing.T) {
		short := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 16))
		t.Setenv("BLERG_CORE_LOCAL_KEY", short)
		_, err := buildKeyBackend(context.Background())
		if err == nil {
			t.Fatal("expected an error for a non-32-byte key")
		}
		if strings.Contains(err.Error(), short) {
			t.Fatalf("error must not echo the raw value: %v", err)
		}
	})
}

// TestBuildKeyBackendSucceedsWithValidLocalKey confirms the happy path: a valid base64-encoded
// 32-byte key produces a working backend with no database involved.
func TestBuildKeyBackendSucceedsWithValidLocalKey(t *testing.T) {
	t.Setenv("BLERG_CORE_LOCAL_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	backend, err := buildKeyBackend(context.Background())
	if err != nil {
		t.Fatalf("buildKeyBackend: %v", err)
	}
	if backend.ID() != "local" {
		t.Fatalf("ID() = %q, want %q", backend.ID(), "local")
	}
}

// TestBuildAuthProviderOIDC covers the provider-selection switch's "oidc" case, which was
// missing entirely: authprovider.NewOIDC existed and worked, but nothing in main.go ever
// called it, so BLERG_CORE_AUTH_PROVIDER=oidc hard-failed at boot with an unknown-provider
// error instead of working.
func TestBuildAuthProviderOIDC(t *testing.T) {
	// An OIDC discovery document is all NewOIDC needs to construct successfully.
	var issuer string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/authorize",
			"token_endpoint":                        issuer + "/token",
			"jwks_uri":                              issuer + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	issuer = srv.URL

	t.Setenv("BLERG_CORE_OIDC_ISSUER_URL", issuer)
	t.Setenv("BLERG_CORE_OIDC_CLIENT_ID", "client-id")
	t.Setenv("BLERG_CORE_OIDC_CLIENT_SECRET", "client-secret")

	p, err := buildAuthProvider(context.Background(), "oidc", nil, "https://core.example.com")
	if err != nil {
		t.Fatalf("buildAuthProvider(oidc) = %v, want a wired provider", err)
	}
	if p.ID() != "oidc" {
		t.Fatalf("provider ID = %q, want oidc", p.ID())
	}
}

// TestBuildAuthProviderOIDCRequiresConfig: a half-configured oidc provider must fail at boot
// with a message naming the missing vars, not construct something that fails on first login.
func TestBuildAuthProviderOIDCRequiresConfig(t *testing.T) {
	t.Setenv("BLERG_CORE_OIDC_ISSUER_URL", "")
	t.Setenv("BLERG_CORE_OIDC_CLIENT_ID", "")
	t.Setenv("BLERG_CORE_OIDC_CLIENT_SECRET", "")

	if _, err := buildAuthProvider(context.Background(), "oidc", nil, "https://core.example.com"); err == nil {
		t.Fatal("buildAuthProvider(oidc) with no config = nil error, want a failure")
	}
}

// TestBuildAuthProviderUnknown: an unrecognised provider name still fails closed.
func TestBuildAuthProviderUnknown(t *testing.T) {
	if _, err := buildAuthProvider(context.Background(), "saml", nil, "https://core.example.com"); err == nil {
		t.Fatal("buildAuthProvider(saml) = nil error, want a failure")
	}
}

// TestBuildAuthProviderGitHubRequiresPublicURL: github must fail to boot without
// BLERG_CORE_PUBLIC_URL, since its OAuth redirect_uri can't be derived per-request.
func TestBuildAuthProviderGitHubRequiresPublicURL(t *testing.T) {
	t.Setenv("BLERG_CORE_GITHUB_CLIENT_ID", "cid")
	t.Setenv("BLERG_CORE_GITHUB_CLIENT_SECRET", "csecret")
	t.Setenv("BLERG_CORE_GITHUB_ORG", "theorg")

	if _, err := buildAuthProvider(context.Background(), "github", nil, ""); err == nil {
		t.Fatal("buildAuthProvider(github) with no BLERG_CORE_PUBLIC_URL = nil error, want a failure")
	}
}

// TestValidateSecrets is R4 (desktop-security I1): a copied install/desktop/.env.example must
// never boot as a live credential. A placeholder BLERG_CORE_REGISTER_KEY must be refused, naming
// the offending variable; once every secret is a real value, validateSecrets must pass.
func TestValidateSecrets(t *testing.T) {
	t.Setenv("BLERG_CORE_REGISTER_KEY", "change-me-register-key")
	t.Setenv("BLERG_CORE_INTERNAL_KEY", "0123456789abcdef0123456789abcdef")
	if err := validateSecrets(); err == nil || !strings.Contains(err.Error(), "BLERG_CORE_REGISTER_KEY") {
		t.Fatalf("placeholder register key must fail naming the var, got %v", err)
	}
	t.Setenv("BLERG_CORE_REGISTER_KEY", "0123456789abcdef0123456789abcdef")
	if err := validateSecrets(); err != nil {
		t.Fatalf("valid secrets: %v", err)
	}
}

// TestUsersCLI exercises `blerg-core users create` and `users set-password` end-to-end against
// a real, migrated database — the operator recovery path this task adds (R8): a lost bootstrap
// admin password, or a second account, must be recoverable without psql or a hand-made bcrypt
// hash.
func TestUsersCLI(t *testing.T) {
	st := mainTestStore(t)
	// runUsers opens its own pool from DATABASE_URL; point it at this test's schema.
	t.Setenv("DATABASE_URL", os.Getenv("DATABASE_URL")+"&options=-csearch_path%3D"+mainTestSchema)
	_ = st
	var out bytes.Buffer
	if code := runUsers([]string{"create", "--subject", "eve", "--role", "member"}, &out); code != 0 {
		t.Fatalf("create exit %d: %s", code, out.String())
	}
	if !regexp.MustCompile(`^provider_subject=eve password=\S{20,}\n$`).MatchString(out.String()) {
		t.Fatalf("unexpected output %q", out.String())
	}
	out.Reset()
	if code := runUsers([]string{"set-password", "--subject", "eve"}, &out); code != 0 || !strings.HasPrefix(out.String(), "provider_subject=eve password=") {
		t.Fatalf("set-password exit %d: %s", code, out.String())
	}
	out.Reset()
	if code := runUsers([]string{"create", "--subject", "x", "--role", "owner"}, &out); code != 2 {
		t.Fatalf("bad role exit %d, want 2", code)
	}
}

// Regression test for the production-only 404 on DELETE /api/tokens/{id} (and every other
// core route with a path wildcard): withSPAFallback used to invoke the handler that
// (*http.ServeMux).Handler returned, but only (*http.ServeMux).ServeHTTP binds a pattern's
// wildcards onto the request, so r.PathValue("id") was empty behind the wrapper while every
// router-level test (which serves the mux directly) passed. The wrapper must hand matched
// requests to the mux itself.
func TestWithSPAFallbackBindsPathValues(t *testing.T) {
	dist := t.TempDir()
	if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte("<html>spa</html>"), 0o644); err != nil {
		t.Fatalf("write index.html: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/things/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "id="+r.PathValue("id"))
	})
	handler := withSPAFallback(mux, spaHandler(dist))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/things/abc-123", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "id=abc-123" {
		t.Fatalf("wrapped wildcard route: code=%d body=%q, want 200 id=abc-123", rec.Code, rec.Body.String())
	}
	// The SPA fallback still answers a genuinely unmatched client route.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app/some/client/route", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "spa") {
		t.Fatalf("SPA fallback: code=%d body=%q", rec.Code, rec.Body.String())
	}
}
