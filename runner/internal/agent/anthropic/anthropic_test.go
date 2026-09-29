package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

const sseTranscript = `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":100,"cache_read_input_tokens":50,"cache_creation_input_tokens":10}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"pondering"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig123"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello "}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"tu_1","name":"bash","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"comm"}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"and\":\"ls\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":25}}

event: message_stop
data: {"type":"message_stop"}

`

func TestStreamParsesSSE(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		if r.Header.Get("x-api-key") != "key123" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("missing headers: %+v", r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseTranscript)
	}))
	defer srv.Close()

	p := New("key123", srv.URL)
	ch, err := p.Stream(context.Background(), agent.Request{
		Model:  "claude-sonnet-5",
		Effort: "high",
		System: "sys prompt",
		Messages: []agent.Message{
			{Role: "user", Blocks: []agent.Block{{Type: "text", Text: "hi"}}},
			{Role: "assistant", Blocks: []agent.Block{
				{Type: "thinking", Raw: json.RawMessage(`{"type":"thinking","thinking":"t","signature":"s"}`)},
				{Type: "tool_use", ID: "prev", Name: "bash", Input: json.RawMessage(`{"command":"pwd"}`)},
			}},
			{Role: "user", Blocks: []agent.Block{{Type: "tool_result", ToolUseID: "prev", Content: "/x", IsError: false}}},
		},
		Tools: []agent.ToolDef{{Name: "bash", Description: "run", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var deltas []string
	var blocks []agent.Block
	var stop string
	var usage *agent.Usage
	for ev := range ch {
		switch ev.Kind {
		case "text_delta":
			deltas = append(deltas, ev.TextDelta)
		case "block":
			blocks = append(blocks, *ev.Block)
		case "done":
			stop, usage = ev.StopReason, ev.Usage
		case "error":
			t.Fatalf("stream error: %v", ev.Err)
		}
	}
	if strings.Join(deltas, "") != "Hello world" {
		t.Fatalf("deltas = %q", deltas)
	}
	if len(blocks) != 3 {
		t.Fatalf("blocks = %+v", blocks)
	}
	if blocks[0].Type != "thinking" || !strings.Contains(string(blocks[0].Raw), "sig123") {
		t.Fatalf("thinking block = %+v", blocks[0])
	}
	if blocks[1].Type != "text" || blocks[1].Text != "Hello world" {
		t.Fatalf("text block = %+v", blocks[1])
	}
	if blocks[2].Type != "tool_use" || blocks[2].ID != "tu_1" || string(blocks[2].Input) != `{"command":"ls"}` {
		t.Fatalf("tool_use block = %+v", blocks[2])
	}
	if stop != "tool_use" || usage == nil || usage.InputTokens != 100 || usage.OutputTokens != 25 || usage.CacheReadTokens != 50 || usage.CacheWriteTokens != 10 {
		t.Fatalf("stop=%q usage=%+v", stop, usage)
	}

	// Request shape assertions.
	var req map[string]any
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatal(err)
	}
	if req["stream"] != true || req["model"] != "claude-sonnet-5" {
		t.Fatalf("req = %v", req)
	}
	body := string(gotBody)
	if !strings.Contains(body, `"cache_control"`) {
		t.Fatal("no cache_control breakpoints in request")
	}
	if !strings.Contains(body, `"thinking":{"type":"adaptive"}`) {
		t.Fatal("adaptive thinking not set")
	}
	if strings.Contains(body, "budget_tokens") {
		t.Fatal("budget_tokens is rejected by current models and must not be sent")
	}
	if !strings.Contains(body, `"output_config":{"effort":"high"}`) {
		t.Fatal("effort did not map to output_config.effort")
	}
	// thinking block replayed verbatim
	if !strings.Contains(body, `"signature":"s"`) {
		t.Fatal("thinking block not passed through raw")
	}
	// tool_result pairing
	if !strings.Contains(body, `"tool_use_id":"prev"`) {
		t.Fatal("tool_result not mapped")
	}
}

func TestRateLimitBecomesRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("retry-after", "7")
		w.WriteHeader(429)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	}))
	defer srv.Close()
	p := New("k", srv.URL)
	_, err := p.Stream(context.Background(), agent.Request{Model: "m"})
	var re *agent.RetryableError
	if !errors.As(err, &re) {
		t.Fatalf("want RetryableError, got %v", err)
	}
	if re.RetryAfter != 7*time.Second {
		t.Fatalf("RetryAfter = %v", re.RetryAfter)
	}
}

func TestAuthErrorNotRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"bad key"}}`)
	}))
	defer srv.Close()
	p := New("k", srv.URL)
	_, err := p.Stream(context.Background(), agent.Request{Model: "m"})
	if err == nil {
		t.Fatal("want error")
	}
	var re *agent.RetryableError
	if errors.As(err, &re) {
		t.Fatal("401 must not be retryable")
	}
	if !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("err = %v", err)
	}
}
