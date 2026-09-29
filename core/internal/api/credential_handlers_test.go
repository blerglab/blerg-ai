package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/credentials"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/identity"
	"github.com/blerglab/blerg-ai/core/internal/keybackend"
)

// newCredentialsTestDeps builds on newTestDeps (this package's shared helper, defined in
// auth_handlers_test.go) by also wiring a real credentials.Service backed by the "local"
// keybackend against the same pool/schema.
func newCredentialsTestDeps(t *testing.T) (api.Deps, *pgxpool.Pool, *identity.Service) {
	t.Helper()
	deps, pool := newTestDeps(t, []string{"http://board.example.com"}, nil)
	st := db.NewPgStore(pool)
	backend, err := keybackend.NewLocal(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	deps.Credentials = credentials.NewService(st, backend)
	idSvc, ok := deps.Identity.(*identity.Service)
	if !ok {
		t.Fatal("deps.Identity is not *identity.Service")
	}
	return deps, pool, idSvc
}

// insertCredAccount inserts a minimal local-provider account with role "member" and returns its
// id (the value MintHumanAccessToken will sign as Sub).
func insertCredAccount(t *testing.T, pool *pgxpool.Pool, subject string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(),
		`INSERT INTO accounts (provider, provider_subject, email, role) VALUES ('local',$1,$1||'@example.com','member') RETURNING id::text`,
		subject).Scan(&id)
	if err != nil {
		t.Fatalf("insert account: %v", err)
	}
	return id
}

// TestCredentialsRejectMissingOrInvalidToken covers all three endpoints with no Authorization
// header and with a garbage bearer token — both must fail closed (401), never fall through to
// the handler.
func TestCredentialsRejectMissingOrInvalidToken(t *testing.T) {
	deps, _, _ := newCredentialsTestDeps(t)
	router := api.NewRouter(deps)
	srv := httptest.NewServer(router)
	defer srv.Close()

	cases := []struct {
		name   string
		method string
		path   string
		body   string
		auth   string
	}{
		{"POST no token", http.MethodPost, "/api/credentials", `{"engine":"claude","credential":"x"}`, ""},
		{"POST bad token", http.MethodPost, "/api/credentials", `{"engine":"claude","credential":"x"}`, "Bearer not-a-real-token"},
		{"GET no token", http.MethodGet, "/api/credentials", "", ""},
		{"GET bad token", http.MethodGet, "/api/credentials", "", "Bearer not-a-real-token"},
		{"DELETE no token", http.MethodDelete, "/api/credentials/claude", "", ""},
		{"DELETE bad token", http.MethodDelete, "/api/credentials/claude", "", "Bearer not-a-real-token"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(tc.method, srv.URL+tc.path, bytes.NewBufferString(tc.body))
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s %s = %d, want 401", tc.method, tc.path, resp.StatusCode)
			}
		})
	}
}

// TestCredentialsStoreListDeleteViaHTTP is the end-to-end happy path against real Postgres +
// the local keybackend: mint a human access token for an account, store a credential, list it
// back (metadata only), delete it, and confirm the list is empty again.
func TestCredentialsStoreListDeleteViaHTTP(t *testing.T) {
	deps, pool, idSvc := newCredentialsTestDeps(t)
	router := api.NewRouter(deps)
	srv := httptest.NewServer(router)
	defer srv.Close()

	accountID := insertCredAccount(t, pool, "cred-user")
	token, err := idSvc.MintHumanAccessToken(context.Background(), accountID, "blerg-core")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	// POST
	body, _ := json.Marshal(map[string]string{"engine": "claude", "credential": "sk-ant-super-secret"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/credentials", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/credentials = %d, want 200", resp.StatusCode)
	}

	// The DB row must hold ciphertext, never the plaintext credential.
	var ciphertext []byte
	if err := pool.QueryRow(context.Background(),
		`SELECT ciphertext FROM user_credentials WHERE account_id=$1 AND engine='claude'`, accountID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("sk-ant-super-secret")) {
		t.Fatal("stored ciphertext must not contain the plaintext credential")
	}

	// GET
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/credentials", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	resp2, err := srv.Client().Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/credentials = %d, want 200", resp2.StatusCode)
	}
	var listed []map[string]any
	if err := json.NewDecoder(resp2.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0]["engine"] != "claude" {
		t.Fatalf("GET /api/credentials body = %+v, want one {engine:claude,...} entry", listed)
	}
	if _, hasCiphertext := listed[0]["ciphertext"]; hasCiphertext {
		t.Fatal("response must never include a ciphertext field")
	}
	if _, hasCredential := listed[0]["credential"]; hasCredential {
		t.Fatal("response must never include a credential (plaintext) field")
	}

	// DELETE
	req3, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/credentials/claude", nil)
	req3.Header.Set("Authorization", "Bearer "+token)
	resp3, err := srv.Client().Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("DELETE /api/credentials/claude = %d, want 200", resp3.StatusCode)
	}

	req4, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/credentials", nil)
	req4.Header.Set("Authorization", "Bearer "+token)
	resp4, err := srv.Client().Do(req4)
	if err != nil {
		t.Fatal(err)
	}
	defer resp4.Body.Close()
	var listed2 []map[string]any
	if err := json.NewDecoder(resp4.Body).Decode(&listed2); err != nil {
		t.Fatal(err)
	}
	if len(listed2) != 0 {
		t.Fatalf("GET after delete = %+v, want empty", listed2)
	}
}

// TestCredentialsGitProviderKinds proves every git-provider kind ("github", "gitlab") is a
// first-class credential kind in the vault: it can be stored, shows up in the caller's own
// listing, decrypts back to exactly what was stored, and can be deleted — the same lifecycle the
// engine kinds have. The runner needs them so a cluster session pod can clone and push as the
// launching user, and to list that user's own repositories.
func TestCredentialsGitProviderKinds(t *testing.T) {
	for _, kind := range []string{"github", "gitlab"} {
		t.Run(kind, func(t *testing.T) {
			deps, pool, idSvc := newCredentialsTestDeps(t)
			srv := httptest.NewServer(api.NewRouter(deps))
			defer srv.Close()

			accountID := insertCredAccount(t, pool, "cred-user-"+kind)
			token, err := idSvc.MintHumanAccessToken(context.Background(), accountID, "blerg-core")
			if err != nil {
				t.Fatalf("mint: %v", err)
			}

			secret := "FAKE-" + kind + "-token-not-real"
			body, _ := json.Marshal(map[string]string{"engine": kind, "credential": secret})
			req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/credentials", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("POST /api/credentials engine=%s = %d, want 200", kind, resp.StatusCode)
			}

			// The vault round trip: what decrypts back is exactly what went in.
			plain, err := deps.Credentials.Fetch(context.Background(), accountID, kind)
			if err != nil || string(plain) != secret {
				t.Fatalf("vault Fetch(%s) = %d bytes, err %v; want the stored value", kind, len(plain), err)
			}

			req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/credentials", nil)
			req2.Header.Set("Authorization", "Bearer "+token)
			resp2, err := srv.Client().Do(req2)
			if err != nil {
				t.Fatal(err)
			}
			defer resp2.Body.Close()
			var listed []map[string]any
			if err := json.NewDecoder(resp2.Body).Decode(&listed); err != nil {
				t.Fatal(err)
			}
			if len(listed) != 1 || listed[0]["engine"] != kind {
				t.Fatalf("GET /api/credentials body = %+v, want one {engine:%s,...} entry", listed, kind)
			}

			req3, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/credentials/"+kind, nil)
			req3.Header.Set("Authorization", "Bearer "+token)
			resp3, err := srv.Client().Do(req3)
			if err != nil {
				t.Fatal(err)
			}
			resp3.Body.Close()
			if resp3.StatusCode != http.StatusOK {
				t.Fatalf("DELETE /api/credentials/%s = %d, want 200", kind, resp3.StatusCode)
			}
		})
	}
}

// TestCredentialsCrossAccountIsolation is the load-bearing proof of the §5 security
// requirement: accountID is derived ONLY from the verified token's Sub claim, never from any
// request parameter. Account B stores a credential for engine "claude"; account A (a
// DIFFERENT, legitimately-authenticated caller) then lists and deletes its OWN credentials —
// there is no request field through which A could even attempt to name B's account, so this
// test proves the isolation by showing A's authenticated view (list, and a delete of the same
// engine name) never touches or removes B's row.
func TestCredentialsCrossAccountIsolation(t *testing.T) {
	deps, pool, idSvc := newCredentialsTestDeps(t)
	router := api.NewRouter(deps)
	srv := httptest.NewServer(router)
	defer srv.Close()

	accountA := insertCredAccount(t, pool, "cred-user-a")
	accountB := insertCredAccount(t, pool, "cred-user-b")
	tokenA, err := idSvc.MintHumanAccessToken(context.Background(), accountA, "blerg-core")
	if err != nil {
		t.Fatalf("mint A: %v", err)
	}
	tokenB, err := idSvc.MintHumanAccessToken(context.Background(), accountB, "blerg-core")
	if err != nil {
		t.Fatalf("mint B: %v", err)
	}

	// B stores a "claude" credential.
	body, _ := json.Marshal(map[string]string{"engine": "claude", "credential": "b-secret-token"})
	reqB, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/credentials", bytes.NewReader(body))
	reqB.Header.Set("Authorization", "Bearer "+tokenB)
	reqB.Header.Set("Content-Type", "application/json")
	respB, err := srv.Client().Do(reqB)
	if err != nil {
		t.Fatal(err)
	}
	respB.Body.Close()
	if respB.StatusCode != http.StatusOK {
		t.Fatalf("B store = %d, want 200", respB.StatusCode)
	}

	// A lists using A's own token: must see nothing of B's.
	reqListA, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/credentials", nil)
	reqListA.Header.Set("Authorization", "Bearer "+tokenA)
	respListA, err := srv.Client().Do(reqListA)
	if err != nil {
		t.Fatal(err)
	}
	defer respListA.Body.Close()
	var listedA []map[string]any
	if err := json.NewDecoder(respListA.Body).Decode(&listedA); err != nil {
		t.Fatal(err)
	}
	if len(listedA) != 0 {
		t.Fatalf("A's list = %+v, want empty (B's credential must not leak to A)", listedA)
	}

	// A attempts to delete "claude" using A's own token (the only engine name the request
	// surface accepts is a path segment — there is no account parameter to forge). This must
	// not remove B's row: accountID is derived from A's token, not from anything A can name.
	reqDelA, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/credentials/claude", nil)
	reqDelA.Header.Set("Authorization", "Bearer "+tokenA)
	respDelA, err := srv.Client().Do(reqDelA)
	if err != nil {
		t.Fatal(err)
	}
	respDelA.Body.Close()
	if respDelA.StatusCode != http.StatusOK {
		t.Fatalf("A delete claude (no-op, A has none) = %d, want 200", respDelA.StatusCode)
	}

	// B's row must still be there, proving A's delete (scoped to A's own account) never
	// touched it.
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM user_credentials WHERE account_id=$1 AND engine='claude'`, accountB).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("B's credential row count = %d, want 1 (A's operations must never affect B's account)", count)
	}
}

// TestCredentialsRejectNonHumanToken: an agent-kind token (correct signature, correct
// audience, not revoked) must still be rejected — /api/credentials is gated on a human access
// token specifically, not just any validly-signed principal.
func TestCredentialsRejectNonHumanToken(t *testing.T) {
	deps, _, idSvc := newCredentialsTestDeps(t)
	router := api.NewRouter(deps)
	srv := httptest.NewServer(router)
	defer srv.Close()

	agentToken, err := idSvc.MintAgentToken(context.Background(), identity.AgentTokenInput{
		Sub: "some-agent", Aud: "blerg-core", Caps: []string{"card.read"},
	})
	if err != nil {
		t.Fatalf("mint agent token: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/credentials", nil)
	req.Header.Set("Authorization", "Bearer "+agentToken)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("GET /api/credentials with agent token = %d, want 403", resp.StatusCode)
	}
}
