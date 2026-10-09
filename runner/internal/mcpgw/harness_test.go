package mcpgw

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool connects to TEST_DATABASE_URL in a private schema (set on every pooled
// connection) and applies the real migrations. A missing variable is a failure, not a skip:
// these tests exist to exercise the database.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Fatal("TEST_DATABASE_URL must be set: the gateway tests need a real database")
	}
	ctx := context.Background()
	schema := "test_mcpgw"
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET search_path TO "+schema)
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		admin.Close()
	})
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	return pool
}

func newUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// ---- fake Streamable HTTP MCP server -------------------------------------------------

type fakeTool struct {
	Name        string
	Description string
	Schema      string // JSON
}

type fakeUpstream struct {
	srv *httptest.Server

	mu          sync.Mutex
	sse         bool
	tools       []fakeTool
	pageSize    int
	sessions    map[string]bool
	nextSession int
	inits       int
	lists       int
	calls       int
	lastHeaders http.Header
	lastArgs    json.RawMessage
	callBody    func(name string, args json.RawMessage) any // result object; nil: text "ok:<name>"
	gate        chan struct{}                               // when set, tools/call blocks until closed
	entered     chan struct{}                               // receives one value per blocked call
	status      int                                         // when non-zero, every request answers with it
}

func newFakeUpstream(t *testing.T, tools ...fakeTool) *fakeUpstream {
	t.Helper()
	u := &fakeUpstream{tools: tools, sessions: map[string]bool{}, entered: make(chan struct{}, 64)}
	u.srv = httptest.NewServer(http.HandlerFunc(u.handle))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *fakeUpstream) url() string { return u.srv.URL + "/mcp" }

func (u *fakeUpstream) snapshot() (inits, lists, calls int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.inits, u.lists, u.calls
}

func (u *fakeUpstream) setSSE(v bool) {
	u.mu.Lock()
	u.sse = v
	u.mu.Unlock()
}

func (u *fakeUpstream) dropSessions() {
	u.mu.Lock()
	u.sessions = map[string]bool{}
	u.mu.Unlock()
}

func (u *fakeUpstream) setDescription(name, desc string) {
	u.mu.Lock()
	for i := range u.tools {
		if u.tools[i].Name == name {
			u.tools[i].Description = desc
		}
	}
	u.mu.Unlock()
}

func (u *fakeUpstream) hash(name string) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, t := range u.tools {
		if t.Name == name {
			return ToolHash(t.Name, t.Description, json.RawMessage(t.Schema))
		}
	}
	return ""
}

func (u *fakeUpstream) reply(w http.ResponseWriter, sse bool, id json.RawMessage, result any) {
	msg, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	if !sse {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(msg)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	// A server notification and a server-initiated request first: both must be ignored.
	_, _ = fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{}}\n\n")
	_, _ = fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":\"srv-1\",\"method\":\"ping\"}\n\n")
	_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
}

func (u *fakeUpstream) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	u.mu.Lock()
	u.lastHeaders = r.Header.Clone()
	sse, status := u.sse, u.status
	u.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	var req rpcMessage
	if err := json.Unmarshal(body, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if req.Method != "initialize" {
		sid := r.Header.Get("Mcp-Session-Id")
		u.mu.Lock()
		ok := u.sessions[sid]
		u.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
	}
	switch req.Method {
	case "initialize":
		u.mu.Lock()
		u.inits++
		u.nextSession++
		sid := fmt.Sprintf("sess-%d", u.nextSession)
		u.sessions[sid] = true
		u.mu.Unlock()
		w.Header().Set("Mcp-Session-Id", sid)
		u.reply(w, sse, req.ID, map[string]any{
			"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{"name": "fake", "version": "0"},
		})
	case "notifications/initialized":
		w.WriteHeader(http.StatusAccepted)
	case "tools/list":
		var p struct{ Cursor string }
		_ = json.Unmarshal(req.Params, &p)
		u.mu.Lock()
		u.lists++
		tools := u.tools
		size := u.pageSize
		u.mu.Unlock()
		start := 0
		if p.Cursor != "" {
			_, _ = fmt.Sscanf(p.Cursor, "c%d", &start)
		}
		end := len(tools)
		next := ""
		if size > 0 && start+size < len(tools) {
			end = start + size
			next = fmt.Sprintf("c%d", end)
		}
		var out []map[string]any
		for _, t := range tools[start:end] {
			out = append(out, map[string]any{
				"name": t.Name, "description": t.Description, "inputSchema": json.RawMessage(t.Schema),
				"annotations": map[string]any{"readOnlyHint": true},
			})
		}
		res := map[string]any{"tools": out}
		if next != "" {
			res["nextCursor"] = next
		}
		u.reply(w, sse, req.ID, res)
	case "tools/call":
		var p struct {
			Name      string
			Arguments json.RawMessage
		}
		_ = json.Unmarshal(req.Params, &p)
		u.mu.Lock()
		u.calls++
		u.lastArgs = p.Arguments
		gate, cb := u.gate, u.callBody
		u.mu.Unlock()
		if gate != nil {
			u.entered <- struct{}{}
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		var result any = map[string]any{"content": []map[string]any{{"type": "text", "text": "ok:" + p.Name}}}
		if cb != nil {
			result = cb(p.Name, p.Arguments)
		}
		u.reply(w, sse, req.ID, result)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

// ---- fake core ---------------------------------------------------------------------

type fakeCore struct {
	mu        sync.Mutex
	url       string
	header    string
	value     string
	expiresIn time.Duration // 0: no expiry
	clock     *fakeClock
	err       error
	calls     int
	lastProof Proof
	lastConn  string
}

func (c *fakeCore) Token(_ context.Context, p Proof, connectionID string) (Credential, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	c.lastProof, c.lastConn = p, connectionID
	if c.err != nil {
		return Credential{}, c.err
	}
	cred := Credential{URL: c.url, HeaderName: c.header, Value: c.value}
	if c.expiresIn != 0 {
		cred.ExpiresAt = c.clock.Now().Add(c.expiresIn)
	}
	return cred, nil
}

func (c *fakeCore) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// ---- harness -----------------------------------------------------------------------

type harness struct {
	t     *testing.T
	pool  *pgxpool.Pool
	gw    *Gateway
	srv   *httptest.Server
	up    *fakeUpstream
	core  *fakeCore
	clock *fakeClock
	// snapURL, when set, is the url_snapshot of new grants instead of the upstream's own URL.
	snapURL string
}

var defaultTools = []fakeTool{
	{Name: "echo", Description: "echoes", Schema: `{"type":"object","properties":{"msg":{"type":"string"}}}`},
	{Name: "secret", Description: "not granted", Schema: `{"type":"object"}`},
	{Name: "send", Description: "sends a message", Schema: `{"type":"object","properties":{"to":{"type":"string"}}}`},
}

func newHarness(t *testing.T, mutate func(*Config), tools ...fakeTool) *harness {
	t.Helper()
	if len(tools) == 0 {
		// A copy: a test that edits a tool (setDescription) must not change the next test's upstream.
		tools = append([]fakeTool(nil), defaultTools...)
	}
	pool := testPool(t)
	clock := &fakeClock{t: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	up := newFakeUpstream(t, tools...)
	core := &fakeCore{url: up.url(), header: "X-Api-Key", value: "secret-1", clock: clock}
	cfg := Config{
		DB: pool, Core: core, Now: clock.Now,
		Policy: netguard.Policy{
			AllowHTTPHosts:    []string{"127.0.0.1", "localhost"},
			AllowPrivateHosts: []string{"127.0.0.1", "localhost"},
		},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	gw := New(cfg)
	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)
	return &harness{t: t, pool: pool, gw: gw, srv: srv, up: up, core: core, clock: clock}
}

type grantHandle struct {
	sessionID, connectionID, name, token string
}

// grant creates one grant on the harness's upstream. tools maps tool name to mode; each
// tool is pinned to the hash the upstream currently reports.
func (h *harness) grant(name string, tools map[string]string, budget int) grantHandle {
	h.t.Helper()
	pins := map[string]ToolGrant{}
	for tool, mode := range tools {
		pins[tool] = ToolGrant{Mode: mode, Hash: h.up.hash(tool)}
	}
	return h.grantPinned(name, pins, budget)
}

func (h *harness) grantPinned(name string, pins map[string]ToolGrant, budget int) grantHandle {
	h.t.Helper()
	gh := grantHandle{sessionID: newUUID(h.t), connectionID: newUUID(h.t), name: name}
	snap := h.up.url()
	if h.snapURL != "" {
		snap = h.snapURL
	}
	toks, err := CreateGrants(context.Background(), h.pool, gh.sessionID, "acct-1",
		Proof{AccountID: "acct-1", SessionID: "login-1"},
		[]GrantSpec{{ConnectionID: gh.connectionID, Name: name, URL: snap, Tools: pins, CallBudget: budget}})
	if err != nil {
		h.t.Fatalf("CreateGrants: %v", err)
	}
	gh.token = toks[gh.connectionID]
	return gh
}

type rpcReply struct {
	status int
	header http.Header
	body   map[string]any
	raw    string
}

func (r rpcReply) errMessage() string {
	if e, ok := r.body["error"].(map[string]any); ok {
		s, _ := e["message"].(string)
		return s
	}
	return ""
}

func (r rpcReply) result() map[string]any {
	m, _ := r.body["result"].(map[string]any)
	return m
}

// post sends a raw body to the gateway.
func (h *harness) post(token, name, body string, hdr map[string]string) rpcReply {
	h.t.Helper()
	return h.do(http.MethodPost, token, name, body, hdr)
}

func (h *harness) do(method, token, name, body string, hdr map[string]string) rpcReply {
	h.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, h.srv.URL+PathPrefix+name, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	out := rpcReply{status: resp.StatusCode, header: resp.Header, raw: string(raw)}
	_ = json.Unmarshal(raw, &out.body)
	return out
}

var idCounter atomic.Int64

func (h *harness) call(gh grantHandle, method string, params any) rpcReply {
	h.t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": idCounter.Add(1), "method": method, "params": params})
	return h.post(gh.token, gh.name, string(b), nil)
}

func (h *harness) callTool(gh grantHandle, tool string, args any) rpcReply {
	h.t.Helper()
	return h.call(gh, "tools/call", map[string]any{"name": tool, "arguments": args})
}

func toolNames(r rpcReply) []string {
	var names []string
	tools, _ := r.result()["tools"].([]any)
	for _, t := range tools {
		m, _ := t.(map[string]any)
		names = append(names, m["name"].(string))
	}
	return names
}

func resultText(r rpcReply) string {
	var sb bytes.Buffer
	content, _ := r.result()["content"].([]any)
	for _, c := range content {
		m, _ := c.(map[string]any)
		s, _ := m["text"].(string)
		sb.WriteString(s)
	}
	return sb.String()
}
