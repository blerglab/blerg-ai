package server

// Runner contract v1 (spec §3): idempotent start, sessions that run as the
// token owner, callback validation. The result endpoint is result_test.go.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// runnerContractPool gives one test its OWN schema, pinned on every connection
// the pool opens (a RuntimeParam, not a `SET` that lands on whichever
// connection ran it). Both halves matter here: these tests drive the API
// through an httptest server, so handler goroutines open pool connections
// concurrently with the test goroutine, and a per-test schema means no other
// test's teardown can drop the tables out from under one of them.
func runnerContractPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database integration tests")
	}
	schema := contractTestSchemaName(t.Name())
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect to postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	for _, q := range []string{
		fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema),
		fmt.Sprintf("CREATE SCHEMA %s", schema),
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(),
			fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
	})
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	return pool
}

// contractTestSchemaName turns a test name into a legal, unique schema
// identifier (lowercase, no punctuation, within Postgres's 63-byte limit).
func contractTestSchemaName(name string) string {
	var b strings.Builder
	b.WriteString("test_rc_")
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	s := b.String()
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// clusterRunnerAPI is the cluster shape of the runner contract: a JobManager
// backed by fakeK8s (and its stub blerg-core credential endpoint), a real
// database, and the static runner key enabled.
func clusterRunnerAPI(t *testing.T, f *fakeK8s) (*API, *Hub, *pgxpool.Pool) {
	t.Helper()
	pool := runnerContractPool(t)
	hub := NewHub()
	hub.SetJobManager(newTestJobManager(t, f))
	api := NewAPI(hub, pool, "daemon-tok-1234567890", nil, "")
	api.SetRunnerKey(runnerTestKey)
	return api, hub, pool
}

// desktopContractAPI is the same thing with no cluster runtime: starts land on
// a connected daemon.
func desktopContractAPI(t *testing.T) (*API, *Hub) {
	t.Helper()
	hub := NewHub()
	api := NewAPI(hub, runnerContractPool(t), "daemon-tok-1234567890", nil, "")
	api.SetRunnerKey(runnerTestKey)
	return api, hub
}

func readAllString(t *testing.T, r io.Reader) string {
	t.Helper()
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// startReq POSTs /api/runner/start with the given credential and optional
// Idempotency-Key, returning the response.
func startReq(t *testing.T, srv *httptest.Server, bearer, idemKey string, body any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/runner/start", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+bearer)
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decodeSessionID(t *testing.T, resp *http.Response) string {
	t.Helper()
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out.SessionID
}

// A retried start with the same key and body must not start a second session:
// the original 202 is replayed, flagged so the caller can tell.
func TestRunnerStartIdempotentReplay(t *testing.T) {
	f := &fakeK8s{}
	api, _, _ := clusterRunnerAPI(t, f)
	srv := httptest.NewServer(agentContractMux(api))
	defer srv.Close()

	body := map[string]any{"repo": "org/proj", "prompt": "build it"}

	resp := startReq(t, srv, runnerTestKey, "key-1", body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("first start: %d, want 202", resp.StatusCode)
	}
	first := decodeSessionID(t, resp)
	resp.Body.Close()
	if got := resp.Header.Get("Idempotent-Replayed"); got != "" {
		t.Errorf("first start carried Idempotent-Replayed=%q", got)
	}

	resp = startReq(t, srv, runnerTestKey, "key-1", body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("replay: %d, want 202", resp.StatusCode)
	}
	second := decodeSessionID(t, resp)
	resp.Body.Close()
	if second != first {
		t.Errorf("replay session_id = %q, want the original %q", second, first)
	}
	if got := resp.Header.Get("Idempotent-Replayed"); got != "true" {
		t.Errorf("Idempotent-Replayed = %q, want true", got)
	}
	f.mu.Lock()
	created := len(f.created)
	f.mu.Unlock()
	if created != 1 {
		t.Errorf("Jobs created = %d, want 1: the replay must not start a second session", created)
	}

	// Same key, different body ⇒ 409 with the documented message.
	resp = startReq(t, srv, runnerTestKey, "key-1", map[string]any{"repo": "org/other", "prompt": "build it"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("reused key with a different body: %d, want 409", resp.StatusCode)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&errBody)
	resp.Body.Close()
	if errBody.Error != "idempotency key reused with a different request" {
		t.Errorf("409 error = %q", errBody.Error)
	}

	// A different key is a different request.
	resp = startReq(t, srv, runnerTestKey, "key-2", body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("second key: %d, want 202", resp.StatusCode)
	}
	if id := decodeSessionID(t, resp); id == first {
		t.Error("a different Idempotency-Key must start a new session")
	}
	resp.Body.Close()
}

// The key is scoped to the calling credential: two callers using the same
// string must not collide (and must not be able to read each other's session
// ids by guessing one).
func TestRunnerStartIdempotencyIsScopedToTheCaller(t *testing.T) {
	f := &fakeK8s{}
	api, _, _ := clusterRunnerAPI(t, f)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	api.coreAuth = newTestCoreAuthClient(t, pub, "core-1")
	tok := mintRunnerToken(t, priv, "core-1", identity.Claims{
		Sub: "tok-abc", Aud: "blerg-runner", Kind: "agent", OnBehalfOf: "acct-1",
		Caps: []string{coreAuthRunnerCap}, ExpiresAt: time.Now().Unix() + 300,
	})
	srv := httptest.NewServer(agentContractMux(api))
	defer srv.Close()

	body := map[string]any{"repo": "org/proj", "prompt": "build it"}
	resp := startReq(t, srv, runnerTestKey, "shared-key", body)
	keyOwner := decodeSessionID(t, resp)
	resp.Body.Close()

	resp = startReq(t, srv, tok, "shared-key", body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("other caller, same key: %d, want 202", resp.StatusCode)
	}
	other := decodeSessionID(t, resp)
	resp.Body.Close()
	if other == keyOwner {
		t.Error("idempotency keys must be scoped per credential, not global")
	}
	if got := resp.Header.Get("Idempotent-Replayed"); got != "" {
		t.Errorf("Idempotent-Replayed = %q for a different scope", got)
	}
}

// Records older than the 24 h retention are ignored (and pruned): the same key
// starts a fresh session instead of replaying a session that is long gone.
func TestRunnerStartIdempotencyExpiresAfter24h(t *testing.T) {
	f := &fakeK8s{}
	api, _, pool := clusterRunnerAPI(t, f)
	srv := httptest.NewServer(agentContractMux(api))
	defer srv.Close()
	body := map[string]any{"repo": "org/proj", "prompt": "build it"}

	resp := startReq(t, srv, runnerTestKey, "old-key", body)
	first := decodeSessionID(t, resp)
	resp.Body.Close()

	if _, err := pool.Exec(context.Background(),
		`UPDATE runner_idempotency SET created_at = now() - interval '25 hours'`); err != nil {
		t.Fatal(err)
	}
	resp = startReq(t, srv, runnerTestKey, "old-key", body)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expired key: %d, want 202", resp.StatusCode)
	}
	second := decodeSessionID(t, resp)
	resp.Body.Close()
	if second == first {
		t.Error("an idempotency record older than 24 h must not replay")
	}
}

func TestRunnerStartRejectsMalformedIdempotencyKey(t *testing.T) {
	api, _, _ := clusterRunnerAPI(t, &fakeK8s{})
	srv := httptest.NewServer(agentContractMux(api))
	defer srv.Close()
	resp := startReq(t, srv, runnerTestKey, strings.Repeat("k", 129),
		map[string]any{"repo": "org/proj"})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("129-character key: %d, want 400", resp.StatusCode)
	}

	// Present but empty is a caller bug, not "no key" — silently dropping the
	// retry protection would be worse than saying so.
	raw, _ := json.Marshal(map[string]any{"repo": "org/proj"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/runner/start", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+runnerTestKey)
	req.Header.Set("Idempotency-Key", "")
	empty, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Body.Close()
	if empty.StatusCode != http.StatusBadRequest {
		t.Errorf("empty key: %d, want 400", empty.StatusCode)
	}
}

// callback_url is a URL the runner will POST to when the session finishes, so
// it must be https (or plain http only against the loopback host an operator
// is testing with).
func TestRunnerStartValidatesCallbackURL(t *testing.T) {
	api, _, pool := clusterRunnerAPI(t, &fakeK8s{})
	srv := httptest.NewServer(agentContractMux(api))
	defer srv.Close()

	// The loopback exception is an opt-in (I-4): the desktop stack sets it, a
	// cluster install does not. TestRunnerStartCallbackLoopbackNeedsAllowPrivate
	// covers the off case.
	t.Setenv(webhookAllowPrivateEnv, "true")

	for _, ok := range []string{
		"https://hooks.example.test/blerg",
		"http://localhost:9000/hook",
		"http://127.0.0.1:9000/hook",
		"http://[::1]:9000/hook",
	} {
		resp := startReq(t, srv, runnerTestKey, "", map[string]any{"repo": "org/proj", "callback_url": ok})
		if resp.StatusCode != http.StatusAccepted {
			t.Errorf("callback_url %q: %d, want 202 (%s)", ok, resp.StatusCode, readAllString(t, resp.Body))
		}
		resp.Body.Close()
	}
	for _, bad := range []string{
		"http://hooks.example.test/blerg",
		"ftp://hooks.example.test/blerg",
		"://nonsense",
		"/relative/only",
		"https://",
	} {
		resp := startReq(t, srv, runnerTestKey, "", map[string]any{"repo": "org/proj", "callback_url": bad})
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("callback_url %q: %d, want 422", bad, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// The callback (and its secret) are persisted on the session row, and the
	// secret never leaves the server.
	resp := startReq(t, srv, runnerTestKey, "", map[string]any{
		"repo": "org/proj", "callback_url": "https://hooks.example.test/blerg",
		"callback_secret": "hmac-key-please-keep",
	})
	sessionID := decodeSessionID(t, resp)
	resp.Body.Close()
	var url, secret *string
	if err := pool.QueryRow(context.Background(),
		`SELECT callback_url, callback_secret FROM sessions WHERE id = $1`, sessionID).Scan(&url, &secret); err != nil {
		t.Fatal(err)
	}
	if url == nil || *url != "https://hooks.example.test/blerg" {
		t.Errorf("stored callback_url = %v", url)
	}
	if secret == nil || *secret != "hmac-key-please-keep" {
		t.Errorf("stored callback_secret was not persisted for signing")
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/runner/sessions/"+sessionID, nil)
	req.Header.Set("Authorization", "Bearer "+runnerTestKey)
	statusResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer statusResp.Body.Close()
	if s := readAllString(t, statusResp.Body); strings.Contains(s, "hmac-key-please-keep") {
		t.Errorf("status body leaked the callback secret: %s", s)
	}
}

// I-4: a cleartext loopback callback is only accepted where the install has
// opted into private callback targets. On a cluster install it is refused at
// start with a 422 — otherwise the runner would accept a callback the
// delivery-time SSRF guard then silently refuses.
func TestRunnerStartCallbackLoopbackNeedsAllowPrivate(t *testing.T) {
	api, _, _ := clusterRunnerAPI(t, &fakeK8s{})
	srv := httptest.NewServer(agentContractMux(api))
	defer srv.Close()

	loopback := []string{
		"http://localhost:9000/hook",
		"http://127.0.0.1:9000/hook",
		"http://[::1]:9000/hook",
	}

	t.Setenv(webhookAllowPrivateEnv, "")
	for _, raw := range loopback {
		resp := startReq(t, srv, runnerTestKey, "", map[string]any{"repo": "org/proj", "callback_url": raw})
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("callback_url %q without %s: %d, want 422",
				raw, webhookAllowPrivateEnv, resp.StatusCode)
		}
		var errBody struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		resp.Body.Close()
		if !strings.Contains(errBody.Error, webhookAllowPrivateEnv) {
			t.Errorf("422 message %q does not say which switch is missing", errBody.Error)
		}
	}
	// https is unaffected either way.
	resp := startReq(t, srv, runnerTestKey, "", map[string]any{
		"repo": "org/proj", "callback_url": "https://hooks.example.test/blerg"})
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("https callback without %s: %d, want 202", webhookAllowPrivateEnv, resp.StatusCode)
	}
	resp.Body.Close()

	t.Setenv(webhookAllowPrivateEnv, "true")
	for _, raw := range loopback {
		resp := startReq(t, srv, runnerTestKey, "", map[string]any{"repo": "org/proj", "callback_url": raw})
		if resp.StatusCode != http.StatusAccepted {
			t.Errorf("callback_url %q with %s=true: %d, want 202 (%s)",
				raw, webhookAllowPrivateEnv, resp.StatusCode, readAllString(t, resp.Body))
		}
		resp.Body.Close()
	}
}

// runtime picks the hosting layer explicitly instead of relying on what this
// install happens to have configured.
func TestRunnerStartRuntimeSelection(t *testing.T) {
	api, _, _ := clusterRunnerAPI(t, &fakeK8s{})
	srv := httptest.NewServer(agentContractMux(api))
	defer srv.Close()

	// runtime:"daemon" on a cluster install goes to the daemon path — with no
	// daemon holding the repo that is today's 503, not a cluster Job.
	resp := startReq(t, srv, runnerTestKey, "", map[string]any{"repo": "org/proj", "runtime": "daemon"})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("runtime=daemon with no daemon: %d, want 503", resp.StatusCode)
	}
	resp.Body.Close()

	// runtime:"docker" is the same daemon path, sandboxed — so on a cluster
	// install with no daemon it is the same 503, not a cluster Job.
	resp = startReq(t, srv, runnerTestKey, "", map[string]any{"repo": "org/proj", "runtime": "docker"})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("runtime=docker with no daemon: %d, want 503", resp.StatusCode)
	}
	resp.Body.Close()

	resp = startReq(t, srv, runnerTestKey, "", map[string]any{"repo": "org/proj", "runtime": "kubernetes"})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("unknown runtime: %d, want 422", resp.StatusCode)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&errBody)
	resp.Body.Close()
	if errBody.Error != runnerRuntimeMessage {
		t.Errorf("error = %q, want %q", errBody.Error, runnerRuntimeMessage)
	}
}

// runtime:"cluster" on a desktop install is a clear 422, not a silent fallback
// onto the developer's own workstation.
func TestRunnerStartClusterRuntimeUnconfigured(t *testing.T) {
	desktop, _ := desktopContractAPI(t)
	srv := httptest.NewServer(agentContractMux(desktop))
	defer srv.Close()
	resp := startReq(t, srv, runnerTestKey, "", map[string]any{"repo": "proj", "runtime": "cluster"})
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("runtime=cluster with no cluster: %d, want 422", resp.StatusCode)
	}
	var errBody struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&errBody)
	resp.Body.Close()
	if errBody.Error != "cluster runtime is not configured" {
		t.Errorf("error = %q, want %q", errBody.Error, "cluster runtime is not configured")
	}
}

// A session started with an agent token runs as the token's owner: the pod is
// built with the owner's personal credentials, fetched from core with the
// token's own id so core can check the token is still live.
func TestRunnerStartRunsAsTheTokenOwner(t *testing.T) {
	f := &fakeK8s{credentialFetchResponses: map[string][]byte{
		"acct-1:claude": []byte("sk-ant-oat-personal"),
	}}
	api, _, pool := clusterRunnerAPI(t, f)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	api.coreAuth = newTestCoreAuthClient(t, pub, "core-1")
	srv := httptest.NewServer(agentContractMux(api))
	defer srv.Close()
	now := time.Now().Unix()

	agentTok := mintRunnerToken(t, priv, "core-1", identity.Claims{
		Sub: "tok-abc", Aud: "blerg-runner", Kind: "agent", OnBehalfOf: "acct-1",
		Caps: []string{coreAuthRunnerCap}, ExpiresAt: now + 300,
	})
	humanTok := mintRunnerToken(t, priv, "core-1", identity.Claims{
		Sub: "acct-2", Aud: "blerg-runner", Kind: "human", Sid: testSID,
		Caps: []string{coreAuthRunnerCap}, ExpiresAt: now + 300,
	})

	cases := []struct {
		name          string
		bearer        string
		wantAccount   string
		wantTokenID   string
		wantFetchedBy string // token_id the credential fetch must carry
	}{
		{"agent token", agentTok, "acct-1", "tok-abc", "tok-abc"},
		{"human token", humanTok, "acct-2", "", ""},
		{"runner key", runnerTestKey, "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f.mu.Lock()
			f.credentialFetchTokenIDs = nil
			f.mu.Unlock()

			resp := startReq(t, srv, c.bearer, "", map[string]any{"repo": "org/proj", "prompt": "go"})
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("start: %d (%s)", resp.StatusCode, readAllString(t, resp.Body))
			}
			sessionID := decodeSessionID(t, resp)
			resp.Body.Close()

			var account, tokenID *string
			if err := pool.QueryRow(context.Background(),
				`SELECT spawning_account_id, token_id FROM sessions WHERE id = $1`, sessionID).
				Scan(&account, &tokenID); err != nil {
				t.Fatal(err)
			}
			if derefOrEmpty(account) != c.wantAccount {
				t.Errorf("spawning_account_id = %v, want %q", derefOrEmpty(account), c.wantAccount)
			}
			if derefOrEmpty(tokenID) != c.wantTokenID {
				t.Errorf("token_id = %v, want %q", derefOrEmpty(tokenID), c.wantTokenID)
			}
			f.mu.Lock()
			ids := append([]string(nil), f.credentialFetchTokenIDs...)
			f.mu.Unlock()
			if c.wantAccount == "" {
				if len(ids) != 0 {
					t.Errorf("the runner key must not fetch anybody's personal credentials: %v", ids)
				}
				return
			}
			if len(ids) == 0 {
				t.Fatal("no credential fetch reached core: the spawning account was not passed through")
			}
			for _, got := range ids {
				if got != c.wantFetchedBy {
					t.Errorf("credential fetch token_id = %q, want %q", got, c.wantFetchedBy)
				}
			}
		})
	}
}

// A Job that could not be created leaves a session row behind. It must be
// terminal and say why: a row stuck at "starting" never converges for status,
// result or the completion webhook, and a replayed idempotency key would keep
// pointing at it.
func TestRunnerStartFailedJobMarksTheSessionTerminal(t *testing.T) {
	f := &fakeK8s{failNext: true}
	api, _, _ := clusterRunnerAPI(t, f)
	srv := httptest.NewServer(agentContractMux(api))
	defer srv.Close()

	resp := startReq(t, srv, runnerTestKey, "", map[string]any{"repo": "org/proj", "prompt": "go"})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("failed job create: %d, want 503", resp.StatusCode)
	}
	resp.Body.Close()

	// The 503 does not name the session, so find the row the start left.
	var sessionID string
	if err := api.dbPool.QueryRow(context.Background(),
		`SELECT id FROM sessions ORDER BY started_at DESC LIMIT 1`).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	res, err := api.buildSessionResult(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Lifecycle != "error" || !res.Terminal {
		t.Errorf("lifecycle/terminal = %q/%v, want error/true", res.Lifecycle, res.Terminal)
	}
	if res.ErrorReason == "" {
		t.Error("error_reason is empty: a failed start must say why")
	}
	if res.EndedAt == nil {
		t.Error("ended_at must be set for a session that never started")
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/runner/sessions/"+sessionID, nil)
	req.Header.Set("Authorization", "Bearer "+runnerTestKey)
	statusResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer statusResp.Body.Close()
	var out struct {
		Lifecycle   string `json:"lifecycle"`
		ErrorReason string `json:"error_reason"`
	}
	if err := json.NewDecoder(statusResp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.Lifecycle != "error" || out.ErrorReason == "" {
		t.Errorf("status = %+v, want error with a reason", out)
	}
}

// The idempotency hash covers the WHOLE request, not just the obvious fields:
// a retry that quietly changes the injected env or the webhook target is a
// different request and must not replay the first one's session.
func TestRunnerStartIdempotencyHashCoversEveryField(t *testing.T) {
	api, _, _ := clusterRunnerAPI(t, &fakeK8s{})
	srv := httptest.NewServer(agentContractMux(api))
	defer srv.Close()

	base := map[string]any{
		"repo": "org/proj", "prompt": "build it",
		"env":          map[string]string{"BLERG_BOARD_BOARD": "b1"},
		"callback_url": "https://hooks.example.test/one",
	}
	resp := startReq(t, srv, runnerTestKey, "field-key", base)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("first start: %d (%s)", resp.StatusCode, readAllString(t, resp.Body))
	}
	resp.Body.Close()

	for name, changed := range map[string]map[string]any{
		"env": {
			"repo": "org/proj", "prompt": "build it",
			"env":          map[string]string{"BLERG_BOARD_BOARD": "b2"},
			"callback_url": "https://hooks.example.test/one",
		},
		"callback_url": {
			"repo": "org/proj", "prompt": "build it",
			"env":          map[string]string{"BLERG_BOARD_BOARD": "b1"},
			"callback_url": "https://hooks.example.test/two",
		},
	} {
		resp := startReq(t, srv, runnerTestKey, "field-key", changed)
		if resp.StatusCode != http.StatusConflict {
			t.Errorf("changed %s only: %d, want 409", name, resp.StatusCode)
		}
		resp.Body.Close()
	}

	// The identical body still replays — the hash is not merely "always different".
	resp = startReq(t, srv, runnerTestKey, "field-key", base)
	if resp.StatusCode != http.StatusAccepted || resp.Header.Get("Idempotent-Replayed") != "true" {
		t.Errorf("identical retry: %d / replayed=%q, want 202 / true",
			resp.StatusCode, resp.Header.Get("Idempotent-Replayed"))
	}
	resp.Body.Close()
}

// A cluster session records the clone URL it actually ran with, so the result
// reports what happened instead of re-deriving a URL an override replaced.
func TestRunnerStartPersistsTheCloneURL(t *testing.T) {
	api, _, pool := clusterRunnerAPI(t, &fakeK8s{})
	srv := httptest.NewServer(agentContractMux(api))
	defer srv.Close()

	for _, c := range []struct{ override, want string }{
		// An org/name is not appended to the per-org default base (that
		// named github.com/org/org/proj, which is no repository).
		{"", "https://github.com/org/proj.git"},
		{"https://git.example.test/other/proj.git", "https://git.example.test/other/proj.git"},
	} {
		body := map[string]any{"repo": "org/proj"}
		if c.override != "" {
			body["git_url"] = c.override
		}
		resp := startReq(t, srv, runnerTestKey, "", body)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("start: %d (%s)", resp.StatusCode, readAllString(t, resp.Body))
		}
		sessionID := decodeSessionID(t, resp)
		resp.Body.Close()

		var stored *string
		if err := pool.QueryRow(context.Background(),
			`SELECT git_url FROM sessions WHERE id = $1`, sessionID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if derefOrEmpty(stored) != c.want {
			t.Errorf("git_url = %q, want %q", derefOrEmpty(stored), c.want)
		}
		res, err := api.buildSessionResult(context.Background(), sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if res.GitURL != c.want {
			t.Errorf("result git_url = %q, want %q", res.GitURL, c.want)
		}
	}
}

// A daemon-routed start is attributed too: the row records which account (and,
// for an agent token, which token) asked for it.
func TestRunnerStartOnDaemonRecordsAttribution(t *testing.T) {
	api, hub := desktopContractAPI(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	api.coreAuth = newTestCoreAuthClient(t, pub, "core-1")
	registerDaemon(t, api, hub, "00000000-0000-4000-8000-0000000000b7", "laptop", "proj")
	tok := mintRunnerToken(t, priv, "core-1", identity.Claims{
		Sub: "tok-xyz", Aud: "blerg-runner", Kind: "agent", OnBehalfOf: "acct-9",
		Caps: []string{coreAuthRunnerCap}, ExpiresAt: time.Now().Unix() + 300,
	})
	srv := httptest.NewServer(agentContractMux(api))
	defer srv.Close()

	resp := startReq(t, srv, tok, "", map[string]any{"repo": "proj", "prompt": "go"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("daemon start: %d (%s)", resp.StatusCode, readAllString(t, resp.Body))
	}
	sessionID := decodeSessionID(t, resp)
	resp.Body.Close()

	var account, tokenID *string
	if err := api.dbPool.QueryRow(context.Background(),
		`SELECT spawning_account_id, token_id FROM sessions WHERE id = $1`, sessionID).
		Scan(&account, &tokenID); err != nil {
		t.Fatal(err)
	}
	if derefOrEmpty(account) != "acct-9" {
		t.Errorf("spawning_account_id = %q, want acct-9", derefOrEmpty(account))
	}
	if derefOrEmpty(tokenID) != "tok-xyz" {
		t.Errorf("token_id = %q, want tok-xyz", derefOrEmpty(tokenID))
	}
}

// A resumed cluster session must carry the same token_id it was started with,
// or core rejects the credential fetch for an agent-owned session.
func TestResumeClusterSessionCarriesTokenID(t *testing.T) {
	f := &fakeK8s{credentialFetchResponses: map[string][]byte{
		"acct-1:claude": []byte("sk-ant-oat-personal"),
	}}
	_, hub, pool := clusterRunnerAPI(t, f)
	ctx := context.Background()
	const daemonID = "00000000-0000-4000-8000-0000000000e9"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	sessionID := newUUID()
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "disconnected", "/workspace/org/proj", "org/proj", "T", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionSpawningAccount(ctx, pool, sessionID, "acct-1"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionTokenID(ctx, pool, sessionID, "tok-abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET status = 'disconnected' WHERE id = $1`, sessionID); err != nil {
		t.Fatal(err)
	}

	resumeClusterSession(ctx, hub, pool, sessionID, "carry on", "acct-1", testSID)

	f.mu.Lock()
	ids := append([]string(nil), f.credentialFetchTokenIDs...)
	f.mu.Unlock()
	if len(ids) == 0 {
		t.Fatal("resume made no credential fetch")
	}
	for _, got := range ids {
		if got != "tok-abc" {
			t.Errorf("resume credential fetch token_id = %q, want tok-abc", got)
		}
	}
}
