package agent

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"
)

// Event is one transcript event. Payload types are per-kind structs below.
// The transport layer (daemon/runner) marshals payloads and ships events to
// the server, which assigns the authoritative per-session seq.
type Event struct {
	ClientEventID string    // UUID v4, minted at emit time; server-side idempotency key
	Ts            time.Time //
	Kind          string    //
	Payload       any       //
}

// Emitter receives loop events. Implementations must be safe for concurrent use.
type Emitter interface{ Emit(Event) }

// UserMessagePayload — kind "user_message".
type UserMessagePayload struct {
	Text   string `json:"text"`
	Source string `json:"source"` // "chat" | "ask_answer"
}

// AssistantTextPayload — kind "assistant_text". Done=false events are
// transient streaming deltas; only the Done=true consolidated event is
// persisted by the transport layer.
type AssistantTextPayload struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

// ToolCallPayload — kind "tool_call".
type ToolCallPayload struct {
	Tool   string          `json:"tool"`
	CallID string          `json:"call_id"`
	Input  json.RawMessage `json:"input"`
}

// ToolResultPayload — kind "tool_result".
type ToolResultPayload struct {
	CallID     string `json:"call_id"`
	Output     string `json:"output"`
	IsError    bool   `json:"is_error"`
	DurationMs int64  `json:"duration_ms"`
}

// ProviderBlocksPayload — kind "provider_blocks": the raw assistant content
// blocks of one provider response (including thinking blocks). The
// crash-recovery source of truth.
type ProviderBlocksPayload struct {
	Blocks []Block `json:"blocks"`
}

// StatusPayload — kind "status_changed".
type StatusPayload struct {
	Status string `json:"status"` // "running" | "waiting" | "idle"
	Reason string `json:"reason"`
}

// CheckInPayload — kind "check_in".
type CheckInPayload struct {
	Phase   string `json:"phase"` // "task_start" | "pre_risky" | "completion"
	Summary string `json:"summary"`
}

// MessagingPayload — kinds "ask" | "update" | "note".
type MessagingPayload struct {
	Body string `json:"body"`
}

// ModelChangedPayload — kind "model_changed".
type ModelChangedPayload struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
	Source string `json:"source"` // "start" | "ui" | "command"
}

// SubagentPayload — kinds "subagent_started" | "subagent_done".
type SubagentPayload struct {
	ChildID   string `json:"child_id"`
	AgentType string `json:"agent_type"`
	Model     string `json:"model"`
	Summary   string `json:"summary"`
}

// TurnDonePayload — kind "turn_done".
type TurnDonePayload struct {
	StopReason     string `json:"stop_reason"`
	Model          string `json:"model"`
	Usage          Usage  `json:"usage"`
	CheckInMissing bool   `json:"check_in_missing,omitempty"`
}

// ErrorPayload — kind "error".
//
// Retryable decides whether the session SURVIVES this error, so it must be
// set deliberately on every construction — the zero value is the fatal one.
// The daemon turns a non-retryable error into session_state_changed{"error"},
// which is a terminal status server-side: the board settles the session and
// revokes its token. Set it false only for a start/pipe/driver failure, where
// the session genuinely never ran or cannot continue; set it true for a turn,
// model or tool outcome, where the driver goes on to emit turn_done/idle and
// the session still takes the next message.
type ErrorPayload struct {
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// CompactionPayload — kind "compaction". Summary is the full replacement
// text (persisted so crash recovery rebuilds context as summary + events
// after it).
type CompactionPayload struct {
	Summary      string `json:"summary"`
	ThroughIndex int    `json:"through_index"`
}

// newUUID returns a random UUID v4 string (same shape as the server's).
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func newEvent(kind string, payload any) Event {
	return Event{ClientEventID: newUUID(), Ts: time.Now(), Kind: kind, Payload: payload}
}
