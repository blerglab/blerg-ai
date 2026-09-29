package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/contracts/agentsmanifest"
	cid "github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/discovery"
	"github.com/blerglab/blerg-ai/core/internal/identity"
	"github.com/blerglab/blerg-ai/core/internal/projects"
	"github.com/blerglab/blerg-ai/core/internal/signing"
)

func farFuture() int64 { return time.Now().Add(time.Hour).Unix() }

// routerTestSchema is distinct from every other package's test schema name so this package's
// isolated schema never collides with theirs, even though all run against the same shared
// DATABASE_URL (audit M-1: this package used to run its tests directly against the shared
// "public" schema with no isolation, which made it order-dependent on whatever other packages'
// tests had left behind — e.g. component rows visible to Aggregate).
const routerTestSchema = "test_blerg_core_api_router"

// routerTestPool connects to DATABASE_URL and gives the caller a fresh, isolated, migrated
// schema: dropped and recreated before the test runs, and dropped again on cleanup. A plain
// "SET search_path" via pool.Exec is NOT enough (pgxpool can hand out any of several underlying
// connections); instead the schema is baked into every connection the returned pool ever opens,
// via ConnConfig.RuntimeParams — the same pattern established in auth_handlers_test.go's
// testPool and identity/accounts_test.go's testPool.
func routerTestPool(t *testing.T) *pgxpool.Pool {
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
	if _, err := setupPool.Exec(ctx, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", routerTestSchema)); err != nil {
		setupPool.Close()
		t.Fatalf("drop schema: %v", err)
	}
	if _, err := setupPool.Exec(ctx, fmt.Sprintf("CREATE SCHEMA %s", routerTestSchema)); err != nil {
		setupPool.Close()
		t.Fatalf("create schema: %v", err)
	}
	setupPool.Close()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = routerTestSchema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		pool.Exec(bg, fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", routerTestSchema))
		pool.Close()
	})
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

// testDeps wires real Deps against an isolated, migrated schema, registering kp's public key
// as an active signing key so JWKS (and therefore requirePrincipal's verification) accepts
// tokens signed with kp. Skips the test if DATABASE_URL is unset.
func testDeps(t *testing.T, kp signing.KeyPair) Deps {
	t.Helper()
	pool := routerTestPool(t)
	ctx := context.Background()
	st := db.NewPgStore(pool)

	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO signing_keys(kid, public_key, private_key, active) VALUES ($1,$2,$3,true)
		 ON CONFLICT (kid) DO UPDATE SET public_key = EXCLUDED.public_key, private_key = EXCLUDED.private_key, active = true`,
		kp.Kid, []byte(kp.Pub), []byte(kp.Priv)); err != nil {
		t.Fatalf("seed signing key: %v", err)
	}

	return Deps{
		Identity: identity.NewService(st, kp),
		Projects: projects.NewService(st),
		Registry: discovery.NewRegistry(st, time.Minute),
		Audience: "blerg-core",
	}
}

// GET / is the SPA's front door: it redirects into /app without touching the DB or auth —
// Deps{} zero value is enough. The SPA itself sends a signed-out visitor on to the login form.
func TestRootRedirectsIntoApp(t *testing.T) {
	h := NewRouter(Deps{Audience: "blerg-core"})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("GET / = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/app" {
		t.Fatalf("GET / Location = %q, want /app", loc)
	}
}

// GET /api/site is public (no principal) and carries the version plus the component URLs
// inverted from OriginAudiences, so the SPA home can render its tiles without a token.
func TestSiteEndpointIsPublic(t *testing.T) {
	t.Setenv("BLERG_VERSION", "v-test")
	h := NewRouter(Deps{Audience: "blerg-core", OriginAudiences: map[string]string{
		"https://board.example.com":  "blerg-board",
		"https://runner.example.com": "blerg-runner",
	}})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/site", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/site = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`"version":"v-test"`, `"board_url":"https://board.example.com"`, `"runner_url":"https://runner.example.com"`} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /api/site body missing %s: %s", want, body)
		}
	}
}

// GET /agents is public: an agent has to be able to read how to obtain a credential before it
// has one, so discovery cannot be gated behind a credential. (It used to require card.read;
// the manifest holds no secrets, only public URLs and documentation.) The document carries the
// contract version, core's own origin, the quickstart, and core's synthetic entry first.
func TestAgentsEndpointIsPublicAndListsCoreFirst(t *testing.T) {
	kp, _ := signing.GenerateKeyPair()
	h := NewRouter(testDeps(t, kp))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/agents", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("anon /agents = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var agg agentsmanifest.AggregateManifest
	if err := json.Unmarshal(rec.Body.Bytes(), &agg); err != nil {
		t.Fatalf("decode /agents: %v", err)
	}
	if agg.ContractVersion != "v1" {
		t.Errorf("contract_version = %q, want v1", agg.ContractVersion)
	}
	// httptest.NewRequest's default host, with no BLERG_CORE_PUBLIC_URL set.
	if agg.CoreBaseURL != "http://example.com" {
		t.Errorf("core_base_url = %q, want http://example.com", agg.CoreBaseURL)
	}
	if !strings.Contains(agg.Quickstart, "http://example.com/settings") {
		t.Errorf("quickstart did not substitute the core origin: %q", agg.Quickstart)
	}
	if strings.Contains(agg.Quickstart, "<core>") {
		t.Errorf("quickstart still carries the <core> placeholder: %q", agg.Quickstart)
	}
	if len(agg.Components) == 0 || agg.Components[0].Name != "blerg-core" {
		t.Fatalf("first component = %+v, want blerg-core", agg.Components)
	}
	core := agg.Components[0]
	if core.Description != "Identity, credentials and discovery for this install" {
		t.Errorf("core description = %q", core.Description)
	}
	if core.OpenAPIURL != "http://example.com/openapi.json" || core.DocsURL != "http://example.com/agents" {
		t.Errorf("core machine-readable URLs wrong: %+v", core)
	}
	if core.Auth == nil || core.Auth.Audience != "blerg-core" ||
		core.Auth.TokenEndpoint != "http://example.com/api/tokens" ||
		len(core.Auth.Presets) != 1 || core.Auth.Presets[0] != "platform" {
		t.Fatalf("core auth wrong: %+v", core.Auth)
	}
	wantOps := map[string]bool{
		"POST /api/tokens": false, "GET /api/tokens": false, "DELETE /api/tokens/{id}": false,
		"GET /api/me": false, "GET /api/credentials": false, "GET /agents": false,
		"GET /openapi.json": false,
	}
	for _, op := range core.Operations {
		wantOps[op.Method+" "+op.Path] = true
	}
	for op, seen := range wantOps {
		if !seen {
			t.Errorf("core entry is missing operation %s", op)
		}
	}
	// EXACTLY those seven: an operation added to the table without being added
	// here (and to openapi.json, which TestOpenAPICoversEveryManifestOperation
	// checks) is a public route nobody decided to publish.
	if len(core.Operations) != len(wantOps) {
		t.Errorf("core entry lists %d operations, want exactly %d: %+v",
			len(core.Operations), len(wantOps), core.Operations)
	}

	// The body depends on Accept and the component list is live, so the
	// response is not something a shared cache may reuse or mix up.
	if got := rec.Header().Get("Vary"); got != "Accept" {
		t.Errorf("Vary = %q, want Accept", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// A registry failure is the operator's problem, not the anonymous caller's:
// /agents is unauthenticated, and the underlying error quotes the query and the
// database's own message.
func TestAgentsEndpointHidesRegistryErrors(t *testing.T) {
	kp, _ := signing.GenerateKeyPair()
	deps := testDeps(t, kp)
	// A registry over a closed pool: Aggregate's query fails for real, with the
	// kind of message ("closed pool", or whatever the driver says) that has no
	// business reaching an anonymous caller.
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	dead, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	dead.Close()
	deps.Registry = discovery.NewRegistry(db.NewPgStore(dead), time.Minute)
	h := NewRouter(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/agents", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("/agents with a broken registry = %d, want 500", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "internal error" {
		t.Errorf("body = %q, want the fixed %q", body, "internal error")
	}
}

// Accept: text/markdown gets the LLM-facing agent guide instead of JSON.
func TestAgentsEndpointRendersMarkdown(t *testing.T) {
	kp, _ := signing.GenerateKeyPair()
	h := NewRouter(testDeps(t, kp))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/agents", nil)
	req.Header.Set("Accept", "text/markdown")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("markdown /agents = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/markdown") {
		t.Errorf("Content-Type = %q, want text/markdown", ct)
	}
	body := rec.Body.String()
	if !strings.HasPrefix(body, "# Blerg — agent guide") {
		t.Fatalf("markdown body does not start with the guide title: %.80q", body)
	}
	for _, want := range []string{"## Quickstart", "## blerg-core", "http://example.com/api/tokens"} {
		if !strings.Contains(body, want) {
			t.Errorf("markdown body missing %q", want)
		}
	}
}

// A component's self-describing fields (description, docs/openapi/mcp URLs, auth, operations)
// survive registration and come back out of /agents intact — with the token endpoint filled in
// by core, which is the one field a component cannot know about itself.
func TestRegisteredComponentManifestRoundTripsThroughAgents(t *testing.T) {
	kp, _ := signing.GenerateKeyPair()
	deps := testDeps(t, kp)
	deps.RegisterKey = "test-register-key"
	h := NewRouter(deps)

	name := fmt.Sprintf("runner-%d", time.Now().UnixNano())
	entry := agentsmanifest.ComponentEntry{
		Name: name, BaseURL: "http://runner:8080", Version: "9.9.9", ContractVersion: "v1",
		Capabilities: []string{"session.start"},
		Description:  "Runs agent sessions",
		DocsURL:      "http://runner:8080/agents",
		OpenAPIURL:   "http://runner:8080/openapi.json",
		MCPURL:       "http://runner:8080/mcp",
		Auth: &agentsmanifest.AuthInfo{
			Audience: "blerg-runner",
			// A component cannot know core's public origin; anything it sends here must be
			// overwritten, so send a wrong one deliberately.
			TokenEndpoint: "http://wrong.invalid/api/tokens",
			Presets:       []string{"run-sessions"},
			Accepts:       []string{"agent_token", "human_session", "runner_key"},
		},
		Operations: []agentsmanifest.Operation{
			{Name: "start_session", Method: "POST", Path: "/api/runner/start", Summary: "Start a session", Cap: "session.start", Idempotent: true},
		},
	}
	body, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/components", strings.NewReader(string(body)))
	req.Header.Set("Authorization", "Bearer test-register-key")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /components = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/agents", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET /agents = %d, want 200", rec2.Code)
	}
	var agg agentsmanifest.AggregateManifest
	if err := json.Unmarshal(rec2.Body.Bytes(), &agg); err != nil {
		t.Fatalf("decode /agents: %v", err)
	}
	var got *agentsmanifest.ComponentEntry
	for i := range agg.Components {
		if agg.Components[i].Name == name {
			got = &agg.Components[i]
		}
	}
	if got == nil {
		t.Fatalf("registered component %q missing from /agents: %+v", name, agg.Components)
	}
	if got.Description != entry.Description || got.DocsURL != entry.DocsURL ||
		got.OpenAPIURL != entry.OpenAPIURL || got.MCPURL != entry.MCPURL {
		t.Errorf("self-describing fields lost: %+v", got)
	}
	if got.Auth == nil || got.Auth.Audience != "blerg-runner" ||
		len(got.Auth.Presets) != 1 || got.Auth.Presets[0] != "run-sessions" ||
		len(got.Auth.Accepts) != 3 {
		t.Fatalf("auth block lost: %+v", got.Auth)
	}
	if got.Auth.TokenEndpoint != "http://example.com/api/tokens" {
		t.Errorf("token_endpoint = %q, want core's own http://example.com/api/tokens", got.Auth.TokenEndpoint)
	}
	if len(got.Operations) != 1 || got.Operations[0].Path != "/api/runner/start" ||
		got.Operations[0].Cap != "session.start" || !got.Operations[0].Idempotent {
		t.Fatalf("operations lost: %+v", got.Operations)
	}
	if got.LastSeen == 0 || got.Stale {
		t.Errorf("core-filled last_seen/stale wrong: last_seen=%d stale=%v", got.LastSeen, got.Stale)
	}
}

func TestMembershipWriteRequiresCapability(t *testing.T) {
	kp, _ := signing.GenerateKeyPair()
	h := NewRouter(testDeps(t, kp))
	// Token WITHOUT membership.write → 403 on POST members.
	tok, _ := kp.Sign(cid.Claims{Sub: "u", Aud: "blerg-core", Kind: "human", Caps: []string{"card.read"}, ExpiresAt: farFuture()})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/p1/members", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no-cap member add = %d, want 403", rec.Code)
	}
}

// A token holds membership.write but is scoped to project A (via the Project claim). It must
// not be able to add members to project B — the handler must check project scope itself,
// since the capability middleware only checks the capability name, not the scope (CRITICAL 1).
func TestMembershipWriteRejectsCrossProjectToken(t *testing.T) {
	kp, _ := signing.GenerateKeyPair()
	h := NewRouter(testDeps(t, kp))
	tok, _ := kp.Sign(cid.Claims{
		Sub: "u", Aud: "blerg-core", Kind: "human", Project: "project-A",
		Caps: []string{"membership.write"}, ExpiresAt: farFuture(),
	})
	body := strings.NewReader(`{"Sub":"victim","Role":"member"}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/project-B/members", body)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-project member add = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// Even within the token's own project scope, an unknown role must be rejected — otherwise a
// caller could persist an arbitrary/bogus role string that bypasses RBAC entirely.
func TestMembershipWriteRejectsUnknownRole(t *testing.T) {
	kp, _ := signing.GenerateKeyPair()
	h := NewRouter(testDeps(t, kp))
	tok, _ := kp.Sign(cid.Claims{
		Sub: "u", Aud: "blerg-core", Kind: "human", Project: "project-A",
		Caps: []string{"membership.write"}, ExpiresAt: farFuture(),
	})
	body := strings.NewReader(`{"Sub":"victim","Role":"superadmin"}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/project-A/members", body)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown-role member add = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestMembershipWriteBodyIsCapped: POST /api/projects/{id}/members must cap its body with
// http.MaxBytesReader before decoding (audit M-4/plan "body caps before decoding"), same as
// every other JSON-decoding handler in this package.
func TestMembershipWriteBodyIsCapped(t *testing.T) {
	kp, _ := signing.GenerateKeyPair()
	h := NewRouter(testDeps(t, kp))
	tok, _ := kp.Sign(cid.Claims{
		Sub: "u", Aud: "blerg-core", Kind: "human", Project: "project-A",
		Caps: []string{"membership.write"}, ExpiresAt: farFuture(),
	})
	body := strings.NewReader(`{"Sub":"` + strings.Repeat("a", 20<<10) + `","Role":"member"}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/project-A/members", body)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized member-add body = %d, want 413", rec.Code)
	}
}

// A same-project token with a known role should still succeed (positive control so the two
// tests above aren't passing for the wrong reason).
func TestMembershipWriteAllowsSameProjectKnownRole(t *testing.T) {
	kp, _ := signing.GenerateKeyPair()
	deps := testDeps(t, kp)
	h := NewRouter(deps)
	proj, err := deps.Projects.Create(context.Background(), fmt.Sprintf("proj-ok-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	projectID := proj.ID
	// No explicit cleanup needed: testDeps's isolated schema (routerTestPool) is dropped
	// entirely on t.Cleanup, which takes this project/its members with it.
	sub := fmt.Sprintf("victim-%d", time.Now().UnixNano())
	tok, _ := kp.Sign(cid.Claims{
		Sub: "u", Aud: "blerg-core", Kind: "human", Project: projectID,
		Caps: []string{"membership.write"}, ExpiresAt: farFuture(),
	})
	body := strings.NewReader(`{"Sub":"` + sub + `","Role":"member"}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/members", body)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("same-project known-role member add = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
}

// brokenStore opens a real store then immediately closes its pool, so any subsequent
// Pool().Query/Exec fails with a "closed pool" error — simulating a transient DB failure
// without needing to actually take Postgres down.
func brokenStore(t *testing.T) db.Store {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	st, err := db.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	st.Close()
	return st
}

// GET /revocations must 500 (not silently serve an empty snapshot) when the DB query fails.
// GET /.well-known/jwks likewise (IMPORTANT 3 & 4).
func TestRevocationsAndJWKSFailClosedOnDBError(t *testing.T) {
	kp, _ := signing.GenerateKeyPair()
	deps := testDeps(t, kp)
	deps.Identity = identity.NewService(brokenStore(t), kp)
	h := NewRouter(deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/revocations", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("/revocations on DB error = %d, want 500", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/.well-known/jwks", nil))
	if rec2.Code != http.StatusInternalServerError {
		t.Fatalf("/.well-known/jwks on DB error = %d, want 500", rec2.Code)
	}
}

// stubIdentity lets tests isolate the revocation-snapshot-error path from the JWKS path: JWKS
// succeeds (so requirePrincipal gets past key lookup and signature verification) while
// RevocationSnapshot fails, in isolation, exactly like a transient Postgres error on that one
// query would. snapshots, when non-nil, is incremented on every RevocationSnapshot call — used
// by TestRevocationSnapshotIsCachedWithinTTL and TestRevocationCacheExpiresAfterTTL to prove
// Deps.RevCache actually memoizes it. revocations, when non-nil, is called fresh on every
// RevocationSnapshot call instead of always returning an empty snapshot — used by
// TestRevocationCacheExpiresAfterTTL to simulate a revocation appearing between two queries.
type stubIdentity struct {
	keys        cid.KeySet
	revErr      error
	snapshots   *int
	revocations func() []identity.Revocation
}

func (s stubIdentity) JWKS(context.Context) (cid.KeySet, error) { return s.keys, nil }
func (s stubIdentity) RevocationSnapshot(context.Context) ([]identity.Revocation, error) {
	if s.snapshots != nil {
		*s.snapshots++
	}
	if s.revErr != nil {
		return nil, s.revErr
	}
	if s.revocations != nil {
		return s.revocations(), nil
	}
	return nil, nil
}

// requirePrincipal must fail closed (503) when the revocation snapshot query errors, rather
// than proceeding with an empty revocation view that would silently treat every revoked token
// as valid (CRITICAL 2).
func TestRequirePrincipalFailsClosedOnRevocationSnapshotError(t *testing.T) {
	kp, _ := signing.GenerateKeyPair()
	deps := Deps{
		Identity: stubIdentity{keys: cid.KeySet{kp.Kid: kp.Pub}, revErr: fmt.Errorf("simulated postgres failure")},
		Projects: nil,
		Registry: nil,
		Audience: "blerg-core",
	}
	// Probed through the middleware itself rather than through a route: /agents, which this
	// test used to call, is public now, and requirePrincipal's fail-closed behaviour is a
	// property of the middleware, not of any one endpoint.
	h := requirePrincipal(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }, deps, "card.read")

	tok, _ := kp.Sign(cid.Claims{Sub: "svc", Aud: "blerg-core", Kind: "service", ExpiresAt: farFuture()})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/guarded", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("guarded route with broken revocation snapshot = %d, want 503", rec.Code)
	}
}

// Sanity check that the happy path (successful snapshot) is unaffected by the fail-closed fix:
// TestAgentsEndpointRequiresPrincipal already exercises this against the real DB (a valid
// token → 200), so no separate stub-based test is needed here.

// TestRevocationSnapshotIsCachedWithinTTL proves Deps.RevCache (audit M-6) actually memoizes
// the revocation snapshot: two authenticated requests within the cache TTL must query the
// underlying snapshot exactly once, and disabling the cache (RevCache == nil) must go back to
// querying on every request.
func TestRevocationSnapshotIsCachedWithinTTL(t *testing.T) {
	kp, _ := signing.GenerateKeyPair()
	deps := testDeps(t, kp) // real Registry/Store so GET /agents can actually succeed (200)
	var snapshots int
	deps.Identity = stubIdentity{keys: cid.KeySet{kp.Kid: kp.Pub}, snapshots: &snapshots}
	deps.RevCache = &RevocationCache{}

	tok, _ := kp.Sign(cid.Claims{Sub: "svc", Aud: "blerg-core", Kind: "service", Caps: []string{"card.read"}, ExpiresAt: farFuture()})
	// Probed through requirePrincipal directly: /agents (the old probe) is public now, so it
	// no longer consults the revocation cache at all.
	guarded := func(d Deps) http.Handler {
		return requirePrincipal(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }, d, "card.read")
	}
	get := func(h http.Handler) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/guarded", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	h := guarded(deps)
	for i := 0; i < 2; i++ {
		if code := get(h); code != http.StatusOK {
			t.Fatalf("request %d = %d, want 200", i, code)
		}
	}
	if snapshots != 1 {
		t.Fatalf("snapshot calls after 2 cached requests = %d, want 1", snapshots)
	}

	// With RevCache == nil, every request queries fresh again — no stale cache smuggled in.
	deps.RevCache = nil
	h2 := guarded(deps)
	if code := get(h2); code != http.StatusOK {
		t.Fatalf("uncached request = %d, want 200", code)
	}
	if snapshots != 2 {
		t.Fatalf("snapshot calls after an uncached request = %d, want 2", snapshots)
	}
}

// TestRevocationCacheExpiresAfterTTL proves the cache actually expires (not just that it caches
// at all): within revCacheTTL a second query is skipped even once a revocation has landed
// server-side (the documented up-to-2s staleness bound), but once the fake clock advances past
// revCacheTTL, the next request re-queries and the revocation is honoured. Uses
// RevocationCache.now (the minor's injectable clock) instead of a real sleep.
func TestRevocationCacheExpiresAfterTTL(t *testing.T) {
	kp, _ := signing.GenerateKeyPair()
	deps := testDeps(t, kp)
	var snapshots int
	var revoked bool
	deps.Identity = stubIdentity{
		keys:      cid.KeySet{kp.Kid: kp.Pub},
		snapshots: &snapshots,
		revocations: func() []identity.Revocation {
			if revoked {
				return []identity.Revocation{{Kind: "sub", Value: "victim"}}
			}
			return nil
		},
	}
	now := time.Now()
	deps.RevCache = &RevocationCache{now: func() time.Time { return now }}
	// requirePrincipal directly: /agents is public now (see the sibling cache test).
	h := requirePrincipal(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }, deps, "card.read")

	tok, _ := kp.Sign(cid.Claims{Sub: "victim", Aud: "blerg-core", Kind: "service", Caps: []string{"card.read"}, ExpiresAt: farFuture()})
	getAgents := func() int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/guarded", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := getAgents(); code != http.StatusOK {
		t.Fatalf("first request = %d, want 200", code)
	}
	if snapshots != 1 {
		t.Fatalf("snapshots after first request = %d, want 1", snapshots)
	}

	// Revoke the subject server-side, but still within the TTL window (fake clock unmoved): the
	// cached (pre-revocation) checker must still be served, matching the documented "up to
	// revCacheTTL to bite" bound.
	revoked = true
	if code := getAgents(); code != http.StatusOK {
		t.Fatalf("second request (within TTL, cache still fresh) = %d, want 200 (cache must not re-query yet)", code)
	}
	if snapshots != 1 {
		t.Fatalf("snapshots after second (still-cached) request = %d, want 1", snapshots)
	}

	// Advance the fake clock past revCacheTTL: the next request must re-query and see the
	// pending revocation take effect.
	now = now.Add(revCacheTTL + time.Millisecond)
	if code := getAgents(); code != http.StatusUnauthorized {
		t.Fatalf("request after TTL expiry with a revocation pending = %d, want 401 (revoked)", code)
	}
	if snapshots != 2 {
		t.Fatalf("snapshots after TTL expiry = %d, want 2 (a fresh query must have run)", snapshots)
	}
}

// A correctly authenticated registration must persist the component so it shows up in
// Aggregate (and therefore GET /agents and the landing page). Component rows no longer need
// explicit per-test cleanup: testDeps's isolated schema (routerTestPool) is dropped on t.Cleanup,
// so a name collision with a prior run or another package is no longer possible either.
func TestComponentRegistrationSucceedsAndAppearsInAggregate(t *testing.T) {
	kp, _ := signing.GenerateKeyPair()
	deps := testDeps(t, kp)
	deps.RegisterKey = "test-register-key"
	h := NewRouter(deps)

	name := fmt.Sprintf("board-%d", time.Now().UnixNano())

	body := fmt.Sprintf(`{"name":%q,"base_url":"http://board:9090","version":"1.2.3","contract_version":"v1","capabilities":["card.read","card.write"]}`, name)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/components", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-register-key")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /components = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	agg, err := deps.Registry.Aggregate(context.Background())
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	var found *agentsmanifest.ComponentEntry
	for i := range agg.Components {
		if agg.Components[i].Name == name {
			found = &agg.Components[i]
		}
	}
	if found == nil {
		t.Fatalf("component %q not found in aggregate: %+v", name, agg.Components)
	}
	if found.BaseURL != "http://board:9090" || found.Version != "1.2.3" || found.ContractVersion != "v1" {
		t.Fatalf("component fields wrong: %+v", found)
	}
}

// Also usable via X-Register-Key instead of Authorization: Bearer.
func TestComponentRegistrationAcceptsXRegisterKeyHeader(t *testing.T) {
	kp, _ := signing.GenerateKeyPair()
	deps := testDeps(t, kp)
	deps.RegisterKey = "test-register-key"
	h := NewRouter(deps)

	name := fmt.Sprintf("runner-%d", time.Now().UnixNano())

	body := fmt.Sprintf(`{"name":%q,"base_url":"http://runner:9091","version":"0.1.0","contract_version":"v1"}`, name)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/components", strings.NewReader(body))
	req.Header.Set("X-Register-Key", "test-register-key")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /components (X-Register-Key) = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

// TestComponentRegistrationBodyIsCapped: POST /components must cap its body with
// http.MaxBytesReader before decoding (audit M-4/plan "body caps before decoding"), same as
// every other JSON-decoding handler in this package — an oversized body must 413, not be
// buffered in full first.
func TestComponentRegistrationBodyIsCapped(t *testing.T) {
	kp, _ := signing.GenerateKeyPair()
	deps := testDeps(t, kp)
	deps.RegisterKey = "test-register-key"
	h := NewRouter(deps)

	name := fmt.Sprintf("big-%d", time.Now().UnixNano())
	body := fmt.Sprintf(`{"name":%q,"base_url":"http://x:1","version":"%s"}`, name, strings.Repeat("a", 20<<10))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/components", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer test-register-key")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized POST /components body = %d, want 413", rec.Code)
	}
}

// Wrong or missing key must not register the component.
func TestComponentRegistrationRejectsBadOrMissingKey(t *testing.T) {
	kp, _ := signing.GenerateKeyPair()
	deps := testDeps(t, kp)
	deps.RegisterKey = "test-register-key"
	h := NewRouter(deps)

	name := fmt.Sprintf("bad-%d", time.Now().UnixNano())
	body := fmt.Sprintf(`{"name":%q,"base_url":"http://x:1"}`, name)

	// Wrong key.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/components", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer nope")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key POST /components = %d, want 401", rec.Code)
	}

	// Missing key entirely.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/components", strings.NewReader(body))
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("missing key POST /components = %d, want 401", rec2.Code)
	}
}

// When BLERG_CORE_REGISTER_KEY (Deps.RegisterKey) is unset, registration must be disabled
// (503) rather than silently allowing unauthenticated registration.
func TestComponentRegistrationDisabledWhenKeyUnset(t *testing.T) {
	kp, _ := signing.GenerateKeyPair()
	deps := testDeps(t, kp)
	deps.RegisterKey = ""
	h := NewRouter(deps)

	body := `{"name":"whatever","base_url":"http://x:1"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/components", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer anything")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST /components with unset key = %d, want 503", rec.Code)
	}
}
