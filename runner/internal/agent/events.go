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
//
// Source is who the message is from: "chat" (the person), "ask_answer" (the person answering
// an ask), "system" (text the daemon, the engine or its harness wrote — never shown as the person),
// and a few more ("note_reply", "runner"…). ClientID is the id the sender gave the message (a
// browser's queued bubble), echoed so the sender can match it; MessageID, on an ask_answer, is the
// id of the ask it answers.
type UserMessagePayload struct {
	TurnID    string `json:"turn_id,omitempty"`
	Text      string `json:"text"`
	Source    string `json:"source"`
	ClientID  string `json:"client_id,omitempty"`
	MessageID string `json:"message_id,omitempty"`
	// AnsweredBy, on an ask_answer, is "session" when the parent session answered (its message
	// came with source "session", which only the runner sets); absent for the person's answer.
	AnsweredBy string `json:"answered_by,omitempty"`
}

// Input is one message for a session's engine. Text is what the engine is given; Shown, when set,
// is what the transcript records instead (a slash skill: the person typed "/review x", the engine
// gets the skill's instructions). ClientID is echoed on the user_message (UserMessagePayload).
type Input struct {
	PlanNudge      string `json:",omitempty"`
	ExpectedTurnID string
	Text           string
	Shown          string
	Source         string
	ClientID       string
}

// Payload is the user_message an input is recorded as.
func (in Input) Payload() UserMessagePayload {
	text := in.Shown
	if text == "" {
		text = in.Text
	}
	return UserMessagePayload{Text: text, Source: in.Source, ClientID: in.ClientID}
}

// UsageLimitPayload is an actual provider refusal of the identified input turn.
// An absent reset can be resolved from the same engine/account usage meter.
type UsageLimitPayload struct {
	WindowID string     `json:"window_id,omitempty"`
	Provider string     `json:"provider"`
	ResetAt  *time.Time `json:"reset_at,omitempty"`
	TurnID   string     `json:"turn_id"`
}

// AssistantTextPayload — kind "assistant_text". Done=false events are
// transient streaming deltas; only the Done=true consolidated event is
// persisted by the transport layer.
//
// ParentCallID, on this payload and on the thinking, tool call and tool result
// ones, is set when the event belongs to a subagent: it is the call id of the
// tool call that started that subagent (its subagent_started child_id). The
// main agent's own events carry none.
type AssistantTextPayload struct {
	Text         string `json:"text"`
	Done         bool   `json:"done"`
	ParentCallID string `json:"parent_call_id,omitempty"`
}

// AssistantThinkingPayload — kind "assistant_thinking": the text of one
// thinking block. Persisted; never the session's answer (that is
// assistant_text).
type AssistantThinkingPayload struct {
	Text         string `json:"text"`
	ParentCallID string `json:"parent_call_id,omitempty"`
}

// ThinkingPayload — kinds "thinking_started" and "thinking_done": the model opened, or closed, a
// thinking block. They carry no text (that comes in assistant_thinking, when there is any to show)
// and are transient like a typing delta: sent live, never persisted or replayed. They only let a
// reader say "Thinking…" while a long think runs; one that missed the done reads the
// assistant_thinking, the next tool call or the turn's end instead.
type ThinkingPayload struct {
	Tokens       *int   `json:"tokens,omitempty"`
	ParentCallID string `json:"parent_call_id,omitempty"`
}

// The ThinkingPayload kinds.
const (
	KindThinkingProgress = "thinking_progress"
	KindThinkingStarted  = "thinking_started"
	KindThinkingDone     = "thinking_done"
)

// IsTransient reports whether an event is sent live only, never persisted: a typing delta
// (assistant_text not done) or a thinking block's start or end.
func IsTransient(ev Event) bool {
	switch ev.Kind {
	case "assistant_text":
		p, ok := ev.Payload.(AssistantTextPayload)
		return ok && !p.Done
	case KindThinkingStarted, KindThinkingDone, KindThinkingProgress:
		return true
	}
	return false
}

// ToolCallPayload — kind "tool_call".
type ToolCallPayload struct {
	Tool         string          `json:"tool"`
	CallID       string          `json:"call_id"`
	Input        json.RawMessage `json:"input"`
	ParentCallID string          `json:"parent_call_id,omitempty"`
}

// ToolResultPayload — kind "tool_result".
type ToolResultPayload struct {
	CallID       string `json:"call_id"`
	Output       string `json:"output"`
	IsError      bool   `json:"is_error"`
	DurationMs   int64  `json:"duration_ms"`
	ParentCallID string `json:"parent_call_id,omitempty"`
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

// MessagingPayload — kinds "ask" | "update" | "note": a message from the session to the person
// (the native loop's tools, or `blerg-runner ask|update|note`, recorded by the server). MessageID is
// the message's id in the messages API, when known: an ask is answered (and a note replied to) with
// POST /api/messages/{id}/reply.
type MessagingPayload struct {
	OwnerOnly bool `json:"owner_only,omitempty"`
	// NoWait: an ask the agent does not wait on (`blerg-runner ask --no-wait`); its answer reaches
	// the agent as a message, and the person's chat messages do not answer it.
	NoWait    bool   `json:"no_wait,omitempty"`
	Body      string `json:"body"`
	MessageID string `json:"message_id,omitempty"`
	TurnID    string `json:"turn_id,omitempty"`
}

// ModelChangedPayload — kind "model_changed".
type ModelChangedPayload struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
	Source string `json:"source"` // "start" | "ui" | "command"
}

// SubagentPayload — kinds "subagent_started" | "subagent_update" | "subagent_done".
//
// The optional fields are set by the Claude Code driver. subagent_update carries the running
// totals of a subagent still at work (Model as the API reports it, Usage, ToolUses), at most every
// few seconds. subagent_done carries the final totals and DurationMs; one with Background set is
// the launch of a background subagent (its call returned at once), and a later subagent_done for
// the same ChildID, without Background, is its real end.
type SubagentPayload struct {
	// Effort is the child-reported configured effort, not inferred from its parent.
	// Empty means unknown/unset; emitted explicitly to clear stale observations.
	Effort    string `json:"effort"`
	ChildID   string `json:"child_id"`
	AgentType string `json:"agent_type"`
	Model     string `json:"model"`
	Summary   string `json:"summary"`
	Usage     *Usage `json:"usage,omitempty"`
	// UsageByModel partitions Usage for a run that changes models. Consumers
	// use this partition instead of also counting the aggregate Usage. An empty
	// model key means attribution is unknown; older producers omit the field.
	UsageByModel map[string]Usage `json:"usage_by_model,omitempty"`
	ToolUses     int              `json:"tool_uses,omitempty"`
	DurationMs   int64            `json:"duration_ms,omitempty"`
	Background   bool             `json:"background,omitempty"`
	// IsError: the subagent failed — its Agent call's result was an error, or its background
	// notice's status was "failed" (killed and stopped mean a person stopped it). Absent from a
	// daemon older than the field (a reader then falls back to an "error:" summary).
	IsError bool `json:"is_error,omitempty"`
	// Resumed: this run is a finished subagent the main agent resumed (Claude Code's SendMessage).
	// ChildID is then the call that resumed it, a new instance with the original's AgentType and
	// Summary; ResumedFrom is the original's ChildID, and Background on subagent_started says
	// whether the original ran in the background. A resumed run is not tied to its call: it ends
	// with a subagent_done of its own (or the engine's end), whatever the main agent's turn does.
	Resumed     bool   `json:"resumed,omitempty"`
	ResumedFrom string `json:"resumed_from,omitempty"`
}

// TopicPayload — kind "topic": a piece of work the agent marked with `blerg-runner topic`, recorded by
// the server (POST /api/sessions/{id}/topics). Action "start" opens topic ID with Title and ends the
// one that was open (no separate "done" is written for it); "done" closes the open one, repeating its
// ID and Title, with an optional one-line Outcome and the Commits the work landed (the agent's
// `--commit <sha>`: lower-case hex, deduplicated, in its order, at most topics.MaxCommits).
type TopicPayload struct {
	ID      string        `json:"id"`
	Title   string        `json:"title"`
	Action  string        `json:"action"`
	Outcome string        `json:"outcome,omitempty"`
	Commits []TopicCommit `json:"commits,omitempty"`
}

// MarkPayload — kind "mark": a stroke for the History tab the agent marked with `blerg-runner mark`
// (or the session's owner marked), recorded by the server (POST /api/sessions/{id}/marks). Kind is
// "decision" | "milestone" | "finding"; By is whose it is ("person": a ruling of the person the agent
// wrote down, "agent": the agent's own, which the person can overrule). Text is plain text, cleaned
// like a topic title. Commits, Card (a card number) and BoardID (the board it is on, when the
// session knows it) are chips. At is set only for a back-dated mark (an import, or --at): the
// moment it records, RFC 3339 UTC; the chat draws no row for it, History anchors it by time.
// PostedBy is who posted it: "agent" (the session's own token) or "owner" (the person, signed in).
type MarkPayload struct {
	ID       string        `json:"id"`
	Kind     string        `json:"kind"`
	By       string        `json:"by"`
	Text     string        `json:"text"`
	Commits  []TopicCommit `json:"commits,omitempty"`
	Card     int           `json:"card,omitempty"`
	BoardID  string        `json:"board_id,omitempty"`
	At       string        `json:"at,omitempty"`
	PostedBy string        `json:"posted_by"`
}

// TopicCommit is one commit a topic's done names.
type TopicCommit struct {
	Sha string `json:"sha"`
}

// BackgroundTaskStartedPayload — kind "background_task_started": something the agent left running
// in the background (a Bash call with run_in_background, a Monitor, a background subagent), sent
// once its id is known (its call's result). TaskID is the CLI's id for it (the agentId of a
// subagent), CallID the tool call that started it, Kind "bash" | "monitor" | "agent", StartedAt
// RFC 3339.
type BackgroundTaskStartedPayload struct {
	TaskID      string `json:"task_id"`
	CallID      string `json:"call_id"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	// Command is the shell command of a bash or monitor task, cut to 500 bytes (empty for an
	// agent, and for a start recorded before the field existed).
	Command   string `json:"command,omitempty"`
	StartedAt string `json:"started_at"`
}

// BackgroundTaskDonePayload — kind "background_task_done": the end of a background task, read from
// the notice the CLI injects when it finishes (or "stopped" when the engine process ended under
// it). Status is the notice's ("completed", "failed", "killed"…), Summary its summary cut to 120
// bytes. Description repeats the start's, for a reader that does not hold it.
type BackgroundTaskDonePayload struct {
	TaskID      string `json:"task_id"`
	CallID      string `json:"call_id"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	Summary     string `json:"summary"`
	Description string `json:"description,omitempty"`
}

// TurnDonePayload — kind "turn_done".
type TurnDonePayload struct {
	// UsageReported distinguishes an explicitly unknown engine report from actual
	// zero counters. Absent on older producers; it never changes billing sums.
	UsageReported  *bool  `json:"usage_reported,omitempty"`
	StopReason     string `json:"stop_reason"`
	Model          string `json:"model"`
	Usage          Usage  `json:"usage"`
	CheckInMissing bool   `json:"check_in_missing,omitempty"`
	// UsageByModel partitions Usage; consumers replace the aggregate with this
	// map, never add both. An empty model key explicitly means unknown.
	UsageByModel map[string]Usage `json:"usage_by_model,omitempty"`
	// Context is how full the main agent's context window was when the turn ended (the context
	// meter on its Running now row). Absent when the engine reported nothing about it.
	Context *ContextUsage `json:"context,omitempty"`
}

// ContextUsage is the main agent's context at the end of a turn, as its engine reported it. Zero
// means unknown: nothing is estimated.
type ContextUsage struct {
	// Tokens is the conversation's size at its latest model call: the prompt the model read
	// (input + cache read + cache write for Claude, the native loop's own calls; Codex's reported
	// token count of its last call). Zero when a compaction came after that call.
	Tokens int `json:"tokens,omitempty"`
	// Window is the model's context window, as the engine reports it.
	Window int `json:"window,omitempty"`
	// CompactAt is the size at which the engine compacts by itself, when it says.
	CompactAt int `json:"compact_at,omitempty"`
	// Compacted says the engine compacted the conversation during this turn; CompactedFrom is
	// its size just before, when the engine says.
	Compacted     bool `json:"compacted,omitempty"`
	CompactedFrom int  `json:"compacted_from,omitempty"`
}

// Reported is the context to put on a turn_done: nil when nothing is known.
func (c ContextUsage) Reported() *ContextUsage {
	if c.Tokens <= 0 && !c.Compacted {
		return nil
	}
	return &c
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
	// Code, when set, says what kind of error this is, for a reader that shows them differently:
	// ErrorCodeAPI or ErrorCodeAPIRetry. Most errors have none.
	Code string `json:"code,omitempty"`
	// ClientID, on the error of a message that was dropped (the queue was full), is that
	// message's client id (Input.ClientID).
	ClientID string `json:"client_id,omitempty"`
	// Attempt, MaxRetries and RetryInMs, on an ErrorCodeAPIRetry notice, are the engine's word on
	// the retry: which attempt this is, how many it makes at most, and how long it waits before it
	// (each 0 when not said).
	Attempt    int `json:"attempt,omitempty"`
	MaxRetries int `json:"max_retries,omitempty"`
	RetryInMs  int `json:"retry_in_ms,omitempty"`
	// SandboxExit, on the runtime_stopped error of a sandboxed session whose container stopped,
	// is what Docker recorded about that container (docker inspect's State).
	SandboxExit *SandboxExit `json:"sandbox_exit,omitempty"`
}

// SandboxExit is a stopped sandbox container's exit, as Docker recorded it. Gone means Docker no
// longer knows the container (it was removed), so nothing else is known.
type SandboxExit struct {
	Gone       bool   `json:"gone,omitempty"`
	Status     string `json:"status,omitempty"`
	ExitCode   int    `json:"exit_code"`
	OOMKilled  bool   `json:"oom_killed"`
	FinishedAt string `json:"finished_at,omitempty"`
	Error      string `json:"error,omitempty"`
}

// The ErrorPayload codes.
const (
	// ErrorCodeAPI: the engine's call to the model's API failed (the Claude CLI reports it as an
	// assistant line flagged isApiErrorMessage). The turn usually ends with it.
	ErrorCodeAPI = "api_error"
	// ErrorCodeAPIRetry: the engine is retrying a failed API call. A notice, not an outcome: the
	// turn goes on.
	ErrorCodeAPIRetry = "api_retry"
	// ErrorCodeChildSync: the daemon could not bring a sandboxed child's commits back to its
	// wip/<id> branch at the end of a turn. A notice, not the turn's outcome; the parent's
	// turn-end note says so.
	ErrorCodeChildSync = "child_sync"
	// ErrorCodeRuntimeStopped: the engine's runtime has stopped for good (Codex's app-server
	// failed repeatedly; Hermes found an unsupported gateway, a changed provider binding, or could
	// not verify its gateway shut down; a sandboxed session's container stopped, with SandboxExit);
	// the session takes no more turns and ends. `blerg-runner session watch`
	// returns on it.
	ErrorCodeRuntimeStopped = "runtime_stopped"
)

// CompactionPayload — kind "compaction". Summary is the full replacement
// text (persisted so crash recovery rebuilds context as summary + events
// after it).
//
// The Claude Code driver reports the CLI's own compaction (its compact_boundary line) with no
// summary (the CLI keeps it): Trigger is "manual" or "auto" and PreTokens the conversation's size
// before it, when the CLI says.
type CompactionPayload struct {
	Summary      string `json:"summary"`
	ThroughIndex int    `json:"through_index"`
	Trigger      string `json:"trigger,omitempty"`
	PreTokens    int    `json:"pre_tokens,omitempty"`
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
