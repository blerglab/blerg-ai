// Package agent implements blerg-runner's own agentic loop: a transport-free
// engine that drives an LLM provider, executes tools against a workspace,
// and emits typed transcript events.
package agent

import (
	"context"
	"encoding/json"
)

// Block is one content block in a provider message.
type Block struct {
	Type      string          // "text" | "thinking" | "tool_use" | "tool_result"
	Text      string          // text blocks
	Raw       json.RawMessage // thinking blocks: opaque, replayed verbatim
	ID        string          // tool_use: block id
	Name      string          // tool_use: tool name
	Input     json.RawMessage // tool_use: arguments
	ToolUseID string          // tool_result: pairs with tool_use ID
	Content   string          // tool_result: output
	IsError   bool            // tool_result
}

// Message is one turn-side message in provider format.
type Message struct {
	Role   string // "user" | "assistant"
	Blocks []Block
}

// ToolDef describes a tool to the provider.
type ToolDef struct {
	Name        string
	Description string
	InputSchema json.RawMessage // JSON Schema
}

// Usage is token accounting for one provider response.
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

// StreamEvent is one element of a provider stream.
type StreamEvent struct {
	Kind       string // "text_delta" | "block" | "done" | "error"
	TextDelta  string // text_delta
	Block      *Block // block: a completed content block
	StopReason string // done: "end_turn" | "tool_use" | "max_tokens"
	Usage      *Usage // done
	Err        error  // error
}

// Request is one provider call.
type Request struct {
	Model    string
	Effort   string
	System   string
	Messages []Message
	Tools    []ToolDef
}

// Provider streams a model response for a request.
type Provider interface {
	Stream(ctx context.Context, req Request) (<-chan StreamEvent, error)
}
