package server

import (
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// The server cannot import the daemon (the daemon's tests import the server), so the env var
// name and the config validation are mirrored. This pins the mirror to the receiver: what the
// pod and the daemon accept is what the server sends, and the variable they read is the one
// the Job's secretKeyRef names.
func TestGatewayConfigMirrorsTheReceiver(t *testing.T) {
	if mcpGatewayEnvVar != daemon.MCPGatewayEnvVar {
		t.Errorf("env var %q != daemon's %q", mcpGatewayEnvVar, daemon.MCPGatewayEnvVar)
	}
	if restrictToolsEnvVar != daemon.RestrictToolsEnvVar {
		t.Errorf("restrict env var %q != daemon's %q", restrictToolsEnvVar, daemon.RestrictToolsEnvVar)
	}
	srv := func(name, tok string) protocol.MCPGatewayServer {
		return protocol.MCPGatewayServer{Name: name, Token: tok}
	}
	many := make([]protocol.MCPGatewayServer, 21)
	for i := range many {
		many[i] = srv("s"+strings.Repeat("a", i), "gw_x")
	}
	cases := map[string]*protocol.MCPGatewayConfig{
		"ok":               {BaseURL: "http://gw:9090", Servers: []protocol.MCPGatewayServer{srv("calendar", "gw_abc")}},
		"ok https, two":    {BaseURL: "https://gw.example", Servers: []protocol.MCPGatewayServer{srv("a", "t1"), srv("b-b_c", "t2")}},
		"nil":              nil,
		"no scheme":        {BaseURL: "gw:9090", Servers: []protocol.MCPGatewayServer{srv("a", "t")}},
		"ftp":              {BaseURL: "ftp://gw", Servers: []protocol.MCPGatewayServer{srv("a", "t")}},
		"userinfo":         {BaseURL: "http://u:p@gw", Servers: []protocol.MCPGatewayServer{srv("a", "t")}},
		"query":            {BaseURL: "http://gw?x=1", Servers: []protocol.MCPGatewayServer{srv("a", "t")}},
		"fragment":         {BaseURL: "http://gw#x", Servers: []protocol.MCPGatewayServer{srv("a", "t")}},
		"no host":          {BaseURL: "http://", Servers: []protocol.MCPGatewayServer{srv("a", "t")}},
		"no servers":       {BaseURL: "http://gw"},
		"too many servers": {BaseURL: "http://gw", Servers: many},
		"bad name":         {BaseURL: "http://gw", Servers: []protocol.MCPGatewayServer{srv("Not A Name", "t")}},
		"leading dash":     {BaseURL: "http://gw", Servers: []protocol.MCPGatewayServer{srv("-a", "t")}},
		"duplicate":        {BaseURL: "http://gw", Servers: []protocol.MCPGatewayServer{srv("a", "t"), srv("a", "u")}},
		"empty token":      {BaseURL: "http://gw", Servers: []protocol.MCPGatewayServer{srv("a", "")}},
		"space in token":   {BaseURL: "http://gw", Servers: []protocol.MCPGatewayServer{srv("a", "a b")}},
		"control in token": {BaseURL: "http://gw", Servers: []protocol.MCPGatewayServer{srv("a", "a\x7fb")}},
		"overlong token":   {BaseURL: "http://gw", Servers: []protocol.MCPGatewayServer{srv("a", strings.Repeat("t", 513))}},
		"longest token":    {BaseURL: "http://gw", Servers: []protocol.MCPGatewayServer{srv("a", strings.Repeat("t", 512))}},
		"longest name":     {BaseURL: "http://gw", Servers: []protocol.MCPGatewayServer{srv("a"+strings.Repeat("b", 63), "t")}},
		"overlong name":    {BaseURL: "http://gw", Servers: []protocol.MCPGatewayServer{srv("a"+strings.Repeat("b", 64), "t")}},
	}
	for name, cfg := range cases {
		mine, theirs := validateGatewayConfig(cfg), daemon.ValidateMCPGateway(cfg)
		if (mine == nil) != (theirs == nil) {
			t.Errorf("%s: server says %v, the receiver says %v", name, mine, theirs)
		}
	}
}
