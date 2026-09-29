package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cid "github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/discovery"
	"github.com/blerglab/blerg-ai/core/internal/identity"
	"github.com/blerglab/blerg-ai/core/internal/projects"
	"github.com/blerglab/blerg-ai/core/internal/signing"
)

// TestRevocationSnapshotCarriesRevokedAt: Revoke stamps revoked_at, RevocationSnapshot
// returns it, and GET /revocations emits it as RFC3339 "revoked_at" next to kind/value —
// the wire shape board/runner's coreauth clients parse.
func TestRevocationSnapshotCarriesRevokedAt(t *testing.T) {
	kp, _ := signing.GenerateKeyPair()
	deps := testDeps(t, kp)
	svc := deps.Identity.(*identity.Service)
	ctx := context.Background()

	before := time.Now().Add(-2 * time.Second)
	if err := svc.Revoke(ctx, "sub", "victim"); err != nil {
		t.Fatal(err)
	}
	snap, err := svc.RevocationSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got *identity.Revocation
	for i := range snap {
		if snap[i].Kind == "sub" && snap[i].Value == "victim" {
			got = &snap[i]
		}
	}
	if got == nil {
		t.Fatal("revoked sub missing from snapshot")
	}
	if got.RevokedAt.IsZero() || got.RevokedAt.Before(before) || got.RevokedAt.After(time.Now().Add(2*time.Second)) {
		t.Fatalf("RevokedAt = %v, want ~now", got.RevokedAt)
	}

	// Re-revoking moves the timestamp forward, never leaves the original.
	time.Sleep(20 * time.Millisecond)
	if err := svc.Revoke(ctx, "sub", "victim"); err != nil {
		t.Fatal(err)
	}
	snap2, _ := svc.RevocationSnapshot(ctx)
	for _, r := range snap2 {
		if r.Kind == "sub" && r.Value == "victim" && !r.RevokedAt.After(got.RevokedAt) {
			t.Fatalf("re-revoke left revoked_at at %v (was %v)", r.RevokedAt, got.RevokedAt)
		}
	}

	h := NewRouter(deps)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/revocations", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /revocations = %d", rec.Code)
	}
	var wire []struct {
		Kind      string `json:"kind"`
		Value     string `json:"value"`
		RevokedAt string `json:"revoked_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &wire); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body.String())
	}
	var found bool
	for _, w := range wire {
		if w.Kind == "sub" && w.Value == "victim" {
			found = true
			if _, err := time.Parse(time.RFC3339Nano, w.RevokedAt); err != nil {
				t.Fatalf("revoked_at %q is not RFC3339: %v", w.RevokedAt, err)
			}
		}
	}
	if !found {
		t.Fatalf("sub:victim not in wire output: %s", rec.Body.String())
	}
}

// TestRevokedSubAppliesOnlyToTokensIssuedBefore: core's own snapshot checker scopes a
// "sub" revocation by the token's iat — a token issued after revoked_at is accepted on
// core's endpoints, one issued at or before it is not.
func TestRevokedSubAppliesOnlyToTokensIssuedBefore(t *testing.T) {
	kp, _ := signing.GenerateKeyPair()
	// testDeps does not expose the pool; build the same deps by hand so the test can
	// pin revoked_at directly in the table.
	pool := routerTestPool(t)
	ctx := context.Background()
	st := db.NewPgStore(pool)
	if _, err := pool.Exec(ctx,
		`INSERT INTO signing_keys(kid, public_key, private_key, active) VALUES ($1,$2,$3,true)`,
		kp.Kid, []byte(kp.Pub), []byte(kp.Priv)); err != nil {
		t.Fatalf("seed signing key: %v", err)
	}
	svc := identity.NewService(st, kp)
	deps := Deps{
		Identity: svc,
		Projects: projects.NewService(st),
		Registry: discovery.NewRegistry(st, time.Minute),
		Audience: "blerg-core",
	}
	if err := svc.Revoke(ctx, "sub", "victim"); err != nil {
		t.Fatal(err)
	}
	// Pin the revocation 10 s in the past so "issued after" needs no sleep.
	if _, err := pool.Exec(ctx,
		`UPDATE revocations SET revoked_at = now() - interval '10 seconds' WHERE kind = 'sub' AND value = 'victim'`); err != nil {
		t.Fatal(err)
	}
	var revokedAt time.Time
	if err := pool.QueryRow(ctx,
		`SELECT revoked_at FROM revocations WHERE kind = 'sub' AND value = 'victim'`).Scan(&revokedAt); err != nil {
		t.Fatal(err)
	}
	// A capability-gated handler behind requirePrincipal, rather than a route: /agents (this
	// test's original probe) is public now, so it never consults the revocation list.
	h := requirePrincipal(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }, deps, "card.read")
	getAgents := func(iat int64) int {
		tok, _ := kp.Sign(cid.Claims{Sub: "victim", Aud: "blerg-core", Kind: "service",
			Caps: []string{"card.read"}, IssuedAt: iat, ExpiresAt: farFuture()})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/guarded", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := getAgents(revokedAt.Unix() - 1); code != http.StatusUnauthorized {
		t.Fatalf("token issued before revocation = %d, want 401", code)
	}
	if code := getAgents(revokedAt.Unix()); code != http.StatusUnauthorized {
		t.Fatalf("token issued in the revocation's second = %d, want 401 (equality fails closed)", code)
	}
	if code := getAgents(revokedAt.Unix() + 1); code != http.StatusOK {
		t.Fatalf("token issued after revocation = %d, want 200", code)
	}
}
