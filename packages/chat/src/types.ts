// The transcript's wire types: every event the chat understands, the payload of each, and the
// session messages a transport relays. No logic lives here — only type definitions. Mirrors the
// runner's internal/protocol/messages.go (and runner/frontend/src/types.ts, which these came from).

// ─── Agent sessions ───────────────────────────────────────────────────────────

export type AgentEventKind =
  | 'user_message'
  | 'assistant_text'
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
  | 'subagent_done'
  | 'turn_done'
  | 'error'
  | 'compaction'
  | 'start_stage'
  | 'capabilities'
  | 'artifact'

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

export interface UserMessagePayload { text: string; source: string }
export interface AssistantTextPayload { text: string; done: boolean }
export interface ToolCallPayload { tool: string; call_id: string; input: unknown }
export interface ToolResultPayload { call_id: string; output: string; is_error: boolean; duration_ms: number }
export interface StatusPayload { status: string; reason: string }
export interface CheckInPayload { phase: string; summary: string }
export interface MessagingPayload { body: string }
export interface ModelChangedPayload { model: string; effort: string; source: string }
export interface SubagentPayload { child_id: string; agent_type: string; model: string; summary: string }
export interface TurnDonePayload {
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
}
export interface ErrorPayload { message: string; retryable: boolean }
export interface CompactionPayload { summary: string; through_index: number }

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
