package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// serveGatewayOnWeb mounts the fixture's gateway on a web-port mux the way the server's main does.
func serveGatewayOnWeb(fx *mcpFx) *http.ServeMux {
	fx.t.Helper()
	cfg := *fx.hub.mcpStart.get()
	cfg.gateway = cfg.Hasher.(*mcpgw.Gateway)
	fx.api.setMCPStart(&cfg)
	mux := http.NewServeMux()
	fx.api.RegisterMCPGatewayRoutes(mux, true)
	if !fx.hub.mcpStart.get().ServedOnWeb {
		fx.t.Fatal("the gateway was not marked as served on the web port")
	}
	return mux
}

func spawnOf(t *testing.T, dc *DaemonConn) protocol.SpawnSession {
	t.Helper()
	var spawn protocol.SpawnSession
	if err := json.Unmarshal(<-dc.send, &spawn); err != nil {
		t.Fatal(err)
	}
	return spawn
}

func pingGateway(mux http.Handler, name, token string) int {
	code, _ := pingGatewayBody(mux, name, token)
	return code
}

func pingGatewayBody(mux http.Handler, name, token string) (int, string) {
	req := httptest.NewRequest(http.MethodPost, "/mcp-gw/"+name, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// A desktop sandbox session cannot reach the gateway's in-cluster address. With the gateway on
// the web port and a daemon that can, the grant tells the daemon to dial it through the server,
// and the web port serves that session's token; a cluster session's token is not served there.
func TestGatewayOnWebPortServesDaemonSessionsOnly(t *testing.T) {
	fx := newMCPFx(t)
	fx.dc.SetMCPGatewayViaServer(true)
	mux := serveGatewayOnWeb(fx)

	rec := fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{
		"daemon_id": fx.dc.ID, "repo": "app", "kind": "agent", "runtime": "docker", "mcp": fx.selection(),
	})
	fx.sessionID(rec)
	spawn := spawnOf(t, fx.dc)
	if spawn.MCPGateway == nil || !spawn.MCPGateway.ViaServer {
		t.Fatalf("the daemon was not told to dial the gateway through the server: %+v", spawn.MCPGateway)
	}
	if got := pingGateway(mux, mcpConn, spawn.MCPGateway.Servers[0].Token); got != http.StatusOK {
		t.Errorf("daemon session on the web port: %d, want 200", got)
	}

	rec = fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{
		"repo": "acme/app", "kind": "agent", "runtime": "cluster", "mcp": fx.selection(),
	})
	cluster := fx.secretConfig(fx.sessionID(rec))
	if cluster == nil || cluster.ViaServer || cluster.BaseURL != mcpGatewayURL {
		t.Fatalf("a cluster pod's grant changed: %+v", cluster)
	}
	// A cluster token, an unknown one and a malformed one are refused alike: same status, same
	// JSON-RPC body, so the route never tells which tokens exist.
	clusterCode, clusterBody := pingGatewayBody(mux, mcpConn, cluster.Servers[0].Token)
	unknownCode, unknownBody := pingGatewayBody(mux, mcpConn, "gw_"+strings.Repeat("0", 64))
	junkCode, junkBody := pingGatewayBody(mux, mcpConn, "not-a-token")
	if clusterCode != http.StatusUnauthorized || unknownCode != clusterCode || junkCode != clusterCode ||
		clusterBody != unknownBody || junkBody != unknownBody || !strings.Contains(unknownBody, `"jsonrpc":"2.0"`) {
		t.Errorf("refusals differ: cluster %d %q, unknown %d %q, junk %d %q", clusterCode, clusterBody, unknownCode, unknownBody, junkCode, junkBody)
	}
	// The right token on another connection's path is refused the same way.
	if code, body := pingGatewayBody(mux, "other", spawn.MCPGateway.Servers[0].Token); code != clusterCode || body != clusterBody {
		t.Errorf("wrong path: %d %q", code, body)
	}
}

// A grant not issued for the public route (an older daemon's, which dials the gateway's own
// listener) is refused there like any unknown token.
func TestGatewayOnWebPortRefusesGrantsNotIssuedForIt(t *testing.T) {
	fx := newMCPFx(t)
	mux := serveGatewayOnWeb(fx) // fx.dc does not report mcp_gateway_via_server
	rec := fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{
		"daemon_id": fx.dc.ID, "repo": "app", "kind": "agent", "runtime": "docker", "mcp": fx.selection(),
	})
	fx.sessionID(rec)
	spawn := spawnOf(t, fx.dc)
	if spawn.MCPGateway.ViaServer {
		t.Fatal("an older daemon was told to dial through the server")
	}
	if got := pingGateway(mux, mcpConn, spawn.MCPGateway.Servers[0].Token); got != http.StatusUnauthorized {
		t.Errorf("grant not issued for the public route: %d, want 401", got)
	}
}

// A public-route grant left unused past the idle limit is refused there; its daemon reconnecting
// with the session running renews it.
func TestGatewayOnWebPortIdleLimitAndRenewal(t *testing.T) {
	fx := newMCPFx(t)
	fx.dc.SetMCPGatewayViaServer(true)
	mux := serveGatewayOnWeb(fx)
	rec := fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{
		"daemon_id": fx.dc.ID, "repo": "app", "kind": "agent", "runtime": "docker", "mcp": fx.selection(),
	})
	sid := fx.sessionID(rec)
	token := spawnOf(t, fx.dc).MCPGateway.Servers[0].Token
	if _, err := fx.pool.Exec(t.Context(), `UPDATE session_mcp_grants SET public_used_at = now() - interval '8 days' WHERE session_id = $1`, sid); err != nil {
		t.Fatal(err)
	}
	if got := pingGateway(mux, mcpConn, token); got != http.StatusUnauthorized {
		t.Fatalf("idle grant: %d, want 401", got)
	}
	if err := db.RenewPublicMCPGrants(t.Context(), fx.pool, "another-daemon", []string{sid}); err != nil {
		t.Fatal(err)
	}
	if got := pingGateway(mux, mcpConn, token); got != http.StatusUnauthorized {
		t.Fatalf("renewed by a daemon that does not run the session: %d, want 401", got)
	}
	if err := db.RenewPublicMCPGrants(t.Context(), fx.pool, fx.dc.ID, []string{sid}); err != nil {
		t.Fatal(err)
	}
	if got := pingGateway(mux, mcpConn, token); got != http.StatusOK {
		t.Fatalf("renewed grant: %d, want 200", got)
	}
}

// A valid token is never turned away, even from an address over its failure allowance (behind an
// ingress without trusted proxies every client shares one); junk from that address is.
func TestGatewayOnWebPortValidTokenNeverThrottled(t *testing.T) {
	fx := newMCPFx(t)
	fx.dc.SetMCPGatewayViaServer(true)
	mux := serveGatewayOnWeb(fx)
	rec := fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{
		"daemon_id": fx.dc.ID, "repo": "app", "kind": "agent", "runtime": "docker", "mcp": fx.selection(),
	})
	fx.sessionID(rec)
	token := spawnOf(t, fx.dc).MCPGateway.Servers[0].Token
	for i := 0; i < 400; i++ {
		pingGateway(mux, mcpConn, "junk")
	}
	if got := pingGateway(mux, mcpConn, "junk"); got != http.StatusTooManyRequests {
		t.Fatalf("junk from a throttled address: %d, want 429", got)
	}
	if got := pingGateway(mux, mcpConn, token); got != http.StatusOK {
		t.Fatalf("valid token from a throttled address: %d, want 200", got)
	}
}

// From a throttled address every unsuccessful token gets the same answer, so the route never tells
// a token that exists but is refused (a cluster session's) from one that does not exist.
func TestGatewayOnWebPortThrottledRefusalsAreUniform(t *testing.T) {
	fx := newMCPFx(t)
	fx.dc.SetMCPGatewayViaServer(true)
	mux := serveGatewayOnWeb(fx)
	rec := fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{
		"repo": "acme/app", "kind": "agent", "runtime": "cluster", "mcp": fx.selection(),
	})
	cluster := fx.secretConfig(fx.sessionID(rec)).Servers[0].Token
	for i := 0; i < 400; i++ {
		pingGateway(mux, mcpConn, "junk")
	}
	junkCode, junkBody := pingGatewayBody(mux, mcpConn, "junk")
	unknownCode, unknownBody := pingGatewayBody(mux, mcpConn, "gw_"+strings.Repeat("1", 64))
	clusterCode, clusterBody := pingGatewayBody(mux, mcpConn, cluster)
	if junkCode != unknownCode || unknownCode != clusterCode || junkBody != unknownBody || unknownBody != clusterBody {
		t.Fatalf("throttled refusals differ: junk %d %q, unknown %d %q, cluster %d %q", junkCode, junkBody, unknownCode, unknownBody, clusterCode, clusterBody)
	}
}

// Behind a trusted proxy each real client has its own allowance: one client spraying junk does
// not throttle another.
func TestGatewayOnWebPortThrottlesRealClientBehindTrustedProxy(t *testing.T) {
	fx := newMCPFx(t)
	trusted, err := mcpgw.ParseTrustedProxies("192.0.2.0/24") // httptest's peer address
	if err != nil {
		t.Fatal(err)
	}
	cfg := *fx.hub.mcpStart.get()
	cfg.Hasher.(*mcpgw.Gateway).SetTrustedProxies(trusted)
	fx.api.setMCPStart(&cfg)
	mux := serveGatewayOnWeb(fx)
	from := func(client string) int {
		req := httptest.NewRequest(http.MethodPost, "/mcp-gw/"+mcpConn, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
		req.Header.Set("Authorization", "Bearer junk")
		req.Header.Set("X-Forwarded-For", client)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	for i := 0; i < 400; i++ {
		from("198.51.100.1")
	}
	if got := from("198.51.100.1"); got != http.StatusTooManyRequests {
		t.Fatalf("spraying client: %d, want 429", got)
	}
	if got := from("198.51.100.2"); got != http.StatusUnauthorized {
		t.Fatalf("another client behind the same proxy: %d, want 401", got)
	}
}

// The operator's switch: off, the prefix is a 404 and no grant is issued for the public route.
func TestGatewayOnWebPortSwitchOff(t *testing.T) {
	if mcpGatewayPublic("") != true || mcpGatewayPublic("on") != true || mcpGatewayPublic("OFF") || mcpGatewayPublic("0") || mcpGatewayPublic("false") {
		t.Fatal("BLERG_RUNNER_MCP_GW_PUBLIC parsing")
	}
	fx := newMCPFx(t)
	fx.dc.SetMCPGatewayViaServer(true)
	cfg := *fx.hub.mcpStart.get()
	cfg.gateway = cfg.Hasher.(*mcpgw.Gateway)
	fx.api.setMCPStart(&cfg)
	mux := http.NewServeMux()
	fx.api.RegisterMCPGatewayRoutes(mux, false)
	if fx.hub.mcpStart.get().ServedOnWeb {
		t.Fatal("served on the web port while switched off")
	}
	rec := fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{
		"daemon_id": fx.dc.ID, "repo": "app", "kind": "agent", "runtime": "docker", "mcp": fx.selection(),
	})
	fx.sessionID(rec)
	spawn := spawnOf(t, fx.dc)
	if spawn.MCPGateway.ViaServer {
		t.Fatal("grant issued for a switched-off route")
	}
	if got := pingGateway(mux, mcpConn, spawn.MCPGateway.Servers[0].Token); got != http.StatusNotFound {
		t.Fatalf("switched-off route: %d, want 404", got)
	}
}

// An older daemon, or a server that does not serve the gateway on its web port, keeps the
// grant as it was: the daemon dials BaseURL.
func TestGatewayGrantNotViaServerWithoutBothSides(t *testing.T) {
	for _, tc := range []struct {
		name          string
		daemon, onWeb bool
	}{{"old daemon", false, true}, {"not on the web port", true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newMCPFx(t)
			fx.dc.SetMCPGatewayViaServer(tc.daemon)
			if tc.onWeb {
				serveGatewayOnWeb(fx)
			}
			rec := fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{
				"daemon_id": fx.dc.ID, "repo": "app", "kind": "agent", "runtime": "docker", "mcp": fx.selection(),
			})
			fx.sessionID(rec)
			if spawn := spawnOf(t, fx.dc); spawn.MCPGateway == nil || spawn.MCPGateway.ViaServer {
				t.Fatalf("grant = %+v, want one dialled at BaseURL", spawn.MCPGateway)
			}
		})
	}
}

// Board access for a session on the daemon's host (This machine): the board alone, dialled
// through the server, for an unrestricted session. Connections are still refused there.
func TestLaunchBoardOnHostDialsThroughServer(t *testing.T) {
	fx := newMCPFx(t)
	fx.hub.SetJobManager(nil)
	fx.dc.SetMCPGatewayViaServer(true)
	enableLaunchBoard(fx)
	serveGatewayOnWeb(fx)

	rec := fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{
		"daemon_id": fx.dc.ID, "repo": "app", "kind": "agent", "runtime": "daemon", "board_id": "board-7",
	})
	sid := fx.sessionID(rec)
	spawn := spawnOf(t, fx.dc)
	if spawn.Sandbox || spawn.RestrictTools || spawn.BoardID != "board-7" {
		t.Fatalf("host board session: sandbox=%v restrict=%v board=%q", spawn.Sandbox, spawn.RestrictTools, spawn.BoardID)
	}
	g := spawn.MCPGateway
	if g == nil || !g.ViaServer || len(g.Servers) != 1 || g.Servers[0].Name != mcpgw.BuiltinBoard || !fx.tokenBelongsTo(g.Servers[0].Token, sid) {
		t.Fatalf("host board grant = %+v", g)
	}

	// A connection on the host is refused, board or not, before anything is recorded.
	before := fx.count("sessions")
	rec = fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{
		"daemon_id": fx.dc.ID, "repo": "app", "kind": "agent", "runtime": "daemon", "board_id": "board-7", "mcp": fx.selection(),
	})
	if rec.Code != http.StatusUnprocessableEntity || fx.count("sessions") != before || len(fx.dc.send) != 0 {
		t.Fatalf("a connection on the host: %d %s", rec.Code, rec.Body.String())
	}
}

// Without the web-port gateway the host has no way to reach it: refused with the reason.
func TestLaunchBoardOnHostNeedsWebGateway(t *testing.T) {
	fx := newMCPFx(t)
	fx.hub.SetJobManager(nil)
	fx.dc.SetMCPGatewayViaServer(true)
	enableLaunchBoard(fx)
	rec := fx.do(fx.api.HandlePostSessions, fx.human(), map[string]any{
		"daemon_id": fx.dc.ID, "repo": "app", "kind": "agent", "runtime": "daemon", "board_id": "board-7",
	})
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "web port") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if fx.count("sessions") != 0 || len(fx.dc.send) != 0 {
		t.Fatal("refusal created a session or sent a spawn")
	}
}
