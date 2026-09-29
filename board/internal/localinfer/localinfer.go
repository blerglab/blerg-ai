// Package localinfer is blerg-board's client for an OpenAI-compatible inference
// endpoint — e.g. a local GPU box running vLLM, but any server speaking
// /v1/chat/completions and /v1/embeddings works.
//
// It is deliberately a transport and not a policy. Every failure comes back
// as a typed *Error so each caller decides for itself what a down box means:
// the admission gate falls open or holds (board field gate_on_unavailable),
// a summary generator should silently skip. Baking either choice in here
// would force it on the other.
//
// Chat and embeddings are separate calls with separate models: in practice
// the box serves a chat model and an embedding model, and neither can answer
// for the other.
package localinfer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Defaults applied when the corresponding config field is empty.
const (
	// DefaultChatModel is what the gate has always sent when no model was
	// named: many single-model servers ignore the field entirely.
	DefaultChatModel = "default"
	// DefaultTimeout bounds a call that the caller's context does not.
	// Without it an unreachable-but-accepting endpoint hangs forever.
	DefaultTimeout = 30 * time.Second
	// maxBody bounds how much of a response we read before giving up.
	maxBody = 8 << 20
)

// Config is the endpoint, read once at startup and shared by every caller.
type Config struct {
	// BaseURL is the server root, e.g. http://localhost:8000 — no /v1
	// suffix. Empty means no local inference is configured and every call
	// fails fast with ErrNotConfigured.
	BaseURL string
	// ChatModel is the model name sent to /v1/chat/completions.
	ChatModel string
	// EmbedModel is the model name sent to /v1/embeddings. It is separate
	// from ChatModel on purpose — one served model rarely does both.
	EmbedModel string
	// APIKey, when set, is sent as "Authorization: Bearer <key>" on every
	// call. The operator's local endpoint (BLERG_BOARD_INFER_URL) has none; a
	// person's own endpoint (a Hermes credential's OPENAI_API_KEY) may. It is
	// a credential: nothing in this package logs it or puts it in an error.
	APIKey string
	// Timeout bounds a single call. Zero uses DefaultTimeout.
	Timeout time.Duration
	// HTTPClient overrides the client the package builds (tests, custom
	// transports). Timeout still applies on top of it, so the effective
	// bound is the shorter of the two.
	HTTPClient *http.Client
}

// ConfigFromEnv reads the endpoint config from the environment.
//
// BLERG_BOARD_INFER_URL / BLERG_BOARD_INFER_MODEL are canonical; GATE_OPENAI_*
// is read as a fallback alias — see docs/CONFIG.md. A malformed
// BLERG_BOARD_INFER_TIMEOUT is ignored in favour of DefaultTimeout rather than
// failing boot.
func ConfigFromEnv() Config {
	cfg := Config{
		BaseURL:    firstEnv("BLERG_BOARD_INFER_URL", "GATE_OPENAI_URL"),
		ChatModel:  firstEnv("BLERG_BOARD_INFER_MODEL", "GATE_OPENAI_MODEL"),
		EmbedModel: firstEnv("BLERG_BOARD_INFER_EMBED_MODEL"),
	}
	if cfg.ChatModel == "" {
		cfg.ChatModel = DefaultChatModel
	}
	if d, err := time.ParseDuration(firstEnv("BLERG_BOARD_INFER_TIMEOUT")); err == nil && d > 0 {
		cfg.Timeout = d
	}
	return cfg
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// Client talks to one OpenAI-compatible endpoint. It is safe for concurrent
// use and cheap to share — build one per process.
type Client struct {
	cfg  Config
	http *http.Client
}

// New normalises the config and applies the defaults, so a Config built by
// hand behaves exactly like one from ConfigFromEnv. A client with no BaseURL
// is still usable: every call returns ErrNotConfigured without touching the
// network, so callers can wire it unconditionally and branch on Configured.
func New(cfg Config) *Client {
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.ChatModel == "" {
		cfg.ChatModel = DefaultChatModel
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	// EmbedModel has no default on purpose — no model serves both.
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: cfg.Timeout}
	}
	return &Client{cfg: cfg, http: hc}
}

// NewFromEnv is New(ConfigFromEnv()).
func NewFromEnv() *Client { return New(ConfigFromEnv()) }

// Configured reports whether an endpoint URL is set. False means every call
// will fail with ErrNotConfigured — callers use it to skip wiring a feature
// rather than to discover the failure per request.
func (c *Client) Configured() bool { return c.cfg.BaseURL != "" }

// BaseURL is the configured server root ("" when unconfigured).
func (c *Client) BaseURL() string { return c.cfg.BaseURL }

// ChatModel is the default model for Chat calls.
func (c *Client) ChatModel() string { return c.cfg.ChatModel }

// EmbedModel is the default model for Embed calls ("" when none is set).
func (c *Client) EmbedModel() string { return c.cfg.EmbedModel }

// Timeout is the per-call bound the client applies on top of the caller's
// context.
func (c *Client) Timeout() time.Duration { return c.cfg.Timeout }

// ── Chat ─────────────────────────────────────────────────────────────────────

// Message is one chat turn. Role is "system", "user" or "assistant".
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest is one /v1/chat/completions call.
type ChatRequest struct {
	// Model overrides the client's ChatModel for this call.
	Model    string
	Messages []Message
	// Temperature is omitted from the request when nil (server default).
	// Use localinfer.Temperature(0) for a deterministic classifier call.
	Temperature *float64
	// MaxTokens is omitted when 0.
	MaxTokens int
	// Stop sequences, omitted when empty.
	Stop []string
}

// Temperature boxes a temperature for ChatRequest.
func Temperature(v float64) *float64 { return &v }

// ChatResponse is the first choice of a completion, flattened.
type ChatResponse struct {
	Text         string
	Model        string // as reported by the server
	FinishReason string
	Usage        Usage
}

// Usage is the token accounting a server reports; all zero when it reports
// none.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Chat runs one completion. Errors are always *Error; use errors.Is against
// ErrUnavailable to tell "the box is down" from a request the server refused.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = c.cfg.ChatModel
	}
	body := map[string]any{"model": model, "messages": req.Messages}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if len(req.Stop) > 0 {
		body["stop"] = req.Stop
	}

	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
		Usage Usage `json:"usage"`
	}
	if err := c.post(ctx, OpChat, "/v1/chat/completions", body, &out); err != nil {
		return ChatResponse{}, err
	}
	if len(out.Choices) == 0 {
		return ChatResponse{}, &Error{Op: OpChat, Kind: ErrBadResponse,
			Detail: "response carried no choices"}
	}
	if out.Model == "" {
		out.Model = model
	}
	return ChatResponse{
		Text:         out.Choices[0].Message.Content,
		Model:        out.Model,
		FinishReason: out.Choices[0].FinishReason,
		Usage:        out.Usage,
	}, nil
}

// ── Embeddings ───────────────────────────────────────────────────────────────

// EmbedRequest is one /v1/embeddings call. Input is a batch; the response
// keeps its order.
type EmbedRequest struct {
	// Model overrides the client's EmbedModel for this call.
	Model string
	Input []string
}

// EmbedResponse holds one vector per input, in input order.
type EmbedResponse struct {
	Vectors [][]float32
	Model   string
	Usage   Usage
}

// Embed computes embeddings. It refuses to fall back to the chat model: an
// endpoint with no embedding model configured returns ErrNotConfigured
// naming the var to set, rather than sending a request that would either
// fail obscurely or return meaningless vectors.
func (c *Client) Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error) {
	model := req.Model
	if model == "" {
		model = c.cfg.EmbedModel
	}
	if model == "" && c.Configured() {
		return EmbedResponse{}, &Error{Op: OpEmbed, Kind: ErrNotConfigured,
			Detail: "no embedding model configured (set BLERG_BOARD_INFER_EMBED_MODEL or EmbedRequest.Model)"}
	}
	if len(req.Input) == 0 {
		return EmbedResponse{}, &Error{Op: OpEmbed, Kind: ErrBadRequest,
			Detail: "no input to embed"}
	}

	var out struct {
		Model string `json:"model"`
		Data  []struct {
			// A pointer so an absent index is distinguishable from index 0 —
			// the two want different handling below.
			Index     *int      `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage Usage `json:"usage"`
	}
	if err := c.post(ctx, OpEmbed, "/v1/embeddings",
		map[string]any{"model": model, "input": req.Input}, &out); err != nil {
		return EmbedResponse{}, err
	}
	if len(out.Data) != len(req.Input) {
		return EmbedResponse{}, &Error{Op: OpEmbed, Kind: ErrBadResponse,
			Detail: "server returned a different number of vectors than inputs"}
	}
	// Place each row at its declared index. A server that omits the field is
	// answering in arrival order, which is the spec's order anyway. Anything
	// else — an index outside the batch, or two rows claiming one slot —
	// would leave an input unembedded, and a nil vector reaching a caller is
	// a bogus similarity score or a nil dereference. Refuse the batch.
	vectors := make([][]float32, len(out.Data))
	for i, d := range out.Data {
		at := i
		if d.Index != nil {
			at = *d.Index
		}
		if at < 0 || at >= len(vectors) || vectors[at] != nil {
			return EmbedResponse{}, &Error{Op: OpEmbed, Kind: ErrBadResponse,
				Detail: "server indexed its embeddings outside the batch or reused an index"}
		}
		if len(d.Embedding) == 0 {
			return EmbedResponse{}, &Error{Op: OpEmbed, Kind: ErrBadResponse,
				Detail: "server returned an empty embedding"}
		}
		vectors[at] = d.Embedding
	}
	if out.Model == "" {
		out.Model = model
	}
	return EmbedResponse{Vectors: vectors, Model: out.Model, Usage: out.Usage}, nil
}

// ── Models ───────────────────────────────────────────────────────────────────

// OpModels is the Op on errors from ListModels.
const OpModels = "models"

// ListModels asks the endpoint which models it serves (GET /v1/models) and
// returns their ids in the server's order. A caller with no configured model
// uses it to pick the one a single-model server (vLLM, llama.cpp) is serving —
// those reject a chat call naming any other model.
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	if !c.Configured() {
		return nil, &Error{Op: OpModels, Kind: ErrNotConfigured, Detail: "no endpoint URL"}
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.BaseURL+"/v1/models", nil)
	if err != nil {
		return nil, &Error{Op: OpModels, Kind: ErrBadRequest, Detail: "bad endpoint URL", Err: err}
	}
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Op: OpModels, Kind: ErrUnavailable, Detail: "request failed", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		kind := ErrBadRequest
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			kind = ErrUnavailable
		}
		return nil, &Error{Op: OpModels, StatusCode: resp.StatusCode, Kind: kind, Detail: "server rejected the call"}
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&out); err != nil {
		return nil, &Error{Op: OpModels, StatusCode: resp.StatusCode, Kind: ErrBadResponse,
			Detail: "response was not the expected JSON", Err: err}
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// ── Transport ────────────────────────────────────────────────────────────────

// post is the single round-trip both calls share: JSON in, JSON out, every
// failure classified into an *Error.
func (c *Client) post(ctx context.Context, op, path string, body any, out any) error {
	if !c.Configured() {
		return &Error{Op: op, Kind: ErrNotConfigured,
			Detail: "no endpoint URL (set BLERG_BOARD_INFER_URL)"}
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return &Error{Op: op, Kind: ErrBadRequest, Detail: "request not encodable", Err: err}
	}
	// The client's own Timeout does not bound a caller that passes a longer
	// context, so apply it here too: no call outlives it.
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+path, bytes.NewReader(buf))
	if err != nil {
		return &Error{Op: op, Kind: ErrBadRequest, Detail: "bad endpoint URL", Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		// Dial refused, DNS failure, timeout, caller cancellation — from the
		// caller's side they are all "no answer from the box".
		return &Error{Op: op, Kind: ErrUnavailable, Detail: "request failed", Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return &Error{Op: op, StatusCode: resp.StatusCode, Kind: ErrUnavailable,
			Detail: "response truncated", Err: err}
	}
	if resp.StatusCode != http.StatusOK {
		kind := ErrBadRequest
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			// Overloaded or broken, not a request the caller can fix.
			kind = ErrUnavailable
		}
		return &Error{Op: op, StatusCode: resp.StatusCode, Kind: kind,
			Detail: "server rejected the call", Body: truncate(string(raw), 200)}
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &Error{Op: op, StatusCode: resp.StatusCode, Kind: ErrBadResponse,
			Detail: "response was not the expected JSON", Body: truncate(string(raw), 200), Err: err}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
