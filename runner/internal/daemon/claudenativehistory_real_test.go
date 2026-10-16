package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// A loopback-only provider and gateway exercise the installed CLI. No real
// login, upstream inference, user's config, or external MCP connection is used.
func TestNativeClaudeHistorySurvivesFreshConfigurationWithoutCredentials(t *testing.T) {
	if os.Getenv("BLERG_NATIVE_HISTORY_CLAUDE") != "1" {
		t.Skip("set BLERG_NATIVE_HISTORY_CLAUDE=1 for the installed CLI fixture")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Fatal("Claude CLI unavailable")
	}
	home, work, identity := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.Chmod(identity, 0700); err != nil {
		t.Fatal(err)
	}
	const firstKey = "fixture-provider-key-before-replacement"
	const secondKey = "fixture-provider-key-after-replacement"
	const firstGrant = "fixture-gateway-grant-before-replacement"
	const secondGrant = "fixture-gateway-grant-after-replacement"
	var mu sync.Mutex
	var bodies []string
	grants := map[string]int{}
	var replacement atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if strings.Contains(r.URL.Path, "count_tokens") {
			_, _ = w.Write([]byte(`{"input_tokens":10}`))
			return
		}
		mu.Lock()
		bodies = append(bodies, string(raw))
		requestNumber := len(bodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		events := []map[string]any{
			{"type": "message_start", "message": map[string]any{"id": "msg_fixture", "type": "message", "role": "assistant", "content": []any{}, "model": "claude-haiku-4-5", "usage": map[string]int{"input_tokens": 1, "output_tokens": 0}}},
			{"type": "content_block_start", "index": 0, "content_block": map[string]string{"type": "text", "text": ""}},
			{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "text_delta", "text": "Fixture conversation response."}},
			{"type": "content_block_stop", "index": 0},
			{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 3}},
			{"type": "message_stop"},
		}
		if requestNumber == 2 {
			events[1] = map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "call_removed_fixture", "name": "mcp__fixture__retired", "input": map[string]any{}}}
			events[2] = map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]string{"type": "input_json_delta", "partial_json": "{}"}}
			events[4] = map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use", "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 3}}
		}
		for _, ev := range events {
			raw, _ := json.Marshal(ev)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev["type"], raw)
		}
	}))
	defer provider.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(400)
			return
		}
		if replacement.Load() && r.Header.Get("Authorization") == "Bearer "+firstGrant {
			t.Error("native resume reused a revoked gateway token")
		}
		mu.Lock()
		grants[r.Header.Get("Authorization")]++
		mu.Unlock()
		if req.ID == nil {
			w.WriteHeader(202)
			return
		}
		result := map[string]any{}
		if req.Method == "initialize" {
			result = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "fixture", "version": "1"}}
		}
		if req.Method == "tools/list" {
			list := []any{}
			if r.Header.Get("Authorization") == "Bearer "+firstGrant {
				list = append(list, map[string]any{"name": "retired", "description": "Synthetic removable tool", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}})
			}
			result = map[string]any{"tools": list}
		}
		if req.Method == "tools/call" {
			if !replacement.Load() || r.Header.Get("Authorization") != "Bearer "+secondGrant {
				t.Error("retained native tool attempted a stale grant")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32001, "message": "fixture current grant denies removed tool"}})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer gateway.Close()
	cfg := NativeHistoryConfig{Directory: identity, AccountID: "fixture-account", ClaimUID: "fixture-pvc"}
	run := func(key, grant, prompt string) string {
		t.Helper()
		k, err := openNativeHistory(cfg, "fixture-session", "claude", work)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = k.root.Close() }()
		mcp := newMCPConfigFile(&protocol.MCPGatewayConfig{BaseURL: gateway.URL, Servers: []protocol.MCPGatewayServer{{Name: "fixture", Token: grant}}}, sandboxExec{})
		defer mcp.Remove()
		em := &collectEmitter{}
		env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "ANTHROPIC_API_KEY=" + key, "ANTHROPIC_BASE_URL=" + provider.URL, "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_TELEMETRY=1", "DISABLE_ERROR_REPORTING=1"}
		d := newClaudeCodeDriver(work, "claude-haiku-4-5", "", em, env, withMCPConfigSource(mcp))
		d.steer.legacy = false
		if err = k.bind(d, home, func() { t.Error("history persistence failed") }); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		done := make(chan struct{})
		go func() { d.Run(ctx); close(done) }()
		d.Enqueue(prompt, "chat")
		deadline := time.Now().Add(25 * time.Second)
		for em.count("turn_done") == 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			t.Fatal("CLI fixture did not stop")
		}
		if em.count("turn_done") == 0 || em.count("error:") != 0 {
			t.Fatalf("local native conversation failed: %v", em.outline())
		}
		k.mu.Lock()
		id := k.state.NativeID
		k.mu.Unlock()
		return id
	}
	first := run(firstKey, firstGrant, "first fixture conversation message")
	entries, err := os.ReadDir(filepath.Join(home, ".claude"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "projects" {
			if err = os.RemoveAll(filepath.Join(home, ".claude", entry.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	_ = os.Remove(filepath.Join(home, ".claude.json"))
	cfg.Resume = true
	replacement.Store(true)
	if second := run(secondKey, secondGrant, "second fixture conversation message"); first == "" || second != first {
		t.Fatal("replacement changed native identity")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) < 2 || !strings.Contains(bodies[len(bodies)-1], "first fixture conversation message") || !strings.Contains(bodies[len(bodies)-1], "second fixture conversation message") {
		t.Fatal("replacement did not restore full native context")
	}
	if len(bodies) < 3 || !strings.Contains(bodies[0], "mcp__fixture__retired") {
		t.Fatal("fixture did not exercise initial schema and forced stale native tool call")
	}
	if !strings.Contains(bodies[2], "is_error") {
		t.Fatal("forced stale tool call was not rejected")
	}
	if grants["Bearer "+firstGrant] == 0 || grants["Bearer "+secondGrant] == 0 {
		t.Fatal("replacement did not establish fresh gateway configuration")
	}
	for _, dir := range []string{identity, filepath.Join(home, ".claude", "projects")} {
		if err = filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			for _, marker := range []string{firstKey, secondKey, firstGrant, secondGrant} {
				if strings.Contains(string(raw), marker) {
					t.Error("native conversation retained a synthetic credential marker")
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}
