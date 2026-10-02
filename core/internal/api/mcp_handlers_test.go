package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/core/internal/api"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/identity"
	"github.com/blerglab/blerg-ai/core/internal/keybackend"
	"github.com/blerglab/blerg-ai/core/internal/mcpconn"
)

const mcpSecret = "mcp-top-secret-value-9f3a"

type mcpEnv struct {
	deps   api.Deps
	pool   *pgxpool.Pool
	srv    *httptest.Server
	idSvc  *identity.Service
	acctA  string
	acctB  string
	humanA string
	humanB string
	agentA string // agent token (card.read) acting for account A
	sidA   string // live human session of A
	sidB   string
	connA  string // a static connection of A holding mcpSecret
}

func newMCPEnv(t *testing.T) *mcpEnv {
	t.Helper()
	deps, pool := newInternalTestDeps(t)
	backend, err := keybackend.NewLocal(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	deps.MCPConnections = mcpconn.NewService(db.NewPgStore(pool), backend, netguard.Policy{})
	srv := httptest.NewServer(api.NewRouter(deps))
	t.Cleanup(srv.Close)
	idSvc := deps.Identity.(*identity.Service)
	ctx := context.Background()

	e := &mcpEnv{deps: deps, pool: pool, srv: srv, idSvc: idSvc}
	e.acctA = insertCredAccount(t, pool, "mcp-a")
	e.acctB = insertCredAccount(t, pool, "mcp-b")
	var err2 error
	if e.humanA, err2 = idSvc.MintHumanAccessToken(ctx, e.acctA, "blerg-core"); err2 != nil {
		t.Fatal(err2)
	}
	if e.humanB, err2 = idSvc.MintHumanAccessToken(ctx, e.acctB, "blerg-core"); err2 != nil {
		t.Fatal(err2)
	}
	if e.agentA, err2 = idSvc.MintAgentToken(ctx, identity.AgentTokenInput{
		Sub: "11111111-1111-1111-1111-111111111111", Aud: "blerg-core",
		OnBehalfOf: e.acctA, Lineage: e.acctA, Caps: []string{"card.read"},
	}); err2 != nil {
		t.Fatal(err2)
	}
	e.sidA = insertLiveHumanSession(t, pool, e.acctA)
	e.sidB = insertLiveHumanSession(t, pool, e.acctB)
	c, err := deps.MCPConnections.Create(ctx, e.acctA, mcpconn.CreateInput{
		Name: "notes", URL: "https://mcp.example.com/mcp", AuthKind: "static", Secret: mcpSecret,
	})
	if err != nil {
		t.Fatal(err)
	}
	e.connA = c.ID
	return e
}

// do sends one request and returns status and body.
func (e *mcpEnv) do(t *testing.T, method, path, bearer string, body any, hdr map[string]string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func (e *mcpEnv) internal(t *testing.T, path, key string, body any) (int, string, http.Header) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := e.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

// The route-group table: every public route, with no token, a garbage token and an agent token.
func TestMCPPublicRoutesAuthMatrix(t *testing.T) {
	e := newMCPEnv(t)
	id := e.connA
	routes := []struct {
		name, method, path string
		body               any
	}{
		{"list", http.MethodGet, "/api/mcp/connections", nil},
		{"create", http.MethodPost, "/api/mcp/connections", map[string]string{"name": "x", "url": "https://m.example.com", "auth_kind": "none"}},
		{"patch", http.MethodPatch, "/api/mcp/connections/" + id, map[string]string{"name": "renamed"}},
		{"delete", http.MethodDelete, "/api/mcp/connections/" + id, nil},
	}
	for _, rt := range routes {
		t.Run(rt.name, func(t *testing.T) {
			if code, _ := e.do(t, rt.method, rt.path, "", rt.body, nil); code != http.StatusUnauthorized {
				t.Errorf("no token = %d, want 401", code)
			}
			if code, _ := e.do(t, rt.method, rt.path, "garbage", rt.body, nil); code != http.StatusUnauthorized {
				t.Errorf("garbage token = %d, want 401", code)
			}
			if code, body := e.do(t, rt.method, rt.path, e.agentA, rt.body, nil); code != http.StatusForbidden {
				t.Errorf("agent token = %d, want 403 (%s)", code, body)
			}
		})
	}
	// None of that touched the connection.
	if _, err := e.deps.MCPConnections.Get(context.Background(), e.acctA, id); err != nil {
		t.Fatalf("connection changed by rejected calls: %v", err)
	}
}

func TestMCPWriteRoutesEnforceSameOrigin(t *testing.T) {
	e := newMCPEnv(t)
	writes := []struct {
		name, method, path string
		body               any
	}{
		{"create", http.MethodPost, "/api/mcp/connections", map[string]string{"name": "x", "url": "https://m.example.com", "auth_kind": "none"}},
		{"patch", http.MethodPatch, "/api/mcp/connections/" + e.connA, map[string]string{"name": "renamed"}},
		{"delete", http.MethodDelete, "/api/mcp/connections/" + e.connA, nil},
	}
	for _, rt := range writes {
		t.Run(rt.name, func(t *testing.T) {
			for _, hdr := range []map[string]string{
				{"Origin": "https://evil.example"},
				{"Sec-Fetch-Site": "cross-site"},
				{"Sec-Fetch-Site": "same-site"},
			} {
				if code, _ := e.do(t, rt.method, rt.path, e.humanA, rt.body, hdr); code != http.StatusForbidden {
					t.Errorf("%v = %d, want 403", hdr, code)
				}
			}
		})
	}
	if l, _ := e.deps.MCPConnections.List(context.Background(), e.acctA); len(l) != 1 || l[0].Name != "notes" {
		t.Fatalf("a cross-site request changed state: %+v", l)
	}
	// Own origin passes (create as the positive control).
	code, body := e.do(t, http.MethodPost, "/api/mcp/connections", e.humanA,
		map[string]string{"name": "x", "url": "https://m.example.com", "auth_kind": "none"}, map[string]string{"Origin": e.srv.URL})
	if code != http.StatusCreated {
		t.Fatalf("same-origin create = %d %s", code, body)
	}
}

func TestMCPAnotherAccountsConnectionIs404(t *testing.T) {
	e := newMCPEnv(t)
	path := "/api/mcp/connections/" + e.connA
	if code, _ := e.do(t, http.MethodPatch, path, e.humanB, map[string]string{"name": "stolen"}, nil); code != 404 {
		t.Errorf("patch = %d, want 404", code)
	}
	if code, _ := e.do(t, http.MethodDelete, path, e.humanB, nil, nil); code != 404 {
		t.Errorf("delete = %d, want 404", code)
	}
	// Identical to an id that does not exist at all.
	if code, _ := e.do(t, http.MethodDelete, "/api/mcp/connections/"+noSuchSession, e.humanB, nil, nil); code != 404 {
		t.Errorf("delete missing = %d, want 404", code)
	}
	code, body := e.do(t, http.MethodGet, "/api/mcp/connections", e.humanB, nil, nil)
	if code != 200 || strings.Contains(body, "notes") || strings.Contains(body, e.connA) {
		t.Errorf("B lists A's connection: %d %s", code, body)
	}
	if c, err := e.deps.MCPConnections.Get(context.Background(), e.acctA, e.connA); err != nil || c.Name != "notes" {
		t.Fatalf("A's connection was touched: %+v %v", c, err)
	}
}

func TestMCPPublicValidationErrors(t *testing.T) {
	e := newMCPEnv(t)
	post := func(body any) (int, string) {
		return e.do(t, http.MethodPost, "/api/mcp/connections", e.humanA, body, nil)
	}
	if code, body := post(map[string]string{"name": "o", "url": "https://m.example.com", "auth_kind": "oauth"}); code != 400 || !strings.Contains(body, "oauth") {
		t.Errorf("oauth = %d %s, want 400 mentioning oauth", code, body)
	}
	if code, _ := post(map[string]string{"name": "board", "url": "https://m.example.com", "auth_kind": "none"}); code != 400 {
		t.Errorf("reserved = %d, want 400", code)
	}
	if code, _ := post(map[string]string{"name": "h", "url": "http://m.example.com", "auth_kind": "none"}); code != 400 {
		t.Errorf("http url = %d, want 400", code)
	}
	if code, _ := post(map[string]string{"name": "notes", "url": "https://m.example.com", "auth_kind": "none"}); code != 409 {
		t.Errorf("duplicate = %d, want 409", code)
	}
	if code, _ := post(map[string]any{"name": "u", "url": "https://m.example.com", "auth_kind": "none", "surprise": 1}); code != 400 {
		t.Errorf("unknown field = %d, want 400", code)
	}
	big := strings.Repeat("x", 20<<10)
	if code, _ := post(map[string]string{"name": "big", "url": "https://m.example.com", "auth_kind": "static", "secret": big}); code != 413 {
		t.Errorf("oversized body = %d, want 413", code)
	}
}

// The secret must not appear in any response of the public API or the internal list, nor in any
// log line; only the internal token route returns it, and that response is never logged either.
func TestMCPSecretNeverInResponsesOrLogs(t *testing.T) {
	e := newMCPEnv(t)
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(io.Discard) })

	var bodies []string
	rec := func(code int, body string) { bodies = append(bodies, fmt.Sprintf("%d %s", code, body)) }

	rec(e.do(t, http.MethodGet, "/api/mcp/connections", e.humanA, nil, nil))
	code, body := e.do(t, http.MethodPost, "/api/mcp/connections", e.humanA,
		map[string]string{"name": "second", "url": "https://m2.example.com", "auth_kind": "static", "secret": mcpSecret + "-2", "header_name": "X-Api-Key"}, nil)
	rec(code, body)
	if code != 201 {
		t.Fatalf("create = %d %s", code, body)
	}
	var created struct {
		ID        string `json:"id"`
		HasSecret bool   `json:"has_secret"`
	}
	_ = json.Unmarshal([]byte(body), &created)
	if !created.HasSecret {
		t.Fatalf("has_secret missing: %s", body)
	}
	rec(e.do(t, http.MethodPatch, "/api/mcp/connections/"+created.ID, e.humanA, map[string]string{"secret": mcpSecret + "-3"}, nil))
	rec(e.do(t, http.MethodPatch, "/api/mcp/connections/"+created.ID, e.humanA, map[string]string{"secret": "bad\nvalue" + mcpSecret}, nil))
	rec(e.do(t, http.MethodGet, "/api/mcp/connections", e.humanA, nil, nil))
	c, b, _ := e.internal(t, "/internal/mcp/connections/list", testInternalKey, map[string]string{"account_id": e.acctA, "session_id": e.sidA})
	rec(c, b)
	// Failure paths that log.
	c, b, _ = e.internal(t, "/internal/mcp/connections/token", testInternalKey, map[string]string{"account_id": e.acctA, "connection_id": e.connA, "session_id": noSuchSession})
	rec(c, b)
	rec(e.do(t, http.MethodDelete, "/api/mcp/connections/"+created.ID, e.humanA, nil, nil))

	for _, b := range bodies {
		if strings.Contains(b, mcpSecret) {
			t.Errorf("secret in a response body: %s", b)
		}
	}

	// The one route that returns it.
	code, tokBody, hdr := e.internal(t, "/internal/mcp/connections/token", testInternalKey, map[string]string{"account_id": e.acctA, "connection_id": e.connA, "session_id": e.sidA})
	if code != 200 || !strings.Contains(tokBody, mcpSecret) {
		t.Fatalf("token = %d %s", code, tokBody)
	}
	if hdr.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", hdr.Get("Cache-Control"))
	}
	if strings.Contains(logs.String(), mcpSecret) {
		t.Errorf("secret in the log output: %s", logs.String())
	}
}

func TestMCPInternalRoutesMatrix(t *testing.T) {
	e := newMCPEnv(t)
	ctx := context.Background()
	revoked := insertLiveHumanSession(t, e.pool, e.acctA)
	if _, err := e.pool.Exec(ctx, `UPDATE human_sessions SET revoked_at = now() WHERE id = $1`, revoked); err != nil {
		t.Fatal(err)
	}
	tokA, _, err := e.idSvc.CreateAgentToken(ctx, e.acctA, "a-tool", "run-sessions", 0)
	if err != nil {
		t.Fatal(err)
	}
	tokB, _, err := e.idSvc.CreateAgentToken(ctx, e.acctB, "b-tool", "run-sessions", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.idSvc.RevokeAgentToken(ctx, e.acctB, tokB.ID); err != nil {
		t.Fatal(err)
	}
	// B has a connection of its own too, to prove account_id scoping.
	bConn, err := e.deps.MCPConnections.Create(ctx, e.acctB, mcpconn.CreateInput{Name: "bs", URL: "https://b.example.com", AuthKind: "static", Secret: "b-secret"})
	if err != nil {
		t.Fatal(err)
	}

	type m = map[string]string
	tokenCases := []struct {
		name string
		key  string
		req  m
		want int
	}{
		{"no key", "", m{"account_id": e.acctA, "connection_id": e.connA, "session_id": e.sidA}, 401},
		{"wrong key", "wrong-key", m{"account_id": e.acctA, "connection_id": e.connA, "session_id": e.sidA}, 401},
		{"a human bearer is not the internal key", e.humanA, m{"account_id": e.acctA, "connection_id": e.connA, "session_id": e.sidA}, 401},
		{"live session", testInternalKey, m{"account_id": e.acctA, "connection_id": e.connA, "session_id": e.sidA}, 200},
		{"live agent token", testInternalKey, m{"account_id": e.acctA, "connection_id": e.connA, "token_id": tokA.ID}, 200},
		{"unknown session", testInternalKey, m{"account_id": e.acctA, "connection_id": e.connA, "session_id": noSuchSession}, 404},
		{"revoked session", testInternalKey, m{"account_id": e.acctA, "connection_id": e.connA, "session_id": revoked}, 404},
		{"revoked agent token", testInternalKey, m{"account_id": e.acctB, "connection_id": bConn.ID, "token_id": tokB.ID}, 404},
		{"other account's live session for A's account", testInternalKey, m{"account_id": e.acctA, "connection_id": e.connA, "session_id": e.sidB}, 404},
		{"other account's agent token for A's account", testInternalKey, m{"account_id": e.acctA, "connection_id": e.connA, "token_id": tokB.ID}, 404},
		{"B's live session asking for A's connection", testInternalKey, m{"account_id": e.acctB, "connection_id": e.connA, "session_id": e.sidB}, 404},
		{"unknown connection", testInternalKey, m{"account_id": e.acctA, "connection_id": noSuchSession, "session_id": e.sidA}, 404},
		{"no proof", testInternalKey, m{"account_id": e.acctA, "connection_id": e.connA}, 400},
		{"both proofs", testInternalKey, m{"account_id": e.acctA, "connection_id": e.connA, "session_id": e.sidA, "token_id": tokA.ID}, 400},
		{"bad connection id", testInternalKey, m{"account_id": e.acctA, "connection_id": "nope", "session_id": e.sidA}, 400},
		{"bad account id", testInternalKey, m{"account_id": "nope", "connection_id": e.connA, "session_id": e.sidA}, 400},
	}
	for _, tc := range tokenCases {
		t.Run("token/"+tc.name, func(t *testing.T) {
			code, body, _ := e.internal(t, "/internal/mcp/connections/token", tc.key, tc.req)
			if code != tc.want {
				t.Fatalf("= %d (%s), want %d", code, body, tc.want)
			}
			if tc.want != 200 && strings.Contains(body, mcpSecret) {
				t.Fatalf("secret in a failure body: %s", body)
			}
		})
	}
	// The 404 bodies must be identical whatever the reason (uniform, no oracle).
	_, dead, _ := e.internal(t, "/internal/mcp/connections/token", testInternalKey, m{"account_id": e.acctA, "connection_id": e.connA, "session_id": noSuchSession})
	_, cross, _ := e.internal(t, "/internal/mcp/connections/token", testInternalKey, m{"account_id": e.acctB, "connection_id": e.connA, "session_id": e.sidB})
	_, missing, _ := e.internal(t, "/internal/mcp/connections/token", testInternalKey, m{"account_id": e.acctA, "connection_id": noSuchSession, "session_id": e.sidA})
	if dead != cross || cross != missing {
		t.Fatalf("404 bodies differ: %q %q %q", dead, cross, missing)
	}

	listCases := []struct {
		name string
		key  string
		req  m
		want int
	}{
		{"no key", "", m{"account_id": e.acctA, "session_id": e.sidA}, 401},
		{"wrong key", "wrong-key", m{"account_id": e.acctA, "session_id": e.sidA}, 401},
		{"live session", testInternalKey, m{"account_id": e.acctA, "session_id": e.sidA}, 200},
		{"dead proof", testInternalKey, m{"account_id": e.acctA, "session_id": noSuchSession}, 404},
		{"cross-account proof", testInternalKey, m{"account_id": e.acctA, "session_id": e.sidB}, 404},
		{"no proof", testInternalKey, m{"account_id": e.acctA}, 400},
	}
	for _, tc := range listCases {
		t.Run("list/"+tc.name, func(t *testing.T) {
			code, body, _ := e.internal(t, "/internal/mcp/connections/list", tc.key, tc.req)
			if code != tc.want {
				t.Fatalf("= %d (%s), want %d", code, body, tc.want)
			}
		})
	}
	// List is scoped to the account and carries no secret.
	_, body, _ := e.internal(t, "/internal/mcp/connections/list", testInternalKey, m{"account_id": e.acctB, "session_id": e.sidB})
	if strings.Contains(body, e.connA) || !strings.Contains(body, bConn.ID) || strings.Contains(body, "b-secret") {
		t.Fatalf("list for B = %s", body)
	}

	// Successful token fetches wrote exactly one audit row each, naming the right principal;
	// rejected ones wrote none.
	var n int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM mcp_secret_access_log`).Scan(&n)
	if n != 2 {
		t.Fatalf("audit rows = %d, want 2 (only the two 200 cases)", n)
	}
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM mcp_secret_access_log WHERE fetched_by_session_id = $1 AND fetched_by_token_id IS NULL`, e.sidA).Scan(&n)
	if n != 1 {
		t.Fatalf("session audit rows = %d, want 1", n)
	}
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM mcp_secret_access_log WHERE fetched_by_token_id = $1 AND fetched_by_session_id IS NULL`, tokA.ID).Scan(&n)
	if n != 1 {
		t.Fatalf("token audit rows = %d, want 1", n)
	}
}

func TestMCPInternalTokenResponseShape(t *testing.T) {
	e := newMCPEnv(t)
	code, body, _ := e.internal(t, "/internal/mcp/connections/token", testInternalKey, map[string]string{"account_id": e.acctA, "connection_id": e.connA, "session_id": e.sidA})
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	if got["url"] != "https://mcp.example.com/mcp" || got["header_name"] != "Authorization" || got["value"] != mcpSecret {
		t.Fatalf("token = %v", got)
	}
	if _, has := got["expires_at"]; has {
		t.Fatalf("a static secret has no expires_at: %v", got)
	}
}

func TestMCPInternalTokenAuditFailureAbortsAndReturnsNoSecret(t *testing.T) {
	e := newMCPEnv(t)
	if _, err := e.pool.Exec(context.Background(), `DROP TABLE mcp_secret_access_log`); err != nil {
		t.Fatal(err)
	}
	code, body, _ := e.internal(t, "/internal/mcp/connections/token", testInternalKey, map[string]string{"account_id": e.acctA, "connection_id": e.connA, "session_id": e.sidA})
	if code != http.StatusInternalServerError {
		t.Fatalf("= %d, want 500", code)
	}
	if strings.Contains(body, mcpSecret) || strings.Contains(body, "value") {
		t.Fatalf("secret material in the failure body: %s", body)
	}
}
