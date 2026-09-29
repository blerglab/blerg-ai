package mcp

// The per-owner session scope (I-1) over MCP. The tools share the API methods
// the REST handlers call, so the rule is enforced once — this proves the tool
// surface really does go through it, and reports the refusal the same way.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/coreauth"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// mcpToken mints a core-issued token signed by priv, as blerg-core would.
func mcpToken(t *testing.T, priv ed25519.PrivateKey, kid string, c identity.Claims) string {
	t.Helper()
	hb, err := json.Marshal(map[string]string{"alg": "EdDSA", "kid": kid})
	if err != nil {
		t.Fatal(err)
	}
	pb, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	si := identity.EncodeSigningInput(hb, pb)
	return si + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(si)))
}

// mcpCoreAuth starts a fake blerg-core (jwks + empty revocation list) and
// returns a client wired against it.
func mcpCoreAuth(t *testing.T, pub ed25519.PublicKey, kid string) *coreauth.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{kid: base64.RawURLEncoding.EncodeToString(pub)})
	})
	mux.HandleFunc("/revocations", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]any{})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return coreauth.New(srv.URL)
}

// One agent token's tools cannot touch another owner's session, and say so
// with the same 404 an unknown session id gets.
func TestMCPAgentTokenIsScopedToItsOwnSessions(t *testing.T) {
	api, _ := clusterMCPAPI(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	api.SetCoreAuth(mcpCoreAuth(t, pub, "core-1"))
	srv := serveMCP(t, api)
	exp := time.Now().Unix() + 300
	tokenA := mcpToken(t, priv, "core-1", identity.Claims{
		Sub: "tok-a", Aud: "blerg-runner", Kind: "agent", OnBehalfOf: "acct-a",
		Caps: []string{"session.start"}, ExpiresAt: exp,
	})
	tokenB := mcpToken(t, priv, "core-1", identity.Claims{
		Sub: "tok-b", Aud: "blerg-runner", Kind: "agent", OnBehalfOf: "acct-b",
		Caps: []string{"session.start"}, ExpiresAt: exp,
	})
	adminTok := mcpToken(t, priv, "core-1", identity.Claims{
		Sub: "tok-admin", Aud: "blerg-runner", Kind: "agent", OnBehalfOf: "acct-admin",
		Caps: []string{"session.start", "board.admin"}, ExpiresAt: exp,
	})

	startedB := toolJSON(t, callAs(t, srv, tokenB, "start_session",
		map[string]any{"repo": "org/proj", "prompt": "go"}))
	idB, _ := startedB["session_id"].(string)
	if idB == "" {
		t.Fatalf("start_session as B: %v", startedB)
	}

	// Every session tool refuses, identically.
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"get_session", map[string]any{"session_id": idB}},
		{"send_message", map[string]any{"session_id": idB, "text": "hi"}},
		{"interrupt_session", map[string]any{"session_id": idB}},
		{"get_events", map[string]any{"session_id": idB}},
		{"get_result", map[string]any{"session_id": idB}},
		{"stop_session", map[string]any{"session_id": idB}},
	} {
		res := callAs(t, srv, tokenA, tc.tool, tc.args)
		if res["isError"] != true {
			t.Errorf("%s on another owner's session: %v, want a tool error", tc.tool, res)
			continue
		}
		body := toolJSON(t, res)
		if body["error"] != "session not found" || body["status"] != float64(http.StatusNotFound) {
			t.Errorf("%s error = %v (status %v), want a 404 session not found", tc.tool, body["error"], body["status"])
		}
	}

	// B still reaches its own session, and an admin token reaches it too.
	own := toolJSON(t, callAs(t, srv, tokenB, "get_session", map[string]any{"session_id": idB}))
	if own["lifecycle"] != "starting" {
		t.Errorf("owner's own get_session = %v", own)
	}
	admin := toolJSON(t, callAs(t, srv, adminTok, "get_session", map[string]any{"session_id": idB}))
	if admin["lifecycle"] != "starting" {
		t.Errorf("admin get_session = %v", admin)
	}
	// So does the static runner key.
	keyed := toolJSON(t, call(t, srv, "get_session", map[string]any{"session_id": idB}))
	if keyed["lifecycle"] != "starting" {
		t.Errorf("runner key get_session = %v", keyed)
	}
}

// A present-but-empty idempotency_key is a caller bug, not "no key" — it would
// silently drop the retry protection the caller believes it has. Same 400, same
// message, as REST; and the message says "idempotency key", not "header",
// because an MCP caller has no header to fix.
func TestMCPStartSessionRejectsAnEmptyIdempotencyKey(t *testing.T) {
	srv := clusterMCPServer(t)

	res := call(t, srv, "start_session", map[string]any{
		"repo": "org/proj", "prompt": "go", "idempotency_key": "",
	})
	if res["isError"] != true {
		t.Fatalf("empty idempotency_key: %v, want a tool error", res)
	}
	body := toolJSON(t, res)
	if body["status"] != float64(http.StatusBadRequest) {
		t.Errorf("status = %v, want 400", body["status"])
	}
	msg, _ := body["error"].(string)
	if msg != "idempotency key must be 1-128 characters" {
		t.Errorf("error = %q, want the shared key message", msg)
	}
	if strings.Contains(strings.ToLower(msg), "header") {
		t.Errorf("the shared message must not mention a header: %q", msg)
	}

	// Over-long is the same rejection.
	long := call(t, srv, "start_session", map[string]any{
		"repo": "org/proj", "prompt": "go", "idempotency_key": strings.Repeat("k", 129),
	})
	if long["isError"] != true || toolJSON(t, long)["error"] != msg {
		t.Errorf("129-character key: %v, want the same rejection", long)
	}

	// Omitting it entirely is not an error: the key is optional.
	ok := toolJSON(t, call(t, srv, "start_session", map[string]any{"repo": "org/proj", "prompt": "go"}))
	if ok["session_id"] == nil {
		t.Errorf("start without an idempotency_key: %v", ok)
	}
}

// Malformed arguments and an unknown tool report the same {error, status}
// envelope as a contract failure, so a client needs one error path.
func TestMCPErrorEnvelopeIsUniform(t *testing.T) {
	srv := clusterMCPServer(t)

	_, env := rpc(t, srv, mcpTestKey, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "get_session", "arguments": map[string]any{"session_id": 7}},
	})
	res, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("a type error must be a tool error, not an RPC error: %v", env)
	}
	if res["isError"] != true {
		t.Fatalf("bad argument type: %v", res)
	}
	if got := toolJSON(t, res)["status"]; got != float64(http.StatusBadRequest) {
		t.Errorf("decode error status = %v, want 400", got)
	}

	unknown := call(t, srv, "delete_everything", map[string]any{})
	if got := toolJSON(t, unknown)["status"]; got != float64(http.StatusNotFound) {
		t.Errorf("unknown tool status = %v, want 404", got)
	}
}

// get_events honours the same window cap as the REST endpoint: a caller asking
// for more than the maximum gets the default page and has_more, not everything.
func TestMCPGetEventsHonoursTheWindowCap(t *testing.T) {
	api, pool := clusterMCPAPI(t)
	srv := serveMCP(t, api)
	started := toolJSON(t, call(t, srv, "start_session", map[string]any{"repo": "org/proj", "prompt": "go"}))
	id, _ := started["session_id"].(string)
	if id == "" {
		t.Fatalf("start_session: %v", started)
	}

	ctx := context.Background()
	// A start records its own progress events (start_stage) first; count
	// whatever is already there so the paging arithmetic below is exact.
	pre, err := db.ListAgentEvents(ctx, pool, id, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 205; i++ {
		if _, _, err := db.AppendAgentEvent(ctx, pool, id, newTestUUID(i), "assistant_text",
			`{"text":"x","done":true}`); err != nil {
			t.Fatal(err)
		}
	}

	// limit above the maximum falls back to the default page size.
	capped := toolJSON(t, call(t, srv, "get_events", map[string]any{"session_id": id, "limit": 5000}))
	events, _ := capped["events"].([]any)
	if len(events) != 200 {
		t.Errorf("get_events(limit 5000) returned %d events, want the 200 default", len(events))
	}
	if capped["has_more"] != true {
		t.Errorf("has_more = %v, want true with 205 events", capped["has_more"])
	}

	// A sane limit is honoured, and paging with after_seq reaches the rest.
	page := toolJSON(t, call(t, srv, "get_events", map[string]any{"session_id": id, "limit": 50}))
	first, _ := page["events"].([]any)
	if len(first) != 50 {
		t.Fatalf("get_events(limit 50) returned %d events, want 50", len(first))
	}
	last, _ := first[len(first)-1].(map[string]any)
	rest := toolJSON(t, call(t, srv, "get_events",
		map[string]any{"session_id": id, "after_seq": last["seq"], "limit": 400}))
	restEvents, _ := rest["events"].([]any)
	if want := 205 + len(pre) - 50; len(restEvents) != want {
		t.Errorf("paged remainder = %d events, want %d", len(restEvents), want)
	}
	if rest["has_more"] != false {
		t.Errorf("has_more = %v at the end of the transcript, want false", rest["has_more"])
	}
}

// newTestUUID is a distinct client_event_id per appended event (the column is
// a uuid and unique per session).
func newTestUUID(i int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
}

// The unconfigured contract answers /mcp with the same 404 the REST routes
// give: an install with no runner credential exposes no tools (T10).
func TestMCPUnconfiguredContractIs404(t *testing.T) {
	api, _ := clusterMCPAPI(t)
	api.SetRunnerKey("")
	srv := serveMCP(t, api)
	resp, _ := rpc(t, srv, "anything", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("POST /mcp with the contract off: %d, want 404", resp.StatusCode)
	}
}
