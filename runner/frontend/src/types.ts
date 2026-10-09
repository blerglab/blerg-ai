// All WebSocket message types exchanged between server and browser.
// No logic lives here — only type definitions.
// Mirrors internal/protocol/messages.go and internal/server/board_events.go.

// ─── Board domain types ───────────────────────────────────────────────────────

export interface Board {
  id: string;
  name: string;
  description: string | null;
  repos: string[];
  default_daemon_id: string | null;
  created_at: string;
  updated_at: string;
}

export interface Column {
  id: string;
  board_id: string;
  rank: string;
  name: string;
  is_terminal: boolean;
  created_at: string;
}

export interface Ticket {
  id: string;
  board_id: string;
  column_id: string | null;
  title: string;
  body: string | null;
  priority: string;
  size: string | null;
  rank: string;
  archived_at: string | null;
  session_id: string | null;
  version: number;
  created_at: string;
  updated_at: string;
  tags: string[];
  repos: string[];
  blocked: boolean;
}

// ─── Domain types ─────────────────────────────────────────────────────────────

export interface MessageInfo {
  id: string;
  session_id: string;
  kind: 'update' | 'ask' | 'note';
  body: string;
  status: 'open' | 'answered';
  answer?: string | null;
  created_at: string;
  answered_at?: string | null;
}

export interface DaemonInfo {
  id: string;
  name: string;
  mode: string;
  version?: string;
  repos_root: string;
  status: string;
  last_seen_at: string;
}

export interface SessionInfo {
  id: string;
  daemon_id: string;
  status: SessionStatus;
  message?: string | null;
  project_path: string;
  repo: string;
  title: string;
  model?: string;
  effort?: string;
  engine?: string; // "" / absent = claude
  started_at: string;
  ended_at?: string | null;
  unread?: boolean;
  starred?: boolean;
  kind?: string; // "" | "tmux" | "agent"
  parent_session_id?: string | null;
  runtime?: 'daemon' | 'docker' | 'cluster';
  skip_permissions?: boolean;
  error_reason?: string;
  // Why the session ended (a fixed code; see lib/sessionEnd.ts) and who
  // ended it. Absent while it runs and for sessions that ended before the
  // runner recorded reasons.
  end_reason?: string;
  ended_by?: EndedBy | null;
  // The cron that started this session (crons, spec 7.6); absent for every other session.
  cron_id?: string;
  // Who started the session when it was not the person in the app: a tool with an agent token
  // (name = the owner's label for it), a cron, or the operator key. Absent for the person's own.
  started_by?: StartedBy | null;
  // Whether a person is reading the chat live. Chosen at start and fixed for the session's life:
  // "interactive" (the agent discusses, narrates, asks and waits) or "unattended" (nobody is
  // watching: it decides, notes its assumptions, finishes and summarises). Absent from a runner
  // or daemon older than the field.
  interaction?: 'interactive' | 'unattended';
}

export interface StartedBy {
  kind: 'agent' | 'cron' | 'runner_key' | string;
  name?: string;
}

export interface EndedBy {
  kind: string; // "human" | "agent" | "service" | "runner_key"
  // Whether that actor is — or acts for — the signed-in viewer. Worked out by
  // the server; the account itself is never sent.
  self?: boolean;
}

// ─── Model lists (GET /api/models/{engine}) ─────────────────────────────────
// Engine-neutral: every engine's list has this one shape. Efforts are plain
// strings — each engine has its own allowlist (Claude: low…max), enforced by
// the server.

export interface EngineModel {
  id: string            // model id to send as `model`, e.g. "claude-sonnet-5"
  name: string          // "Sonnet 5"
  description: string
  section: 'main' | 'overflow'
  efforts: string[]     // lowest first; [] = the model takes no effort
  default_effort?: string
  effort_kind?: string  // optional wording hint, e.g. "reasoning"
}

export interface ModelList {
  models: EngineModel[]
  source: 'live' | 'builtin' | 'daemon' | 'none'
  daemon_id?: string    // source 'daemon': the daemon whose probe this is
  engine_efforts?: string[] // the engine's whole effort allowlist
  fetched_at?: string | null
}

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
  | 'artifact';

export interface AgentEvent {
  type: 'agent_event';
  session_id: string;
  client_event_id: string;
  seq?: number;
  ts: string;
  kind: AgentEventKind;
  payload: unknown;
  transient?: boolean;
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
  stop_reason: string;
  model: string;
  usage: { input_tokens: number; output_tokens: number; cache_read_tokens: number; cache_write_tokens: number };
  check_in_missing?: boolean;
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

// MCP connections (runner GET /api/mcp/connections and .../{id}/tools), and what a launch
// sends as `mcp`. The hash is the one the picker saw; the server refuses a tool whose live
// hash differs.
export type McpToolMode = 'off' | 'propose' | 'allow'
export interface McpConnectionInfo {
  id: string
  name: string
  url: string
  auth_kind: string
  status: string // "ok" is the only status a session can use
  default_tools: Record<string, { mode: string; hash: string }>
}
export interface McpToolInfo {
  name: string
  description: string
  inputSchema?: unknown
  // What the server claims about itself (readOnlyHint, destructiveHint, title...). Display only.
  annotations?: { readOnlyHint?: boolean; [k: string]: unknown }
  hash: string
}
export interface McpSelectionEntry {
  connection: string
  tools: Record<string, { mode: 'allow' | 'propose'; hash: string }>
}

export interface AgentEventsReplayDone {
  type: 'agent_events_replay_done';
  session_id: string;
  last_seq: number;
  has_more: boolean;
  // Set on the answer to a tail / before_seq request, which reads backwards from the end of the
  // transcript: first_seq is the oldest event it carried, has_older says whether earlier ones remain.
  older?: boolean;
  has_older?: boolean;
  first_seq?: number;
  server_time?: string; // server clock at replay end — for skew-free elapsed times
}

export interface SessionMetaChanged {
  type: "session_meta_changed";
  session_id: string;
  model?: string;
  effort?: string;
}

export interface SessionTitleChanged {
  type: "session_title_changed";
  session_id: string;
  title: string;
}

// Session status values
export type SessionStatus = "starting" | "running" | "idle" | "waiting" | "stopped" | "error" | "disconnected" | "ended";

// ─── Server → Browser message interfaces ──────────────────────────────────────

export interface InitialState {
  type: "initial_state";
  daemons: DaemonInfo[];
  sessions: SessionInfo[];
  messages?: MessageInfo[];
  server_version?: string;
}

export interface DaemonConnected {
  type: "daemon_connected";
  daemon: DaemonInfo;
}

export interface DaemonDisconnected {
  type: "daemon_disconnected";
  daemon_id: string;
  affected_session_ids: string[];
}

export interface SessionStarted {
  type: "session_started";
  session: SessionInfo;
}

export interface SessionOutput {
  type: "session_output";
  session_id: string;
  data: string; // base64-encoded bytes
  seq: number;
}

export interface SessionStateChanged {
  type: "session_state_changed";
  session_id: string;
  status: SessionStatus;
  message?: string | null;
  unread: boolean;
  end_reason?: string;
  ended_by?: EndedBy | null;
}

export interface SessionReadChanged {
  type: "session_read_changed";
  session_id: string;
}

export interface SessionStarChanged {
  type: "session_star_changed";
  session_id: string;
  starred: boolean;
}

export interface SessionEnded {
  type: "session_ended";
  session_id: string;
  exit_code: number;
  signal?: string | null;
  end_reason?: string;
  ended_by?: EndedBy | null;
}

export interface HistoryDone {
  type: "history_done";
  session_id: string;
  cols?: number; // session's current PTY cols; absent for old servers
}

export interface FocusStolen {
  type: "focus_stolen";
  session_id: string;
}

export interface FocusGranted {
  type: "focus_granted";
  session_id: string;
}

export interface SessionScrollback {
  type: "session_scrollback";
  session_id: string;
  data: string; // base64-encoded ANSI text (tmux scrollback); "" means none
  mode_prefix?: string; // base64-encoded mode-sync escape sequences; absent/"" means none
}

export interface MessageCreated {
  type: "message_created";
  message: MessageInfo;
}

export interface MessageAnswered {
  type: "message_answered";
  message: MessageInfo;
}

// ─── Board event interfaces (server → browser) ────────────────────────────────
// Mirrors internal/server/board_events.go

export interface BoardCreated {
  type: "board_created";
  board: Board;
  op_id: string;
}

export interface ColumnChanged {
  type: "column_changed";
  column: Column;
  op_id: string;
}

export interface ColumnRemoved {
  type: "column_removed";
  column_id: string;
  board_id: string;
  op_id: string;
}

export interface TicketCreated {
  type: "ticket_created";
  ticket: Ticket;
  op_id: string;
}

export interface TicketUpdated {
  type: "ticket_updated";
  ticket: Ticket;
  op_id: string;
}

export interface TicketMoved {
  type: "ticket_moved";
  ticket: Ticket;
  from_column_id: string | null;
  to_column_id: string | null;
  op_id: string;
}

export interface TicketSplit {
  type: "ticket_split";
  origin_id: string;
  origin_ticket: Ticket;
  children: Ticket[];
  op_id: string;
}

export interface TicketArchived {
  type: "ticket_archived";
  ticket: Ticket;
  op_id: string;
}

export interface TicketDependencyChanged {
  type: "ticket_dependency_changed";
  ticket_id: string;
  depends_on_ticket_id: string;
  action: "added" | "removed";
  op_id: string;
}

// Discriminated union of all server→browser messages.
export type ServerMessage =
  | InitialState
  | DaemonConnected
  | DaemonDisconnected
  | SessionStarted
  | SessionOutput
  | SessionStateChanged
  | SessionReadChanged
  | SessionStarChanged
  | SessionMetaChanged
  | SessionTitleChanged
  | SessionEnded
  | HistoryDone
  | FocusStolen
  | FocusGranted
  | SessionScrollback
  | MessageCreated
  | MessageAnswered
  | BoardCreated
  | ColumnChanged
  | ColumnRemoved
  | TicketCreated
  | TicketUpdated
  | TicketMoved
  | TicketSplit
  | TicketArchived
  | TicketDependencyChanged
  | AgentEvent
  | AgentEventsReplayDone;

// ─── Browser → Server message interfaces ──────────────────────────────────────

export interface SubscribeSession {
  type: "subscribe_session";
  session_id: string;
}

export interface UnsubscribeSession {
  type: "unsubscribe_session";
  session_id: string;
}

export interface SendInput {
  type: "send_input";
  session_id: string;
  data: string;
}

export interface SpawnSession {
  type: "spawn_session";
  daemon_id: string;
  repo: string;
  title: string;
  initial_prompt: string;
  kind?: string; // "" (tmux) | "agent"
  model?: string;
}

export interface SubscribeAgentEvents {
  type: "subscribe_agent_events";
  session_id: string;
  after_seq?: number;
  limit?: number;
  tail?: number; // the newest N events, instead of the oldest after after_seq
  before_seq?: number; // the limit events just before this seq (the next older page)
}

export interface AgentUserMessage {
  type: "agent_user_message";
  session_id: string;
  text: string;
  source?: string;
}

export interface SetSessionModel {
  type: "set_session_model";
  session_id: string;
  model?: string;
  effort?: string;
}

export interface InterruptSession {
  type: "interrupt_session";
  session_id: string;
}

export interface ResizeSession {
  type: "resize_session";
  session_id: string;
  cols: number;
  rows: number;
}

export interface RequestScrollback {
  type: "request_scrollback";
  session_id: string;
  max_lines?: number; // caps the tmux capture to the last N lines of history
}

// Activity is sent (throttled) on user interaction so the server can tell whether
// anyone is engaged — gating OS push (away) vs in-app toast (active).
export interface Activity {
  type: "activity";
}

export interface MarkSessionRead {
  type: "mark_session_read";
  session_id: string;
}

export interface SetSessionStar {
  type: "set_session_star";
  session_id: string;
  starred: boolean;
}

// ─── Ticket detail (GET /api/tickets/{id}) ────────────────────────────────────

/** Full ticket detail returned by GET /api/tickets/{id}. Adds dep edges. */
export interface TicketDetail extends Ticket {
  depends_on: string[];
  blocks: string[];
}

/** One activity-feed event from GET /api/tickets/{id}/events. */
export interface TicketEvent {
  id: number;
  ticket_id: string;
  type: string;
  actor: string;
  from_column_id: string | null;
  to_column_id: string | null;
  data: unknown;
  created_at: string;
}

// ─── Board subscription messages (browser → server) ───────────────────────────
// Mirrors internal/protocol/messages.go SubscribeBoard / UnsubscribeBoard.

export interface SubscribeBoard {
  type: "subscribe_board";
  board_id: string;
}

export interface UnsubscribeBoard {
  type: "unsubscribe_board";
  board_id: string;
}

// Discriminated union of all browser→server messages.
export type BrowserMessage =
  | SubscribeSession
  | UnsubscribeSession
  | SendInput
  | SpawnSession
  | ResizeSession
  | RequestScrollback
  | Activity
  | MarkSessionRead
  | SetSessionStar
  | SubscribeBoard
  | UnsubscribeBoard
  | SubscribeAgentEvents
  | AgentUserMessage
  | SetSessionModel
  | InterruptSession;

// ─── Crons (runner /api/crons, spec 7.7) ──────────────────────────────────────

export type CronStatus = 'active' | 'disabled' | 'paused' | 'expired'
export type CronRunStatus = 'claimed' | 'started' | 'held' | 'skipped' | 'failed'
export type CronRuntime = 'auto' | 'cluster' | 'docker'

export interface CronRunInfo {
  id: string
  cron_id: string
  scheduled_for: string
  claimed_at: string
  started_at: string | null
  session_id: string | null
  status: CronRunStatus
  reason: string | null
  late: boolean
  manual: boolean
}

// A cron as the server sends it. There is no token id: only when its access token expires.
export interface CronInfo {
  id: string
  name: string
  status: CronStatus
  enabled: boolean
  schedule: string
  timezone: string
  prompt: string
  engine: string
  model: string | null
  effort: string | null
  runtime: CronRuntime
  daemon_id: string | null
  board_id: string | null
  mcp: McpSelectionEntry[]
  token_expires_at: string
  grace_seconds: number
  max_runtime_seconds: number
  next_run_at: string
  last_run_at: string | null
  consecutive_failures: number
  paused_reason: string | null
  last_run: CronRunInfo | null
  connection_problems: string[]
  created_at: string
  updated_at: string
}

// MCP proposals (spec 9): a call the agent asked for on a tool in `propose` mode, frozen until the
// person approves or rejects it. `arguments` is authoritative (exactly what an approval sends);
// `agent_summary` is a convenience line built by the gateway. `result` is untrusted upstream output.
export type ProposalState = 'pending' | 'executing' | 'done' | 'rejected' | 'expired' | 'failed' | 'unknown'
export type ProposalResult = null | { content?: { type: string; text?: string }[]; isError?: boolean } | { error: string }
export interface ProposalInfo {
  id: string
  state: ProposalState
  connection_id: string
  connection_name: string
  tool: string
  arguments: Record<string, unknown>
  // The frozen arguments as the exact stored JSON text (absent on an older server). Shown instead
  // of re-serialising `arguments`, which would change number lexemes.
  arguments_raw?: string
  agent_summary: string
  session_id: string | null
  cron_id: string | null
  created_at: string
  expires_at: string
  decided_at: string | null
  decided_by: string | null
  result: ProposalResult
}
