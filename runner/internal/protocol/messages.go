// Package protocol defines all WebSocket message types exchanged between
// daemon, server, and browser. No logic lives here — only type definitions.
package protocol

import (
	"encoding/json"

	"github.com/blerglab/blerg-ai/runner/internal/models"
)

// ─── Domain types ────────────────────────────────────────────────────────────

// DaemonInfo describes a connected daemon.
type DaemonInfo struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Mode       string `json:"mode"`
	Version    string `json:"version"`
	ReposRoot  string `json:"repos_root"`
	Status     string `json:"status"`
	LastSeenAt string `json:"last_seen_at"`
	// SandboxAvailable reports whether this daemon can actually run "Local
	// sandbox" sessions right now (the blerg-runner-sandbox image is present)
	// — surfaced in the launch UI so picking that runtime isn't a guess.
	SandboxAvailable bool `json:"sandbox_available"`
	// ClaudeCLIAvailable: the `claude` CLI is on this daemon's PATH, so a
	// Claude agent session on the host runs the user's Claude Code login.
	// AnthropicKeySet: ANTHROPIC_API_KEY is set for the daemon, so the native
	// loop is the fallback. With neither, a host Claude agent session cannot
	// run — the launch UI says so up front.
	ClaudeCLIAvailable bool `json:"claude_cli_available"`
	AnthropicKeySet    bool `json:"anthropic_key_set"`
	// SandboxClaudeCredential: a sandboxed Claude session on this daemon has
	// a credential to run with — a host ~/.claude login to mount, or
	// CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY in the daemon's environment
	// (sandboxClaudeCredentialEnv). false means such a spawn is refused, so
	// the launch sheet warns before Launch. nil (an older daemon) claims
	// nothing either way.
	SandboxClaudeCredential *bool `json:"sandbox_claude_credential,omitempty"`
	// AvailableEngines lists the engine IDs ("claude", "codex", "hermes",
	// "openclaw") that appear configured and usable on this daemon's host
	// right now — best-effort, same caveat as SandboxAvailable. Lets the
	// launch UI default its Engine picker to something that will actually
	// work instead of a client-side guess with no awareness of the daemon.
	AvailableEngines []string `json:"available_engines"`
	// CloneFrom: the daemon can clone a repository named by owner/name or URL
	// into its repos root (POST /api/sessions "clone"/"git_url"). False for
	// an older daemon, which the launch UI then offers only its folders and
	// listed repositories.
	CloneFrom bool `json:"clone_from"`
	// AllowHostCredentialClone: a This machine session on this daemon may
	// clone a private repository with the person's own token (see
	// DaemonHello.AllowHostCredentialClone). False: only Local sandbox can.
	AllowHostCredentialClone bool `json:"allow_host_credential_clone"`
}

// ClusterStatus summarizes the k8s cluster-runtime's configuration and live
// state for the status dashboard and the launch UI's engine gating. Never
// carries secret values — only key presence (AvailableEngines) and
// non-secret config, all safe to show in a browser.
type ClusterStatus struct {
	// Configured is false when this server has no cluster runtime at all
	// (not running in k8s, or BLERG_RUNNER_AGENT_IMAGE unset) — every other
	// field is the zero value in that case.
	Configured bool   `json:"configured"`
	Namespace  string `json:"namespace,omitempty"`
	Image      string `json:"image,omitempty"`
	// DaemonID is the fixed synthetic daemon_id a cluster session is recorded
	// under only until its pod actually connects (each session pod then
	// registers as its own ephemeral per-session daemon, overwriting this).
	// Not useful for listing sessions — use GET /api/sessions?runtime=cluster
	// instead, which matches every cluster session regardless of which
	// per-pod daemon currently owns it.
	DaemonID string `json:"daemon_id,omitempty"`
	// MaxSessions is the concurrent cluster session cap (BLERG_RUNNER_MAX_SESSIONS).
	MaxSessions int `json:"max_sessions,omitempty"`
	// ActiveSessions is the live count of non-terminal session Jobs, from a
	// real-time k8s API call. -1 means the call failed (RBAC, connectivity)
	// — "couldn't tell", not "zero".
	ActiveSessions int `json:"active_sessions"`
	// AvailableEngines lists the engine IDs whose credential Secret key is
	// actually present right now — see JobManager.AvailableEngines. OpenClaw
	// never appears here: it isn't wired into cluster sessions at all.
	AvailableEngines []string `json:"available_engines"`
	// GitConfigured reports whether the operator Secret carries a shared git
	// token (BLERG_RUNNER_GIT_TOKEN). A user who has connected their own
	// GitHub credential does not need it; a user who has not can only clone
	// when it is present. Never the token itself — only its presence.
	GitConfigured           bool   `json:"git_configured"`
	SecretName              string `json:"secret_name,omitempty"`
	OAuthSecretName         string `json:"oauth_secret_name,omitempty"`
	CPURequest              string `json:"cpu_request,omitempty"`
	MemRequest              string `json:"mem_request,omitempty"`
	CPULimit                string `json:"cpu_limit,omitempty"`
	MemLimit                string `json:"mem_limit,omitempty"`
	PodTTLSeconds           int64  `json:"pod_ttl_seconds,omitempty"`
	TerminationGraceSeconds int64  `json:"termination_grace_seconds,omitempty"`
	TTLSecondsAfterFinished int64  `json:"ttl_seconds_after_finished,omitempty"`
}

// MyCredentials reports which personal credentials the calling account has
// stored in blerg-core — presence only, never a value. Engines are the agent
// engine ids ("claude", "codex", …); git-provider credentials ("github",
// "gitlab", … — every kind gitprovider.Default registers) are reported
// separately as Git/GitProviders because the UI treats them differently
// (they gate cloning and repo listing, not engine choice).
type MyCredentials struct {
	// Engines is never nil, so it serialises as [] rather than null.
	Engines []string `json:"engines"`
	// Git is true when the account holds a token for any git provider;
	// GitProviders names which ("github", "gitlab", …). Never nil.
	Git          bool     `json:"git"`
	GitProviders []string `json:"git_providers"`
	// Unavailable is true when core could not be asked at all (not
	// configured, unreachable, or an error response) — "unknown", as
	// distinct from a definite "none configured", which is an empty
	// Engines with Git false and no Unavailable.
	Unavailable bool `json:"unavailable,omitempty"`
}

// SessionInfo describes a terminal or agent session.
type SessionInfo struct {
	ID              string  `json:"id"`
	DaemonID        string  `json:"daemon_id"`
	Status          string  `json:"status"`
	ProjectPath     string  `json:"project_path"`
	Repo            string  `json:"repo"`
	Title           string  `json:"title"`
	Model           string  `json:"model,omitempty"`
	Engine          string  `json:"engine,omitempty"` // "" (claude) | "codex" | "hermes" | "openclaw"
	Effort          string  `json:"effort,omitempty"`
	StartedAt       string  `json:"started_at"`
	EndedAt         *string `json:"ended_at,omitempty"`
	Unread          bool    `json:"unread"`
	Starred         bool    `json:"starred"`
	Kind            string  `json:"kind,omitempty"` // "" | "tmux" | "agent"
	ParentSessionID *string `json:"parent_session_id,omitempty"`
	Runtime         string  `json:"runtime,omitempty"` // "daemon" | "docker" | "cluster"
	SkipPermissions bool    `json:"skip_permissions"`
	ErrorReason     string  `json:"error_reason,omitempty"` // filled by Task 6; declared here so the row/info shapes change once
	// EndReason is why the session ended — a code from a fixed vocabulary
	// (db/session_end.go) — and EndedBy the actor that ended it, when one did.
	// Both absent for a live session and for rows ended before migration 018.
	EndReason string   `json:"end_reason,omitempty"`
	EndedBy   *EndedBy `json:"ended_by,omitempty"`
}

// EndedBy is who ended a session: the principal kind ("human", "agent",
// "service", "runner_key") and, for the browser only, whether that actor is —
// or acts for — the viewer's own account. The account itself is never sent:
// the server compares it with the viewer and sends the answer.
type EndedBy struct {
	Kind string `json:"kind"`
	Self bool   `json:"self,omitempty"`
}

// ─── Daemon → Server (/ws/daemon) ────────────────────────────────────────────

// DaemonHello is the first message a daemon sends upon connecting.
type DaemonHello struct {
	Type            string   `json:"type"` // "daemon_hello"
	Name            string   `json:"name"`
	Mode            string   `json:"mode"`
	ReposRoot       string   `json:"repos_root"`
	Token           string   `json:"token"`
	ProtocolVersion string   `json:"protocol_version"`
	Version         string   `json:"version"`
	ActiveSessions  []string `json:"active_sessions"`
	CheckedOutRepos []string `json:"checked_out_repos"` // repo names present on disk in repos_root
	// RepoRemotes maps a CheckedOutRepos name to the GitHub "org/name" its
	// origin remote points at. Only recognisable github.com remotes appear;
	// a folder with no origin, another host, or an unparseable URL is simply
	// absent (unknown), never guessed. A folder's name can differ from its
	// remote's repository name, which is why this can't be derived server-side.
	//
	// Legacy, GitHub only: kept so a server that predates providers still
	// learns GitHub remotes (it reads every entry as GitHub, so nothing
	// hosted elsewhere may appear here). RepoOrigins supersedes it.
	RepoRemotes map[string]string `json:"repo_remotes,omitempty"`
	// RepoOrigins maps a CheckedOutRepos name to the hosted repository its
	// origin remote points at on any registered git provider (GitHub,
	// GitLab, …) — the same rules as RepoRemotes, with the provider carried.
	// A server that receives this field uses it and ignores RepoRemotes;
	// when it is absent (an older daemon, or nothing resolved) the server
	// falls back to RepoRemotes as GitHub.
	RepoOrigins map[string]RepoOrigin `json:"repo_origins,omitempty"`
	// GitProviders lists the git providers (gitprovider IDs) this daemon can
	// clone from when a spawn names one (SpawnSession.Provider). Absent from
	// daemons that predate providers, which clone from GitHub only.
	GitProviders []string `json:"git_providers,omitempty"`
	// CloneFrom: this daemon honours SpawnSession.CloneFrom (clone a named
	// hosted repository into a named folder, over HTTPS with an optional
	// GitToken). Absent from older daemons, which would ignore CloneFrom and
	// treat the folder name as a repository to clone from GitHub — so the
	// server never sends CloneFrom to a daemon that does not say this.
	CloneFrom bool `json:"clone_from,omitempty"`
	// AllowHostCredentialClone: this daemon's owner allows a named clone to
	// use the launching person's own git token on the bare host (a This
	// machine session). Without it the server sends a token only for a
	// Local sandbox session, whose clone runs inside the sandbox image: on
	// the host any other unsandboxed session could read it from git's
	// environment. Meant only for a daemon nobody else uses.
	AllowHostCredentialClone bool `json:"allow_host_credential_clone,omitempty"`
	// SandboxAvailable reports whether blerg-runner-sandbox:latest is present
	// on this host — see DaemonInfo.SandboxAvailable.
	SandboxAvailable bool `json:"sandbox_available"`
	// ClaudeCLIAvailable: the `claude` CLI is on this daemon's PATH, so a
	// Claude agent session on the host runs the user's Claude Code login.
	// AnthropicKeySet: ANTHROPIC_API_KEY is set for the daemon, so the native
	// loop is the fallback. With neither, a host Claude agent session cannot
	// run — the launch UI says so up front.
	ClaudeCLIAvailable bool `json:"claude_cli_available"`
	AnthropicKeySet    bool `json:"anthropic_key_set"`
	// SandboxClaudeCredential: a sandboxed Claude session on this daemon has
	// a credential to run with — a host ~/.claude login to mount, or
	// CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY in the daemon's environment
	// (sandboxClaudeCredentialEnv). false means such a spawn is refused, so
	// the launch sheet warns before Launch. nil (an older daemon) claims
	// nothing either way.
	SandboxClaudeCredential *bool `json:"sandbox_claude_credential,omitempty"`
	// AvailableEngines — see DaemonInfo.AvailableEngines.
	AvailableEngines []string `json:"available_engines"`
	// EngineModels is the model lists this daemon probed from its engines'
	// own CLIs or endpoints (the EngineSpec.ListModels hooks), keyed by engine
	// id, one entry per engine that has a probe — an empty Models means
	// "probed, nothing to offer". When present it is the full set and
	// replaces whatever the server held for this daemon. A hello carries it
	// once any probe has finished; a heartbeat only when a list changed since
	// the last message on this connection (absent = unchanged). The server
	// re-validates every entry (models.SanitizeReports): the daemon is
	// authenticated, but a list is still read from an engine's output.
	EngineModels map[string]models.Report `json:"engine_models,omitempty"`
}

// RepoOrigin names a checked-out folder's hosted repository: the git
// provider (a gitprovider ID — also the credential kind a personal token for
// it is stored under) and "owner/name" on it.
type RepoOrigin struct {
	Provider string `json:"provider"`
	FullName string `json:"full_name"`
}

// DaemonHeartbeat is a periodic keep-alive from the daemon. It includes the
// current active session IDs so the server can mark orphaned sessions stopped,
// and each session's current detector state so the server can self-heal from a
// dropped session_state_changed event (a lost "idle" would otherwise leave the
// session stuck on "running" forever).
type DaemonHeartbeat struct {
	Type           string            `json:"type"`                     // "daemon_heartbeat"
	ActiveSessions []string          `json:"active_sessions"`          // IDs of currently live sessions
	SessionStates  map[string]string `json:"session_states,omitempty"` // sessionID → "running"|"idle"|"waiting"
	// SandboxAvailable reports whether blerg-runner-sandbox:latest is present
	// on this host — see DaemonInfo.SandboxAvailable. Refreshed every
	// heartbeat so a manually-removed image doesn't wait for a reconnect.
	SandboxAvailable bool `json:"sandbox_available"`
	// ClaudeCLIAvailable: the `claude` CLI is on this daemon's PATH, so a
	// Claude agent session on the host runs the user's Claude Code login.
	// AnthropicKeySet: ANTHROPIC_API_KEY is set for the daemon, so the native
	// loop is the fallback. With neither, a host Claude agent session cannot
	// run — the launch UI says so up front.
	ClaudeCLIAvailable bool `json:"claude_cli_available"`
	AnthropicKeySet    bool `json:"anthropic_key_set"`
	// SandboxClaudeCredential: a sandboxed Claude session on this daemon has
	// a credential to run with — a host ~/.claude login to mount, or
	// CLAUDE_CODE_OAUTH_TOKEN / ANTHROPIC_API_KEY in the daemon's environment
	// (sandboxClaudeCredentialEnv). false means such a spawn is refused, so
	// the launch sheet warns before Launch. nil (an older daemon) claims
	// nothing either way.
	SandboxClaudeCredential *bool `json:"sandbox_claude_credential,omitempty"`
	// CheckedOutRepos refreshes DaemonHello's field of the same name every
	// heartbeat — a hello-only snapshot meant a repo checked out after the
	// daemon started stayed invisible in the launch UI until a reconnect.
	CheckedOutRepos []string `json:"checked_out_repos"`
	// RepoRemotes refreshes DaemonHello's field of the same name every
	// heartbeat, alongside CheckedOutRepos.
	RepoRemotes map[string]string `json:"repo_remotes,omitempty"`
	// RepoOrigins — see DaemonHello.RepoOrigins; refreshed alongside.
	RepoOrigins map[string]RepoOrigin `json:"repo_origins,omitempty"`
	// AvailableEngines refreshes DaemonHello's field of the same name every
	// heartbeat — see DaemonInfo.AvailableEngines.
	AvailableEngines []string `json:"available_engines"`
	// EngineModels — see DaemonHello.EngineModels. Only sent when changed.
	EngineModels map[string]models.Report `json:"engine_models,omitempty"`
	// ReposRoot refreshes DaemonHello's field of the same name: the owner can
	// change it from the app (set_repos_root) without a reconnect. Absent
	// from older daemons, meaning unchanged.
	ReposRoot string `json:"repos_root,omitempty"`
}

// ReposRootResult answers a SetReposRoot (daemon → server). OK with the root
// now in effect, or not OK with a reason a person can act on — in which case
// nothing changed. On success CheckedOutRepos lists the folders under the new
// root, so the launch UI's repo list is right straight away; the folders'
// remotes follow in a heartbeat the daemon sends right after.
type ReposRootResult struct {
	Type            string   `json:"type"` // "repos_root_result"
	RequestID       string   `json:"request_id"`
	OK              bool     `json:"ok"`
	ReposRoot       string   `json:"repos_root,omitempty"`
	Error           string   `json:"error,omitempty"`
	CheckedOutRepos []string `json:"checked_out_repos,omitempty"`
}

// SessionStarted is sent when the daemon has spawned a new session.
type SessionStarted struct {
	Type        string `json:"type"` // "session_started"
	SessionID   string `json:"session_id"`
	ProjectPath string `json:"project_path"`
	Repo        string `json:"repo"`
	Title       string `json:"title"`
	Model       string `json:"model,omitempty"`
	Cols        int    `json:"cols"`
	Rows        int    `json:"rows"`
	Kind        string `json:"kind,omitempty"` // "" (tmux) | "agent"
}

// SessionOutput carries terminal output from a running session.
type SessionOutput struct {
	Type      string `json:"type"` // "session_output"
	SessionID string `json:"session_id"`
	Data      string `json:"data"` // base64-encoded bytes
	Seq       int64  `json:"seq"`
}

// SessionStateChanged notifies that a session's status has changed.
type SessionStateChanged struct {
	Type      string  `json:"type"` // "session_state_changed"
	SessionID string  `json:"session_id"`
	Status    string  `json:"status"`
	Message   *string `json:"message,omitempty"`
	Unread    bool    `json:"unread"`
	// EndReason / EndedBy ride along on a terminal status so an open detail
	// view can say why without a reload. Filled by the server from the row,
	// never taken from what a daemon sent.
	EndReason string   `json:"end_reason,omitempty"`
	EndedBy   *EndedBy `json:"ended_by,omitempty"`
}

// SessionMetaChanged notifies that a session's model or effort has changed.
// Sent by the daemon when it detects a /model or /effort command in PTY output,
// and forwarded by the server to all connected browsers.
type SessionMetaChanged struct {
	Type      string `json:"type"` // "session_meta_changed"
	SessionID string `json:"session_id"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
}

// SessionTitleChanged notifies that a session's title has changed.
type SessionTitleChanged struct {
	Type      string `json:"type"` // "session_title_changed"
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
}

// SessionEnded is sent when a session's process exits.
type SessionEnded struct {
	Type      string  `json:"type"` // "session_ended"
	SessionID string  `json:"session_id"`
	ExitCode  int     `json:"exit_code"`
	Signal    *string `json:"signal,omitempty"`
	// Server → browser only (as BrowserSessionEnded), filled from the row:
	// why the session ended and who ended it. See SessionStateChanged.
	EndReason string   `json:"end_reason,omitempty"`
	EndedBy   *EndedBy `json:"ended_by,omitempty"`
}

// ─── Server → Daemon (/ws/daemon) ────────────────────────────────────────────

// SpawnSession instructs the daemon to start a new session.
type SpawnSession struct {
	Type          string `json:"type"` // "spawn_session"
	SessionID     string `json:"session_id"`
	ProjectPath   string `json:"project_path"`
	Repo          string `json:"repo"`
	Title         string `json:"title"`
	Cols          int    `json:"cols"`
	Rows          int    `json:"rows"`
	InitialPrompt string `json:"initial_prompt"`
	Model         string `json:"model,omitempty"`
	// Effort is the launch reasoning effort for a Claude session ("" = the
	// model's default); one of models.Efforts. Other engines ignore it.
	Effort                     string `json:"effort,omitempty"`
	DangerouslySkipPermissions bool   `json:"dangerously_skip_permissions,omitempty"`
	NewRepo                    bool   `json:"new_repo,omitempty"`
	// NoRepo marks a "No repository" session: Repo is then a scratch folder
	// name (see internal/scratch) to create empty, never clone. The server
	// sends NewRepo with it, so a daemon that predates this field still just
	// creates the folder; a current daemon also refuses a Repo that is not a
	// scratch name.
	NoRepo bool `json:"no_repo,omitempty"`
	// Assist fields: when Assist is true the session is an AI-driven ticket agent.
	// The daemon injects board/token vars.
	Assist     bool   `json:"assist,omitempty"`
	BoardID    string `json:"board_id,omitempty"`
	TicketID   string `json:"ticket_id,omitempty"`
	BoardToken string `json:"board_token,omitempty"`
	// SessionToken is the per-session messaging token (board_tokens row with
	// no board, "message" capability) the server mints on every daemon-routed
	// spawn. The daemon exports it as BLERG_RUNNER_SESSION_TOKEN; the master
	// daemon token is never in any session's environment.
	SessionToken string `json:"session_token,omitempty"`
	// ExtraEnv is caller-supplied env for the session (board-driven starts);
	// never BLERG_RUNNER_* or the engine credential.
	ExtraEnv map[string]string `json:"extra_env,omitempty"`
	Kind     string            `json:"kind,omitempty"` // "" (tmux) | "agent"
	// Sandbox runs the session's tmux server inside a throwaway Docker
	// container (mounting only the target repo) instead of directly on the
	// daemon host. Daemon-routed like the default runtime; the daemon itself
	// decides how to spawn, no server-side involvement (unlike "cluster").
	Sandbox bool `json:"sandbox,omitempty"`
	// Engine picks which CLI actually drives the session: "" (default) is
	// Claude Code, "codex" is OpenAI's Codex CLI, "hermes" is Nous Research's
	// Hermes Agent CLI. Orthogonal to Runtime/Sandbox — any engine can run
	// bare-host or sandboxed.
	Engine string `json:"engine,omitempty"`
	// Provider is the git provider (gitprovider ID) Repo lives on, when the
	// caller named one. A daemon clones an absent folder from that provider's
	// host and never from GitHub; empty keeps the legacy GitHub default for
	// callers that predate providers. The server only sends a non-GitHub
	// provider to a daemon that lists it in DaemonHello.GitProviders — an
	// older daemon would ignore the field and clone from GitHub.
	Provider string `json:"provider,omitempty"`
	// CloneFrom, when set, is the hosted repository ("owner/name" on
	// Provider, which is then required) to clone into the folder Repo under
	// the repos root — a repository the caller named that need not be
	// checked out anywhere or listed. Repo is then a single folder name. An
	// existing folder is used only when its origin is this repository; any
	// other existing folder is refused, never cloned into or over. Only sent
	// to a daemon whose hello set CloneFrom.
	CloneFrom string `json:"clone_from,omitempty"`
	// GitToken is the launching account's own personal token for Provider,
	// fetched by the server from blerg-core, for a CloneFrom clone of a
	// private repository. Empty for a public clone, or when the account has
	// none. The daemon uses it for that one clone only, sends it to
	// Provider's own host only, and never writes it anywhere: not to a
	// session record, not to the clone's .git/config, not to a log.
	GitToken string `json:"git_token,omitempty"`
}

// Activity is sent by a browser (throttled) on user interaction so the server
// can tell whether anyone is engaged — used to choose between an in-app toast
// (active) and an OS push notification (away).
type Activity struct {
	Type string `json:"type"` // "activity"
}

// SendInput forwards user keystrokes to a session.
type SendInput struct {
	Type      string `json:"type"` // "send_input"
	SessionID string `json:"session_id"`
	Data      string `json:"data"`
}

// KillSession asks the daemon to terminate a session.
type KillSession struct {
	Type      string `json:"type"` // "kill_session"
	SessionID string `json:"session_id"`
}

// InjectNoteReply is sent from the server to the daemon to inject a note reply
// into a session's PTY. The daemon gates injection on classifyScreen == "idle";
// otherwise it queues it in a single-slot pending map (latest-wins).
type InjectNoteReply struct {
	Type      string `json:"type"` // "inject_note_reply"
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

// ResizeSession asks the daemon to resize a session's pseudo-terminal.
type ResizeSession struct {
	Type      string `json:"type"` // "resize_session"
	SessionID string `json:"session_id"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

// SetReposRoot asks a daemon to move its repos root — where new sessions'
// checkouts live — to ReposRoot (server → daemon; the browser asks via
// PUT /api/daemons/{id}/repos-root). The daemon validates it (it is the one
// that can see the filesystem), saves it so it survives a restart, and
// answers with a ReposRootResult carrying the same RequestID. Sessions
// already running keep the paths they started in.
type SetReposRoot struct {
	Type      string `json:"type"` // "set_repos_root"
	RequestID string `json:"request_id"`
	ReposRoot string `json:"repos_root"`
}

// ─── Server → Browser (/ws/browser) ──────────────────────────────────────────

// InitialState is the first message delivered to a newly connected browser.
type InitialState struct {
	Type          string        `json:"type"` // "initial_state"
	Daemons       []DaemonInfo  `json:"daemons"`
	Sessions      []SessionInfo `json:"sessions"`
	Messages      []MessageInfo `json:"messages"`
	ServerVersion string        `json:"server_version"`
}

// DaemonConnected notifies the browser that a daemon came online.
type DaemonConnected struct {
	Type   string     `json:"type"` // "daemon_connected"
	Daemon DaemonInfo `json:"daemon"`
}

// DaemonDisconnected notifies the browser that a daemon went offline.
type DaemonDisconnected struct {
	Type               string   `json:"type"` // "daemon_disconnected"
	DaemonID           string   `json:"daemon_id"`
	AffectedSessionIDs []string `json:"affected_session_ids"`
}

// BrowserSessionStarted notifies the browser that a session was created.
type BrowserSessionStarted struct {
	Type    string      `json:"type"` // "session_started"
	Session SessionInfo `json:"session"`
}

// BrowserSessionOutput carries terminal output to the browser.
// (Shares the same wire type as SessionOutput; aliased for clarity.)
type BrowserSessionOutput = SessionOutput

// BrowserSessionStateChanged carries a state change notification to the browser.
// (Shares the same wire type as SessionStateChanged.)
type BrowserSessionStateChanged = SessionStateChanged

// BrowserSessionEnded carries a session-ended notification to the browser.
// (Shares the same wire type as SessionEnded.)
type BrowserSessionEnded = SessionEnded

// PreviewUpdated delivers an updated HTML preview to the browser.
type PreviewUpdated struct {
	Type string `json:"type"` // "preview_updated"
	HTML string `json:"html"`
}

// HistoryDone is sent to the browser after history replay is complete for a
// subscribe_session. The browser uses it to trigger a resize, ensuring the
// SIGWINCH-triggered PTY redraw happens after history is rendered, not before.
// Cols carries the session's current PTY width so the browser can detect a
// width mismatch (desktop history replayed on a narrow mobile terminal) and
// discard the garbled scrollback before requesting a fresh tmux repaint.
type HistoryDone struct {
	Type      string `json:"type"` // "history_done"
	SessionID string `json:"session_id"`
	Cols      int    `json:"cols,omitempty"`
}

// FocusStolen tells a browser that another device took over this session.
type FocusStolen struct {
	Type      string `json:"type"` // "focus_stolen"
	SessionID string `json:"session_id"`
}

// FocusGranted tells a browser it is now the active device for this session
// (e.g. auto-promoted after the previous active device left).
type FocusGranted struct {
	Type      string `json:"type"` // "focus_granted"
	SessionID string `json:"session_id"`
}

// SessionScrollback carries the session's tmux scrollback (the lines that have
// scrolled above the visible screen) so the browser can render real, scrollable
// history. Sourced authoritatively from `tmux capture-pane` on the daemon —
// unlike replaying the raw PTY byte stream, this works even for full-screen /
// alt-screen TUIs that redraw via absolute positioning. Data is base64-encoded
// text WITH SGR escape sequences (captured via `capture-pane -e`).
// Flows daemon → server → browser.
type SessionScrollback struct {
	Type      string `json:"type"` // "session_scrollback"
	SessionID string `json:"session_id"`
	Data      string `json:"data"` // base64-encoded ANSI text; "" means no scrollback
	// ModePrefix is base64-encoded escape sequences reflecting the pane's
	// current modes (application-cursor-keys, cursor visibility) at capture
	// time. The client writes it into the live terminal before applying Data,
	// so the terminal's mode state matches the pane's even though replaying
	// history doesn't naturally re-derive it. "" means no mode-sync needed
	// (e.g. an alt-screen pane, which repaints fully on its own).
	ModePrefix string `json:"mode_prefix,omitempty"`
}

// SessionReadChanged notifies all browsers that a session's unread flag was
// cleared (a browser called mark_session_read). Unread is implicitly false.
type SessionReadChanged struct {
	Type      string `json:"type"` // "session_read_changed"
	SessionID string `json:"session_id"`
}

// ─── Browser → Server (/ws/browser) ──────────────────────────────────────────

// SubscribeSession asks the server to start streaming output for a session.
type SubscribeSession struct {
	Type      string `json:"type"` // "subscribe_session"
	SessionID string `json:"session_id"`
}

// UnsubscribeSession asks the server to stop streaming output for a session.
type UnsubscribeSession struct {
	Type      string `json:"type"` // "unsubscribe_session"
	SessionID string `json:"session_id"`
}

// BrowserSendInput forwards user keystrokes from the browser.
// (Same wire shape as SendInput; aliased for clarity.)
type BrowserSendInput = SendInput

// BrowserSpawnSession asks the server to create a new session via a daemon.
type BrowserSpawnSession struct {
	Type          string `json:"type"` // "spawn_session"
	DaemonID      string `json:"daemon_id"`
	Repo          string `json:"repo"`
	Title         string `json:"title"`
	InitialPrompt string `json:"initial_prompt"`
	// Assist fields: forwarded to the daemon's SpawnSession.
	Assist   bool   `json:"assist,omitempty"`
	BoardID  string `json:"board_id,omitempty"`
	TicketID string `json:"ticket_id,omitempty"`
	Kind     string `json:"kind,omitempty"` // "" (tmux) | "agent"
	Model    string `json:"model,omitempty"`
}

// BrowserResizeSession asks the server to resize a session.
// (Same wire shape as ResizeSession; aliased for clarity.)
type BrowserResizeSession = ResizeSession

// RequestScrollback asks for the session's current tmux scrollback. The server
// forwards it to the owning daemon, which replies with a SessionScrollback.
// The browser sends this after history replay and to refresh history as the
// session produces more output. Flows browser → server → daemon.
type RequestScrollback struct {
	Type      string `json:"type"` // "request_scrollback"
	SessionID string `json:"session_id"`
	// MaxLines bounds the tmux capture to the last MaxLines lines of history
	// (0 means "no bound", the old full-history behavior). Set by the browser
	// for a cheap incremental refresh instead of re-pulling everything.
	MaxLines int `json:"max_lines,omitempty"`
}

// MarkSessionRead asks the server to clear the unread flag for a session and
// broadcast a session_read_changed event to all browsers.
type MarkSessionRead struct {
	Type      string `json:"type"` // "mark_session_read"
	SessionID string `json:"session_id"`
}

// SetSessionStar asks the server to set or clear the starred flag for a session.
type SetSessionStar struct {
	Type      string `json:"type"` // "set_session_star"
	SessionID string `json:"session_id"`
	Starred   bool   `json:"starred"`
}

// SessionStarChanged notifies all browsers that a session's starred flag changed.
type SessionStarChanged struct {
	Type      string `json:"type"` // "session_star_changed"
	SessionID string `json:"session_id"`
	Starred   bool   `json:"starred"`
}

// ─── Board subscription (Browser → Server) ───────────────────────────────────

// SubscribeBoard asks the server to start delivering board-change events for
// the given board. The browser will receive events (board_created,
// column_changed, ticket_created, etc.) for as long as it is subscribed.
type SubscribeBoard struct {
	Type    string `json:"type"` // "subscribe_board"
	BoardID string `json:"board_id"`
}

// UnsubscribeBoard asks the server to stop delivering board-change events.
type UnsubscribeBoard struct {
	Type    string `json:"type"` // "unsubscribe_board"
	BoardID string `json:"board_id"`
}

// MessageInfo is a session→user message as seen by the UI. Answer/AnsweredAt are
// pointers because answer is overloaded: nil (an update), "" (closed/expired/ended),
// or real text (a reply).
type MessageInfo struct {
	ID         string  `json:"id"`
	SessionID  string  `json:"session_id"`
	Kind       string  `json:"kind"` // "update" | "ask" | "note"
	Body       string  `json:"body"`
	Status     string  `json:"status"` // "open" | "answered"
	Answer     *string `json:"answer,omitempty"`
	CreatedAt  string  `json:"created_at"`
	AnsweredAt *string `json:"answered_at,omitempty"`
}

// ─── Agent sessions ──────────────────────────────────────────────────────────

// StartStageKind is the agent_event kind that reports how far a session has
// got on its way to ready (see StartStagePayload). It is persisted like every
// other agent event, so a browser that reloads mid-start replays it.
const StartStageKind = "start_stage"

// Start stage states. A stage is pending until something starts it, active
// while it is in progress, then done or failed. failed is not necessarily
// final: a pod that could not be scheduled may still schedule later, and a
// later update moves the stage on.
const (
	StageStatePending = "pending"
	StageStateActive  = "active"
	StageStateDone    = "done"
	StageStateFailed  = "failed"
	// StageStateWarning is a stage that is over but not as asked — an optional
	// step skipped or only partly done (always-on plugins). It never fails the
	// start and is not folded to done when later stages progress.
	StageStateWarning = "warning"
)

// Start stage ids. They are data, not UI branches: a runtime's plan is just
// an ordered list of these, and a viewer renders whatever list it is given.
const (
	StageQueued   = "queued"   // session row + (cluster) Job being created
	StageSchedule = "schedule" // cluster: waiting for a node
	StageImage    = "image"    // cluster: pulling the image / creating the container
	StageDaemon   = "daemon"   // daemon: the daemon picking the spawn up
	StageSandbox  = "sandbox"  // daemon, Docker runtime: the sandbox container
	StageConnect  = "connect"  // cluster: the pod dialling the server
	StageClone    = "clone"    // cluster: config bundle + git clone
	StagePlugins  = "plugins"  // cluster, claude: the always-on plugins being installed
	StageEngine   = "engine"   // the agent engine starting
	StageReady    = "ready"    // the session is live
)

// StartStage is one step of a session's start. Label, Detail and Hint are
// human text written by whoever knows the stage (server or pod); Hint is the
// next step to take when the stage has failed or is stuck.
type StartStage struct {
	ID     string `json:"id"`
	Label  string `json:"label,omitempty"`
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

// StartStagePayload is the payload of a start_stage event. Plan=true opens a
// new start attempt (a fresh start or a resume) and Stages is then the whole
// ordered plan; otherwise Stages are updates merged by id into the current
// attempt. A viewer applies them in seq order, and treats every stage before
// the furthest active/done one as done.
type StartStagePayload struct {
	Plan    bool         `json:"plan,omitempty"`
	Runtime string       `json:"runtime,omitempty"`
	Stages  []StartStage `json:"stages"`
}

// AgentEvent carries one structured transcript event from a daemon-hosted
// agent loop. Seq is 0 from the daemon; the SERVER assigns it on append and
// echoes it in AgentEventAck and in the browser broadcast.
type AgentEvent struct {
	Type          string          `json:"type"` // "agent_event"
	SessionID     string          `json:"session_id"`
	ClientEventID string          `json:"client_event_id"`
	Seq           int64           `json:"seq,omitempty"`
	Ts            string          `json:"ts"`
	Kind          string          `json:"kind"`
	Payload       json.RawMessage `json:"payload"`
	Transient     bool            `json:"transient,omitempty"` // deltas: not persisted/acked
}

// AgentEventAck confirms persistence of an agent event (server → daemon).
type AgentEventAck struct {
	Type          string `json:"type"` // "agent_event_ack"
	SessionID     string `json:"session_id"`
	ClientEventID string `json:"client_event_id"`
	Seq           int64  `json:"seq"`
}

// AgentUserMessage delivers a user message to an agent session
// (browser → server → daemon).
type AgentUserMessage struct {
	Type      string `json:"type"` // "agent_user_message"
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
	Source    string `json:"source,omitempty"` // "" → "chat"
}

// SetSessionModel changes an agent session's model/effort
// (browser → server → daemon).
type SetSessionModel struct {
	Type      string `json:"type"` // "set_session_model"
	SessionID string `json:"session_id"`
	Model     string `json:"model,omitempty"`
	Effort    string `json:"effort,omitempty"`
}

// InterruptSession cancels an agent session's in-flight turn
// (browser → server → daemon).
type InterruptSession struct {
	Type      string `json:"type"` // "interrupt_session"
	SessionID string `json:"session_id"`
}

// SubscribeAgentEvents requests replay + live stream of an agent session's
// transcript (browser → server). Replay returns events with seq > AfterSeq,
// capped at Limit (server default 200, max 500).
type SubscribeAgentEvents struct {
	Type      string `json:"type"` // "subscribe_agent_events"
	SessionID string `json:"session_id"`
	AfterSeq  int64  `json:"after_seq"`
	Limit     int    `json:"limit,omitempty"`
}

// AgentEventsReplayDone marks the end of a replay batch (server → browser).
type AgentEventsReplayDone struct {
	Type      string `json:"type"` // "agent_events_replay_done"
	SessionID string `json:"session_id"`
	LastSeq   int64  `json:"last_seq"`
	HasMore   bool   `json:"has_more"`
	// ServerTime is the server's clock when the replay finished (RFC 3339,
	// ms). Event ts values are server time; a browser measures "how long
	// ago" against this, not its own possibly-skewed clock.
	ServerTime string `json:"server_time,omitempty"`
}

// MessageCreated notifies all browsers of a new session→user message.
type MessageCreated struct {
	Type    string      `json:"type"` // "message_created"
	Message MessageInfo `json:"message"`
}

// MessageAnswered notifies all browsers that a message was answered/closed.
type MessageAnswered struct {
	Type    string      `json:"type"` // "message_answered"
	Message MessageInfo `json:"message"`
}
