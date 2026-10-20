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

func TestHermesRealNativeOfflineAdapter(t *testing.T) {
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
			Stream   bool `json:"stream"`
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
		call := canRead && !prior
		delta := map[string]any{"content": "OFFLINE_GATEWAY_OK"}
		finish := "stop"
		if call {
			args, _ := json.Marshal(map[string]any{"path": filepath.Join(work, "proof.txt")})
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "offline-read-1", "type": "function", "function": map[string]any{"name": "read_file", "arguments": string(args)}}}}
			finish = "tool_calls"
		}
		if canClarify && toolMessages == 1 {
			delta = map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "offline-clarify-1", "type": "function", "function": map[string]any{"name": "clarify", "arguments": `{"question":"Which color for the fixture?","choices":["Blue","Green"]}`}}}}
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
	d.EnqueueInput(agent.Input{Text: "Read proof.txt then reply OFFLINE_GATEWAY_OK.", Source: "chat", ClientID: "offline-first"})
	wait(1)
	d.EnqueueInput(agent.Input{Text: "Reply OFFLINE_GATEWAY_OK for this second turn.", Source: "chat", ClientID: "offline-second"})
	wait(2)
	if questionCount.Load() != 1 || !sawClarification.Load() {
		t.Error("native clarification answer not round-tripped")
	}
	if !sawGuide.Load() {
		t.Error("trusted Blerg guide did not reach native system prompt")
	}
	if !toolResult.Load() || countKind(em, "tool_call") == 0 || countKind(em, "tool_result") == 0 {
		t.Error("actual native file tool was not round-tripped")
	}
	if countKind(em, "user_message") != 2 {
		t.Error("accepted input lost or duplicated")
	}
	n := 0
	for _, e := range em.snapshot() {
		t.Logf("observed kind=%s", e.Kind)
		if e.Kind == "user_message" {
			t.Logf("accepted payload=%+v", e.Payload)
		}
		if e.Kind == "error" {
			t.Logf("classified error=%+v", e.Payload)
		}
		if e.Kind == "turn_done" {
			p := e.Payload.(agent.TurnDonePayload)
			n++
			wantIn, wantOut := 60, 24
			if n == 2 {
				wantIn, wantOut = 20, 8
			}
			if p.StopReason != "end_turn" || p.Usage.InputTokens != wantIn || p.Usage.OutputTokens != wantOut {
				t.Errorf("turn %d: reason=%s usage=%+v", n, p.StopReason, p.Usage)
			}
		}
		if e.Kind == "error" {
			t.Error("native adapter emitted an error")
		}
		if e.Kind == "assistant_text" {
			p := e.Payload.(agent.AssistantTextPayload)
			if p.Done && !strings.Contains(p.Text, "OFFLINE_GATEWAY_OK") {
				t.Error("unexpected final reply")
			}
		}
	}
	if n != 2 {
		t.Errorf("completion count=%d", n)
	}
	d.mu.Lock()
	saved := d.nativeID
	d.mu.Unlock()
	if saved == "" {
		t.Fatal("missing exact durable native identity")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("first gateway did not stop")
	}
	em2 := &collectEmitter{}
	d2 := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: work, SessionID: "offline-blerg", Emitter: em2, Env: d.env})
	d2.nativeID = saved
	d2.DeveloperInstructions = "OFFLINE_BLERG_GUIDE_SENTINEL"
	ctx2, cancel2 := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel2()
	done2 := make(chan struct{})
	go func() { defer close(done2); d2.Run(ctx2) }()
	defer func() {
		cancel2()
		select {
		case <-done2:
		case <-time.After(15 * time.Second):
			t.Error("resumed gateway did not stop")
		}
	}()
	d2.EnqueueInput(agent.Input{Text: "Reply OFFLINE_GATEWAY_OK for the third turn after resume.", Source: "chat", ClientID: "offline-third"})
	deadline := time.Now().Add(90 * time.Second)
	for countKind(em2, "turn_done") == 0 && countKind(em2, "error") == 0 && time.Now().Before(deadline) {
		select {
		case <-done2:
			t.Fatal("exact resume failed")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if countKind(em2, "turn_done") != 1 || countKind(em2, "user_message") != 1 {
		t.Error("resumed turn input/completion lost or duplicated")
	}
	if !sawRecoveredHistory.Load() {
		t.Error("exact durable resume lost prior native conversation")
	}
	for _, e := range em2.snapshot() {
		if e.Kind == "error" {
			t.Errorf("resumed adapter error=%+v", e.Payload)
		}
	}

}
