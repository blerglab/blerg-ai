// Package anthropic implements agent.Provider against the Anthropic Messages
// API with SSE streaming, prompt-cache breakpoints, thinking passthrough, and
// retryable-error mapping.
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

const (
	defaultBaseURL   = "https://api.anthropic.com"
	apiVersion       = "2023-06-01"
	defaultMaxTokens = 32768
)

// New returns an agent.Provider backed by the Anthropic Messages API.
// baseURL "" defaults to the public API; override for tests.
func New(apiKey, baseURL string) agent.Provider {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &provider{apiKey: apiKey, baseURL: baseURL, client: &http.Client{}}
}

type provider struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// validEffort reports whether effort is an accepted output_config.effort
// level. On current models the old thinking budget_tokens form is rejected
// with a 400 — effort goes in output_config and thinking runs adaptive.
func validEffort(effort string) bool {
	switch effort {
	case "low", "medium", "high", "xhigh", "max":
		return true
	}
	return false
}

type cacheControl struct {
	Type string `json:"type"`
}

var ephemeral = &cacheControl{Type: "ephemeral"}

type apiTextBlock struct {
	Type         string        `json:"type"` // "text"
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type apiToolUseBlock struct {
	Type  string          `json:"type"` // "tool_use"
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type apiToolResultBlock struct {
	Type         string        `json:"type"` // "tool_result"
	ToolUseID    string        `json:"tool_use_id"`
	Content      string        `json:"content"`
	IsError      bool          `json:"is_error,omitempty"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type apiTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	CacheControl *cacheControl   `json:"cache_control,omitempty"`
}

type apiMessage struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
}

func marshalBlock(b agent.Block, cache bool) (json.RawMessage, error) {
	switch b.Type {
	case "text":
		blk := apiTextBlock{Type: "text", Text: b.Text}
		if cache {
			blk.CacheControl = ephemeral
		}
		return json.Marshal(blk)
	case "thinking":
		// Replayed verbatim — signatures are opaque and model-specific.
		return b.Raw, nil
	case "tool_use":
		return json.Marshal(apiToolUseBlock{Type: "tool_use", ID: b.ID, Name: b.Name, Input: b.Input})
	case "tool_result":
		blk := apiToolResultBlock{Type: "tool_result", ToolUseID: b.ToolUseID, Content: b.Content, IsError: b.IsError}
		if cache {
			blk.CacheControl = ephemeral
		}
		return json.Marshal(blk)
	default:
		return nil, fmt.Errorf("unknown block type %q", b.Type)
	}
}

func buildBody(req agent.Request) ([]byte, error) {
	body := map[string]any{
		"model":      req.Model,
		"max_tokens": defaultMaxTokens,
		"stream":     true,
	}
	if req.System != "" {
		// System as a block array so it can carry a cache breakpoint.
		body["system"] = []apiTextBlock{{Type: "text", Text: req.System, CacheControl: ephemeral}}
	}
	// Adaptive thinking is the only on-mode on current models; depth is
	// controlled via output_config.effort.
	body["thinking"] = map[string]any{"type": "adaptive"}
	if validEffort(req.Effort) {
		body["output_config"] = map[string]any{"effort": req.Effort}
	}
	if len(req.Tools) > 0 {
		tools := make([]apiTool, len(req.Tools))
		for i, t := range req.Tools {
			tools[i] = apiTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}
		}
		tools[len(tools)-1].CacheControl = ephemeral // breakpoint after system+tools
		body["tools"] = tools
	}
	msgs := make([]apiMessage, 0, len(req.Messages))
	// Rolling tail breakpoint: final block of the last-but-one user message.
	tailIdx := -1
	for i := len(req.Messages) - 2; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			tailIdx = i
			break
		}
	}
	for i, m := range req.Messages {
		am := apiMessage{Role: m.Role}
		for j, b := range m.Blocks {
			cache := i == tailIdx && j == len(m.Blocks)-1
			raw, err := marshalBlock(b, cache)
			if err != nil {
				return nil, err
			}
			am.Content = append(am.Content, raw)
		}
		msgs = append(msgs, am)
	}
	body["messages"] = msgs
	return json.Marshal(body)
}

// apiError is the JSON error envelope returned on non-2xx responses.
type apiError struct {
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (p *provider) Stream(ctx context.Context, req agent.Request) (<-chan agent.StreamEvent, error) {
	payload, err := buildBody(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", apiVersion)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, &agent.RetryableError{Err: err} // network errors are retryable
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var ae apiError
		_ = json.Unmarshal(raw, &ae)
		msg := ae.Error.Message
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
		}
		err := fmt.Errorf("anthropic: %d %s: %s", resp.StatusCode, ae.Error.Type, msg)
		if resp.StatusCode == 429 || resp.StatusCode >= 500 || ae.Error.Type == "overloaded_error" {
			re := &agent.RetryableError{Err: err}
			if ra := resp.Header.Get("retry-after"); ra != "" {
				if secs, perr := strconv.Atoi(ra); perr == nil {
					re.RetryAfter = time.Duration(secs) * time.Second
				}
			}
			return nil, re
		}
		return nil, err
	}

	ch := make(chan agent.StreamEvent)
	go func() {
		defer close(ch)
		defer func() { _ = resp.Body.Close() }()
		parseSSE(ctx, resp.Body, ch)
	}()
	return ch, nil
}

// sseEvent mirrors the union of Messages-API stream event payloads.
type sseEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		Usage apiUsage `json:"usage"`
	} `json:"message"`
	ContentBlock json.RawMessage `json:"content_block"`
	Delta        struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage apiUsage `json:"usage"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type apiUsage struct {
	InputTokens         int `json:"input_tokens"`
	OutputTokens        int `json:"output_tokens"`
	CacheReadTokens     int `json:"cache_read_input_tokens"`
	CacheCreationTokens int `json:"cache_creation_input_tokens"`
}

// blockAccum accumulates one content block across start/delta/stop events.
type blockAccum struct {
	kind      string // text | thinking | tool_use
	text      strings.Builder
	partial   strings.Builder // tool_use input JSON
	thinking  strings.Builder
	signature string
	id, name  string
	data      string // redacted_thinking payload
}

func parseSSE(ctx context.Context, body io.Reader, ch chan<- agent.StreamEvent) {
	send := func(ev agent.StreamEvent) bool {
		select {
		case ch <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	blocks := map[int]*blockAccum{}
	usage := agent.Usage{}
	stopReason := ""
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev sseEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "message_start":
			usage.InputTokens = ev.Message.Usage.InputTokens
			usage.CacheReadTokens = ev.Message.Usage.CacheReadTokens
			usage.CacheWriteTokens = ev.Message.Usage.CacheCreationTokens
		case "content_block_start":
			var cb struct {
				Type  string          `json:"type"`
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				Text  string          `json:"text"`
				Data  string          `json:"data"`
				Input json.RawMessage `json:"input"`
			}
			_ = json.Unmarshal(ev.ContentBlock, &cb)
			acc := &blockAccum{kind: cb.Type, id: cb.ID, name: cb.Name, data: cb.Data}
			acc.text.WriteString(cb.Text)
			blocks[ev.Index] = acc
		case "content_block_delta":
			acc := blocks[ev.Index]
			if acc == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				acc.text.WriteString(ev.Delta.Text)
				if !send(agent.StreamEvent{Kind: "text_delta", TextDelta: ev.Delta.Text}) {
					return
				}
			case "input_json_delta":
				acc.partial.WriteString(ev.Delta.PartialJSON)
			case "thinking_delta":
				acc.thinking.WriteString(ev.Delta.Thinking)
			case "signature_delta":
				acc.signature += ev.Delta.Signature
			}
		case "content_block_stop":
			acc := blocks[ev.Index]
			if acc == nil {
				continue
			}
			delete(blocks, ev.Index)
			var blk agent.Block
			switch acc.kind {
			case "text":
				blk = agent.Block{Type: "text", Text: acc.text.String()}
			case "thinking":
				raw, _ := json.Marshal(map[string]string{
					"type": "thinking", "thinking": acc.thinking.String(), "signature": acc.signature,
				})
				blk = agent.Block{Type: "thinking", Raw: raw}
			case "tool_use":
				input := acc.partial.String()
				if input == "" {
					input = "{}"
				}
				blk = agent.Block{Type: "tool_use", ID: acc.id, Name: acc.name, Input: json.RawMessage(input)}
			case "redacted_thinking":
				// Must be replayed intact when tool use follows — dropping it
				// makes the continuation request invalid.
				raw, _ := json.Marshal(map[string]string{
					"type": "redacted_thinking", "data": acc.data,
				})
				blk = agent.Block{Type: "thinking", Raw: raw}
			default:
				continue
			}
			if !send(agent.StreamEvent{Kind: "block", Block: &blk}) {
				return
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				stopReason = ev.Delta.StopReason
			}
			// message_delta usage is cumulative — assign, don't accumulate.
			usage.OutputTokens = ev.Usage.OutputTokens
		case "message_stop":
			u := usage
			send(agent.StreamEvent{Kind: "done", StopReason: stopReason, Usage: &u})
			return
		case "error":
			send(agent.StreamEvent{Kind: "error", Err: fmt.Errorf("anthropic stream: %s: %s", ev.Error.Type, ev.Error.Message)})
			return
		}
	}
	if err := scanner.Err(); err != nil {
		send(agent.StreamEvent{Kind: "error", Err: err})
	}
	// Stream ended without message_stop — the loop treats a missing stop
	// reason as an error, so nothing more to send here.
}
