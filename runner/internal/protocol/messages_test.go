package protocol

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"strings"
	"testing"
)

// A spawn from a server that predates the MCP gateway carries no
// mcp_gateway, and must decode with no grant.
func TestSpawnSessionWithoutMCPGatewayDecodes(t *testing.T) {
	var msg SpawnSession
	if err := json.Unmarshal([]byte(`{"type":"spawn_session","session_id":"s1","kind":"agent"}`), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.MCPGateway != nil {
		t.Fatalf("grant appeared from nowhere: %+v", msg.MCPGateway)
	}
}

// A spawn without a grant marshals byte-identically to before: no mcp_gateway
// key, so idempotency hashes and older peers see nothing new.
func TestSpawnSessionOmitsAbsentMCPGateway(t *testing.T) {
	raw, err := json.Marshal(SpawnSession{Type: "spawn_session", SessionID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "mcp_gateway") {
		t.Fatalf("absent grant was marshalled: %s", raw)
	}
}

func TestSpawnSessionMCPGatewayRoundTripAndUnknownFields(t *testing.T) {
	in := `{"type":"spawn_session","session_id":"s1","future_field":1,
	  "mcp_gateway":{"base_url":"http://gw:9","servers":[{"name":"cal","token":"t1","extra":true}],"next":"x"}}`
	var msg SpawnSession
	if err := json.Unmarshal([]byte(in), &msg); err != nil {
		t.Fatalf("unknown fields must be tolerated: %v", err)
	}
	g := msg.MCPGateway
	if g == nil || g.BaseURL != "http://gw:9" || len(g.Servers) != 1 || g.Servers[0].Name != "cal" || g.Servers[0].Token != "t1" {
		t.Fatalf("decoded %+v", g)
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"mcp_gateway":{"base_url":"http://gw:9","servers":[{"name":"cal","token":"t1"}]}`) {
		t.Fatalf("wire shape changed: %s", raw)
	}
}

// An old daemon's hello has no mcp_gateway: the capability reads false, so the
// server refuses a grant. A new daemon's says true; false is never sent.
func TestRestrictToolsFieldAndCapability(t *testing.T) {
	var old DaemonHello
	if err := json.Unmarshal([]byte(`{"type":"daemon_hello","name":"d","mcp_gateway":true}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.RestrictTools {
		t.Fatal("a daemon that predates restrict_tools claims it")
	}
	raw, _ := json.Marshal(DaemonHello{Type: "daemon_hello", RestrictTools: true})
	if !strings.Contains(string(raw), `"restrict_tools":true`) {
		t.Fatalf("capability missing: %s", raw)
	}
	raw, _ = json.Marshal(SpawnSession{Type: "spawn_session"})
	if strings.Contains(string(raw), "restrict_tools") {
		t.Fatalf("absent restriction marshalled: %s", raw)
	}
	raw, _ = json.Marshal(SpawnSession{Type: "spawn_session", RestrictTools: true})
	if !strings.Contains(string(raw), `"restrict_tools":true`) {
		t.Fatalf("restriction missing: %s", raw)
	}
}

func TestPluginsFieldAndCapability(t *testing.T) {
	var old DaemonHello
	if err := json.Unmarshal([]byte(`{"type":"daemon_hello","name":"d","restrict_tools":true}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Plugins {
		t.Fatal("a daemon that predates plugins claims it")
	}
	raw, _ := json.Marshal(DaemonHello{Type: "daemon_hello", Plugins: true})
	if !strings.Contains(string(raw), `"plugins":true`) {
		t.Fatalf("capability missing: %s", raw)
	}
	raw, _ = json.Marshal(SpawnSession{Type: "spawn_session"})
	if strings.Contains(string(raw), "plugins") {
		t.Fatalf("absent plugin list marshalled: %s", raw)
	}
	raw, _ = json.Marshal(SpawnSession{Type: "spawn_session", Plugins: []pluginspec.Entry{{Marketplace: "a/b", Plugin: "c"}}})
	if !strings.Contains(string(raw), `"plugins":[{"marketplace":"a/b","plugin":"c"}]`) {
		t.Fatalf("plugin list missing: %s", raw)
	}
	var back SpawnSession
	if err := json.Unmarshal(raw, &back); err != nil || len(back.Plugins) != 1 || back.Plugins[0].Plugin != "c" {
		t.Fatalf("round trip: %+v %v", back.Plugins, err)
	}
}

func TestDaemonHelloMCPGatewayCapability(t *testing.T) {
	var old DaemonHello
	if err := json.Unmarshal([]byte(`{"type":"daemon_hello","name":"d","clone_from":true}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.MCPGateway {
		t.Fatal("an old daemon's hello claims the mcp_gateway capability")
	}
	raw, err := json.Marshal(DaemonHello{Type: "daemon_hello"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "mcp_gateway") {
		t.Fatalf("false capability marshalled: %s", raw)
	}
	raw, _ = json.Marshal(DaemonHello{Type: "daemon_hello", MCPGateway: true})
	if !strings.Contains(string(raw), `"mcp_gateway":true`) {
		t.Fatalf("capability missing: %s", raw)
	}
}
