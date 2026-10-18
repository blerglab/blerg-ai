// The transcript's wire types: every event the chat understands, the payload of each, and the
// session messages a transport relays. No logic lives here — only type definitions. Mirrors the
// runner's internal/protocol/messages.go (and runner/frontend/src/types.ts, which these came from).

// ─── Agent sessions ───────────────────────────────────────────────────────────

export type AgentEventKind =
  | 'user_message'
  | 'input_accepted'
  | 'assistant_text'
  | 'assistant_thinking'
  | 'thinking_started'
  | 'thinking_done'
  | 'tool_call'
  | 'tool_result'
  | 'provider_blocks'
  | 'status_changed'
  | 'check_in'
  | 'ask'
  | 'update'
  | 'note'
  | 'model_changed'
  | 'subagent_started'
  | 'subagent_update'
  | 'subagent_done'
  | 'background_task_started'
  | 'background_task_done'
  | 'turn_done'
  | 'error'
  | 'compaction'
  | 'start_stage'
  | 'capabilities'
  | 'session_upload'
  | 'artifact'
  | 'topic'
  | 'checklist'
  | 'ticket'
  | 'mcp_status'
  | 'delegation_request'
  | 'delegation_decided'

export interface AgentEvent {
  type: 'agent_event'
  session_id: string
  client_event_id: string
  seq?: number
  ts: string
  kind: AgentEventKind
  payload: unknown
  transient?: boolean
}

// source: chat (the person, in the app) | human (a person through an app's proxy) | ask_answer (an
// answer to an ask) | note_reply | system (text the daemon, engine or harness wrote: never the
// person) | runner | blerg-board | ….
// client_id: the id the sender gave the message (a queued bubble's), echoed by the daemon; absent
// from a daemon older than the field and on messages no browser sent.
// message_id: on an ask_answer, the ask (messages API id) it answers.
export interface SessionUploadPayload { name: string; size: number; artifact_id: string; from_session_id: string; from_title: string }

export interface UserMessagePayload { text: string; source: string; client_id?: string; message_id?: string }
// parent_call_id (on assistant text, thinking, tool calls and results): the event belongs to a
// subagent, started by the tool call with this call id (its subagent_started child_id). Absent on
// the main agent's own events, and on every event of a daemon older than the field.
export interface AssistantTextPayload { text: string; done: boolean; parent_call_id?: string }
/** kind "assistant_thinking": one thinking block's text. Never the assistant's answer. */
export interface AssistantThinkingPayload { text: string; parent_call_id?: string }
/** kinds "thinking_started" / "thinking_done": a thinking block opened or closed. Transient (sent
 *  live only, never held in the transcript) and without text; model/workingActivity reads them. */
export interface ThinkingPayload { parent_call_id?: string }
export interface ToolCallPayload { tool: string; call_id: string; input: unknown; parent_call_id?: string }
export interface ToolResultPayload { call_id: string; output: string; is_error: boolean; duration_ms: number; parent_call_id?: string }
export interface StatusPayload { status: string; reason: string }
export interface CheckInPayload { phase: string; summary: string }
// kinds ask | update | note: what the agent sent the person (blerg-runner ask/update/note, or the
// native loop's tools). message_id: the messages API id (an ask's answer names it); absent on
// older events and on native updates and notes.
export interface MessagingPayload { body: string; message_id?: string }
export interface ModelChangedPayload { model: string; effort: string; source: string }
// kinds subagent_started | subagent_update | subagent_done. The optional fields come from a Claude
// Code session: an update carries the subagent's model and running totals; a done carries the
// final totals and duration_ms, or background: true when it is only the launch of a background
// subagent (a later done for the same child_id is its real end).
export interface TokenUsage { input_tokens: number; output_tokens: number; cache_read_tokens: number; cache_write_tokens: number }
export interface SubagentPayload {
  child_id: string; agent_type: string; model: string; summary: string
  /** Child-reported configured effort; empty/absent means unknown, never inherited from the parent. */
  effort?: string
  usage_by_model?: Record<string, TokenUsage>
  usage?: TokenUsage; tool_uses?: number; duration_ms?: number; background?: boolean
  // it failed (a daemon older than the field leaves it out; an "error:" summary says the same)
  is_error?: boolean
  // A finished subagent the main agent resumed (Claude Code's SendMessage): a new instance whose
  // child_id is the SendMessage call's, resumed_from the original's child_id. On its start,
  // background says how the original ran. It runs until a subagent_done of its own.
  resumed?: boolean
  resumed_from?: string
}
// A piece of work the agent marked (`blerg-runner topic`; the server records it): "start" opens topic
// id with its title and ends the open one; "done" closes the open one, with an optional outcome.
export interface TopicPayload { id: string; title: string; action: 'start' | 'done'; outcome?: string }
// kind "checklist" (`blerg-runner checklist`; the server records it): the FULL list after the change,
// the operation, and the item it touched (index 1-based; absent for set and clear).
/** diff: the id of the session's published diff of the item's commits (`checklist tick --commit`). */
export interface ChecklistItem { text: string; done: boolean; commits?: string[]; diff?: string }
export interface ChecklistPayload {
  items: ChecklistItem[]
  op: 'set' | 'add' | 'tick' | 'untick' | 'remove' | 'clear' | string
  changed?: { index: number; text: string; done: boolean; commits?: string[]; diff?: string }
}
// kind "ticket" (`blerg-runner pin` / `unpin`): a tracker's ticket the agent pinned, in one
// source-neutral shape (the server caps every field). Untrusted tracker data: drawn as text, the url
// only when it is http(s). A pin is identified by (source, key). action: pin (new), update (a re-pin;
// previous holds the status and title it had), unpin (ticket holds only source, key, title, status, url).
export interface TicketField { name: string; value: string }
export interface TicketComment { author?: string; at?: string; body: string }
export interface Ticket {
  source: string
  key: string
  url?: string
  title: string
  status?: string
  type?: string
  priority?: string
  assignee?: string
  reporter?: string
  labels?: string[]
  description?: string
  fields?: TicketField[]
  comments?: TicketComment[]
  acceptance?: string[]
}
export interface TicketPayload {
  ticket: Ticket
  action: 'pin' | 'update' | 'unpin' | string
  previous?: { status: string; title: string }
}
// kinds background_task_started | background_task_done: something the agent left running in the
// background (a background shell, a Monitor, a background subagent) and its end. task_id is the
// engine's id for it, call_id the tool call that started it. Only a Claude Code session sends them.
export type BackgroundTaskKind = 'bash' | 'monitor' | 'agent'
export interface BackgroundTaskStartedPayload {
  task_id: string; call_id: string; kind: BackgroundTaskKind | string; description: string; started_at: string
  /** The shell command of a bash or monitor task (absent on an older start). */
  command?: string
}
/** status: completed | failed | killed | expired (a monitor) | stopped (the engine ended) | … */
export interface BackgroundTaskDonePayload {
  task_id: string; call_id: string; kind: BackgroundTaskKind | string; status: string; summary: string; description?: string
}
export interface TurnDonePayload {
  /** Complete usage partition; replaces aggregate usage for attribution, never added to it. */
  usage_by_model?: Record<string, TokenUsage>
  stop_reason: string
  model: string
  usage: { input_tokens: number; output_tokens: number; cache_read_tokens: number; cache_write_tokens: number }
  check_in_missing?: boolean
}
// A file the agent handed the user (`blerg-runner publish`). Mirrors the server's artifactInfo.
// view says how the app can show it: markdown | text | json | csv | image | pdf | audio | video |
// html | none (download only). An unknown value is treated as download-only.
// origin: 'agent' (published by the session) or 'user' (attached by a person from the chat box);
// absent on older replies, which are agent files.
// version: this file's number among the files of the same name and origin (1 for the first);
// latest_version (the list only): the highest of them. Both are absent from an older server.
export interface ArtifactPayload {
  id: string; name: string; size: number; content_type: string; view: string
  origin?: 'agent' | 'user'; version?: number; latest_version?: number
  /** Set by the server's list when the file is still listed but its bytes are no longer stored
   *  (a data volume that did not survive a restart): it can be deleted, not opened. */
  missing?: boolean
}
// code: 'api_error' (the model API failed) or 'api_retry' (a notice: the engine is retrying the API;
// the turn goes on). client_id: on a message the session dropped (its queue was full), that
// message's client id. attempt, max_retries and retry_in_ms: on an api_retry notice, the engine's
// word on the retry (absent when not said, and from a daemon older than the fields).
export interface ErrorPayload {
  message: string; retryable: boolean; code?: string; client_id?: string
  attempt?: number; max_retries?: number; retry_in_ms?: number
}
// trigger and pre_tokens come from a Claude Code compaction ('manual' | 'auto', the context's size
// before it), whose summary the CLI keeps (summary is then empty).
export interface CompactionPayload { summary: string; through_index: number; trigger?: string; pre_tokens?: number }

// Session start progress (kind "start_stage"). A payload with plan=true opens
// a new start attempt and lists every stage in order; later payloads update
// stages by id. Mirrors protocol.StartStagePayload.
/** 'warning': over, but not as asked (an optional step skipped or partly done) — never a failed start. */
export type StartStageState = 'pending' | 'active' | 'done' | 'failed' | 'warning'
export interface StartStage {
  id: string
  label?: string
  state: StartStageState
  detail?: string
  hint?: string // next step, when failed or stuck
}
export interface StartStagePayload {
  plan?: boolean
  runtime?: string
  stages: StartStage[]
}

// What a session has loaded (kind "capabilities"). Engine-neutral data:
// render whatever groups arrive. Mirrors protocol.CapabilitiesPayload.
export type CapabilityGroupId = 'skills' | 'plugins' | 'mcp' | 'commands' | 'tools' | 'agents'
export interface CapabilityItem {
  name: string
  description?: string
  detail?: string
  status?: string
  source?: string
}
export interface CapabilityGroup {
  id: CapabilityGroupId
  label: string
  note?: string
  total?: number // count before the server's per-group cap
  items: CapabilityItem[]
}
export interface CapabilitiesPayload {
  engine: string
  engine_name?: string
  model?: string
  version?: string
  cwd?: string
  permission_mode?: string
  note?: string
  groups: CapabilityGroup[]
}

export interface AgentEventsReplayDone {
  type: 'agent_events_replay_done'
  session_id: string
  last_seq: number
  has_more: boolean
  // Set on the answer to a tail / before_seq request, which reads backwards from the end of the
  // transcript: first_seq is the oldest event it carried, has_older says whether earlier ones remain.
  older?: boolean
  has_older?: boolean
  first_seq?: number
  server_time?: string // server clock at replay end — for skew-free elapsed times
}

/** The server dropped live events of a session for this browser (its buffer was full); the first
 *  one missed has seq from_seq. Later ones may already have arrived. */
export interface AgentEventsGap {
  type: 'agent_events_gap'
  session_id: string
  from_seq: number
}

// ─── Sessions ─────────────────────────────────────────────────────────────────

// Session status values
export type SessionStatus = 'starting' | 'running' | 'idle' | 'waiting' | 'stopped' | 'error' | 'disconnected' | 'ended'

export interface SessionInfo {
  id: string
  daemon_id: string
  status: SessionStatus
  message?: string | null
  project_path: string
  repo: string
  title: string
  model?: string
  effort?: string
  steering_supported?: boolean
  engine?: string // "" / absent = claude
  started_at: string
  ended_at?: string | null
  unread?: boolean
  starred?: boolean
  kind?: string // "" | "tmux" | "agent"
  parent_session_id?: string | null
  runtime?: 'daemon' | 'docker' | 'cluster'
  skip_permissions?: boolean
  error_reason?: string
  // Why the session ended (a fixed code) and who ended it. Absent while it runs and for
  // sessions that ended before the runner recorded reasons.
  end_reason?: string
  ended_by?: EndedBy | null
  // The cron that started this session; absent for every other session.
  cron_id?: string
  // Who started the session when it was not the person in the app: a tool with an agent token
  // (name = the owner's label for it), a cron, or the operator key. Absent for the person's own.
  started_by?: StartedBy | null
  // Whether a person is reading the chat live. Chosen at start and fixed for the session's life:
  // "interactive" (the agent discusses, narrates, asks and waits) or "unattended" (nobody is
  // watching: it decides, notes its assumptions, finishes and summarises). Absent from a runner
  // or daemon older than the field.
  interaction?: 'interactive' | 'unattended'
  // How many background tasks the session's agent has running; absent when none (and from a runner
  // older than the field).
  background_tasks?: number
}

export interface StartedBy {
  kind: 'agent' | 'cron' | 'runner_key' | string
  name?: string
}

export interface EndedBy {
  kind: string // "human" | "agent" | "service" | "runner_key"
  // Whether that actor is — or acts for — the signed-in viewer. Worked out by
  // the server; the account itself is never sent.
  self?: boolean
}

export interface SessionStateChanged {
  type: 'session_state_changed'
  session_id: string
  status: SessionStatus
  message?: string | null
  unread: boolean
  end_reason?: string
  ended_by?: EndedBy | null
}

export interface SessionMetaChanged {
  type: 'session_meta_changed'
  session_id: string
  model?: string
  effort?: string
}

export interface SessionTitleChanged {
  type: 'session_title_changed'
  session_id: string
  title: string
}

export interface SessionEnded {
  type: 'session_ended'
  session_id: string
  exit_code: number
  signal?: string | null
  end_reason?: string
  ended_by?: EndedBy | null
}

// ─── Browser → Server messages a chat sends ───────────────────────────────────

export interface SubscribeAgentEvents {
  type: 'subscribe_agent_events'
  session_id: string
  after_seq?: number
  limit?: number
  tail?: number // the newest N events, instead of the oldest after after_seq
  before_seq?: number // the limit events just before this seq (the next older page)
}

export interface UnsubscribeSession {
  type: 'unsubscribe_session'
  session_id: string
}

export interface AgentUserMessage {
  type: 'agent_user_message'
  session_id: string
  text: string
  source?: string
  /** The sender's id for the message, echoed on its user_message (and on a drop's error). */
  client_id?: string
  /** This message answers that open ask (an ask card's reply). */
  answer_message_id?: string
}

export interface SetSessionModel {
  type: 'set_session_model'
  session_id: string
  model?: string
  effort?: string
}

export interface InterruptSession {
  type: 'interrupt_session'
  session_id: string
}

// kinds delegation_request | delegation_decided: a session asked to start another one, and the
// person's answer (the server writes both into the parent's transcript).
export type DelegationChoice = 'once' | 'session' | 'always' | 'deny'
export interface DelegationRequestPayload {
  request_id: string
  engine: string
  model?: string
  effort?: string
  repo?: string
  no_repo?: boolean
  runtime: 'docker' | 'daemon'
  daemon_name: string
  title: string
  /** The first 500 characters; the whole prompt is Transport.delegationRequest. */
  prompt: string
  prompt_truncated?: boolean
  budget?: {
    engine: string
    available: boolean
    local?: boolean
    windows: { id: string; used_percent: number; resets_at?: string }[]
    capped_until?: string | null
    note?: string
  }
  /** The choices the server offers this person for this request. */
  offered: DelegationChoice[]
  expires_at: string
}
export interface DelegationDecidedPayload {
  request_id: string
  status: 'allowed' | 'denied' | 'expired'
  choice?: 'once' | 'session' | 'always'
  child_session_id?: string
  start_error?: string
}
