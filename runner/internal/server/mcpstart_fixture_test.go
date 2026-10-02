package server

// Fixtures for the MCP start-path tests: a fake core (connection list), a fake Streamable
// HTTP MCP upstream, the real gateway code path for the live tool check, a fake Kubernetes
// API and a real database.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	mcpAcct = "user-1" // the human's account: Sub of the tokens minted below
	mcpConn = "calendar"
)

// ---- fake upstream MCP server (JSON replies) ------------------------------------------

type mcpTestTool struct{ Name, Description, Schema, Annotations string }

type mcpTestUpstream struct {
	srv *httptest.Server
	mu  sync.Mutex
	// tools is what tools/list returns; status, when set, answers every request with it.
	tools  []mcpTestTool
	status int
	lists  int
	// tools/call: how many arrived and the last arguments; callDelay holds each call for that
	// long (honouring the request's cancellation); callResult, when set, is the result (default
	// a text block "ok:<tool>"); callRPCError answers with a JSON-RPC error instead.
	calls        int
	lastArgs     json.RawMessage
	callDelay    time.Duration
	callResult   any
	callRPCError string
	callRPCCode  int // the error code of callRPCError; 0 means -32000
}

func (u *mcpTestUpstream) callCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls
}

func (u *mcpTestUpstream) lastCallArgs() json.RawMessage {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lastArgs
}

func newMCPTestUpstream(t *testing.T) *mcpTestUpstream {
	t.Helper()
	u := &mcpTestUpstream{tools: []mcpTestTool{
		{Name: "echo", Description: "echoes", Schema: `{"type":"object","properties":{"msg":{"type":"string"}}}`},
		{Name: "send", Description: "sends a message", Schema: `{"type":"object"}`},
	}}
	u.srv = httptest.NewServer(http.HandlerFunc(u.handle))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *mcpTestUpstream) url() string { return u.srv.URL + "/mcp" }

func (u *mcpTestUpstream) hash(name string) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, tl := range u.tools {
		if tl.Name == name {
			return mcpgw.ToolHash(tl.Name, tl.Description, json.RawMessage(tl.Schema))
		}
	}
	return ""
}

func (u *mcpTestUpstream) setDescription(name, desc string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for i := range u.tools {
		if u.tools[i].Name == name {
			u.tools[i].Description = desc
		}
	}
}

func (u *mcpTestUpstream) listCount() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lists
}

func (u *mcpTestUpstream) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	status := u.status
	u.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	reply := func(result any) {
		msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(msg)
	}
	switch req.Method {
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "sess-1")
		reply(map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{"name": "fake", "version": "0"}})
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		u.mu.Lock()
		u.lists++
		var out []map[string]any
		for _, tl := range u.tools {
			entry := map[string]any{"name": tl.Name, "description": tl.Description, "inputSchema": json.RawMessage(tl.Schema)}
			if tl.Annotations != "" {
				entry["annotations"] = json.RawMessage(tl.Annotations)
			}
			out = append(out, entry)
		}
		u.mu.Unlock()
		reply(map[string]any{"tools": out})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		var env struct {
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal(body, &env)
		_ = json.Unmarshal(env.Params, &p)
		u.mu.Lock()
		u.calls++
		u.lastArgs = p.Arguments
		delay, res, rpcErr, rpcCode := u.callDelay, u.callResult, u.callRPCError, u.callRPCCode
		if rpcCode == 0 {
			rpcCode = -32000
		}
		u.mu.Unlock()
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
		if rpcErr != "" {
			msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": rpcCode, "message": rpcErr}})
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(msg)
			return
		}
		if res == nil {
			res = map[string]any{"content": []map[string]any{{"type": "text", "text": "ok:" + p.Name}}}
		}
		reply(res)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

// ---- fakes for core ---------------------------------------------------------------------

// mcpTokenCore is the gateway's credential fetch (core's /token): it points at the upstream.
type mcpTokenCore struct {
	url string
	err error

	mu     sync.Mutex
	proofs []mcpgw.Proof // every credential fetch's proof
}

func (c *mcpTokenCore) proofsSeen() []mcpgw.Proof {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]mcpgw.Proof(nil), c.proofs...)
}

func (c *mcpTokenCore) Token(_ context.Context, p mcpgw.Proof, _ string) (mcpgw.Credential, error) {
	c.mu.Lock()
	c.proofs = append(c.proofs, p)
	c.mu.Unlock()
	if c.err != nil {
		return mcpgw.Credential{}, c.err
	}
	return mcpgw.Credential{URL: c.url, HeaderName: "X-Api-Key", Value: "upstream-secret"}, nil
}

// fakeMCPList is core's connection list.
type fakeMCPList struct {
	mu     sync.Mutex
	conns  []MCPConnection
	err    error
	proofs []mcpgw.Proof

	saved   []savedDefaults
	saveErr error
}

type savedDefaults struct {
	proof  mcpgw.Proof
	connID string
	tools  map[string]mcpgw.ToolGrant
}

func (f *fakeMCPList) SetDefaultTools(_ context.Context, p mcpgw.Proof, connectionID string, tools map[string]mcpgw.ToolGrant) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved = append(f.saved, savedDefaults{proof: p, connID: connectionID, tools: tools})
	return nil
}

func (f *fakeMCPList) ListConnections(_ context.Context, p mcpgw.Proof) ([]MCPConnection, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.proofs = append(f.proofs, p)
	if f.err != nil {
		return nil, f.err
	}
	return append([]MCPConnection(nil), f.conns...), nil
}

// ---- the fixture ------------------------------------------------------------------------

type mcpFx struct {
	t      *testing.T
	api    *API
	hub    *Hub
	pool   *pgxpool.Pool
	dc     *DaemonConn
	k8s    *fakeK8s
	up     *mcpTestUpstream
	list   *fakeMCPList
	tokens *mcpTokenCore
	priv   ed25519.PrivateKey
	connID string
}

const mcpGatewayURL = "http://gateway.internal:9090"

func newMCPFx(t *testing.T) *mcpFx {
	t.Helper()
	fx := &mcpFx{t: t, pool: runnerContractPool(t), k8s: &fakeK8s{}, up: newMCPTestUpstream(t), connID: newUUID()}
	fx.hub = NewHub()
	jm := newTestJobManager(t, fx.k8s)
	jm.GitHubOrg = "acme"
	fx.hub.SetJobManager(jm)

	daemonID := "0d0d0d0d-0d0d-4d0d-8d0d-0d0d0d0d0d0d"
	if err := db.UpsertDaemon(context.Background(), fx.pool, daemonID, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	fx.dc = &DaemonConn{ID: daemonID, Name: "laptop", ReposRoot: "/repos", send: make(chan []byte, 8)}
	fx.dc.SetMCPGateway(true)
	fx.dc.SetRestrictTools(true)
	fx.dc.SetSandboxAvailable(true)
	fx.hub.Register(fx.dc)

	fx.api = NewAPI(fx.hub, fx.pool, "daemon-tok-1234567890", nil, "")
	fx.api.SetRunnerKey(runnerTestKey)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	fx.priv = priv
	fx.api.coreAuth = newTestCoreAuthClient(t, pub, "core-1")

	fx.tokens = &mcpTokenCore{url: fx.up.url()}
	gw := mcpgw.New(mcpgw.Config{
		DB: fx.pool, Core: fx.tokens,
		Policy: netguard.Policy{AllowHTTPHosts: []string{"127.0.0.1"}, AllowPrivateHosts: []string{"127.0.0.1"}},
	})
	fx.list = &fakeMCPList{conns: []MCPConnection{{
		ID: fx.connID, Name: mcpConn, URL: fx.up.url(), AuthKind: "static", Status: "ok",
		DefaultTools: map[string]mcpgw.ToolGrant{"echo": {Mode: mcpgw.ModeAllow, Hash: fx.up.hash("echo")}},
	}}}
	fx.api.setMCPStart(&mcpStartConfig{Hasher: gw, GatewayURL: mcpGatewayURL, Core: fx.list})
	return fx
}

// token mints a core token: kind "human" (Sub user-1, login session sid) or "agent" (acting for user-1).
func (fx *mcpFx) token(kind, sid string) string {
	fx.t.Helper()
	c := identity.Claims{
		Sub: mcpAcct, Aud: coreAuthAudience, Kind: kind, Sid: sid,
		Caps: []string{"session.start"}, ExpiresAt: time.Now().Add(time.Minute).Unix(),
	}
	if kind == "agent" {
		c.Sub, c.OnBehalfOf, c.Sid = "tok-1", mcpAcct, ""
	}
	return mintRunnerToken(fx.t, fx.priv, "core-1", c)
}

func (fx *mcpFx) human() string { return fx.token("human", testSID) }

// selection is a one-connection `mcp` value naming echo with the hash the upstream serves.
func (fx *mcpFx) selection() []map[string]any {
	return []map[string]any{{"connection": fx.connID, "tools": map[string]any{
		"echo": map[string]any{"mode": "allow", "hash": fx.up.hash("echo")},
	}}}
}

// do calls a handler directly with a JSON body.
func (fx *mcpFx) do(h http.HandlerFunc, bearer string, body any) *httptest.ResponseRecorder {
	fx.t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		fx.t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(string(raw)))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

// sessionID decodes {"session_id": ...} from a 202.
func (fx *mcpFx) sessionID(rec *httptest.ResponseRecorder) string {
	fx.t.Helper()
	if rec.Code != http.StatusAccepted {
		fx.t.Fatalf("status %d, want 202: %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out["session_id"] == "" {
		fx.t.Fatalf("no session_id in %s (%v)", rec.Body.String(), err)
	}
	return out["session_id"]
}

func (fx *mcpFx) count(table string) int {
	fx.t.Helper()
	var n int
	if err := fx.pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		fx.t.Fatal(err)
	}
	return n
}

func (fx *mcpFx) grants(sessionID string) []db.MCPGrant {
	fx.t.Helper()
	g, err := db.ListMCPGrantsForSession(context.Background(), fx.pool, sessionID)
	if err != nil {
		fx.t.Fatal(err)
	}
	return g
}

// secretConfig returns the gateway config the session's Kubernetes Secret holds.
func (fx *mcpFx) secretConfig(sessionID string) *protocol.MCPGatewayConfig {
	fx.t.Helper()
	fx.k8s.mu.Lock()
	defer fx.k8s.mu.Unlock()
	for _, s := range fx.k8s.createdSecrets {
		meta, _ := s["metadata"].(map[string]any)
		if meta["name"] != sessionSecretName(sessionID) {
			continue
		}
		sd, _ := s["stringData"].(map[string]any)
		raw, ok := sd["BLERG_RUNNER_MCP_CONFIG"].(string)
		if !ok {
			fx.t.Fatalf("Secret %v holds no BLERG_RUNNER_MCP_CONFIG", meta["name"])
		}
		var cfg protocol.MCPGatewayConfig
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			fx.t.Fatal(err)
		}
		return &cfg
	}
	fx.t.Fatalf("no Secret for session %s among %d", sessionID, len(fx.k8s.createdSecrets))
	return nil
}

// tokenBelongsTo reports whether the raw gateway token is the live token of a grant of sessionID.
func (fx *mcpFx) tokenBelongsTo(token, sessionID string) bool {
	fx.t.Helper()
	sum := sha256.Sum256([]byte(token))
	g, err := db.MCPGrantByTokenHash(context.Background(), fx.pool, sum[:])
	return err == nil && g.SessionID == sessionID
}

// assertGranted checks what every accepted start with a grant must leave behind: the grant
// row for the caller's account with the human's login as proof, the pinned tool, a private
// session owned by that account, and a config whose token is the grant's live token.
func (fx *mcpFx) assertGranted(sessionID string, cfg *protocol.MCPGatewayConfig) {
	fx.t.Helper()
	gs := fx.grants(sessionID)
	if len(gs) != 1 {
		fx.t.Fatalf("session %s has %d grants, want 1", sessionID, len(gs))
	}
	g := gs[0]
	if g.AccountID != mcpAcct || g.ConnectionID != fx.connID || g.Name != mcpConn ||
		g.ProofKind != "session_id" || g.ProofValue != testSID || g.URLSnapshot != fx.up.url() {
		fx.t.Errorf("grant = %+v", g)
	}
	if tg := g.Tools["echo"]; tg.Mode != mcpgw.ModeAllow || tg.Hash != fx.up.hash("echo") || len(g.Tools) != 1 {
		fx.t.Errorf("grant tools = %+v", g.Tools)
	}
	row, err := db.GetSession(context.Background(), fx.pool, sessionID)
	if err != nil || row == nil {
		fx.t.Fatalf("GetSession: %v", err)
	}
	if !row.Private {
		fx.t.Error("a session with a grant must be private")
	}
	if row.SpawningAccountID == nil || *row.SpawningAccountID != mcpAcct {
		fx.t.Errorf("spawning account = %v, want %s", row.SpawningAccountID, mcpAcct)
	}
	if cfg == nil || cfg.BaseURL != mcpGatewayURL || len(cfg.Servers) != 1 || cfg.Servers[0].Name != mcpConn {
		fx.t.Fatalf("delivered config = %+v", cfg)
	}
	if !fx.tokenBelongsTo(cfg.Servers[0].Token, sessionID) {
		fx.t.Error("the delivered token is not the live token of the session's grant")
	}
}
