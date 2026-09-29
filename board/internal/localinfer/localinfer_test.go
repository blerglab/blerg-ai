package localinfer_test

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

	"github.com/blerglab/blerg-ai/board/internal/localinfer"
)

// capture records what the fake endpoint was asked for.
type capture struct {
	path string
	body map[string]any
}

// fakeServer answers both endpoints with whatever handler is given and
// records the request.
func fakeServer(t *testing.T, got *capture, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.path = r.URL.Path
		got.body = map[string]any{}
		_ = json.Unmarshal(raw, &got.body)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func jsonHandler(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

const chatOK = `{"model":"served-model","choices":[{"message":{"role":"assistant","content":"hello"},
  "finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}`

func TestChatPostsToChatEndpoint(t *testing.T) {
	var got capture
	srv := fakeServer(t, &got, jsonHandler(200, chatOK))
	c := localinfer.New(localinfer.Config{BaseURL: srv.URL + "/", ChatModel: "qwen"})

	resp, err := c.Chat(context.Background(), localinfer.ChatRequest{
		Messages:    []localinfer.Message{{Role: "user", Content: "hi"}},
		Temperature: localinfer.Temperature(0),
		MaxTokens:   512,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions (trailing slash on BaseURL must not double up)", got.path)
	}
	if got.body["model"] != "qwen" {
		t.Errorf("model = %v, want the configured chat model", got.body["model"])
	}
	if got.body["temperature"] != float64(0) {
		t.Errorf("temperature = %v, want 0 sent explicitly", got.body["temperature"])
	}
	if got.body["max_tokens"] != float64(512) {
		t.Errorf("max_tokens = %v, want 512", got.body["max_tokens"])
	}
	if resp.Text != "hello" || resp.Model != "served-model" || resp.FinishReason != "stop" {
		t.Errorf("response flattened wrong: %+v", resp)
	}
	if resp.Usage.TotalTokens != 9 {
		t.Errorf("usage not carried through: %+v", resp.Usage)
	}
}

func TestChatOmitsUnsetOptions(t *testing.T) {
	var got capture
	srv := fakeServer(t, &got, jsonHandler(200, chatOK))
	c := localinfer.New(localinfer.Config{BaseURL: srv.URL, ChatModel: "qwen"})

	if _, err := c.Chat(context.Background(), localinfer.ChatRequest{
		Model:    "other-model",
		Messages: []localinfer.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got.body["model"] != "other-model" {
		t.Errorf("per-request model must override the configured one, got %v", got.body["model"])
	}
	if _, ok := got.body["temperature"]; ok {
		t.Error("nil temperature must be omitted so the server default applies")
	}
	if _, ok := got.body["max_tokens"]; ok {
		t.Error("zero max_tokens must be omitted")
	}
}

func TestChatUnconfiguredNeverDials(t *testing.T) {
	c := localinfer.New(localinfer.Config{})
	if c.Configured() {
		t.Fatal("no BaseURL must report unconfigured")
	}
	_, err := c.Chat(context.Background(), localinfer.ChatRequest{
		Messages: []localinfer.Message{{Role: "user", Content: "hi"}},
	})
	if !errors.Is(err, localinfer.ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
	if localinfer.Unavailable(err) {
		t.Error("unconfigured is not unavailable: nothing is down, nothing is wired")
	}
}

func TestChatUnreachableIsTypedUnavailable(t *testing.T) {
	// A server that is closed before the call: connection refused.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := localinfer.New(localinfer.Config{BaseURL: url, ChatModel: "m", Timeout: 2 * time.Second})
	_, err := c.Chat(context.Background(), localinfer.ChatRequest{
		Messages: []localinfer.Message{{Role: "user", Content: "hi"}},
	})
	if !localinfer.Unavailable(err) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	var e *localinfer.Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v (%T), want *localinfer.Error", err, err)
	}
	if e.Op != localinfer.OpChat {
		t.Errorf("Op = %q, want %q", e.Op, localinfer.OpChat)
	}
	if e.StatusCode != 0 {
		t.Errorf("StatusCode = %d, want 0 when no response arrived", e.StatusCode)
	}
}

func TestChatTimesOutRatherThanHanging(t *testing.T) {
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-done // never answers
	}))
	t.Cleanup(func() { close(done); srv.Close() })

	// No deadline on the caller's context: the client's own timeout is what
	// has to bound this.
	c := localinfer.New(localinfer.Config{BaseURL: srv.URL, ChatModel: "m", Timeout: 150 * time.Millisecond})
	start := time.Now()
	_, err := c.Chat(context.Background(), localinfer.ChatRequest{
		Messages: []localinfer.Message{{Role: "user", Content: "hi"}},
	})
	if !localinfer.Unavailable(err) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the cause to stay matchable as DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("call took %s — the client timeout did not bound it", elapsed)
	}
}

func TestChatStatusClassification(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{http.StatusInternalServerError, localinfer.ErrUnavailable},
		{http.StatusBadGateway, localinfer.ErrUnavailable},
		{http.StatusTooManyRequests, localinfer.ErrUnavailable},
		{http.StatusBadRequest, localinfer.ErrBadRequest},
		{http.StatusNotFound, localinfer.ErrBadRequest},
	}
	for _, tc := range cases {
		var got capture
		srv := fakeServer(t, &got, jsonHandler(tc.status, `{"error":"nope"}`))
		c := localinfer.New(localinfer.Config{BaseURL: srv.URL, ChatModel: "m"})
		_, err := c.Chat(context.Background(), localinfer.ChatRequest{
			Messages: []localinfer.Message{{Role: "user", Content: "hi"}},
		})
		if !errors.Is(err, tc.want) {
			t.Errorf("HTTP %d: err = %v, want %v", tc.status, err, tc.want)
		}
		var e *localinfer.Error
		if errors.As(err, &e) && e.StatusCode != tc.status {
			t.Errorf("HTTP %d: Error.StatusCode = %d", tc.status, e.StatusCode)
		}
		if !strings.Contains(err.Error(), "nope") {
			t.Errorf("HTTP %d: server body should reach the message, got %q", tc.status, err)
		}
	}
}

func TestChatUnusableBodyIsBadResponse(t *testing.T) {
	for _, body := range []string{`not json at all`, `{"choices":[]}`} {
		var got capture
		srv := fakeServer(t, &got, jsonHandler(200, body))
		c := localinfer.New(localinfer.Config{BaseURL: srv.URL, ChatModel: "m"})
		_, err := c.Chat(context.Background(), localinfer.ChatRequest{
			Messages: []localinfer.Message{{Role: "user", Content: "hi"}},
		})
		if !errors.Is(err, localinfer.ErrBadResponse) {
			t.Errorf("body %q: err = %v, want ErrBadResponse", body, err)
		}
		if localinfer.Unavailable(err) {
			t.Errorf("body %q: a 200 with a bad body is not 'the box is down'", body)
		}
	}
}

// ── Embeddings ───────────────────────────────────────────────────────────────

func TestEmbedUsesEmbeddingEndpointAndModel(t *testing.T) {
	var got capture
	srv := fakeServer(t, &got, jsonHandler(200,
		`{"model":"bge-served","data":[{"index":1,"embedding":[0.5,0.6]},{"index":0,"embedding":[0.1,0.2]}],
		  "usage":{"prompt_tokens":4,"total_tokens":4}}`))
	c := localinfer.New(localinfer.Config{BaseURL: srv.URL, ChatModel: "qwen", EmbedModel: "bge"})

	resp, err := c.Embed(context.Background(), localinfer.EmbedRequest{Input: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.path != "/v1/embeddings" {
		t.Errorf("path = %q, want /v1/embeddings", got.path)
	}
	if got.body["model"] != "bge" {
		t.Errorf("model = %v, want the embedding model — the chat model must not serve embeddings", got.body["model"])
	}
	// Out-of-order rows are placed by their index, not arrival.
	if len(resp.Vectors) != 2 || resp.Vectors[0][0] != 0.1 || resp.Vectors[1][0] != 0.5 {
		t.Errorf("vectors not in input order: %+v", resp.Vectors)
	}
	if resp.Model != "bge-served" {
		t.Errorf("Model = %q, want the served model name", resp.Model)
	}
}

func TestEmbedRefusesWithoutAnEmbeddingModel(t *testing.T) {
	var got capture
	srv := fakeServer(t, &got, jsonHandler(200, `{"data":[{"index":0,"embedding":[1]}]}`))
	c := localinfer.New(localinfer.Config{BaseURL: srv.URL, ChatModel: "qwen"})

	_, err := c.Embed(context.Background(), localinfer.EmbedRequest{Input: []string{"a"}})
	if !errors.Is(err, localinfer.ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
	if !strings.Contains(err.Error(), "BLERG_BOARD_INFER_EMBED_MODEL") {
		t.Errorf("error should name the var to set, got %q", err)
	}
	if got.path != "" {
		t.Error("no embedding model must fail before any request is sent")
	}
	// ...but a per-request model is enough on its own.
	if _, err := c.Embed(context.Background(), localinfer.EmbedRequest{
		Model: "bge", Input: []string{"a"},
	}); err != nil {
		t.Fatalf("per-request embed model should work: %v", err)
	}
}

func TestEmbedRejectsShortOrEmptyResults(t *testing.T) {
	cases := map[string]string{
		"missing a vector": `{"data":[{"index":0,"embedding":[1,2]}]}`,
		"empty vector":     `{"data":[{"index":0,"embedding":[]},{"index":1,"embedding":[]}]}`,
		// Two rows claiming the same index: the row count matches, but one
		// input ends up unembedded. A nil vector must never reach a caller.
		"duplicate index":   `{"data":[{"index":1,"embedding":[0.1]},{"index":1,"embedding":[0.2]}]}`,
		"index off the end": `{"data":[{"index":9,"embedding":[0.1]},{"index":0,"embedding":[0.2]}]}`,
		"negative index":    `{"data":[{"index":-1,"embedding":[0.1]},{"index":0,"embedding":[0.2]}]}`,
	}
	for name, body := range cases {
		var got capture
		srv := fakeServer(t, &got, jsonHandler(200, body))
		c := localinfer.New(localinfer.Config{BaseURL: srv.URL, EmbedModel: "bge"})
		resp, err := c.Embed(context.Background(), localinfer.EmbedRequest{Input: []string{"a", "b"}})
		if !errors.Is(err, localinfer.ErrBadResponse) {
			t.Errorf("%s: err = %v, want ErrBadResponse", name, err)
		}
		for i, v := range resp.Vectors {
			if v == nil {
				t.Errorf("%s: vector %d is nil — a hole must be an error, not a value", name, i)
			}
		}
	}
}

func TestEmbedFallsBackToArrivalOrderWhenIndexIsAbsent(t *testing.T) {
	var got capture
	// Servers that don't implement the index field omit it entirely — that
	// is arrival order, not "everything at slot 0".
	srv := fakeServer(t, &got, jsonHandler(200,
		`{"data":[{"embedding":[0.1]},{"embedding":[0.2]},{"embedding":[0.3]}]}`))
	c := localinfer.New(localinfer.Config{BaseURL: srv.URL, EmbedModel: "bge"})

	resp, err := c.Embed(context.Background(), localinfer.EmbedRequest{Input: []string{"a", "b", "c"}})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []float32{0.1, 0.2, 0.3} {
		if resp.Vectors[i][0] != want {
			t.Errorf("vector %d = %v, want %v — arrival order", i, resp.Vectors[i], want)
		}
	}
}

func TestNewAppliesTheModelDefault(t *testing.T) {
	var got capture
	srv := fakeServer(t, &got, jsonHandler(200, chatOK))
	// A Config built by hand, not from the environment: the non-gate caller
	// this package exists for. It must not post model:"".
	c := localinfer.New(localinfer.Config{BaseURL: srv.URL})

	if c.ChatModel() != localinfer.DefaultChatModel {
		t.Errorf("ChatModel = %q, want %q", c.ChatModel(), localinfer.DefaultChatModel)
	}
	if _, err := c.Chat(context.Background(), localinfer.ChatRequest{
		Messages: []localinfer.Message{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got.body["model"] != localinfer.DefaultChatModel {
		t.Errorf("model on the wire = %q, want %q", got.body["model"], localinfer.DefaultChatModel)
	}
	if c.EmbedModel() != "" {
		t.Errorf("EmbedModel = %q, want empty — no model serves both", c.EmbedModel())
	}
}

func TestEmbedRejectsEmptyInput(t *testing.T) {
	c := localinfer.New(localinfer.Config{BaseURL: "http://example.invalid", EmbedModel: "bge"})
	if _, err := c.Embed(context.Background(), localinfer.EmbedRequest{}); !errors.Is(err, localinfer.ErrBadRequest) {
		t.Fatalf("err = %v, want ErrBadRequest", err)
	}
}

// ── Config ───────────────────────────────────────────────────────────────────

func TestConfigFromEnvAliasPrecedence(t *testing.T) {
	all := []string{
		"BLERG_BOARD_INFER_URL", "GATE_OPENAI_URL",
		"BLERG_BOARD_INFER_MODEL", "GATE_OPENAI_MODEL",
		"BLERG_BOARD_INFER_EMBED_MODEL", "BLERG_BOARD_INFER_TIMEOUT",
	}
	clearEnv := func(t *testing.T) {
		for _, k := range all {
			t.Setenv(k, "")
		}
	}

	t.Run("legacy GATE_OPENAI names still work", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("GATE_OPENAI_URL", "http://gate:8000")
		t.Setenv("GATE_OPENAI_MODEL", "gate-model")
		cfg := localinfer.ConfigFromEnv()
		if cfg.BaseURL != "http://gate:8000" || cfg.ChatModel != "gate-model" {
			t.Fatalf("GATE_OPENAI_* must keep working, got %+v", cfg)
		}
	})

	t.Run("canonical beats the alias", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("GATE_OPENAI_URL", "http://gate:8000")
		t.Setenv("BLERG_BOARD_INFER_URL", "http://infer:8000")
		t.Setenv("GATE_OPENAI_MODEL", "gate-model")
		t.Setenv("BLERG_BOARD_INFER_MODEL", "infer-model")
		cfg := localinfer.ConfigFromEnv()
		if cfg.BaseURL != "http://infer:8000" || cfg.ChatModel != "infer-model" {
			t.Fatalf("BLERG_BOARD_INFER_* is canonical, got %+v", cfg)
		}
	})

	t.Run("defaults", func(t *testing.T) {
		clearEnv(t)
		cfg := localinfer.ConfigFromEnv()
		if cfg.BaseURL != "" {
			t.Errorf("BaseURL = %q, want empty when nothing is set", cfg.BaseURL)
		}
		if cfg.ChatModel != localinfer.DefaultChatModel {
			t.Errorf("ChatModel = %q, want %q", cfg.ChatModel, localinfer.DefaultChatModel)
		}
		if cfg.EmbedModel != "" {
			t.Errorf("EmbedModel = %q, want empty — no model serves both by default", cfg.EmbedModel)
		}
		if localinfer.New(cfg).Timeout() != localinfer.DefaultTimeout {
			t.Error("unset timeout must fall back to DefaultTimeout")
		}
	})

	t.Run("timeout parsed, malformed ignored", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("BLERG_BOARD_INFER_TIMEOUT", "5s")
		if got := localinfer.ConfigFromEnv().Timeout; got != 5*time.Second {
			t.Errorf("Timeout = %s, want 5s", got)
		}
		t.Setenv("BLERG_BOARD_INFER_TIMEOUT", "not-a-duration")
		if got := localinfer.New(localinfer.ConfigFromEnv()).Timeout(); got != localinfer.DefaultTimeout {
			t.Errorf("Timeout = %s, want the default — a malformed value must not break boot", got)
		}
	})
}
