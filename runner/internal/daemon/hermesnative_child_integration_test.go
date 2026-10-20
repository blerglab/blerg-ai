package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHermesRealNativeChildAdapter(t *testing.T) {
	source, python := os.Getenv("BLERG_TEST_HERMES_NATIVE_ROOT"), os.Getenv("BLERG_TEST_HERMES_NATIVE_PYTHON")
	if source == "" && python == "" {
		t.Skip("set native source and Python to run this optional integration test")
	}
	if source == "" || python == "" {
		t.Fatal("both native integration settings are required")
	}
	home := t.TempDir()
	profile := filepath.Join(home, ".hermes")
	work := filepath.Join(home, "workspace")
	for _, p := range []string{profile, work} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(work, "proof.txt"), []byte("Synthetic offline tool proof.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var toolResult atomic.Bool
	var sawGuide atomic.Bool
	var sawClarification atomic.Bool
	var questionCount atomic.Int32
	var sawRecoveredHistory atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": "offline-proof"}}})
			return
		}
		var q struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&q) != nil {
			http.Error(w, "bad fixture request", 400)
			return
		}
		prior, canRead, canClarify := false, false, false
		toolMessages := 0
		for _, m := range q.Messages {
			prior = prior || m.Role == "tool"
			if m.Role == "tool" {
				toolMessages++
				if strings.Contains(string(m.Content), "Blue") {
					sawClarification.Store(true)
				}
			}
			if m.Role == "system" && strings.Contains(string(m.Content), "OFFLINE_BLERG_GUIDE_SENTINEL") {
				sawGuide.Store(true)
			}
		}
		var firstSeen, secondSeen, thirdSeen bool
		for _, m := range q.Messages {
			c := string(m.Content)
			firstSeen = firstSeen || strings.Contains(c, "Read proof.txt then")
			secondSeen = secondSeen || strings.Contains(c, "second turn")
			thirdSeen = thirdSeen || strings.Contains(c, "third turn")
		}
		if firstSeen && secondSeen && thirdSeen {
			sawRecoveredHistory.Store(true)
		}
		for _, v := range q.Tools {
			canRead = canRead || v.Function.Name == "read_file"
			canClarify = canClarify || v.Function.Name == "clarify"
		}
		if prior {
			toolResult.Store(true)
		}
		canDelegate := false
		for _, v := range q.Tools {
			canDelegate = canDelegate || v.Function.Name == "delegate_task"
		}
		delta := map[string]any{"content": "PARENT_OFFLINE_DONE"}
		finish := "stop"
		if q.Model == "offline-child" {
			delta = map[string]any{"content": "CHILD_OFFLINE_DONE"}
			if !prior && canRead {
				args, _ := json.Marshal(map[string]any{"path": filepath.Join(work, "proof.txt")})
				delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "offline-child-read", "type": "function", "function": map[string]any{"name": "read_file", "arguments": string(args)}}}}
				finish = "tool_calls"
			}
		} else if !prior && canDelegate {
			args, _ := json.Marshal(map[string]any{"tasks": []any{map[string]any{"goal": "Read the fixture file then reply CHILD_OFFLINE_DONE.", "context": "The fixture is " + filepath.Join(work, "proof.txt")}}})
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "offline-delegate-1", "type": "function", "function": map[string]any{"name": "delegate_task", "arguments": string(args)}}}}
			finish = "tool_calls"
		}
		usage := map[string]any{"prompt_tokens": 20, "completion_tokens": 8, "total_tokens": 28}
		if !q.Stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "offline", "object": "chat.completion", "model": "offline-proof", "choices": []any{map[string]any{"index": 0, "message": map[string]any{"role": "assistant", "content": "OFFLINE_GATEWAY_OK"}, "finish_reason": "stop"}}, "usage": usage})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range []map[string]any{
			{"id": "offline", "object": "chat.completion.chunk", "model": "offline-proof", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}},
			{"id": "offline", "object": "chat.completion.chunk", "model": "offline-proof", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}, "usage": usage},
		} {
			b, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", b)
			w.(http.Flusher).Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}))
	defer srv.Close()
	cfg := fmt.Sprintf("model:\n  provider: custom\n  default: offline-proof\n  base_url: %s/v1\n  context_length: 65536\nterminal:\n  backend: local\n", srv.URL)
	cfg += "delegation:\n  model: offline-child\n"
	if err := os.WriteFile(filepath.Join(profile, "config.yaml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: work, SessionID: "offline-blerg", Emitter: em, Env: []string{"HOME=" + home, "HERMES_HOME=" + profile, "PATH=/usr/local/bin:/usr/bin:/bin", "TERM=dumb", "NO_COLOR=1", "HERMES_PYTHON_SRC_ROOT=" + source, "HERMES_PYTHON=" + python}})
	d.AskUser = func(ctx context.Context, q string) (string, error) {
		questionCount.Add(1)
		if !strings.Contains(q, "fixture") {
			t.Error("native question text lost")
		}
		return "Blue", nil
	}
	d.DeveloperInstructions = "OFFLINE_BLERG_GUIDE_SENTINEL"
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("isolated native gateway did not stop")
		}
	}()
	wait := func(turns int) {
		t.Helper()
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			if countKind(em, "turn_done") >= turns {
				return
			}
			select {
			case <-done:
				t.Fatal("native driver ended before completion")
			case <-time.After(10 * time.Millisecond):
			}
		}
		t.Fatal("native adapter completion timed out")
	}
	d.EnqueueInput(agent.Input{Text: "Delegate one fixture file read to a child and report completion.", Source: "chat", ClientID: "offline-parent"})
	wait(1)
	deadline := time.Now().Add(90 * time.Second)
	for countKind(em, "subagent_done") == 0 && time.Now().Before(deadline) {
		select {
		case <-done:
			t.Fatal("parent gateway stopped before native child settled")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if countKind(em, "subagent_started") != 1 || countKind(em, "subagent_done") != 1 {
		t.Errorf("native child lifecycle counts started=%d done=%d", countKind(em, "subagent_started"), countKind(em, "subagent_done"))
	}
	for _, e := range em.snapshot() {
		if e.Kind == "subagent_started" || e.Kind == "subagent_done" {
			p := e.Payload.(agent.SubagentPayload)
			t.Logf("child event=%s model=%s usage=%+v tools=%d failed=%v", e.Kind, p.Model, p.Usage, p.ToolUses, p.IsError)
			if p.ChildID == "" || p.Model != "offline-child" {
				t.Error("native child identity/model missing or guessed")
			}
			if e.Kind == "subagent_done" {
				if p.IsError || p.Usage == nil || p.Usage.InputTokens != 40 || p.Usage.OutputTokens != 16 || p.ToolUses < 1 {
					t.Error("native child usage/tools did not match actual child requests")
				}
			}
		}
		if e.Kind == "error" {
			t.Errorf("native child adapter error=%+v", e.Payload)
		}
	}
}
