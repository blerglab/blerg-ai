package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

// TestCredentialsGitReadOnlyOwnerKinds: BLG-460's one-token-per-owner shape. An account can
// hold more than one of these at once (one per GitHub/GitLab owner), unlike the single-slot
// "github"/"gitlab" kind tested above — storing a second owner must not replace the first, and
// deleting one must leave the other.
func TestCredentialsGitReadOnlyOwnerKinds(t *testing.T) {
	deps, pool, idSvc := newCredentialsTestDeps(t)
	// A fake GitHub host: finding 2's validation call must succeed for POST to store the
	// credential at all, and must never reach the real api.github.com in a test.
	fakeGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer fakeGitHub.Close()
	deps.GitHubAPIBase = fakeGitHub.URL
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()

	accountID := insertCredAccount(t, pool, "cred-user-git-readonly")
	token, err := idSvc.MintHumanAccessToken(context.Background(), accountID, "blerg-core")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	authed := func(method, path string, body []byte) *http.Response {
		var r io.Reader
		if body != nil {
			r = bytes.NewReader(body)
		}
		req, _ := http.NewRequest(method, srv.URL+path, r)
		req.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	for _, owner := range []string{"acme-corp", "personal-account"} {
		engine := "git-readonly:github:" + owner
		body, _ := json.Marshal(map[string]string{"engine": engine, "credential": "FAKE-readonly-" + owner})
		resp := authed(http.MethodPost, "/api/credentials", body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST /api/credentials engine=%s = %d, want 200", engine, resp.StatusCode)
		}
	}

	listResp := authed(http.MethodGet, "/api/credentials", nil)
	defer listResp.Body.Close()
	var listed []map[string]any
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected both owners' tokens listed, got %+v", listed)
	}
	seen := map[string]bool{}
	for _, entry := range listed {
		seen[entry["engine"].(string)] = true
		if _, hasToken := entry["credential"]; hasToken {
			t.Fatalf("credential value must never be listed: %+v", entry)
		}
	}
	if !seen["git-readonly:github:acme-corp"] || !seen["git-readonly:github:personal-account"] {
		t.Fatalf("expected both owner engines present, got %+v", listed)
	}

	// Deleting one owner's token must not touch the other's.
	delResp := authed(http.MethodDelete, "/api/credentials/"+url.PathEscape("git-readonly:github:acme-corp"), nil)
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE acme-corp = %d, want 200", delResp.StatusCode)
	}
	listResp2 := authed(http.MethodGet, "/api/credentials", nil)
	defer listResp2.Body.Close()
	var listed2 []map[string]any
	if err := json.NewDecoder(listResp2.Body).Decode(&listed2); err != nil {
		t.Fatal(err)
	}
	if len(listed2) != 1 || listed2[0]["engine"] != "git-readonly:github:personal-account" {
		t.Fatalf("expected only personal-account's token to remain, got %+v", listed2)
	}
}

// TestCredentialsGitReadOnlyKindShapeRejectsInvalid: a malformed owner, or a provider other
// than github/gitlab, must be refused rather than silently stored under an unvalidated key.
func TestCredentialsGitReadOnlyKindShapeRejectsInvalid(t *testing.T) {
	deps, pool, idSvc := newCredentialsTestDeps(t)
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()

	accountID := insertCredAccount(t, pool, "cred-user-git-readonly-invalid")
	token, err := idSvc.MintHumanAccessToken(context.Background(), accountID, "blerg-core")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	for _, engine := range []string{
		"git-readonly:bitbucket:acme", // not a supported provider
		"git-readonly:github:",        // empty owner
		"git-readonly:github",         // missing owner segment entirely
	} {
		body, _ := json.Marshal(map[string]string{"engine": engine, "credential": "FAKE-token"})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/credentials", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("POST /api/credentials engine=%q = %d, want 400", engine, resp.StatusCode)
		}
	}
}

// TestCredentialsGitReadOnlyRefusesClassicGitHubToken: BLG-460 security re-review finding 2.
// A classic token (the "ghp_" prefix alone is enough) must be refused end to end — never stored,
// never even reaching the fake GitHub host, since the prefix check runs before the request.
func TestCredentialsGitReadOnlyRefusesClassicGitHubToken(t *testing.T) {
	deps, pool, idSvc := newCredentialsTestDeps(t)
	calls := 0
	fakeGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer fakeGitHub.Close()
	deps.GitHubAPIBase = fakeGitHub.URL
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()

	accountID := insertCredAccount(t, pool, "cred-user-git-readonly-classic")
	token, err := idSvc.MintHumanAccessToken(context.Background(), accountID, "blerg-core")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	body, _ := json.Marshal(map[string]string{"engine": "git-readonly:github:acme-corp", "credential": "ghp_classictoken1234567890"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/credentials", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST classic token = %d, want 400; body=%s", resp.StatusCode, respBody)
	}
	if !strings.Contains(string(respBody), "Classic GitHub tokens can't be limited to read") {
		t.Fatalf("expected the short refusal reason, got %q", respBody)
	}
	if strings.Contains(string(respBody), "fine-grained token's permissions") {
		t.Fatalf("the refusal must not repeat the section's whole description paragraph, got %q", respBody)
	}
	if calls != 0 {
		t.Fatalf("expected the ghp_ prefix to short-circuit before any call to GitHub, got %d calls", calls)
	}

	listResp, err := authedGet(t, srv, token, "/api/credentials")
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()
	var listed []map[string]any
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("a refused token must never be stored, got %+v", listed)
	}
}

// TestCredentialsGitReadOnlyOwnerLowercasedOnStoreAndLookup: BLG-460 security re-review finding
// 3. Storing "Acme-Corp" and later deleting "acme-corp" (or vice versa) must act on the exact
// same row — the owner is always lowercased before it touches the vault.
func TestCredentialsGitReadOnlyOwnerLowercasedOnStoreAndLookup(t *testing.T) {
	deps, pool, idSvc := newCredentialsTestDeps(t)
	fakeGitHub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer fakeGitHub.Close()
	deps.GitHubAPIBase = fakeGitHub.URL
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()

	accountID := insertCredAccount(t, pool, "cred-user-git-readonly-case")
	token, err := idSvc.MintHumanAccessToken(context.Background(), accountID, "blerg-core")
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	body, _ := json.Marshal(map[string]string{"engine": "git-readonly:github:Acme-Corp", "credential": "github_pat_fixture"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/credentials", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST mixed-case owner = %d, want 200", resp.StatusCode)
	}

	listResp, err := authedGet(t, srv, token, "/api/credentials")
	if err != nil {
		t.Fatal(err)
	}
	var listed []map[string]any
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	listResp.Body.Close()
	if len(listed) != 1 || listed[0]["engine"] != "git-readonly:github:acme-corp" {
		t.Fatalf("expected the stored engine key to be lowercased, got %+v", listed)
	}

	// Delete using the ORIGINAL mixed-case owner: must still find and remove the (lowercased) row.
	delReq, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/credentials/"+url.PathEscape("git-readonly:github:Acme-Corp"), nil)
	delReq.Header.Set("Authorization", "Bearer "+token)
	delResp, err := srv.Client().Do(delReq)
	if err != nil {
		t.Fatal(err)
	}
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE mixed-case owner = %d, want 200", delResp.StatusCode)
	}
	listResp2, err := authedGet(t, srv, token, "/api/credentials")
	if err != nil {
		t.Fatal(err)
	}
	defer listResp2.Body.Close()
	var listed2 []map[string]any
	if err := json.NewDecoder(listResp2.Body).Decode(&listed2); err != nil {
		t.Fatal(err)
	}
	if len(listed2) != 0 {
		t.Fatalf("expected the row to be gone after deleting by its mixed-case owner, got %+v", listed2)
	}
}

func authedGet(t *testing.T, srv *httptest.Server, token, path string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return srv.Client().Do(req)
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

// Deleting old vault material is independent of whether its importer remains
// registered. The owner may remove it, without granting a storage sink or a way
// to discover/delete another person's entries.
func TestDeleteLegacyCredentialRemainsOwnerScoped(t *testing.T) {
	deps, pool, idSvc := newCredentialsTestDeps(t)
	owner := insertCredAccount(t, pool, "legacy-owner")
	other := insertCredAccount(t, pool, "legacy-other")
	token, err := idSvc.MintHumanAccessToken(t.Context(), owner, "blerg-core")
	if err != nil {
		t.Fatal(err)
	}
	router := api.NewRouter(deps)
	for _, engine := range []string{"openclaw", "removed-engine"} {
		for _, account := range []string{owner, other} {
			if err := deps.Credentials.Store(t.Context(), account, engine, []byte("fixture-private")); err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(http.MethodDelete, "/api/credentials/"+engine, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("legacy deletion %s: status %d", engine, rec.Code)
		}
		if _, err := deps.Credentials.Fetch(t.Context(), owner, engine); !errors.Is(err, credentials.ErrNotFound) {
			t.Fatal("owner legacy entry survived")
		}
		if _, err := deps.Credentials.Fetch(t.Context(), other, engine); err != nil {
			t.Fatal("other owner's entry was altered")
		}
		raw, _ := json.Marshal(map[string]string{"engine": engine, "credential": "fixture-private"})
		req = httptest.NewRequest(http.MethodPost, "/api/credentials", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+token)
		rec = httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatal("deletion compatibility widened unsupported imports")
		}
	}
	if err := deps.Credentials.Store(t.Context(), other, "unregistered", []byte("fixture-private")); err != nil {
		t.Fatal(err)
	}
	for _, engine := range []string{"unregistered", "BAD-ID"} {
		req := httptest.NewRequest(http.MethodDelete, "/api/credentials/"+engine, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatal("unknown or malformed deletion accepted without an owner entry")
		}
	}
}
