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
	"sync"
	"testing"
	"time"
)

// No owner configuration, keys, real model, GPU or network service is used.
func TestHermesRealNamedProviderRouting(t *testing.T) {
	source, python := os.Getenv("BLERG_TEST_HERMES_NATIVE_ROOT"), os.Getenv("BLERG_TEST_HERMES_NATIVE_PYTHON")
	if source == "" && python == "" {
		t.Skip("set native source and Python for optional gateway proof")
	}
	if source == "" || python == "" {
		t.Fatal("both native integration settings required")
	}
	var mu sync.Mutex
	hits := map[string]int{}
	histories := map[string]bool{}
	backend := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == "GET" {
				fmt.Fprint(w, `{"data":[{"id":"same-model"}]}`)
				return
			}
			var q struct {
				Stream   bool   `json:"stream"`
				Model    string `json:"model"`
				Messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&q) != nil {
				http.Error(w, "invalid", 400)
				return
			}
			var text string
			for _, m := range q.Messages {
				if m.Role == "user" {
					text += string(m.Content)
				}
			}
			mu.Lock()
			hits[name]++
			if strings.Contains(text, "routing-first") && strings.Contains(text, "routing-third") {
				histories[name] = true
			}
			mu.Unlock()
			if q.Model != "same-model" {
				t.Error("native provider received qualified model instead of raw model")
			}
			w.Header().Set("Content-Type", "application/json")
			if !q.Stream {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "model": "same-model", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "ROUTE_OK"}, "finish_reason": "stop"}}, "usage": map[string]int{"prompt_tokens": 20, "completion_tokens": 8}})
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			for _, delta := range []map[string]any{{"content": "ROUTE_OK"}, {}} {
				chunk := map[string]any{"id": "fixture", "model": "same-model", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}}
				if len(delta) == 0 {
					chunk["choices"] = []any{map[string]any{"index": 0, "delta": delta, "finish_reason": "stop"}}
					chunk["usage"] = map[string]int{"prompt_tokens": 20, "completion_tokens": 8}
				}
				b, _ := json.Marshal(chunk)
				fmt.Fprintf(w, "data: %s\n\n", b)
				w.(http.Flusher).Flush()
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
		}))
	}
	a, b := backend("a"), backend("b")
	defer a.Close()
	defer b.Close()
	home := t.TempDir()
	profile := filepath.Join(home, ".hermes")
	work := filepath.Join(home, "work")
	if os.MkdirAll(work, 0700) != nil {
		t.Fatal("workspace")
	}
	cfg := fmt.Sprintf("model:\n  provider: custom\n  default: same-model\n  base_url: %s/v1\n  context_length: 65536\nproviders:\n  local-a:\n    base_url: %s/v1\n    api_mode: chat_completions\n    context_length: 65536\n  local-b:\n    base_url: %s/v1\n    api_mode: chat_completions\n    context_length: 65536\nterminal:\n  backend: local\n", a.URL, a.URL, b.URL)
	mustWrite(t, filepath.Join(profile, "config.yaml"), cfg)
	env := []string{"HOME=" + home, "HERMES_HOME=" + profile, "PATH=/usr/local/bin:/usr/bin:/bin", "TERM=dumb", "NO_COLOR=1", "HERMES_PYTHON_SRC_ROOT=" + source, "HERMES_PYTHON=" + python}
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], Model: "custom:local-b/same-model", WorkDir: work, Emitter: em, Env: env})
	var persisted EngineSessionState
	if err := d.SetEngineSession(nil, func(s EngineSessionState) { mu.Lock(); persisted = s; mu.Unlock() }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	wait := func(em *collectEmitter, n int) {
		t.Helper()
		timer := time.NewTimer(60 * time.Second)
		defer timer.Stop()
		for countKind(em, "turn_done") < n {
			select {
			case <-timer.C:
				for _, e := range em.snapshot() {
					if e.Kind == "error" {
						t.Logf("classified error: %+v", e.Payload)
					}
				}
				t.Fatal("native routing completion timeout")
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	d.EnqueueInput(agent.Input{Text: "routing-first reply ROUTE_OK", ClientID: "route-first"})
	wait(em, 1)
	mu.Lock()
	firstA, firstB := hits["a"], hits["b"]
	mu.Unlock()
	if firstA != 0 || firstB == 0 {
		t.Fatalf("wrong initial provider a=%d b=%d", firstA, firstB)
	}
	d.SetModel("custom:local-a/same-model", "", "")
	d.EnqueueInput(agent.Input{Text: "routing-second reply ROUTE_OK", ClientID: "route-second"})
	wait(em, 2)
	d.SetModel("custom:local-b/same-model", "", "")
	d.EnqueueInput(agent.Input{Text: "routing-third reply ROUTE_OK", ClientID: "route-third"})
	wait(em, 3)
	expected := []string{"custom:local-b/same-model", "custom:local-a/same-model", "custom:local-b/same-model"}
	index := 0
	for _, event := range em.snapshot() {
		if event.Kind == "turn_done" {
			turn := event.Payload.(agent.TurnDonePayload)
			if index >= len(expected) || turn.Model != expected[index] {
				t.Fatal("native turn lost provider-qualified usage attribution")
			}
			if _, ok := turn.UsageByModel[turn.Model]; !ok {
				t.Fatal("usage model partition lost")
			}
			index++
		}
	}
	if index != 3 {
		t.Fatal("missing native turn attribution")
	}
	d.mu.Lock()
	native := d.nativeID
	selected := d.model
	d.mu.Unlock()
	if selected != "custom:local-b/same-model" {
		t.Fatal("native acknowledgment erased provider binding")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("native stop timeout")
	}
	em2 := &collectEmitter{}
	d2 := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], Model: selected, WorkDir: work, Emitter: em2, Env: env})
	mu.Lock()
	saved := persisted
	mu.Unlock()
	if saved.ID != native || saved.RouteFingerprint == "" {
		t.Fatal("private native binding was not persisted")
	}
	raw, _ := json.Marshal(saved)
	var restored EngineSessionState
	if json.Unmarshal(raw, &restored) != nil {
		t.Fatal("private binding roundtrip")
	}
	if err := d2.SetEngineSession(&restored, nil); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel2()
	done2 := make(chan struct{})
	go func() { defer close(done2); d2.Run(ctx2) }()
	defer func() { cancel2(); <-done2 }()
	mu.Lock()
	beforeA, beforeB := hits["a"], hits["b"]
	mu.Unlock()
	d2.EnqueueInput(agent.Input{Text: "routing-fourth reply ROUTE_OK", ClientID: "route-fourth"})
	wait(em2, 1)
	finalConfig, err := os.ReadFile(filepath.Join(profile, "config.yaml"))
	if err != nil || string(finalConfig) != cfg {
		t.Fatal("native session-only model routing modified profile configuration")
	}
	mu.Lock()
	finalA, finalB, history := hits["a"], hits["b"], histories["b"]
	mu.Unlock()
	if finalA != beforeA || finalB <= beforeB || !history {
		t.Fatalf("resume provider/history mismatch: aDelta=%d bDelta=%d history=%v", finalA-beforeA, finalB-beforeB, history)
	}
	for _, ev := range em2.snapshot() {
		if ev.Kind == "model_changed" && ev.Payload.(agent.ModelChangedPayload).Model != "custom:local-b/same-model" {
			t.Fatal("resume emitted unqualified model")
		}
	}
	cancel2()
	select {
	case <-done2:
	case <-time.After(15 * time.Second):
		t.Fatal("second native stop timeout")
	}
	em3 := &collectEmitter{}
	d3 := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], Model: "same-model", WorkDir: work, Emitter: em3, Env: env})
	ctx3, cancel3 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel3()
	done3 := make(chan struct{})
	go func() { defer close(done3); d3.Run(ctx3) }()
	defer func() { cancel3(); <-done3 }()
	d3.EnqueueInput(agent.Input{Text: "routing-raw-default reply ROUTE_OK", ClientID: "route-raw"})
	wait(em3, 1)
	mu.Lock()
	rawA, rawB := hits["a"], hits["b"]
	mu.Unlock()
	if rawA <= finalA || rawB != finalB {
		t.Fatal("raw legacy selection did not stay on default named endpoint")
	}
	for _, ev := range em3.snapshot() {
		if ev.Kind == "model_changed" && ev.Payload.(agent.ModelChangedPayload).Model != "same-model" {
			t.Fatal("raw default selector lost backward compatibility")
		}
	}
	finalConfig, err = os.ReadFile(filepath.Join(profile, "config.yaml"))
	if err != nil || string(finalConfig) != cfg {
		t.Fatal("raw route changed profile defaults")
	}

}
