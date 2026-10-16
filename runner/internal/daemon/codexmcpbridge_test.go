package daemon

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCodexMCPBridgeKeepsGrantInDaemon(t *testing.T) {
	const token = "test-only-gateway-token"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("missing gateway grant")
			w.WriteHeader(401)
			return
		}
		var req struct {
			ID     int
			Method string
			Params struct {
				Name      string
				Arguments json.RawMessage
			}
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		if req.Method == "tools/list" {
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": []any{map[string]any{"name": "read", "description": "Read docs", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}})
			return
		}
		if req.Method != "tools/call" || req.Params.Name != "read" {
			t.Error("unexpected tool route")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"content": []any{map[string]string{"type": "text", "text": "documentation"}}}})
	}))
	defer upstream.Close()
	bridge, err := newCodexMCPBridge(context.Background(), &protocol.MCPGatewayConfig{BaseURL: upstream.URL, Servers: []protocol.MCPGatewayServer{{Name: "docs", Token: token}}})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	specs, _ := json.Marshal(bridge.Specs())
	if strings.Contains(string(specs), token) || strings.Contains(string(specs), upstream.URL) {
		t.Fatal("gateway credentials escaped into model tool specs")
	}
	out, err := bridge.Call(context.Background(), "blerg_mcp__docs__read", json.RawMessage(`{}`))
	if err != nil || out != "documentation" {
		t.Fatalf("proxy call=%q %v", out, err)
	}
	if _, err := bridge.Call(context.Background(), "blerg_mcp__other__read", json.RawMessage(`{}`)); err == nil {
		t.Fatal("ungranted tool accepted")
	}
}
func TestCodexMCPBridgeNeverReturnsRemoteErrorBodies(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("test-only-token-reflection"))
	}))
	defer upstream.Close()
	_, err := newCodexMCPBridge(context.Background(), &protocol.MCPGatewayConfig{BaseURL: upstream.URL, Servers: []protocol.MCPGatewayServer{{Name: "docs", Token: "test-only-token"}}})
	if err == nil || strings.Contains(err.Error(), "reflection") {
		t.Fatal("remote error body was exposed")
	}
}
