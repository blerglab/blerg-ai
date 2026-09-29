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
  | 'capabilities';

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
  type: 'agent_events_replay_done';
  session_id: string;
  last_seq: number;
  has_more: boolean;
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
export type SessionStatus = "starting" | "running" | "idle" | "waiting" | "stopped" | "error" | "disconnected";

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

export interface PreviewUpdated {
  type: "preview_updated";
  html: string;
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
  | PreviewUpdated
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
  after_seq: number;
  limit?: number;
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
