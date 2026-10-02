package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

func humanWho() grantRequester { return grantRequester{Kind: "human", Account: mcpAcct, Sid: testSID} }

func clusterTarget() grantTarget {
	return grantTarget{Runtime: runnerRuntimeCluster, Kind: "agent"}
}

func (fx *mcpFx) dockerTarget() grantTarget {
	return grantTarget{Runtime: runnerRuntimeDocker, Kind: "agent", Daemon: fx.dc}
}

func echoSel(fx *mcpFx, mode, hash string) []MCPSelection {
	return []MCPSelection{{Connection: fx.connID, Tools: map[string]MCPToolSelection{"echo": {Mode: mode, Hash: hash}}}}
}

// Every refusal a start with MCP connections can get, each with its own message, and none
// of them leaves a row, a grant or an upstream session behind.
func TestResolveGrantRefusals(t *testing.T) {
	type tc struct {
		name   string
		who    grantRequester
		sel    func(fx *mcpFx) []MCPSelection
		target func(fx *mcpFx) grantTarget
		setup  func(fx *mcpFx)
		status int
		want   string
	}
	echoOK := func(fx *mcpFx) []MCPSelection { return echoSel(fx, "allow", fx.up.hash("echo")) }
	cluster := func(*mcpFx) grantTarget { return clusterTarget() }
	docker := func(fx *mcpFx) grantTarget { return fx.dockerTarget() }
	cases := []tc{
		{name: "agent principal", who: grantRequester{Kind: "agent", Account: mcpAcct}, sel: echoOK, target: cluster,
			status: 403, want: msgMCPHumanOnly},
		{name: "runner key (no kind)", who: grantRequester{}, sel: echoOK, target: cluster, status: 403, want: msgMCPHumanOnly},
		{name: "service principal", who: grantRequester{Kind: "service", Account: mcpAcct, Sid: testSID}, sel: echoOK, target: cluster,
			status: 403, want: msgMCPHumanOnly},
		{name: "human without a login session", who: grantRequester{Kind: "human", Account: mcpAcct}, sel: echoOK, target: cluster,
			status: 401, want: msgMCPSignIn},
		{name: "engine codex", who: humanWho(), sel: echoOK, status: 422, want: msgMCPEngine,
			target: func(*mcpFx) grantTarget {
				return grantTarget{Runtime: runnerRuntimeCluster, Kind: "agent", Engine: "codex"}
			}},
		{name: "kind terminal", who: humanWho(), sel: echoOK, status: 422, want: msgMCPKind,
			target: func(*mcpFx) grantTarget { return grantTarget{Runtime: runnerRuntimeCluster, Kind: ""} }},
		{name: "bare host", who: humanWho(), sel: echoOK, status: 422, want: msgMCPRuntime,
			target: func(fx *mcpFx) grantTarget {
				return grantTarget{Runtime: daemonRuntimeName, Kind: "agent", Daemon: fx.dc}
			}},
		{name: "no runtime", who: humanWho(), sel: echoOK, status: 422, want: msgMCPRuntime,
			target: func(*mcpFx) grantTarget { return grantTarget{Kind: "agent"} }},
		{name: "daemon without mcp_gateway", who: humanWho(), sel: echoOK, target: docker, status: 422, want: msgMCPDaemonOld,
			setup: func(fx *mcpFx) { fx.dc.SetMCPGateway(false) }},
		{name: "docker with no daemon", who: humanWho(), sel: echoOK, status: 422, want: msgMCPDaemonOld,
			target: func(*mcpFx) grantTarget { return grantTarget{Runtime: runnerRuntimeDocker, Kind: "agent"} }},
		{name: "gateway not configured", who: humanWho(), sel: echoOK, target: cluster, status: 503, want: msgMCPNoGateway,
			setup: func(fx *mcpFx) { fx.api.setMCPStart(&mcpStartConfig{GatewayURL: mcpGatewayURL, Core: fx.list}) }},
		{name: "never wired", who: humanWho(), sel: echoOK, target: cluster, status: 503, want: msgMCPNoGateway,
			setup: func(fx *mcpFx) { fx.api.setMCPStart(nil) }},
		{name: "no gateway URL for sessions", who: humanWho(), sel: echoOK, target: cluster, status: 503, want: msgMCPNoGatewayURL,
			setup: func(fx *mcpFx) {
				cfg := *fx.hub.mcpStart.get()
				cfg.GatewayURL = ""
				fx.api.setMCPStart(&cfg)
			}},
		{name: "core not configured", who: humanWho(), sel: echoOK, target: cluster, status: 503, want: msgMCPNoCore,
			setup: func(fx *mcpFx) {
				cfg := *fx.hub.mcpStart.get()
				cfg.Core = nil
				fx.api.setMCPStart(&cfg)
			}},
		{name: "proof invalid at core", who: humanWho(), sel: echoOK, target: cluster, status: 401, want: msgMCPSignIn,
			setup: func(fx *mcpFx) { fx.list.err = ErrMCPProofInvalid }},
		{name: "core unreachable", who: humanWho(), sel: echoOK, target: cluster, status: 503, want: "could not read your MCP connections",
			setup: func(fx *mcpFx) { fx.list.err = errors.New("dial tcp: refused") }},
		{name: "connection owned by someone else", who: humanWho(), target: cluster, status: 404, want: msgMCPNotFound,
			sel: func(*mcpFx) []MCPSelection {
				return []MCPSelection{{Connection: newUUID(), Tools: map[string]MCPToolSelection{"echo": {Mode: "allow", Hash: "h"}}}}
			}},
		{name: "empty connection id", who: humanWho(), target: cluster, status: 404, want: msgMCPNotFound,
			sel: func(*mcpFx) []MCPSelection { return []MCPSelection{{}} }},
		{name: "core says the connection is gone at fetch time", who: humanWho(), sel: echoOK, target: cluster, status: 404, want: msgMCPNotFound,
			setup: func(fx *mcpFx) { fx.tokens.err = mcpgw.ErrConnectionGone }},
		{name: "connection needs attention", who: humanWho(), sel: echoOK, target: cluster, status: 422, want: "cannot be used now",
			setup: func(fx *mcpFx) { fx.list.conns[0].Status = "needs_auth" }},
		{name: "connection name the gateway cannot serve", who: humanWho(), sel: echoOK, target: cluster, status: 422, want: "name the gateway cannot serve",
			setup: func(fx *mcpFx) { fx.list.conns[0].Name = "Not A Name" }},
		{name: "tool without a hash", who: humanWho(), target: cluster, status: 400, want: "hash you saw",
			sel: func(fx *mcpFx) []MCPSelection { return echoSel(fx, "allow", "") }},
		{name: "mode off is not selectable", who: humanWho(), target: cluster, status: 400, want: "mode must be",
			sel: func(fx *mcpFx) []MCPSelection { return echoSel(fx, "off", fx.up.hash("echo")) }},
		{name: "unknown mode", who: humanWho(), target: cluster, status: 400, want: "mode must be",
			sel: func(fx *mcpFx) []MCPSelection { return echoSel(fx, "yolo", fx.up.hash("echo")) }},
		{name: "a present but empty tools object", who: humanWho(), target: cluster, status: 400, want: "selects no tools",
			sel: func(fx *mcpFx) []MCPSelection {
				return []MCPSelection{{Connection: fx.connID, Tools: map[string]MCPToolSelection{}}}
			}},
		{name: "no tools and no defaults", who: humanWho(), target: cluster, status: 422, want: "no default tools",
			sel:   func(fx *mcpFx) []MCPSelection { return []MCPSelection{{Connection: fx.connID}} },
			setup: func(fx *mcpFx) { fx.list.conns[0].DefaultTools = nil }},
		{name: "the same connection twice", who: humanWho(), target: cluster, status: 400, want: "selected twice",
			sel: func(fx *mcpFx) []MCPSelection {
				return append(echoSel(fx, "allow", fx.up.hash("echo")), echoSel(fx, "allow", fx.up.hash("echo"))...)
			}},
		{name: "too many connections", who: humanWho(), target: cluster, status: 400, want: "at most",
			sel: func(fx *mcpFx) []MCPSelection { return make([]MCPSelection, maxMCPSelections+1) }},
		{name: "stale hash", who: humanWho(), target: cluster, status: 409, want: "changed, or is not offered",
			sel: func(fx *mcpFx) []MCPSelection { return echoSel(fx, "allow", "0000") }},
		{name: "description changed after the person confirmed it", who: humanWho(), target: cluster, status: 409, want: "changed, or is not offered",
			sel: func(fx *mcpFx) []MCPSelection {
				s := echoSel(fx, "allow", fx.up.hash("echo"))
				fx.up.setDescription("echo", "now also deletes things")
				return s
			}},
		{name: "tool the upstream does not offer", who: humanWho(), target: cluster, status: 409, want: "changed, or is not offered",
			sel: func(fx *mcpFx) []MCPSelection {
				return []MCPSelection{{Connection: fx.connID, Tools: map[string]MCPToolSelection{"vanish": {Mode: "allow", Hash: "h"}}}}
			}},
		{name: "a stale default", who: humanWho(), target: cluster, status: 409, want: "changed, or is not offered",
			sel:   func(fx *mcpFx) []MCPSelection { return []MCPSelection{{Connection: fx.connID}} },
			setup: func(fx *mcpFx) { fx.up.setDescription("echo", "edited after the default was saved") }},
		{name: "upstream down", who: humanWho(), sel: echoOK, target: cluster, status: 502, want: "could not list the tools",
			setup: func(fx *mcpFx) { fx.up.mu.Lock(); fx.up.status = 500; fx.up.mu.Unlock() }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := newMCPFx(t)
			if c.setup != nil {
				c.setup(fx)
			}
			g, apiErr := fx.api.resolveGrant(context.Background(), c.who, c.sel(fx), c.target(fx))
			if apiErr == nil {
				t.Fatalf("no refusal; grant = %+v", g)
			}
			if apiErr.Status != c.status || !strings.Contains(apiErr.Message, c.want) {
				t.Errorf("refusal = %d %q, want %d containing %q", apiErr.Status, apiErr.Message, c.status, c.want)
			}
			if g != nil {
				t.Errorf("a refusal returned a grant: %+v", g)
			}
			if fx.count("session_mcp_grants") != 0 || fx.count("sessions") != 0 {
				t.Error("a refusal left rows behind")
			}
		})
	}
}

// The cheap refusals happen before any network call: nothing is asked of core or the upstream.
func TestResolveGrantEarlyRefusalsMakeNoCalls(t *testing.T) {
	fx := newMCPFx(t)
	for _, who := range []grantRequester{{Kind: "agent", Account: mcpAcct}, {Kind: "human", Account: mcpAcct}} {
		if _, apiErr := fx.api.resolveGrant(context.Background(), who, echoSel(fx, "allow", "h"), clusterTarget()); apiErr == nil {
			t.Fatal("expected a refusal")
		}
	}
	if _, apiErr := fx.api.resolveGrant(context.Background(), humanWho(), echoSel(fx, "allow", "h"),
		grantTarget{Runtime: daemonRuntimeName, Kind: "agent", Daemon: fx.dc}); apiErr == nil {
		t.Fatal("bare host accepted")
	}
	if len(fx.list.proofs) != 0 || fx.up.listCount() != 0 {
		t.Errorf("early refusals reached core (%d) or the upstream (%d)", len(fx.list.proofs), fx.up.listCount())
	}
}

func TestResolveGrantAcceptsSelectionsAndDefaults(t *testing.T) {
	fx := newMCPFx(t)
	ctx := context.Background()

	// Explicit tools: allow and propose are both accepted and stored with the confirmed hash.
	sel := []MCPSelection{{Connection: fx.connID, Tools: map[string]MCPToolSelection{
		"echo": {Mode: "allow", Hash: fx.up.hash("echo")},
		"send": {Mode: "propose", Hash: fx.up.hash("send")},
	}}}
	g, apiErr := fx.api.resolveGrant(ctx, humanWho(), sel, fx.dockerTarget())
	if apiErr != nil {
		t.Fatalf("resolveGrant: %v", apiErr)
	}
	if g.AccountID != mcpAcct || g.Proof != (mcpgw.Proof{AccountID: mcpAcct, SessionID: testSID}) || len(g.Connections) != 1 {
		t.Fatalf("grant = %+v", g)
	}
	spec := g.Connections[0]
	if spec.ConnectionID != fx.connID || spec.Name != mcpConn || spec.URL != fx.up.url() {
		t.Errorf("spec = %+v", spec)
	}
	if spec.Tools["echo"] != (mcpgw.ToolGrant{Mode: "allow", Hash: fx.up.hash("echo")}) ||
		spec.Tools["send"] != (mcpgw.ToolGrant{Mode: "propose", Hash: fx.up.hash("send")}) || len(spec.Tools) != 2 {
		t.Errorf("tools = %+v", spec.Tools)
	}
	// The proof handed to core is the human's login session.
	if p := fx.list.proofs[0]; p.SessionID != testSID || p.AccountID != mcpAcct || p.TokenID != "" {
		t.Errorf("core saw proof %+v", p)
	}

	// tools omitted: the connection's default_tools, with their own hashes, still checked live.
	g, apiErr = fx.api.resolveGrant(ctx, humanWho(), []MCPSelection{{Connection: fx.connID}}, clusterTarget())
	if apiErr != nil {
		t.Fatalf("defaults: %v", apiErr)
	}
	if tools := g.Connections[0].Tools; len(tools) != 1 || tools["echo"].Hash != fx.up.hash("echo") || tools["echo"].Mode != "allow" {
		t.Errorf("default tools = %+v", tools)
	}

	// No selection at all is no grant, not an error.
	if g, apiErr := fx.api.resolveGrant(ctx, grantRequester{Kind: "agent"}, nil, grantTarget{}); g != nil || apiErr != nil {
		t.Errorf("empty selection = %v, %v", g, apiErr)
	}
}

// ---- the public route and the routes that must not take `mcp` ------------------------------

func TestPostSessionsMCPRequesterMatrix(t *testing.T) {
	fx := newMCPFx(t)
	base := func() map[string]any {
		return map[string]any{"repo": "acme/widget", "runtime": "cluster", "kind": "agent", "mcp": fx.selection()}
	}
	t.Run("no token", func(t *testing.T) {
		if rec := fx.do(fx.api.HandlePostSessions, "", base()); rec.Code != http.StatusUnauthorized {
			t.Errorf("status %d, want 401", rec.Code)
		}
	})
	t.Run("agent token", func(t *testing.T) {
		rec := fx.do(fx.api.HandlePostSessions, fx.token("agent", ""), base())
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), msgMCPHumanOnly) {
			t.Errorf("agent token: %d %s, want 403 %q", rec.Code, rec.Body.String(), msgMCPHumanOnly)
		}
	})
	t.Run("human token from before core stamped sid", func(t *testing.T) {
		rec := fx.do(fx.api.HandlePostSessions, fx.token("human", ""), base())
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), msgMCPSignIn) {
			t.Errorf("no sid: %d %s, want 401 sign in", rec.Code, rec.Body.String())
		}
	})
	t.Run("another account's connection", func(t *testing.T) {
		b := base()
		b["mcp"] = []map[string]any{{"connection": newUUID(), "tools": map[string]any{"echo": map[string]any{"mode": "allow", "hash": "h"}}}}
		rec := fx.do(fx.api.HandlePostSessions, fx.human(), b)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), msgMCPNotFound) {
			t.Errorf("foreign connection: %d %s, want 404 %q", rec.Code, rec.Body.String(), msgMCPNotFound)
		}
	})
	if fx.count("sessions") != 0 || fx.count("session_mcp_grants") != 0 || len(fx.k8s.created) != 0 {
		t.Error("a refused start left a session, grant or Job behind")
	}
}

// A body naming `mcp` is refused, not dropped, on the v1 start (runner key, agent token and
// human token alike), and by the runner's MCP tool through RejectMCPArgument.
func TestRunnerStartRefusesMCPKey(t *testing.T) {
	fx := newMCPFx(t)
	bodies := []struct {
		name string
		body string
	}{
		{"mcp", `{"repo":"acme/widget","prompt":"p","runtime":"cluster","mcp":[{"connection":"x"}]}`},
		{"mcp empty", `{"repo":"acme/widget","prompt":"p","runtime":"cluster","mcp":[]}`},
		{"mcp null", `{"repo":"acme/widget","prompt":"p","runtime":"cluster","mcp":null}`},
		{"MCP upper case", `{"repo":"acme/widget","prompt":"p","runtime":"cluster","MCP":[{"connection":"x"}]}`},
		{"Mcp mixed case", `{"repo":"acme/widget","prompt":"p","runtime":"cluster","Mcp":{}}`},
	}
	bearers := map[string]string{"runner key": runnerTestKey, "agent token": fx.token("agent", ""), "human token": fx.human()}
	for who, bearer := range bearers {
		for _, b := range bodies {
			t.Run(who+"/"+b.name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodPost, "/api/runner/start", strings.NewReader(b.body))
				req.Header.Set("Authorization", "Bearer "+bearer)
				rec := httptest.NewRecorder()
				fx.api.HandleRunnerStart(rec, req)
				if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), msgMCPBodyForbidden) {
					t.Errorf("%d %s, want 400 %q", rec.Code, rec.Body.String(), msgMCPBodyForbidden)
				}
			})
		}
	}
	if fx.count("sessions") != 0 || len(fx.k8s.created) != 0 {
		t.Error("a refused start created a session or Job")
	}

	// Without the key the same body starts (the route is otherwise unchanged).
	req := httptest.NewRequest(http.MethodPost, "/api/runner/start", strings.NewReader(`{"repo":"acme/widget","prompt":"p","runtime":"cluster"}`))
	req.Header.Set("Authorization", "Bearer "+runnerTestKey)
	rec := httptest.NewRecorder()
	fx.api.HandleRunnerStart(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Errorf("start without mcp: %d %s", rec.Code, rec.Body.String())
	}

	// The MCP tool's arguments go through the same check.
	if apiErr := RejectMCPArgument(json.RawMessage(`{"repo":"x","mcp":[]}`)); apiErr == nil || apiErr.Status != http.StatusBadRequest {
		t.Errorf("RejectMCPArgument = %v", apiErr)
	}
	if apiErr := RejectMCPArgument(json.RawMessage(`{"repo":"x","prompt":"mcp"}`)); apiErr != nil {
		t.Errorf("an unrelated value named mcp was refused: %v", apiErr)
	}
	if apiErr := RejectMCPArgument(json.RawMessage(`not json`)); apiErr != nil {
		t.Errorf("malformed JSON is decodeArgs' business: %v", apiErr)
	}
}

// A grant on an in-process StartSession belongs to the account the principal acts for, and
// must be well formed: anything else is refused before a session exists.
func TestStartSessionRefusesAForeignOrMalformedGrant(t *testing.T) {
	fx := newMCPFx(t)
	ctx := context.Background()
	good, apiErr := fx.api.resolveGrant(ctx, humanWho(), echoSel(fx, "allow", fx.up.hash("echo")), clusterTarget())
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	owner := runnerPrincipal{Kind: "human", Sub: mcpAcct, Sid: testSID}
	other := runnerPrincipal{Kind: "human", Sub: "someone-else", Sid: testSID}
	bad := *good
	bad.Connections = nil
	for name, tc := range map[string]struct {
		p runnerPrincipal
		g *ResolvedGrant
	}{
		"another account": {other, good},
		"runner key":      {runnerPrincipal{Kind: runnerKeyPrincipalKind}, good},
		"no connections":  {owner, &bad},
	} {
		t.Run(name, func(t *testing.T) {
			_, apiErr := fx.api.StartSession(ctx, tc.p, RunnerStartRequest{Repo: "acme/widget", Prompt: "p", Runtime: "cluster", Grant: tc.g}, "")
			if apiErr == nil || apiErr.Status != http.StatusForbidden {
				t.Fatalf("StartSession = %v, want 403", apiErr)
			}
		})
	}
	if fx.count("sessions") != 0 || fx.count("session_mcp_grants") != 0 {
		t.Error("a refused start left rows behind")
	}
}

// The wire: core's list route is called as the internal API documents, and its answers map
// to the errors the start path acts on.
func TestHTTPMCPCoreClientList(t *testing.T) {
	var gotBody map[string]string
	var gotKey string
	status := http.StatusOK
	bare404 := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Internal-Key")
		if r.URL.Path != "/internal/mcp/connections/list" || r.Method != http.MethodPost {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		gotBody = nil
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		if status == http.StatusNotFound && !bare404 {
			http.Error(w, "not found", status) // core's own uniform answer
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte(`{"connections":[
		  {"id":"c1","name":"calendar","url":"https://mcp.example/x","auth_kind":"static","status":"ok",
		   "default_tools":{"echo":{"mode":"allow","hash":"h1"}}},
		  {"id":"c2","name":"mail","url":"https://mcp.example/y","auth_kind":"none","status":"ok","default_tools":{}}]}`))
	}))
	defer srv.Close()
	c := &HTTPMCPCoreClient{BaseURL: srv.URL + "/", InternalKey: "k", HTTP: srv.Client()}

	conns, err := c.ListConnections(context.Background(), mcpgw.Proof{AccountID: "a1", SessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "k" || gotBody["account_id"] != "a1" || gotBody["session_id"] != "s1" || gotBody["token_id"] != "" {
		t.Errorf("request key %q body %v", gotKey, gotBody)
	}
	if len(conns) != 2 || conns[0].Name != "calendar" || conns[0].DefaultTools["echo"] != (mcpgw.ToolGrant{Mode: "allow", Hash: "h1"}) || len(conns[1].DefaultTools) != 0 {
		t.Errorf("connections = %+v", conns)
	}

	if _, err := c.ListConnections(context.Background(), mcpgw.Proof{AccountID: "a1", TokenID: "t1"}); err != nil || gotBody["token_id"] != "t1" || gotBody["session_id"] != "" {
		t.Errorf("token proof: %v %v", err, gotBody)
	}
	status = http.StatusNotFound
	if _, err := c.ListConnections(context.Background(), mcpgw.Proof{AccountID: "a1", SessionID: "s1"}); !errors.Is(err, ErrMCPProofInvalid) {
		t.Errorf("404 = %v, want ErrMCPProofInvalid", err)
	}
	bare404 = true // a 404 that is not core's own uniform answer is a missing route, not a dead proof
	if _, err := c.ListConnections(context.Background(), mcpgw.Proof{AccountID: "a1", SessionID: "s1"}); !errors.Is(err, ErrMCPCoreOutdated) {
		t.Errorf("a router 404 = %v, want ErrMCPCoreOutdated", err)
	}
	bare404 = false
	status = http.StatusInternalServerError
	if _, err := c.ListConnections(context.Background(), mcpgw.Proof{AccountID: "a1", SessionID: "s1"}); err == nil || errors.Is(err, ErrMCPProofInvalid) {
		t.Errorf("500 = %v, want a plain error", err)
	}
	if _, err := c.ListConnections(context.Background(), mcpgw.Proof{AccountID: "a1"}); err == nil {
		t.Error("a call without a proof succeeded")
	}
	if _, err := (&HTTPMCPCoreClient{}).ListConnections(context.Background(), mcpgw.Proof{AccountID: "a1", SessionID: "s1"}); err == nil {
		t.Error("an unconfigured client succeeded")
	}
}

// SetDefaultTools posts the session proof and the tool map to core's defaults route and maps its
// answers: 400 keeps core's message, 404 is a dead proof.
func TestHTTPMCPCoreClientSetDefaultTools(t *testing.T) {
	var gotBody map[string]any
	var gotKey string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Internal-Key")
		if r.URL.Path != "/internal/mcp/connections/defaults" || r.Method != http.MethodPost {
			t.Errorf("request %s %s", r.Method, r.URL.Path)
		}
		gotBody = nil
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		if status == http.StatusBadRequest {
			http.Error(w, "default_tools: hash is required", status)
			return
		}
		if status == http.StatusNotFound {
			http.Error(w, "not found", status) // core's own uniform answer
			return
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	c := &HTTPMCPCoreClient{BaseURL: srv.URL + "/", InternalKey: "k", HTTP: srv.Client()}
	tools := map[string]mcpgw.ToolGrant{"echo": {Mode: "allow", Hash: "h1"}}
	proof := mcpgw.Proof{AccountID: "a1", SessionID: "s1"}

	if err := c.SetDefaultTools(context.Background(), proof, "c1", tools); err != nil {
		t.Fatal(err)
	}
	dt, _ := gotBody["default_tools"].(map[string]any)
	if gotKey != "k" || gotBody["account_id"] != "a1" || gotBody["session_id"] != "s1" || gotBody["connection_id"] != "c1" || dt["echo"] == nil {
		t.Errorf("request key %q body %v", gotKey, gotBody)
	}
	if _, has := gotBody["token_id"]; has {
		t.Errorf("a token_id was sent: %v", gotBody)
	}
	if err := c.SetDefaultTools(context.Background(), mcpgw.Proof{AccountID: "a1", TokenID: "t1"}, "c1", tools); err == nil {
		t.Error("a token proof was accepted")
	}
	status = http.StatusNotFound
	if err := c.SetDefaultTools(context.Background(), proof, "c1", tools); !errors.Is(err, ErrMCPProofInvalid) {
		t.Errorf("404 = %v", err)
	}
	status = http.StatusBadRequest
	var inv *mcpDefaultsInvalid
	if err := c.SetDefaultTools(context.Background(), proof, "c1", tools); !errors.As(err, &inv) || !strings.Contains(inv.Msg, "hash is required") {
		t.Errorf("400 = %v", err)
	}
	status = http.StatusInternalServerError
	if err := c.SetDefaultTools(context.Background(), proof, "c1", tools); err == nil || errors.Is(err, ErrMCPProofInvalid) {
		t.Errorf("500 = %v", err)
	}
	if err := (&HTTPMCPCoreClient{}).SetDefaultTools(context.Background(), proof, "c1", tools); err == nil {
		t.Error("an unconfigured client succeeded")
	}
}

// SetMCPGateway is what main wires: without the gateway handle, a URL or core, a start is refused.
func TestSetMCPGatewayWiring(t *testing.T) {
	fx := newMCPFx(t)
	fx.api.SetMCPGateway(nil, mcpGatewayURL, "http://core", "key")
	if _, apiErr := fx.api.resolveGrant(context.Background(), humanWho(), echoSel(fx, "allow", "h"), clusterTarget()); apiErr == nil || apiErr.Message != msgMCPNoGateway {
		t.Errorf("no gateway handle: %v", apiErr)
	}
	fx.api.SetMCPGateway(&MCPGatewayHandle{Gateway: mcpgw.New(mcpgw.Config{DB: fx.pool, Core: fx.tokens})}, "not a url", "http://core", "key")
	if cfg := fx.hub.mcpStart.get(); cfg.GatewayURL != "" || cfg.Hasher == nil || cfg.Core == nil {
		t.Errorf("an unusable gateway URL must read as unset: %+v", cfg)
	}
	fx.api.SetMCPGateway(&MCPGatewayHandle{Gateway: mcpgw.New(mcpgw.Config{DB: fx.pool, Core: fx.tokens})}, mcpGatewayURL+"/", "", "")
	if cfg := fx.hub.mcpStart.get(); cfg.GatewayURL != mcpGatewayURL || cfg.Core != nil {
		t.Errorf("config = %+v", cfg)
	}
}

// F3 MINOR 13: the internal key is never re-sent through a redirect, and a 404 from a core that has
// no such route (an old core) is not reported as a dead sign-in.
func TestHTTPMCPCoreClientRedirectsAndMissingRoute(t *testing.T) {
	var elsewhere int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere++
		if r.Header.Get("X-Internal-Key") != "" {
			t.Errorf("the internal key followed a redirect")
		}
		_, _ = w.Write([]byte(`{"connections":[]}`))
	}))
	defer other.Close()
	mode := "redirect"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode {
		case "redirect":
			http.Redirect(w, r, other.URL+"/x", http.StatusTemporaryRedirect)
		case "route":
			http.NotFound(w, r) // what a core without the route answers: "404 page not found"
		case "uniform":
			http.Error(w, "not found", http.StatusNotFound) // core's uniform dead-proof answer
		}
	}))
	defer srv.Close()
	proof := mcpgw.Proof{AccountID: "a1", SessionID: "s1"}
	for name, c := range map[string]*HTTPMCPCoreClient{
		"default client": {BaseURL: srv.URL, InternalKey: "k"},
		"given client":   {BaseURL: srv.URL, InternalKey: "k", HTTP: srv.Client()},
	} {
		mode = "redirect"
		if _, err := c.ListConnections(context.Background(), proof); err == nil {
			t.Errorf("%s: a redirect was followed to success", name)
		}
		if err := c.SetDefaultTools(context.Background(), proof, "c1", map[string]mcpgw.ToolGrant{"e": {Mode: "allow", Hash: "h"}}); err == nil {
			t.Errorf("%s: a redirect was followed to success (defaults)", name)
		}
		mode = "route"
		if _, err := c.ListConnections(context.Background(), proof); !errors.Is(err, ErrMCPCoreOutdated) || errors.Is(err, ErrMCPProofInvalid) {
			t.Errorf("%s: missing route = %v, want ErrMCPCoreOutdated", name, err)
		}
		mode = "uniform"
		if _, err := c.ListConnections(context.Background(), proof); !errors.Is(err, ErrMCPProofInvalid) {
			t.Errorf("%s: uniform 404 = %v, want ErrMCPProofInvalid", name, err)
		}
	}
	if elsewhere != 0 {
		t.Errorf("the redirect target was reached %d times", elsewhere)
	}
}

// F3 MAJOR 9: with the propose gate closed, propose is refused with a 422 (for a selection and for
// saved defaults). The gate is open in production now that proposals exist; this pins the switch.
func TestProposeModeIsRefusedWhileTheGateIsClosed(t *testing.T) {
	mcpProposeAllowed = false
	t.Cleanup(func() { mcpProposeAllowed = true })
	fx := newMCPFx(t)
	ctx := context.Background()
	sel := []MCPSelection{{Connection: fx.connID, Tools: map[string]MCPToolSelection{
		"send": {Mode: "propose", Hash: fx.up.hash("send")},
	}}}
	_, apiErr := fx.api.resolveGrant(ctx, humanWho(), sel, fx.dockerTarget())
	if apiErr == nil || apiErr.Status != http.StatusUnprocessableEntity || !strings.Contains(apiErr.Message, "propose") {
		t.Fatalf("propose selection: %+v, want a 422 naming propose", apiErr)
	}
	if fx.up.listCount() != 0 {
		t.Error("a refused selection reached the upstream")
	}
	// allow keeps working.
	if _, apiErr := fx.api.resolveGrant(ctx, humanWho(), echoSel(fx, "allow", fx.up.hash("echo")), fx.dockerTarget()); apiErr != nil {
		t.Fatalf("allow: %v", apiErr)
	}
}
