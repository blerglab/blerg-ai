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
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Only synthetic credentials, a private fixture home and loopback HTTP are used.
// The actual CLI persists its schemas; the fresh daemon must still authorize
// each call against current grants rather than those stored schemas.
func TestNativeCodexHistoryRejectsRetainedRevokedToolsAndCredentials(t *testing.T) {
	if os.Getenv("BLERG_REAL_CODEX") != "1" {
		t.Skip("set BLERG_REAL_CODEX=1 for installed native history fixture")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Fatal("Codex unavailable")
	}
	home, work, persistent := t.TempDir(), t.TempDir(), t.TempDir()
	identity := filepath.Join(persistent, "identity")
	sessions := filepath.Join(home, ".codex", "sessions")
	for _, dir := range []string{identity, sessions, filepath.Join(persistent, "sqlite")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	const firstKey = "fixture-codex-provider-before-replacement"
	const secondKey = "fixture-codex-provider-after-replacement"
	const firstGrant = "fixture-codex-grant-before-replacement"
	const secondGrant = "fixture-codex-grant-after-replacement"
	name := codexDynamicMCPName("fixture", "retired")
	var mu sync.Mutex
	var bodies []string
	auths := map[string]int{}
	grants := map[string]int{}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(400)
			return
		}
		mu.Lock()
		grants[r.Header.Get("Authorization")]++
		mu.Unlock()
		if req.Method != "tools/list" {
			t.Error("retained revoked tool reached gateway")
			w.WriteHeader(403)
			return
		}
		list := []any{}
		if r.Header.Get("Authorization") == "Bearer "+firstGrant {
			list = append(list, map[string]any{"name": "retired", "description": "Synthetic removable tool", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"tools": list}})
	}))
	defer gateway.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		mu.Lock()
		bodies = append(bodies, string(raw))
		auths[r.Header.Get("Authorization")]++
		mu.Unlock()
		item := map[string]any{"type": "message", "id": "msg_fixture", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": "Fixture retained conversation response."}}}
		if strings.Contains(string(raw), "second native fixture conversation message") && !strings.Contains(string(raw), "call_removed") {
			item = map[string]any{"type": "function_call", "id": "fc_removed", "call_id": "call_removed", "name": name, "arguments": "{}"}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		send := func(ev map[string]any) { raw, _ := json.Marshal(ev); _, _ = fmt.Fprintf(w, "data: %s\n\n", raw) }
		send(map[string]any{"type": "response.created", "response": map[string]string{"id": "response_fixture"}})
		send(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item})
		send(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
		send(map[string]any{"type": "response.completed", "response": map[string]any{"id": "response_fixture", "status": "completed", "output": []any{item}, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}}})
	}))
	defer provider.Close()
	cfg := NativeHistoryConfig{Directory: identity, AccountID: "fixture-account", ClaimUID: "fixture-native-pvc"}
	run := func(key, grant, prompt string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(`{"OPENAI_API_KEY":"`+key+`"}`), 0600); err != nil {
			t.Fatal(err)
		}
		k, err := openNativeHistory(cfg, "fixture-session", "codex", work)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = k.root.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		runtime, err := prepareCodexRestrictedRuntime(ctx, work, filepath.Join(home, ".codex"), nil, &protocol.MCPGatewayConfig{BaseURL: gateway.URL, Servers: []protocol.MCPGatewayServer{{Name: "fixture", Token: grant}}}, false)
		if err != nil {
			t.Fatal(err)
		}
		defer runtime.Close()
		providers := map[string]any{"fixture": map[string]any{"name": "Loopback native fixture", "base_url": provider.URL + "/v1", "wire_api": "responses", "requires_openai_auth": false, "supports_websockets": false, "env_key": "FIXTURE_NATIVE_PROVIDER_KEY"}}
		if err = writeCodexRestrictedConfig(runtime.Home, codexRestrictedConfig("gpt-5-codex", "fixture", providers)); err != nil {
			t.Fatal(err)
		}
		em := &collectEmitter{}
		d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: work, Model: "gpt-5-codex", Emitter: em, Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "FIXTURE_NATIVE_PROVIDER_KEY=" + key, "HTTPS_PROXY=http://127.0.0.1:9", "HTTP_PROXY=http://127.0.0.1:9", "ALL_PROXY=http://127.0.0.1:9", "NO_PROXY=127.0.0.1,localhost"}})
		d.RestrictedRuntime = runtime
		if err = k.bind(d, home, func() { t.Error("native identity persistence failed") }); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() { d.Run(ctx); close(done) }()
		d.Enqueue(prompt, "chat")
		deadline := time.Now().Add(30 * time.Second)
		for countKind(em, "turn_done") == 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Fatal("native fixture did not stop")
		}
		if countKind(em, "turn_done") == 0 || countKind(em, "error") != 0 {
			t.Fatalf("native conversation failed: kinds=%v", em.kinds())
		}
		notice := false
		for _, ev := range em.snapshot() {
			if p, ok := ev.Payload.(protocol.CapabilitiesPayload); ok && ev.Kind == "capabilities" && strings.Contains(p.Note, "retains earlier tool definitions") {
				notice = true
			}
		}
		if notice != cfg.Resume {
			t.Fatal("schema-change capabilities notice did not match retained/current definitions")
		}
		k.mu.Lock()
		id := k.state.NativeID
		k.mu.Unlock()
		return id
	}
	first := run(firstKey, firstGrant, "first native fixture conversation message")
	// The deterministic private home was removed by runtime.Close; auth and
	// configuration will be rebuilt, while only conversations and SQLite remain.
	cfg.Resume = true
	if next := run(secondKey, secondGrant, "second native fixture conversation message"); first == "" || next != first {
		t.Fatal("replacement changed native thread")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) < 3 || !strings.Contains(bodies[1], "first native fixture conversation message") || !strings.Contains(bodies[1], "second native fixture conversation message") {
		shapes := []string{}
		for _, body := range bodies {
			shapes = append(shapes, fmt.Sprintf("first=%v second=%v removedResult=%v", strings.Contains(body, "first native fixture conversation message"), strings.Contains(body, "second native fixture conversation message"), strings.Contains(body, "call_removed")))
		}
		t.Fatalf("native history context was not restored: %v", shapes)
	}
	if !strings.Contains(bodies[0], name) || !strings.Contains(bodies[1], name) {
		t.Fatal("fixture did not exercise retained native tool schema")
	}
	if !strings.Contains(bodies[2], "not in this session") {
		t.Fatal("forced stale schema call was not denied by the fresh bridge")
	}
	if auths["Bearer "+firstKey] == 0 || auths["Bearer "+secondKey] == 0 || grants["Bearer "+firstGrant] == 0 || grants["Bearer "+secondGrant] == 0 {
		t.Fatal("replacement did not use fresh provider and gateway credentials")
	}
	for _, dir := range []string{persistent, sessions} {
		if err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, marker := range []string{firstKey, secondKey, firstGrant, secondGrant} {
				if strings.Contains(string(raw), marker) {
					t.Error("retained native state contains a synthetic credential")
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}
