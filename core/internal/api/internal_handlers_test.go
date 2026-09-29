package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

const testInternalKey = "test-internal-key-do-not-use-in-prod"

// newInternalTestDeps builds on newCredentialsTestDeps, additionally wiring InternalKey so
// POST /internal/credentials/fetch is enabled.
func newInternalTestDeps(t *testing.T) (api.Deps, *pgxpool.Pool) {
	t.Helper()
	deps, pool, _ := newCredentialsTestDeps(t)
	deps.InternalKey = testInternalKey
	deps.Store = db.NewPgStore(pool)
	return deps, pool
}

// insertLiveHumanSession inserts a human_sessions row for accountID with revoked_at NULL (a
// "live" session) and returns its id.
func insertLiveHumanSession(t *testing.T, pool *pgxpool.Pool, accountID string) string {
	t.Helper()
	var id string
	// Like IssueRefreshToken: chain_id starts as the row's own id, which is what the access
	// token's `sid` claim carries and what the internal endpoints are given as session_id.
	err := pool.QueryRow(context.Background(),
		`INSERT INTO human_sessions (account_id, token_hash, chain_id) VALUES ($1, $2, gen_random_uuid()) RETURNING id::text`,
		accountID, "tokenhash-"+accountID+"-"+time.Now().Format("150405.000000000")).Scan(&id)
	if err == nil {
		_, err = pool.Exec(context.Background(), `UPDATE human_sessions SET chain_id = id WHERE id = $1`, id)
	}
	if err != nil {
		t.Fatalf("insert human session: %v", err)
	}
	return id
}

// noSuchSession is a well-formed session id no human_sessions row has.
const noSuchSession = "00000000-0000-4000-8000-00000000dead"

// doFetch posts a fetch naming sessionID (the browser session that authorises it; "" omits it).
func doFetch(t *testing.T, srv *httptest.Server, authHeader, accountID, engine, sessionID string) *http.Response {
	t.Helper()
	m := map[string]string{"account_id": accountID, "engine": engine}
	if sessionID != "" {
		m["session_id"] = sessionID
	}
	body, _ := json.Marshal(m)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/credentials/fetch", bytes.NewReader(body))
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func doListCredentials(t *testing.T, srv *httptest.Server, authHeader, accountID, sessionID string) *http.Response {
	t.Helper()
	m := map[string]string{"account_id": accountID}
	if sessionID != "" {
		m["session_id"] = sessionID
	}
	body, _ := json.Marshal(m)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/internal/credentials/list", bytes.NewReader(body))
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestInternalListCredentials covers POST /internal/credentials/list: the runner asks which
// credential KINDS an account has stored (names only, never values) so a cluster session pod can
// be built for the launching user. It is gated exactly like the fetch endpoint, but because no
// plaintext moves it writes no credential_access_log row.
func TestInternalListCredentials(t *testing.T) {
	t.Run("503 when internal key unconfigured", func(t *testing.T) {
		deps, pool, _ := newCredentialsTestDeps(t) // InternalKey left ""
		srv := httptest.NewServer(api.NewRouter(deps))
		defer srv.Close()
		accountID := insertCredAccount(t, pool, "internal-list-unconfigured")
		insertLiveHumanSession(t, pool, accountID)

		resp := doListCredentials(t, srv, "Bearer whatever", accountID, "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("list with unconfigured internal key = %d, want 503", resp.StatusCode)
		}
	})

	t.Run("401 on missing or wrong key", func(t *testing.T) {
		deps, pool := newInternalTestDeps(t)
		srv := httptest.NewServer(api.NewRouter(deps))
		defer srv.Close()
		accountID := insertCredAccount(t, pool, "internal-list-badkey")
		insertLiveHumanSession(t, pool, accountID)
		if err := deps.Credentials.Store(context.Background(), accountID, "claude", []byte("sk-ant-plaintext")); err != nil {
			t.Fatalf("Store: %v", err)
		}

		for _, auth := range []string{"", "Bearer not-the-real-key"} {
			resp := doListCredentials(t, srv, auth, accountID, "")
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("list with auth %q = %d, want 401", auth, resp.StatusCode)
			}
		}
	})

	t.Run("400 on non-uuid account_id", func(t *testing.T) {
		deps, _ := newInternalTestDeps(t)
		srv := httptest.NewServer(api.NewRouter(deps))
		defer srv.Close()

		for _, bad := range []string{"not-a-uuid", ""} {
			resp := doListCredentials(t, srv, "Bearer "+testInternalKey, bad, "")
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("list with account_id %q = %d, want 400", bad, resp.StatusCode)
			}
		}
	})

	t.Run("404 when the account has no live session", func(t *testing.T) {
		deps, pool := newInternalTestDeps(t)
		srv := httptest.NewServer(api.NewRouter(deps))
		defer srv.Close()
		accountID := insertCredAccount(t, pool, "internal-list-nosession")
		if err := deps.Credentials.Store(context.Background(), accountID, "claude", []byte("sk-ant-plaintext")); err != nil {
			t.Fatalf("Store: %v", err)
		}
		// No human_sessions row at all.

		resp := doListCredentials(t, srv, "Bearer "+testInternalKey, accountID, noSuchSession)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("list with no live session = %d, want 404 (same anti-enumeration shape as fetch)", resp.StatusCode)
		}
	})

	t.Run("200 lists stored engine names and writes no audit row", func(t *testing.T) {
		deps, pool := newInternalTestDeps(t)
		srv := httptest.NewServer(api.NewRouter(deps))
		defer srv.Close()
		accountID := insertCredAccount(t, pool, "internal-list-happy")
		sid := insertLiveHumanSession(t, pool, accountID)
		// Stored out of alphabetical order on purpose — the response must be sorted.
		if err := deps.Credentials.Store(context.Background(), accountID, "github", []byte("ghp-github-secret")); err != nil {
			t.Fatalf("Store github: %v", err)
		}
		if err := deps.Credentials.Store(context.Background(), accountID, "claude", []byte("sk-ant-claude-secret")); err != nil {
			t.Fatalf("Store claude: %v", err)
		}

		var before int
		if err := pool.QueryRow(context.Background(),
			`SELECT count(*) FROM credential_access_log WHERE account_id = $1`, accountID).Scan(&before); err != nil {
			t.Fatal(err)
		}

		resp := doListCredentials(t, srv, "Bearer "+testInternalKey, accountID, sid)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list = %d, want 200", resp.StatusCode)
		}
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Engines []string `json:"engines"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Engines) != 2 || out.Engines[0] != "claude" || out.Engines[1] != "github" {
			t.Fatalf("engines = %v, want [claude github]", out.Engines)
		}
		// (f) the response body must never carry a stored credential value.
		for _, secret := range []string{"ghp-github-secret", "sk-ant-claude-secret"} {
			if bytes.Contains(raw, []byte(secret)) {
				t.Fatalf("response body leaked a stored credential value: %s", raw)
			}
		}

		var after int
		if err := pool.QueryRow(context.Background(),
			`SELECT count(*) FROM credential_access_log WHERE account_id = $1`, accountID).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after != before {
			t.Fatalf("credential_access_log rows before=%d after=%d, want unchanged (no plaintext moved)", before, after)
		}
	})

	t.Run("200 with an empty array when the account has no credentials", func(t *testing.T) {
		deps, pool := newInternalTestDeps(t)
		srv := httptest.NewServer(api.NewRouter(deps))
		defer srv.Close()
		accountID := insertCredAccount(t, pool, "internal-list-empty")
		sid := insertLiveHumanSession(t, pool, accountID)

		resp := doListCredentials(t, srv, "Bearer "+testInternalKey, accountID, sid)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list = %d, want 200", resp.StatusCode)
		}
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(raw, []byte(`"engines":[]`)) {
			t.Fatalf("body = %s, want an empty engines array (not null)", raw)
		}
	})
}

// TestInternalFetchSucceedsWithKeyAndLiveSession is the happy path: a caller presenting the
// correct internal key, fetching an account that has a live (non-revoked) human_sessions row
// and a stored credential, gets the plaintext back.
func TestInternalFetchSucceedsWithKeyAndLiveSession(t *testing.T) {
	deps, pool := newInternalTestDeps(t)
	router := api.NewRouter(deps)
	srv := httptest.NewServer(router)
	defer srv.Close()

	accountID := insertCredAccount(t, pool, "internal-fetch-happy")
	sid := insertLiveHumanSession(t, pool, accountID)
	if err := deps.Credentials.Store(context.Background(), accountID, "claude", []byte("sk-ant-plaintext")); err != nil {
		t.Fatalf("Store: %v", err)
	}

	resp := doFetch(t, srv, "Bearer "+testInternalKey, accountID, "claude", sid)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fetch = %d, want 200", resp.StatusCode)
	}
	var out struct {
		PlaintextBase64 string `json:"plaintext_base64"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	got, err := base64.StdEncoding.DecodeString(out.PlaintextBase64)
	if err != nil {
		t.Fatalf("decode plaintext_base64: %v", err)
	}
	if string(got) != "sk-ant-plaintext" {
		t.Fatalf("plaintext = %q, want %q", got, "sk-ant-plaintext")
	}
}

// TestInternalFetchRejectsUnregisteredCaller: a request with no valid internal key (missing,
// wrong, or the endpoint unconfigured) is rejected regardless of account_id/engine validity —
// this must hold even when the account has a live session and a stored credential, proving the
// key check isn't bypassable.
func TestInternalFetchRejectsUnregisteredCaller(t *testing.T) {
	deps, pool := newInternalTestDeps(t)
	router := api.NewRouter(deps)
	srv := httptest.NewServer(router)
	defer srv.Close()

	accountID := insertCredAccount(t, pool, "internal-fetch-badkey")
	insertLiveHumanSession(t, pool, accountID)
	if err := deps.Credentials.Store(context.Background(), accountID, "claude", []byte("sk-ant-plaintext")); err != nil {
		t.Fatalf("Store: %v", err)
	}

	cases := []struct {
		name string
		auth string
	}{
		{"missing key", ""},
		{"wrong key", "Bearer not-the-real-key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := doFetch(t, srv, tc.auth, accountID, "claude", "")
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("fetch with %s = %d, want 401", tc.name, resp.StatusCode)
			}
		})
	}
}

// TestInternalFetchDisabledWhenUnconfigured: an empty InternalKey (unconfigured) must fail
// closed (503), never accept any key including an empty one.
func TestInternalFetchDisabledWhenUnconfigured(t *testing.T) {
	deps, pool, _ := newCredentialsTestDeps(t) // deps.InternalKey left as the zero value ""
	router := api.NewRouter(deps)
	srv := httptest.NewServer(router)
	defer srv.Close()

	accountID := insertCredAccount(t, pool, "internal-fetch-unconfigured")
	insertLiveHumanSession(t, pool, accountID)

	resp := doFetch(t, srv, "Bearer whatever", accountID, "claude", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("fetch with unconfigured internal key = %d, want 503", resp.StatusCode)
	}
}

// TestInternalFetchRejectsAccountWithNoLiveSession is the specific defense-in-depth property
// this endpoint exists to enforce: a valid internal-key caller fetching an account that has a
// stored credential but NO live human_sessions row (never logged in, or fully logged out) must
// be rejected — the static key alone is not sufficient.
func TestInternalFetchRejectsAccountWithNoLiveSession(t *testing.T) {
	deps, pool := newInternalTestDeps(t)
	router := api.NewRouter(deps)
	srv := httptest.NewServer(router)
	defer srv.Close()

	accountID := insertCredAccount(t, pool, "internal-fetch-nosession")
	if err := deps.Credentials.Store(context.Background(), accountID, "claude", []byte("sk-ant-plaintext")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	// No human_sessions row at all for this account.

	resp := doFetch(t, srv, "Bearer "+testInternalKey, accountID, "claude", noSuchSession)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("fetch with no live session = %d, want 404 (matches missing-credential, so a key holder cannot tell logged-out from never-stored)", resp.StatusCode)
	}
}

// TestInternalFetchRejectsAccountWithOnlyRevokedSession: same property as above, but covers the
// "was logged in, then logged out" case specifically — a revoked (not merely absent)
// human_sessions row must not count as live.
func TestInternalFetchRejectsAccountWithOnlyRevokedSession(t *testing.T) {
	deps, pool := newInternalTestDeps(t)
	router := api.NewRouter(deps)
	srv := httptest.NewServer(router)
	defer srv.Close()

	accountID := insertCredAccount(t, pool, "internal-fetch-revoked")
	if err := deps.Credentials.Store(context.Background(), accountID, "claude", []byte("sk-ant-plaintext")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	sessionID := insertLiveHumanSession(t, pool, accountID)
	if _, err := pool.Exec(context.Background(),
		`UPDATE human_sessions SET revoked_at = now() WHERE id = $1`, sessionID); err != nil {
		t.Fatalf("revoke session: %v", err)
	}

	resp := doFetch(t, srv, "Bearer "+testInternalKey, accountID, "claude", sessionID)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("fetch with only a revoked session = %d, want 404", resp.StatusCode)
	}
}

// TestInternalFetchLogsEveryAccess: a successful fetch (valid key + live session) must insert
// exactly one credential_access_log row, recording the session that was used to authorize it.
func TestInternalFetchLogsEveryAccess(t *testing.T) {
	deps, pool := newInternalTestDeps(t)
	router := api.NewRouter(deps)
	srv := httptest.NewServer(router)
	defer srv.Close()

	accountID := insertCredAccount(t, pool, "internal-fetch-audit")
	sessionID := insertLiveHumanSession(t, pool, accountID)
	if err := deps.Credentials.Store(context.Background(), accountID, "claude", []byte("sk-ant-plaintext")); err != nil {
		t.Fatalf("Store: %v", err)
	}

	resp := doFetch(t, srv, "Bearer "+testInternalKey, accountID, "claude", sessionID)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fetch = %d, want 200", resp.StatusCode)
	}

	var count int
	var loggedAccount, loggedEngine, loggedSession string
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM credential_access_log WHERE account_id = $1`, accountID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("credential_access_log row count = %d, want exactly 1", count)
	}
	if err := pool.QueryRow(context.Background(),
		`SELECT account_id::text, engine, fetched_by_session_id::text FROM credential_access_log WHERE account_id = $1`,
		accountID).Scan(&loggedAccount, &loggedEngine, &loggedSession); err != nil {
		t.Fatal(err)
	}
	if loggedAccount != accountID || loggedEngine != "claude" || loggedSession != sessionID {
		t.Fatalf("logged row = (%q,%q,%q), want (%q,%q,%q)", loggedAccount, loggedEngine, loggedSession, accountID, "claude", sessionID)
	}
}

// TestInternalFetchDoesNotLogOnRejection: a rejected fetch (bad key, or no live session) must
// not write an audit row — the log is specifically an access log, not an attempt log.
func TestInternalFetchDoesNotLogOnRejection(t *testing.T) {
	deps, pool := newInternalTestDeps(t)
	router := api.NewRouter(deps)
	srv := httptest.NewServer(router)
	defer srv.Close()

	accountID := insertCredAccount(t, pool, "internal-fetch-noauditonreject")
	if err := deps.Credentials.Store(context.Background(), accountID, "claude", []byte("sk-ant-plaintext")); err != nil {
		t.Fatalf("Store: %v", err)
	}

	resp1 := doFetch(t, srv, "Bearer wrong-key", accountID, "claude", noSuchSession)
	resp1.Body.Close()
	resp2 := doFetch(t, srv, "Bearer "+testInternalKey, accountID, "claude", noSuchSession) // no live session yet
	resp2.Body.Close()

	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM credential_access_log WHERE account_id = $1`, accountID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("credential_access_log row count = %d, want 0 (no successful fetch occurred)", count)
	}
}

// TestInternalFetchRejectsAccountWithOnlyExpiredSession: a live-looking (non-revoked) session
// whose expires_at has passed must not count as live either — expiry, not just revocation,
// bounds the live-session gate.
func TestInternalFetchRejectsAccountWithOnlyExpiredSession(t *testing.T) {
	deps, pool := newInternalTestDeps(t)
	accountID := insertCredAccount(t, pool, "expired-session")
	sid := insertLiveHumanSession(t, pool, accountID)
	if _, err := pool.Exec(context.Background(),
		`UPDATE human_sessions SET expires_at = now() - interval '1 minute' WHERE account_id = $1`, accountID); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	resp := doFetch(t, srv, "Bearer "+testInternalKey, accountID, "claude", sid)
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("an account whose only session has expired must not pass the live-session gate")
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("fetch with only an expired session = %d, want 404 (matches TestInternalFetchRejectsAccountWithNoLiveSession)", resp.StatusCode)
	}
}

// TestInternalFetchNoCORSHeaders: this endpoint must never send CORS headers — it is never
// meant to be reachable from a browser.
func TestInternalFetchNoCORSHeaders(t *testing.T) {
	deps, pool := newInternalTestDeps(t)
	router := api.NewRouter(deps)
	srv := httptest.NewServer(router)
	defer srv.Close()

	accountID := insertCredAccount(t, pool, "internal-fetch-cors")
	sid := insertLiveHumanSession(t, pool, accountID)
	if err := deps.Credentials.Store(context.Background(), accountID, "claude", []byte("sk-ant-plaintext")); err != nil {
		t.Fatalf("Store: %v", err)
	}

	resp := doFetch(t, srv, "Bearer "+testInternalKey, accountID, "claude", sid)
	defer resp.Body.Close()
	if v := resp.Header.Get("Access-Control-Allow-Origin"); v != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want unset", v)
	}
}

// doInternal posts an arbitrary body to one of the two internal credential endpoints, so the
// token_id tests below can send a field the fixed-shape doFetch/doListCredentials helpers
// don't carry.
func doInternal(t *testing.T, srv *httptest.Server, path string, body map[string]string) *http.Response {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+testInternalKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// TestInternalCredentialsAcceptAgentTokenID: a session started by an agent token has no live
// human session behind it — its owner may be logged out or have never had a browser open — so
// the liveness gate becomes "this agent token is still live" instead. The gate is not removed,
// only re-pointed: the blast radius of a leaked internal key is still bounded to principals
// their owner currently stands behind.
func TestInternalCredentialsAcceptAgentTokenID(t *testing.T) {
	deps, pool := newInternalTestDeps(t)
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	ctx := context.Background()

	accountID := insertCredAccount(t, pool, "internal-token-id")
	if err := deps.Credentials.Store(ctx, accountID, "claude", []byte("sk-ant-plaintext")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	idSvc := deps.Identity.(*identity.Service)
	rec, _, err := idSvc.CreateAgentToken(ctx, accountID, "runner", "run-sessions", 0)
	if err != nil {
		t.Fatalf("CreateAgentToken: %v", err)
	}

	// Baseline: naming no proof at all is a 400 on both endpoints (see internal_proof_test.go).
	for _, path := range []string{"/internal/credentials/fetch", "/internal/credentials/list"} {
		resp := doInternal(t, srv, path, map[string]string{"account_id": accountID, "engine": "claude"})
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s with no session_id and no token_id = %d, want 400", path, resp.StatusCode)
		}
	}

	// With the live token_id, fetch returns the plaintext and list returns the engine — still
	// with no human session anywhere.
	resp := doInternal(t, srv, "/internal/credentials/fetch",
		map[string]string{"account_id": accountID, "engine": "claude", "token_id": rec.ID})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("fetch with token_id = %d (%s), want 200", resp.StatusCode, b)
	}
	var fetched struct {
		PlaintextBase64 string `json:"plaintext_base64"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&fetched); err != nil {
		t.Fatal(err)
	}
	plain, err := base64.StdEncoding.DecodeString(fetched.PlaintextBase64)
	if err != nil || string(plain) != "sk-ant-plaintext" {
		t.Fatalf("plaintext = %q (err %v), want sk-ant-plaintext", plain, err)
	}

	listResp := doInternal(t, srv, "/internal/credentials/list",
		map[string]string{"account_id": accountID, "token_id": rec.ID})
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list with token_id = %d, want 200", listResp.StatusCode)
	}
	var listed struct {
		Engines []string `json:"engines"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Engines) != 1 || listed.Engines[0] != "claude" {
		t.Errorf("engines = %v, want [claude]", listed.Engines)
	}

	// The fetch was audited against the token, not against a session it never had.
	var tokenID *string
	var sessionID *string
	if err := pool.QueryRow(ctx,
		`SELECT fetched_by_token_id::text, fetched_by_session_id::text FROM credential_access_log
		  WHERE account_id = $1 ORDER BY fetched_at DESC LIMIT 1`, accountID).Scan(&tokenID, &sessionID); err != nil {
		t.Fatalf("read access log: %v", err)
	}
	if tokenID == nil || *tokenID != rec.ID {
		t.Errorf("fetched_by_token_id = %v, want %q", tokenID, rec.ID)
	}
	if sessionID != nil {
		t.Errorf("fetched_by_session_id = %v, want NULL for a token-authenticated fetch", *sessionID)
	}

	// last_used_at was stamped by the liveness check.
	list, err := idSvc.ListAgentTokens(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].LastUsedAt == nil {
		t.Errorf("token = %+v, want last_used_at stamped", list)
	}
}

// TestInternalCredentialsRejectBadTokenID: a revoked, expired, foreign or malformed token_id
// never opens the endpoint — and a rejected one keeps the same anti-enumeration 404 as a
// missing session, so an internal-key holder still cannot probe which token ids exist.
func TestInternalCredentialsRejectBadTokenID(t *testing.T) {
	deps, pool := newInternalTestDeps(t)
	srv := httptest.NewServer(api.NewRouter(deps))
	defer srv.Close()
	ctx := context.Background()

	accountID := insertCredAccount(t, pool, "internal-token-id-bad")
	otherID := insertCredAccount(t, pool, "internal-token-id-other")
	if err := deps.Credentials.Store(ctx, accountID, "claude", []byte("sk-ant-plaintext")); err != nil {
		t.Fatalf("Store: %v", err)
	}
	idSvc := deps.Identity.(*identity.Service)

	revoked, _, err := idSvc.CreateAgentToken(ctx, accountID, "revoked", "run-sessions", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := idSvc.RevokeAgentToken(ctx, accountID, revoked.ID); err != nil {
		t.Fatal(err)
	}
	expired, _, err := idSvc.CreateAgentToken(ctx, accountID, "expired", "run-sessions", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE agent_tokens SET expires_at = now() - interval '1 minute' WHERE id = $1`, expired.ID); err != nil {
		t.Fatal(err)
	}
	foreign, _, err := idSvc.CreateAgentToken(ctx, otherID, "foreign", "run-sessions", 0)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, tokenID string
		want          int
	}{
		{"revoked", revoked.ID, http.StatusNotFound},
		{"expired", expired.ID, http.StatusNotFound},
		{"another account's", foreign.ID, http.StatusNotFound},
		{"unknown", "00000000-0000-0000-0000-000000000000", http.StatusNotFound},
		{"malformed", "not-a-uuid", http.StatusBadRequest},
	} {
		for _, path := range []string{"/internal/credentials/fetch", "/internal/credentials/list"} {
			resp := doInternal(t, srv, path,
				map[string]string{"account_id": accountID, "engine": "claude", "token_id": tc.tokenID})
			resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Errorf("%s with %s token_id = %d, want %d", path, tc.name, resp.StatusCode, tc.want)
			}
		}
	}

	// A live human session does NOT rescue a bad token_id: the named token is the gate (and
	// naming a session alongside it is a 400, see internal_proof_test.go).
	insertLiveHumanSession(t, pool, accountID)
	resp := doInternal(t, srv, "/internal/credentials/fetch",
		map[string]string{"account_id": accountID, "engine": "claude", "token_id": revoked.ID})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("revoked token_id with a live human session = %d, want 404", resp.StatusCode)
	}
}
