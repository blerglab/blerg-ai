package protocol

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/contracts/pluginspec"
	"strings"
	"testing"
)

func TestSessionPluginWireContractAndOldPeer(t *testing.T) {
	var old DaemonHello
	if err := json.Unmarshal([]byte(`{"type":"daemon_hello","plugins":true}`), &old); err != nil || len(old.SessionPluginEngines) != 0 {
		t.Fatal("old peer claimed live updates")
	}
	hello, _ := json.Marshal(DaemonHello{SessionPluginEngines: []string{"claude"}})
	if !strings.Contains(string(hello), `"session_plugin_engines":["claude"]`) {
		t.Fatal("capability missing")
	}
	raw, _ := json.Marshal(ApplySessionPlugins{Type: "apply_session_plugins", RequestID: "request", SessionID: "session", Plugins: []pluginspec.Entry{{Marketplace: "example-org/example-repo", Plugin: "safe"}}})
	var back ApplySessionPlugins
	if json.Unmarshal(raw, &back) != nil || back.RequestID != "request" || len(back.Plugins) != 1 {
		t.Fatal("request lost")
	}
	raw, _ = json.Marshal(SessionPluginsResult{Type: "session_plugins_result", RequestID: "request", SessionID: "session", State: "queued"})
	if strings.Contains(string(raw), `"plugins"`) || !strings.Contains(string(raw), `"state":"queued"`) {
		t.Fatal("queued state claims installed plugins")
	}
}
