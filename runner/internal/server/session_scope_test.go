package server

// Agent tokens are scoped to their owner's sessions (I-1): a token a user
// minted may drive the sessions that token's owner started, and nothing else.
// The static runner key, human/service tokens and a board.admin agent token
// keep the install-wide view.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// scopeFixture is a cluster runner with three core-issued credentials: two
// ordinary agent tokens owned by different accounts, and an admin one.
type scopeFixture struct {
	api      *API
	pool     *pgxpool.Pool
	srv      *httptest.Server
	tokenA   string
	tokenB   string
	adminTok string
}

func newScopeFixture(t *testing.T) *scopeFixture {
	t.Helper()
	api, _, pool := clusterRunnerAPI(t, &fakeK8s{})
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	api.coreAuth = newTestCoreAuthClient(t, pub, "core-1")
	exp := time.Now().Unix() + 300
	f := &scopeFixture{api: api, pool: pool}
	f.tokenA = mintRunnerToken(t, priv, "core-1", identity.Claims{
		Sub: "tok-a", Aud: "blerg-runner", Kind: "agent", OnBehalfOf: "acct-a",
		Caps: []string{coreAuthRunnerCap}, ExpiresAt: exp,
	})
	f.tokenB = mintRunnerToken(t, priv, "core-1", identity.Claims{
		Sub: "tok-b", Aud: "blerg-runner", Kind: "agent", OnBehalfOf: "acct-b",
		Caps: []string{coreAuthRunnerCap}, ExpiresAt: exp,
	})
	f.adminTok = mintRunnerToken(t, priv, "core-1", identity.Claims{
		Sub: "tok-admin", Aud: "blerg-runner", Kind: "agent", OnBehalfOf: "acct-admin",
		Caps: []string{coreAuthRunnerCap, sessionAdminCap}, ExpiresAt: exp,
	})
	f.srv = httptest.NewServer(agentContractMux(api))
	t.Cleanup(f.srv.Close)
	return f
}

// start runs a session as the given credential and returns its id.
func (f *scopeFixture) start(t *testing.T, bearer string) string {
	t.Helper()
	resp := startReq(t, f.srv, bearer, "", map[string]any{"repo": "org/proj", "prompt": "go"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d (%s)", resp.StatusCode, readAllString(t, resp.Body))
	}
	id := decodeSessionID(t, resp)
	resp.Body.Close()
	return id
}

// do issues one contract request with the given credential.
func (f *scopeFixture) do(t *testing.T, bearer, method, path string, body any) *http.Response {
	t.Helper()
	rd := strings.NewReader("")
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rd = strings.NewReader(string(raw))
	}
	req, _ := http.NewRequest(method, f.srv.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// sessionOps is every per-session operation of the contract, as (method, path
// suffix, body) — the full set the scope rule has to cover.
func sessionOps(id string) []struct {
	name, method, path string
	body               any
} {
	base := "/api/runner/sessions/" + id
	return []struct {
		name, method, path string
		body               any
	}{
		{"status", http.MethodGet, base, nil},
		{"message", http.MethodPost, base + "/message", map[string]string{"text": "hi"}},
		{"interrupt", http.MethodPost, base + "/interrupt", nil},
		{"events", http.MethodGet, base + "/events", nil},
		{"events_stream", http.MethodGet, base + "/events/stream", nil},
		{"result", http.MethodGet, base + "/result", nil},
		// stop last: it ends the session the other operations read.
		{"stop", http.MethodPost, base + "/stop", nil},
	}
}

// An agent token cannot read, steer or stop a session another account started
// — and the refusal is indistinguishable from an unknown session id.
func TestAgentTokenCannotReachAnotherOwnersSession(t *testing.T) {
	f := newScopeFixture(t)
	sessionB := f.start(t, f.tokenB)

	for _, op := range sessionOps(sessionB) {
		resp := f.do(t, f.tokenA, op.method, op.path, op.body)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s as a foreign agent token: %d, want 404 (%s)",
				op.name, resp.StatusCode, readAllString(t, resp.Body))
			continue
		}
		var errBody struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errBody)
		if errBody.Error != "session not found" {
			t.Errorf("%s error = %q, want the same body an unknown session gets", op.name, errBody.Error)
		}
	}

	// Identical shape for an id that genuinely does not exist: no enumeration.
	resp := f.do(t, f.tokenA, http.MethodGet, "/api/runner/sessions/"+newUUID(), nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown session: %d, want 404", resp.StatusCode)
	}
}

// The same token reaches its OWN session on every operation.
func TestAgentTokenReachesItsOwnSession(t *testing.T) {
	f := newScopeFixture(t)
	sessionA := f.start(t, f.tokenA)

	for _, op := range sessionOps(sessionA) {
		resp := f.do(t, f.tokenA, op.method, op.path, op.body)
		// interrupt has no live runtime in this fixture (409) — an honest
		// answer about the session, not a refusal to see it.
		if resp.StatusCode == http.StatusNotFound {
			t.Errorf("%s on the token's own session: 404 (%s)", op.name, readAllString(t, resp.Body))
		}
	}
}

// An operator credential — the static runner key — and an admin agent token
// both keep the install-wide view.
func TestOperatorCredentialsSeeEverySession(t *testing.T) {
	f := newScopeFixture(t)
	sessionA := f.start(t, f.tokenA)
	sessionB := f.start(t, f.tokenB)
	keyed := f.start(t, runnerTestKey) // no spawning account at all

	for _, bearer := range []struct{ name, tok string }{
		{"runner key", runnerTestKey},
		{"admin agent token", f.adminTok},
	} {
		for _, id := range []string{sessionA, sessionB, keyed} {
			resp := f.do(t, bearer.tok, http.MethodGet, "/api/runner/sessions/"+id, nil)
			if resp.StatusCode != http.StatusOK {
				t.Errorf("%s reading session %s: %d, want 200", bearer.name, id, resp.StatusCode)
			}
		}
	}

	// And a scoped token still cannot see the runner key's ownerless session.
	resp := f.do(t, f.tokenA, http.MethodGet, "/api/runner/sessions/"+keyed, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("agent token reading an ownerless session: %d, want 404", resp.StatusCode)
	}
}

// I-2: a message that resumes a disconnected cluster session carries the
// CALLER's account, so the replacement pod runs on the owner's personal
// credentials rather than falling back to the operator Secret.
func TestAgentTokenResumeCarriesTheOwner(t *testing.T) {
	fake := &fakeK8s{credentialFetchResponses: map[string][]byte{
		"acct-a:claude": []byte("sk-ant-oat-personal"),
	}}
	api, _, pool := clusterRunnerAPI(t, fake)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	api.coreAuth = newTestCoreAuthClient(t, pub, "core-1")
	tok := mintRunnerToken(t, priv, "core-1", identity.Claims{
		Sub: "tok-a", Aud: "blerg-runner", Kind: "agent", OnBehalfOf: "acct-a",
		Caps: []string{coreAuthRunnerCap}, ExpiresAt: time.Now().Unix() + 300,
	})
	srv := httptest.NewServer(agentContractMux(api))
	defer srv.Close()

	resp := startReq(t, srv, tok, "", map[string]any{"repo": "org/proj", "prompt": "go"})
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("start: %d (%s)", resp.StatusCode, readAllString(t, resp.Body))
	}
	sessionID := decodeSessionID(t, resp)
	resp.Body.Close()

	ctx := context.Background()
	if _, err := pool.Exec(ctx, `UPDATE sessions SET status = 'disconnected' WHERE id = $1`, sessionID); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.created = nil
	fake.createdSecrets = nil
	fake.credentialFetchTokenIDs = nil
	fake.mu.Unlock()

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/runner/sessions/"+sessionID+"/message",
		strings.NewReader(`{"text":"carry on"}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	msgResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer msgResp.Body.Close()
	if msgResp.StatusCode != http.StatusAccepted {
		t.Fatalf("message: %d (%s)", msgResp.StatusCode, readAllString(t, msgResp.Body))
	}

	fake.mu.Lock()
	jobs := len(fake.created)
	secrets := append([]map[string]any(nil), fake.createdSecrets...)
	ids := append([]string(nil), fake.credentialFetchTokenIDs...)
	fake.mu.Unlock()
	if jobs != 1 {
		t.Fatalf("the resume created %d Jobs, want 1", jobs)
	}
	// SpawningAccountID reached the spec: the replacement pod carries acct-a's
	// OWN engine credential, which only a resume that knows the owner fetches.
	if len(secrets) != 1 {
		t.Fatalf("resume created %d per-session Secrets, want 1", len(secrets))
	}
	raw, _ := json.Marshal(secrets[0])
	if !strings.Contains(string(raw), "sk-ant-oat-personal") {
		t.Errorf("resumed Secret does not carry the owner's personal credential: %s", raw)
	}
	if len(ids) == 0 {
		t.Fatal("the resume fetched no personal credential: the owner was not threaded through")
	}
	for _, got := range ids {
		if got != "tok-a" {
			t.Errorf("resume credential fetch token_id = %q, want tok-a", got)
		}
	}
}

// A session row with no spawning account is nobody's as far as a scoped token
// is concerned, even when the token's own account id is empty-ish.
func TestRequireSessionAccessOwnerlessSession(t *testing.T) {
	api, _, pool := clusterRunnerAPI(t, &fakeK8s{})
	ctx := context.Background()
	const daemonID = "00000000-0000-4000-8000-0000000000f1"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "cluster", "runner", ""); err != nil {
		t.Fatal(err)
	}
	id := newUUID()
	if err := db.InsertSession(ctx, pool, id, daemonID, "running", "/workspace/p", "p", "T", ""); err != nil {
		t.Fatal(err)
	}
	scoped := runnerPrincipal{Kind: agentPrincipalKind, Sub: "tok-x", OnBehalfOf: "acct-x",
		Caps: []string{coreAuthRunnerCap}}
	if _, apiErr := api.requireSessionAccess(ctx, scoped, id); apiErr == nil || apiErr.Status != http.StatusNotFound {
		t.Errorf("ownerless session for a scoped token: %v, want 404", apiErr)
	}
	if _, apiErr := api.requireSessionAccess(ctx, runnerPrincipal{Kind: runnerKeyPrincipalKind}, id); apiErr != nil {
		t.Errorf("ownerless session for the runner key: %v, want access", apiErr)
	}
}
