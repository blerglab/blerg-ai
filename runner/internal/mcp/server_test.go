package mcp

// The runner's MCP surface (agent contract v1, spec §4): the same session
// operations the REST contract exposes, reachable as JSON-RPC 2.0 tools at
// POST /mcp. These tests drive the real API — real database, fake k8s — so a
// tool that diverges from its REST twin fails here rather than in an agent's
// hands.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/server"
	"github.com/jackc/pgx/v5/pgxpool"
)

const mcpTestKey = "runner-key-1234567890"

// mcpTestPool gives one test its own schema, pinned on every connection the
// pool opens, so concurrent handler goroutines and other tests' teardown
// cannot collide.
func mcpTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database integration tests")
	}
	schema := mcpTestSchemaName(t.Name())
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

func mcpTestSchemaName(name string) string {
	var b strings.Builder
	b.WriteString("test_mcp_")
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

// fakeK8sHandler is the smallest k8s API a cluster start needs: no Secrets
// configured, no active Jobs, every create accepted.
func fakeK8sHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/secrets/"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/jobs") && r.URL.RawQuery != "":
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/jobs"):
			_ = json.NewEncoder(w).Encode(map[string]any{"status": map[string]any{"succeeded": 0, "failed": 0}})
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"uid": "job-uid-1"}})
		case r.Method == http.MethodPatch, r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

// clusterMCPServer is the runner with a cluster runtime and the static runner
// key enabled, served at /mcp.
func clusterMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	api, _ := clusterMCPAPI(t)
	return serveMCP(t, api)
}

// serveMCP mounts one API's MCP endpoint on a test server.
func serveMCP(t *testing.T, api *server.API) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/mcp", New(api))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// clusterMCPAPI builds the API behind clusterMCPServer, separately so a test
// that needs to wire something else in (a core identity authority, say) can.
// The pool comes back with it: a test that has to seed rows must use the SAME
// one, since each pool call owns a schema named after the test.
func clusterMCPAPI(t *testing.T) (*server.API, *pgxpool.Pool) {
	t.Helper()
	k8s := httptest.NewServer(fakeK8sHandler())
	t.Cleanup(k8s.Close)
	hub := server.NewHub()
	hub.SetJobManager(&server.JobManager{
		BaseURL: k8s.URL, Token: "tok", Namespace: "blerg-runner-sessions",
		Image: "blerg-runner-devcontainer:test", Client: k8s.Client(),
		ServerWSURL: "ws://server/ws/daemon", ServerHTTPURL: "http://server",
		SecretName: "blerg-runner-agent", GitURLBase: "https://git.example.test/org",
		MaxSessions: 4, PodTTLSeconds: 3600,
		CPURequest: "500m", MemRequest: "1Gi", CPULimit: "1", MemLimit: "4Gi",
		TerminationGraceSeconds: 120, TTLSecondsAfterFinished: 3600,
	})
	pool := mcpTestPool(t)
	api := server.NewAPI(hub, pool, "daemon-tok-1234567890", nil, "")
	api.SetRunnerKey(mcpTestKey)
	return api, pool
}

// rpc posts one JSON-RPC request and returns the raw response plus its decoded
// envelope (nil when the body is empty, as it is for a 202'd notification).
func rpc(t *testing.T, srv *httptest.Server, bearer string, body any) (*http.Response, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/mcp", strings.NewReader(string(raw)))
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	var env map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return resp, nil
	}
	return resp, env
}

// call invokes one tool as the static runner key and returns the tool result
// object.
func call(t *testing.T, srv *httptest.Server, name string, args map[string]any) map[string]any {
	t.Helper()
	return callAs(t, srv, mcpTestKey, name, args)
}

// callAs is call with an explicit credential — the per-owner session scope is
// a property of WHO is calling, so those tests need to choose.
func callAs(t *testing.T, srv *httptest.Server, bearer, name string, args map[string]any) map[string]any {
	t.Helper()
	_, env := rpc(t, srv, bearer, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	})
	if env == nil {
		t.Fatalf("tools/call %s: no response body", name)
	}
	if e, ok := env["error"]; ok {
		t.Fatalf("tools/call %s: rpc error %v", name, e)
	}
	res, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/call %s: result is not an object: %v", name, env)
	}
	return res
}

// toolJSON decodes the single text block of a tool result.
func toolJSON(t *testing.T, res map[string]any) map[string]any {
	t.Helper()
	content, ok := res["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("result content = %v, want one block", res["content"])
	}
	block, _ := content[0].(map[string]any)
	if block["type"] != "text" {
		t.Fatalf("content block type = %v, want text", block["type"])
	}
	text, _ := block["text"].(string)
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("tool text is not JSON: %v (%q)", err, text)
	}
	return out
}

// initialize is the handshake every MCP client makes first; ping keeps the
// session alive. Both must answer without a database or a runtime.
func TestMCPInitializeAndPing(t *testing.T) {
	srv := clusterMCPServer(t)

	_, env := rpc(t, srv, mcpTestKey, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	res, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize: %v", env)
	}
	if res["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocolVersion = %v, want 2025-06-18", res["protocolVersion"])
	}
	info, _ := res["serverInfo"].(map[string]any)
	if info["name"] != "blerg-runner" {
		t.Errorf("serverInfo.name = %v, want blerg-runner", info["name"])
	}
	if _, ok := res["capabilities"].(map[string]any)["tools"]; !ok {
		t.Errorf("capabilities missing tools: %v", res["capabilities"])
	}

	_, env = rpc(t, srv, mcpTestKey, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "ping"})
	if _, ok := env["result"].(map[string]any); !ok {
		t.Errorf("ping: %v", env)
	}
}

// A notification (no id) is acknowledged and produces no body; an unknown
// method is a JSON-RPC -32601.
func TestMCPNotificationAndUnknownMethod(t *testing.T) {
	srv := clusterMCPServer(t)

	resp, _ := rpc(t, srv, mcpTestKey, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("notification: %d, want 202", resp.StatusCode)
	}

	_, env := rpc(t, srv, mcpTestKey, map[string]any{"jsonrpc": "2.0", "id": 9, "method": "nope"})
	rpcErr, ok := env["error"].(map[string]any)
	if !ok {
		t.Fatalf("unknown method: %v", env)
	}
	if rpcErr["code"].(float64) != -32601 {
		t.Errorf("code = %v, want -32601", rpcErr["code"])
	}
}

// tools/list is the catalog: the seven session operations of spec §4, each
// with a JSON Schema an agent can fill in.
func TestMCPToolsList(t *testing.T) {
	srv := clusterMCPServer(t)
	_, env := rpc(t, srv, mcpTestKey, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	res, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list: %v", env)
	}
	tools, _ := res["tools"].([]any)
	want := map[string][]string{
		"start_session":     {"repo", "prompt", "title", "model", "effort", "engine", "runtime", "idempotency_key", "callback_url", "callback_secret", "git_url", "provider", "env", "auto_stop", "no_repo"},
		"get_session":       {"session_id"},
		"send_message":      {"session_id", "text"},
		"interrupt_session": {"session_id"},
		"stop_session":      {"session_id"},
		"get_events":        {"session_id", "after_seq", "limit"},
		"get_result":        {"session_id"},
	}
	if len(tools) != len(want) {
		t.Fatalf("tools/list returned %d tools, want %d", len(tools), len(want))
	}
	seen := map[string]bool{}
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		name, _ := tool["name"].(string)
		wantProps, known := want[name]
		if !known {
			t.Errorf("unexpected tool %q", name)
			continue
		}
		seen[name] = true
		if desc, _ := tool["description"].(string); desc == "" {
			t.Errorf("tool %q has no description", name)
		}
		schema, _ := tool["inputSchema"].(map[string]any)
		if schema["type"] != "object" {
			t.Errorf("tool %q inputSchema type = %v, want object", name, schema["type"])
		}
		props, _ := schema["properties"].(map[string]any)
		for _, p := range wantProps {
			if _, ok := props[p]; !ok {
				t.Errorf("tool %q schema missing property %q", name, p)
			}
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("tools/list missing %q", name)
		}
	}

	// start_session's runtime spells out the three runtimes it accepts —
	// including the local sandbox (R1) — so an agent picks from the schema
	// rather than guessing.
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		if tool["name"] != "start_session" {
			continue
		}
		schema, _ := tool["inputSchema"].(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		runtime, _ := props["runtime"].(map[string]any)
		enum, _ := runtime["enum"].([]any)
		got := map[string]bool{}
		for _, v := range enum {
			s, _ := v.(string)
			got[s] = true
		}
		if len(enum) != 3 || !got["cluster"] || !got["docker"] || !got["daemon"] {
			t.Errorf("start_session runtime enum = %v, want cluster/docker/daemon", enum)
		}
	}
}

// start_session starts a real session, and a retry carrying the same
// idempotency_key replays the first one instead of starting a second —
// the REST contract's Idempotency-Key behaviour, reported in the tool result.
func TestMCPStartSessionIdempotent(t *testing.T) {
	srv := clusterMCPServer(t)
	args := map[string]any{"repo": "org/proj", "prompt": "build it", "idempotency_key": "key-1"}

	first := toolJSON(t, call(t, srv, "start_session", args))
	id, _ := first["session_id"].(string)
	if id == "" {
		t.Fatalf("start_session returned no session_id: %v", first)
	}
	if _, ok := first["idempotent_replayed"]; ok {
		t.Errorf("first start reported idempotent_replayed: %v", first)
	}

	second := toolJSON(t, call(t, srv, "start_session", args))
	if second["session_id"] != id {
		t.Errorf("replay session_id = %v, want %q", second["session_id"], id)
	}
	if second["idempotent_replayed"] != true {
		t.Errorf("idempotent_replayed = %v, want true", second["idempotent_replayed"])
	}
}

// The session operations over MCP answer for a session that MCP started:
// status, events, result, and a stop that ends it.
func TestMCPSessionOperations(t *testing.T) {
	srv := clusterMCPServer(t)
	started := toolJSON(t, call(t, srv, "start_session", map[string]any{"repo": "org/proj", "prompt": "go"}))
	id, _ := started["session_id"].(string)
	if id == "" {
		t.Fatalf("start_session returned no session_id: %v", started)
	}

	status := toolJSON(t, call(t, srv, "get_session", map[string]any{"session_id": id}))
	if status["lifecycle"] != "starting" {
		t.Errorf("lifecycle = %v, want starting", status["lifecycle"])
	}
	if status["runtime"] != "cluster" {
		t.Errorf("runtime = %v, want cluster", status["runtime"])
	}

	events := toolJSON(t, call(t, srv, "get_events", map[string]any{"session_id": id, "limit": 10}))
	if _, ok := events["events"].([]any); !ok {
		t.Errorf("get_events returned no events array: %v", events)
	}
	if events["has_more"] != false {
		t.Errorf("has_more = %v, want false", events["has_more"])
	}

	result := toolJSON(t, call(t, srv, "get_result", map[string]any{"session_id": id}))
	if result["session_id"] != id {
		t.Errorf("get_result session_id = %v, want %q", result["session_id"], id)
	}
	if result["repo"] != "org/proj" {
		t.Errorf("get_result repo = %v, want org/proj", result["repo"])
	}
	if result["terminal"] != false {
		t.Errorf("get_result terminal = %v, want false", result["terminal"])
	}

	// No live runtime for a session whose pod never connected: interrupt is a
	// 409 on the REST path, and the same message here.
	interrupted := call(t, srv, "interrupt_session", map[string]any{"session_id": id})
	if interrupted["isError"] != true {
		t.Errorf("interrupt of a session with no runtime: %v", interrupted)
	}
	if got := toolJSON(t, interrupted)["error"]; got != "no live runtime for this session" {
		t.Errorf("interrupt error = %v, want the REST message", got)
	}

	stopped := toolJSON(t, call(t, srv, "stop_session", map[string]any{"session_id": id}))
	if stopped["status"] != "stopped" {
		t.Errorf("stop_session = %v, want status stopped", stopped)
	}
	after := toolJSON(t, call(t, srv, "get_result", map[string]any{"session_id": id}))
	if after["lifecycle"] != "ended" || after["terminal"] != true {
		t.Errorf("after stop: lifecycle=%v terminal=%v, want ended/true", after["lifecycle"], after["terminal"])
	}
}

// start_session with no_repo starts a session tied to no repository: on the
// cluster its result reports an empty repo.
func TestMCPStartSessionNoRepo(t *testing.T) {
	srv := clusterMCPServer(t)
	started := toolJSON(t, call(t, srv, "start_session", map[string]any{"no_repo": true, "prompt": "go"}))
	id, _ := started["session_id"].(string)
	if id == "" {
		t.Fatalf("start_session no_repo returned no session_id: %v", started)
	}
	result := toolJSON(t, call(t, srv, "get_result", map[string]any{"session_id": id}))
	if result["repo"] != "" {
		t.Errorf("get_result repo = %v, want empty for a no-repo session", result["repo"])
	}
}

// A tool whose arguments the contract rejects fails as a tool error carrying
// the REST handler's own message — not a transport-level RPC error.
func TestMCPToolArgumentValidation(t *testing.T) {
	srv := clusterMCPServer(t)

	cases := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"missing repo", "start_session", map[string]any{"prompt": "go"}, "repo is required"},
		{"no_repo false is still a missing repo", "start_session", map[string]any{"prompt": "go", "no_repo": false}, "repo is required"},
		{"no_repo with a repo", "start_session", map[string]any{"prompt": "go", "no_repo": true, "repo": "org/proj"},
			"no_repo cannot be combined with repo, git_url, provider, clone or new_repo"},
		{"bad runtime", "start_session", map[string]any{"repo": "org/proj", "runtime": "kubernetes"}, `runtime must be "cluster", "docker" or "daemon"`},
		{"cleartext callback", "start_session", map[string]any{"repo": "org/proj", "callback_url": "http://example.test/hook"},
			"callback_url must use https (http is allowed only for localhost, and only where BLERG_RUNNER_WEBHOOK_ALLOW_PRIVATE is on)"},
		{"reserved env", "start_session", map[string]any{"repo": "org/proj", "env": map[string]any{"ANTHROPIC_API_KEY": "x"}},
			"env key ANTHROPIC_API_KEY is reserved"},
		{"bad effort", "start_session", map[string]any{"repo": "org/proj", "effort": "ultra"}, "effort must be one of low, medium, high, xhigh, max"},
		{"bad model", "start_session", map[string]any{"repo": "org/proj", "model": "sonnet --x"},
			"model must be a model name: lowercase letters, digits, '.', '-', '[' and ']', at most 64 characters"},
		{"empty message", "send_message", map[string]any{"session_id": "nope", "text": ""}, "text required"},
		{"unknown session", "get_result", map[string]any{"session_id": "no-such-session"}, "session not found"},
		{"unknown status", "get_session", map[string]any{"session_id": "no-such-session"}, "session not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := call(t, srv, tc.tool, tc.args)
			if res["isError"] != true {
				t.Fatalf("%s: isError = %v, want true (%v)", tc.tool, res["isError"], res)
			}
			if got := toolJSON(t, res)["error"]; got != tc.want {
				t.Errorf("error = %v, want %q", got, tc.want)
			}
		})
	}
}

// An unknown tool name is a tool error, not a crash and not a -32601: the
// agent asked for something this server does not have.
func TestMCPUnknownTool(t *testing.T) {
	srv := clusterMCPServer(t)
	res := call(t, srv, "delete_everything", map[string]any{})
	if res["isError"] != true {
		t.Fatalf("unknown tool: %v", res)
	}
	if msg, _ := toolJSON(t, res)["error"].(string); !strings.Contains(msg, "delete_everything") {
		t.Errorf("error = %q, want it to name the tool", msg)
	}
}

// The MCP endpoint is authenticated exactly like the REST contract: the same
// runner credential, checked on every request.
func TestMCPUnauthenticated(t *testing.T) {
	srv := clusterMCPServer(t)

	resp, _ := rpc(t, srv, "", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no credential: %d, want 401", resp.StatusCode)
	}
	resp, _ = rpc(t, srv, "wrong-key-0000000000", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong credential: %d, want 401", resp.StatusCode)
	}
}

// MCP over streamable HTTP is POST-only here — a browser GET is not a
// transport this server speaks.
func TestMCPGetNotAllowed(t *testing.T) {
	srv := clusterMCPServer(t)
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+mcpTestKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /mcp: %d, want 405", resp.StatusCode)
	}
}

// start_session accepts auto_stop, and the session it starts reports the
// one-shot mode back through get_session — so an agent that asked for a
// fire-and-forget task can see it got one.
func TestMCPStartSessionAutoStop(t *testing.T) {
	srv := clusterMCPServer(t)
	started := toolJSON(t, call(t, srv, "start_session", map[string]any{
		"repo": "org/proj", "prompt": "one thing", "auto_stop": true,
	}))
	id, _ := started["session_id"].(string)
	if id == "" {
		t.Fatalf("start_session returned no session_id: %v", started)
	}
	status := toolJSON(t, call(t, srv, "get_session", map[string]any{"session_id": id}))
	if status["auto_stop"] != true {
		t.Errorf("get_session auto_stop = %v, want true", status["auto_stop"])
	}

	// The default is unchanged: a start that says nothing is interactive.
	plain := toolJSON(t, call(t, srv, "start_session", map[string]any{"repo": "org/proj", "prompt": "chat"}))
	plainID, _ := plain["session_id"].(string)
	status = toolJSON(t, call(t, srv, "get_session", map[string]any{"session_id": plainID}))
	if status["auto_stop"] != false {
		t.Errorf("get_session auto_stop = %v for a default start, want false", status["auto_stop"])
	}
}
