// Package server implements the WebSocket hub that brokers messages between
// connected daemons and browser clients.
package server

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/gitremote"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// BrowserConn represents a connected browser client.
type BrowserConn struct {
	ID string
	// AccountID is the verified account id (identity.Principal.Sub) of the
	// human behind this socket, captured at upgrade time from the same token
	// that authorized it. Empty only for call sites with no identity gate
	// (tests). It is what lets a socket-driven action decide whether the
	// requester is the account a session was launched under — see
	// resumeClusterSession.
	AccountID string
	// SessionID is the `sid` of the token that authorized the socket: the browser
	// session core checks when this socket's resume needs the launcher's credentials.
	SessionID string
	send      chan []byte
	ctx       context.Context
	cancel    context.CancelFunc

	subMu sync.Mutex
	subs  map[string]context.CancelFunc // sessionID → cancel func for forwarder goroutine
}

// DaemonConn represents a connected daemon.
type DaemonConn struct {
	ID   string
	Name string
	Mode string
	// ReposRoot is the root the daemon reported in its hello. The owner can
	// change it live (set_repos_root), so read CurrentReposRoot instead.
	ReposRoot string
	Version   string
	send      chan []byte

	// liveMu guards the fields below: unlike the ones above (set once at
	// Register and read-only thereafter), these are refreshed on every
	// heartbeat — a manually-removed sandbox image, or a repo cloned after
	// the daemon was already running, are reflected without waiting for a
	// reconnect (a hello-only snapshot bit a real user: repos checked out
	// before AND after daemon start both stayed invisible until restart).
	liveMu           sync.RWMutex
	checkedOutRepos  []string                       // repo names present on disk at ReposRoot
	repoRemotes      map[string]protocol.RepoOrigin // checked-out name → provider + "owner/name" of its origin (known ones only)
	gitProviders     []string                       // providers the daemon can clone from (hello); nil = GitHub only
	cloneFrom        bool                           // honours SpawnSession.CloneFrom (hello)
	mcpGateway       bool                           // honours SpawnSession.MCPGateway (hello mcp_gateway)
	restrictTools    bool                           // honours SpawnSession.RestrictTools (hello restrict_tools)
	hostTokenClone   bool                           // owner allows personal-token clones on the bare host (hello)
	sandboxAvailable bool
	hostClaude       hostClaudeCaps
	availableEngines []string                 // engine IDs detected as configured/usable on this daemon's host
	engineModels     map[string]models.Report // engine → the model list this daemon probed (sanitized)
	reposRoot        string                   // repos root changed since the hello; "" = ReposRoot
}

// SetReposRoot records the daemon's repos root after a live change (its
// set_repos_root answer, or a heartbeat that reports a different one).
func (dc *DaemonConn) SetReposRoot(root string) {
	dc.liveMu.Lock()
	dc.reposRoot = root
	dc.liveMu.Unlock()
}

// CurrentReposRoot is the daemon's repos root now: the latest live change,
// else what its hello reported.
func (dc *DaemonConn) CurrentReposRoot() string {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	if dc.reposRoot != "" {
		return dc.reposRoot
	}
	return dc.ReposRoot
}

// SetEngineModels replaces the model lists this daemon reported (its hello,
// or a heartbeat that carried them — a heartbeat without them leaves them
// alone). Every entry is re-validated here (models.SanitizeReports): the
// daemon is authenticated, but a list is read from an engine's output.
func (dc *DaemonConn) SetEngineModels(reports map[string]models.Report) {
	clean := models.SanitizeReports(reports, time.Now())
	dc.liveMu.Lock()
	dc.engineModels = clean
	dc.liveMu.Unlock()
}

// EngineModels returns what this daemon last reported for engine, and whether
// it reported a non-empty list. The slices are copies.
func (dc *DaemonConn) EngineModels(engine string) (models.Report, bool) {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	r, ok := dc.engineModels[engine]
	if !ok || len(r.Models) == 0 {
		return models.Report{}, false
	}
	ms := make([]models.Model, len(r.Models))
	for i, m := range r.Models {
		m.Efforts = append([]string{}, m.Efforts...)
		ms[i] = m
	}
	return models.Report{Models: ms, FetchedAt: r.FetchedAt}, true
}

// SetCheckedOutRepos updates the repo names this daemon has on disk at
// ReposRoot, as reported in its latest hello or heartbeat.
func (dc *DaemonConn) SetCheckedOutRepos(repos []string) {
	dc.liveMu.Lock()
	dc.checkedOutRepos = repos
	dc.liveMu.Unlock()
}

// CheckedOutRepos returns the repo names this daemon currently has on disk.
func (dc *DaemonConn) CheckedOutRepos() []string {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	return dc.checkedOutRepos
}

// SetRepoRemotes records a legacy (GitHub-only) name → "org/name" report —
// what a daemon that predates providers sends. See SetRepoOrigins.
func (dc *DaemonConn) SetRepoRemotes(remotes map[string]string) {
	origins := make(map[string]protocol.RepoOrigin, len(remotes))
	for name, full := range remotes {
		origins[name] = protocol.RepoOrigin{Provider: gitprovider.GitHubID, FullName: full}
	}
	dc.setOrigins(origins)
}

// SetRepoOrigins records the folder → hosted-repository map a daemon
// reported in its latest hello or heartbeat: origins when the daemon sent
// repo_origins (non-nil), else its legacy GitHub-only repo_remotes.
func (dc *DaemonConn) SetRepoOrigins(origins map[string]protocol.RepoOrigin, legacy map[string]string) {
	if origins == nil {
		dc.SetRepoRemotes(legacy)
		return
	}
	dc.setOrigins(origins)
}

// setOrigins replaces this daemon's folder → origin map. The daemon is
// authenticated but its report is still input: entries whose key is not a
// plain folder name, whose provider is not registered, or whose full name
// fails that provider's naming rules or the server's own repo-name rules (one
// slash — so a GitLab subgroup project is dropped, see validateRepoName) are
// dropped, and at most gitremote.MaxRemotes entries are kept — an entry that
// doesn't survive is simply "unknown", which the launch UI already handles.
func (dc *DaemonConn) setOrigins(origins map[string]protocol.RepoOrigin) {
	clean := make(map[string]protocol.RepoOrigin, min(len(origins), gitremote.MaxRemotes))
	for name, o := range origins {
		if len(clean) >= gitremote.MaxRemotes {
			break
		}
		if strings.Contains(name, "/") || validateRepoName(name) != nil {
			continue
		}
		if _, ok := gitprovider.Default.ParseFullName(o.Provider, o.FullName); !ok || validateRepoName(o.FullName) != nil {
			continue
		}
		clean[name] = o
	}
	dc.liveMu.Lock()
	dc.repoRemotes = clean
	dc.liveMu.Unlock()
}

// SetGitProviders records which git providers the daemon said (in its hello)
// it can clone from. Nil for a daemon that predates providers.
func (dc *DaemonConn) SetGitProviders(ids []string) {
	dc.liveMu.Lock()
	dc.gitProviders = append([]string(nil), ids...)
	dc.liveMu.Unlock()
}

// SetCloneFrom records whether the daemon said (in its hello) that it
// honours SpawnSession.CloneFrom.
func (dc *DaemonConn) SetCloneFrom(v bool) {
	dc.liveMu.Lock()
	dc.cloneFrom = v
	dc.liveMu.Unlock()
}

// CanCloneTarget reports whether a named-repository clone (CloneFrom) may be
// sent to this daemon. An older daemon would ignore CloneFrom and read the
// folder name as a repository to clone from GitHub — the wrong repository —
// so it is never sent one.
func (dc *DaemonConn) CanCloneTarget() bool {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	return dc.cloneFrom
}

// SetAllowHostCredentialClone records the daemon's hello
// allow_host_credential_clone.
func (dc *DaemonConn) SetAllowHostCredentialClone(v bool) {
	dc.liveMu.Lock()
	dc.hostTokenClone = v
	dc.liveMu.Unlock()
}

// AllowsHostCredentialClone reports whether this daemon's owner lets a
// person's own git token be used for a clone on the bare host (This machine).
// Without it a token only ever goes with a Local sandbox session's clone,
// which runs inside the sandbox image.
func (dc *DaemonConn) AllowsHostCredentialClone() bool {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	return dc.hostTokenClone
}

// CanCloneFrom reports whether a spawn naming provider may be sent to this
// daemon: "" and "github" always (every daemon clones GitHub), anything else
// only when the daemon listed it — an older daemon would ignore the provider
// and clone the same owner/name from GitHub.
func (dc *DaemonConn) CanCloneFrom(provider string) bool {
	if provider == "" || provider == gitprovider.GitHubID {
		return true
	}
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	return slices.Contains(dc.gitProviders, provider)
}

// RepoOrigin returns the hosted repository this daemon reported for its
// checked-out folder name, if known.
func (dc *DaemonConn) RepoOrigin(name string) (protocol.RepoOrigin, bool) {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	o, ok := dc.repoRemotes[name]
	return o, ok
}

// RepoRemote returns just the "owner/name" part of RepoOrigin.
func (dc *DaemonConn) RepoRemote(name string) (string, bool) {
	o, ok := dc.RepoOrigin(name)
	return o.FullName, ok
}

// SetSandboxAvailable updates whether this daemon's blerg-runner-sandbox
// image is present, as reported in its latest hello or heartbeat.
func (dc *DaemonConn) SetSandboxAvailable(v bool) {
	dc.liveMu.Lock()
	dc.sandboxAvailable = v
	dc.liveMu.Unlock()
}

// hostClaudeCaps is what a daemon reported about running Claude agent
// sessions on its host (see DaemonInfo.ClaudeCLIAvailable).
type hostClaudeCaps struct {
	cli, apiKey bool
	// sandboxCred: a sandboxed Claude session would find a credential
	// (DaemonInfo.SandboxClaudeCredential); nil = the daemon didn't say.
	sandboxCred *bool
}

// SetHostClaude records the daemon's latest hello/heartbeat report.
func (dc *DaemonConn) SetHostClaude(cli, apiKey bool, sandboxCred *bool) {
	dc.liveMu.Lock()
	dc.hostClaude = hostClaudeCaps{cli: cli, apiKey: apiKey, sandboxCred: sandboxCred}
	dc.liveMu.Unlock()
}

// SandboxClaudeCredential returns whether a sandboxed Claude session on this
// daemon has a credential to run with, or nil when the daemon didn't report it.
func (dc *DaemonConn) SandboxClaudeCredential() *bool {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	return dc.hostClaude.sandboxCred
}

// HostClaude returns (claude CLI on PATH, ANTHROPIC_API_KEY set).
func (dc *DaemonConn) HostClaude() (cli, apiKey bool) {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	return dc.hostClaude.cli, dc.hostClaude.apiKey
}

// SandboxAvailable reports whether this daemon can run "Local sandbox"
// sessions right now.
func (dc *DaemonConn) SandboxAvailable() bool {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	return dc.sandboxAvailable
}

// SetAvailableEngines updates the engine IDs detected as configured/usable
// on this daemon's host, as reported in its latest hello or heartbeat.
func (dc *DaemonConn) SetAvailableEngines(engines []string) {
	dc.liveMu.Lock()
	dc.availableEngines = engines
	dc.liveMu.Unlock()
}

// AvailableEngines returns the engine IDs this daemon last reported as
// configured/usable.
func (dc *DaemonConn) AvailableEngines() []string {
	dc.liveMu.RLock()
	defer dc.liveMu.RUnlock()
	return dc.availableEngines
}

// Hub holds maps of connected daemons and browsers and serialises access to
// those maps.
type Hub struct {
	mu                   sync.RWMutex
	daemons              map[string]*DaemonConn            // keyed by daemon ID (UUID string)
	browsers             map[string]*BrowserConn           // keyed by connection ID
	subscriptions        map[string]map[string]chan []byte // sessionID → browserID → send channel
	sessionOwners        map[string]string                 // sessionID → daemonID
	sessionActiveBrowser map[string]string                 // sessionID → active browserID
	sessionPTYCols       map[string]int                    // sessionID → current PTY cols
	boardSubs            map[string]map[string]bool        // boardID → set[browserID]
	lastActivity         time.Time                         // last time any browser reported user interaction
	preview              string                            // latest pushed HTML; empty if none
	serverVersion        string
	jobs                 *JobManager // cluster runner Jobs; nil = runtime unavailable
	// completionNotifier fires a session's completion webhook (webhook.go).
	// Registered by NewAPI; nil until then (and in hub-only tests), which is
	// why every call goes through NotifyCompletion rather than the field.
	completionNotifier func(sessionID string)
	// autoStopper ends a one-shot session whose turn has just finished
	// (autostop.go). Registered by NewAPI for the same reason as the notifier
	// above: the daemon message path holds a Hub, not an API.
	autoStopper func(sessionID string)
	// starts tracks each session's current start attempt (startstages.go).
	starts startTracker
	// reposRootReqs are set_repos_root requests waiting for their daemon's
	// answer (reposroot.go).
	reposRootReqs reposRootRequests
	// privacy reads which sessions are private (privacy.go). Guarded by mu.
	privacy privacyGate
	// mcpStart is how a session start reaches the MCP gateway (mcpstart.go).
	mcpStart mcpStartHolder
}

// NewHub creates a ready-to-use Hub.
func NewHub() *Hub {
	return &Hub{
		daemons:              make(map[string]*DaemonConn),
		browsers:             make(map[string]*BrowserConn),
		subscriptions:        make(map[string]map[string]chan []byte),
		sessionOwners:        make(map[string]string),
		sessionActiveBrowser: make(map[string]string),
		sessionPTYCols:       make(map[string]int),
		boardSubs:            make(map[string]map[string]bool),
	}
}

// SetCompletionNotifier registers the callback the hub invokes when a session
// reaches a terminal state. Called once, by NewAPI.
func (h *Hub) SetCompletionNotifier(fn func(sessionID string)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.completionNotifier = fn
}

// NotifyCompletion tells the API a session has reached a terminal state, so it
// can deliver the completion webhook. A no-op when no notifier is registered
// (hub-only tests) — and, on the API side, for a session with no callback_url,
// so the terminal paths can call it unconditionally.
func (h *Hub) NotifyCompletion(sessionID string) {
	h.mu.RLock()
	fn := h.completionNotifier
	h.mu.RUnlock()
	if fn != nil {
		fn(sessionID)
	}
}

// SetAutoStopper registers the callback the hub invokes when a session's turn
// finishes, so a one-shot session can be ended. Called once, by NewAPI.
func (h *Hub) SetAutoStopper(fn func(sessionID string)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.autoStopper = fn
}

// AutoStopOnTurnDone tells the API a session has finished a turn. A no-op when
// no stopper is registered (hub-only tests) — and, on the API side, for every
// session that was not started with auto_stop, so the event path can call it
// on every turn_done without knowing which sessions are one-shot.
func (h *Hub) AutoStopOnTurnDone(sessionID string) {
	h.mu.RLock()
	fn := h.autoStopper
	h.mu.RUnlock()
	if fn != nil {
		fn(sessionID)
	}
}

// SetSessionPTYCols records the current PTY column count for a session.
// Called when a session is spawned and whenever a browser resize updates the
// effective PTY width. Used to populate HistoryDone.Cols so browsers can detect
// a width mismatch between recorded history and their own terminal size.
func (h *Hub) SetSessionPTYCols(sessionID string, cols int) {
	if cols <= 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessionPTYCols[sessionID] = cols
}

// GetSessionPTYCols returns the last known PTY column count for a session,
// or 0 if not yet recorded.
func (h *Hub) GetSessionPTYCols(sessionID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sessionPTYCols[sessionID]
}

// SetActiveBrowser marks browserID as the active browser for sessionID and
// returns the previously active browser ID ("" if none).
func (h *Hub) SetActiveBrowser(sessionID, browserID string) (prev string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	prev = h.sessionActiveBrowser[sessionID]
	h.sessionActiveBrowser[sessionID] = browserID
	return prev
}

// IsActiveBrowser reports whether browserID is the current active browser for
// sessionID.
func (h *Hub) IsActiveBrowser(sessionID, browserID string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.sessionActiveBrowser[sessionID] == browserID
}

// MarkActivity records that a browser reported user interaction. The browser
// sends a throttled "activity" message on real input, letting the server tell
// whether anyone is engaged right now.
func (h *Hub) MarkActivity() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastActivity = time.Now()
}

// ActiveWithin reports whether a browser reported user interaction within d.
// Used to suppress OS push notifications when the user is engaged — the client
// shows an in-app toast for those instead. When no one has interacted within d
// (the app is closed, or left open but idle), this is false and a push is sent.
func (h *Hub) ActiveWithin(d time.Duration) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return !h.lastActivity.IsZero() && time.Since(h.lastActivity) < d
}

// ReleaseActiveBrowser clears the active browser for sessionID when the
// releasingID is indeed the current active. It then promotes another subscriber
// if one exists, returning the newly promoted browser ID ("" if none).
// If releasingID is not the current active, no state is changed and "" is returned.
func (h *Hub) ReleaseActiveBrowser(sessionID, releasingID string) (newActive string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.sessionActiveBrowser[sessionID] != releasingID {
		return ""
	}
	delete(h.sessionActiveBrowser, sessionID)

	// Promote any other subscriber (order is unspecified when multiple remain).
	for id := range h.subscriptions[sessionID] {
		if id != releasingID {
			h.sessionActiveBrowser[sessionID] = id
			return id
		}
	}
	return ""
}

// SendToBrowser delivers msg to the named browser via a non-blocking channel
// send, mirroring the skip-if-full style of BroadcastToBrowsers.
func (h *Hub) SendToBrowser(browserID string, msg []byte) {
	h.mu.RLock()
	b := h.browsers[browserID]
	h.mu.RUnlock()
	if b == nil {
		return
	}
	select {
	case b.send <- msg:
	default:
		// browser channel full — skip rather than block
	}
}

// SetServerVersion stores the server's version string.
func (h *Hub) SetServerVersion(v string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.serverVersion = v
}

// Register adds a daemon to the hub.
func (h *Hub) Register(d *DaemonConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.daemons[d.ID] = d
}

// UnregisterIfCurrent removes a daemon from the hub only if dc is still the
// registered connection for its ID. It returns true if it removed dc. This
// guards against a reconnect race: a daemon restart opens a new connection
// (same stable ID) that registers before the old connection's cleanup runs;
// an unconditional delete would clobber the live connection. The boolean lets
// callers skip stale disconnect handling when they have been superseded.
func (h *Hub) UnregisterIfCurrent(dc *DaemonConn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.daemons[dc.ID] == dc {
		delete(h.daemons, dc.ID)
		return true
	}
	return false
}

// GetDaemon returns the DaemonConn for the given ID, or nil if not present.
func (h *Hub) GetDaemon(id string) *DaemonConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.daemons[id]
}

// RegisterBrowser adds a browser connection to the hub.
func (h *Hub) RegisterBrowser(b *BrowserConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.browsers[b.ID] = b
}

// UnregisterBrowser removes a browser connection from the hub by ID and cleans
// up any board subscriptions it held.
func (h *Hub) UnregisterBrowser(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.browsers, id)
	// Remove this browser from all board subscriptions so BroadcastBoard does
	// not attempt to deliver to a disconnected connection.
	for boardID, subs := range h.boardSubs {
		delete(subs, id)
		if len(subs) == 0 {
			delete(h.boardSubs, boardID)
		}
	}
}

// BroadcastToBrowsers sends a pre-encoded JSON message to every connected browser.
// Slow or blocked browsers are skipped (non-blocking send).
//
// A message that names a private session (a session_id, a nested session or
// message, a daemon_disconnected's list) goes only to sockets of the account
// that owns it; see privacy.go. Every broadcast helper below funnels through
// here, so no call site has to remember to filter.
func (h *Hub) BroadcastToBrowsers(msg []byte) {
	scope := h.scopeOf(msg) // reads the database: never under h.mu
	perAccount := map[string][]byte{}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, b := range h.browsers {
		data, ok := perAccount[b.AccountID]
		if !ok {
			var allowed bool
			if data, allowed = scope.forAccount(b.AccountID, msg); !allowed {
				data = nil
			}
			perAccount[b.AccountID] = data
		}
		if data == nil {
			continue
		}
		select {
		case b.send <- data:
		default:
			// browser channel full — skip rather than block
		}
	}
}

// BroadcastJSON marshals v to JSON and broadcasts it to all browser connections.
func (h *Hub) BroadcastJSON(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.BroadcastToBrowsers(data)
}

// BroadcastJSONPerAccount is BroadcastJSON for a message whose content depends
// on who is looking (whether a session was ended by "you"). build is called
// once per distinct browser account — it must be pure and must not call back
// into the hub. A payload naming a private session reaches only its owner's
// sockets (privacy.go), exactly as with BroadcastToBrowsers.
func (h *Hub) BroadcastJSONPerAccount(build func(accountID string) any) {
	h.mu.RLock()
	browsers := make([]*BrowserConn, 0, len(h.browsers))
	for _, b := range h.browsers {
		browsers = append(browsers, b)
	}
	h.mu.RUnlock()
	// Browser send channels are never closed, so sending after the lock is
	// released is safe; it lets the privacy lookup (a database read) run
	// outside the hub's lock.
	cache := map[string][]byte{}
	for _, b := range browsers {
		data, ok := cache[b.AccountID]
		if !ok {
			raw, err := json.Marshal(build(b.AccountID))
			if err != nil {
				cache[b.AccountID] = nil
				continue
			}
			if out, allowed := h.scopeOf(raw).forAccount(b.AccountID, raw); allowed {
				data = out
			}
			cache[b.AccountID] = data
		}
		if data == nil {
			continue
		}
		select {
		case b.send <- data:
		default:
			// browser channel full — skip rather than block
		}
	}
}

// SubscribeBoard registers browserID to receive board-change events for boardID.
// Idempotent: registering the same (board, browser) pair multiple times is safe.
func (h *Hub) SubscribeBoard(boardID, browserID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.boardSubs[boardID] == nil {
		h.boardSubs[boardID] = make(map[string]bool)
	}
	h.boardSubs[boardID][browserID] = true
}

// UnsubscribeBoard removes browserID from the board's event stream.
func (h *Hub) UnsubscribeBoard(boardID, browserID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if subs, ok := h.boardSubs[boardID]; ok {
		delete(subs, browserID)
		if len(subs) == 0 {
			delete(h.boardSubs, boardID)
		}
	}
}

// BroadcastBoard marshals v to JSON once and delivers it to every browser
// subscribed to boardID. Unsubscribed browsers receive nothing. Slow browsers
// are skipped (non-blocking send, matching BroadcastToBrowsers behaviour).
func (h *Hub) BroadcastBoard(boardID string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	// Snapshot the subscriber set under the read lock to avoid holding the lock
	// during the per-browser sends (which acquire a second read lock in SendToBrowser).
	h.mu.RLock()
	ids := make([]string, 0, len(h.boardSubs[boardID]))
	for id := range h.boardSubs[boardID] {
		ids = append(ids, id)
	}
	h.mu.RUnlock()

	for _, id := range ids {
		h.SendToBrowser(id, data)
	}
}

// BroadcastTitleChanged broadcasts a session_title_changed event to all browser connections.
func (h *Hub) BroadcastTitleChanged(sessionID, title string) {
	data, err := json.Marshal(protocol.SessionTitleChanged{
		Type:      "session_title_changed",
		SessionID: sessionID,
		Title:     title,
	})
	if err != nil {
		return
	}
	h.BroadcastToBrowsers(data)
}

// SetPreview stores the latest preview HTML and broadcasts it to browsers.
func (h *Hub) SetPreview(html string) {
	h.mu.Lock()
	h.preview = html
	h.mu.Unlock()
}

// Preview returns the current preview HTML.
func (h *Hub) Preview() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.preview
}

// Subscribe registers a browser channel to receive output for a session.
func (h *Hub) Subscribe(sessionID, browserID string, ch chan []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subscriptions[sessionID] == nil {
		h.subscriptions[sessionID] = make(map[string]chan []byte)
	}
	h.subscriptions[sessionID][browserID] = ch
}

// UnsubscribeSession removes all browser subscriptions for a session and clears
// its active-browser record. Call this when a session ends to release all
// associated hub state.
func (h *Hub) UnsubscribeSession(sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subscriptions, sessionID)
	delete(h.sessionActiveBrowser, sessionID)
}

// Unsubscribe removes a browser from the session output fan-out.
func (h *Hub) Unsubscribe(sessionID, browserID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if subs, ok := h.subscriptions[sessionID]; ok {
		delete(subs, browserID)
		if len(subs) == 0 {
			delete(h.subscriptions, sessionID)
		}
	}
}

// FanOutSessionOutput sends data to every browser subscribed to a session.
// Slow browsers are skipped (non-blocking send).
//
// Callers must call Unsubscribe or UnsubscribeSession before closing
// a BrowserConn's send channel to avoid a send-on-closed-channel panic.
func (h *Hub) FanOutSessionOutput(sessionID string, data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, ch := range h.subscriptions[sessionID] {
		select {
		case ch <- data:
		default:
		}
	}
}

// SetSessionOwner records which daemon owns a session.
func (h *Hub) SetSessionOwner(sessionID, daemonID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sessionOwners[sessionID] = daemonID
}

// RemoveSessionOwner removes the daemon-ownership record for a session.
func (h *Hub) RemoveSessionOwner(sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sessionOwners, sessionID)
}

// FindDaemonForSession returns the DaemonConn that owns sessionID, or nil.
func (h *Hub) FindDaemonForSession(sessionID string) *DaemonConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	daemonID, ok := h.sessionOwners[sessionID]
	if !ok {
		return nil
	}
	return h.daemons[daemonID]
}

// DaemonForRepo returns the connected daemon to run repo on: the one whose
// CheckedOutRepos contains it (lowest daemon ID wins when several do — map
// iteration order is random, and consecutive spawns for the same board must
// not bounce between machines); else, if exactly one daemon is connected, that
// one (it clones/creates the repo on demand); else nil — with several daemons
// and no match there is no defensible guess, and picking wrong would run a
// board's agent against a stranger's checkout.
//
// Unlike AnyDaemon this is repo-aware: it backs the board-driven desktop start
// path (R9), where the caller names a repo and only some daemons have it.
func (h *Hub) DaemonForRepo(repo string) *DaemonConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ids := make([]string, 0, len(h.daemons))
	for id := range h.daemons {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		if slices.Contains(h.daemons[id].CheckedOutRepos(), repo) {
			return h.daemons[id]
		}
	}
	if len(ids) == 1 {
		return h.daemons[ids[0]]
	}
	return nil
}

// DaemonForScratch picks the daemon a "No repository" v1 start runs on: any
// connected daemon will do, since the session only needs a new scratch folder.
// Deterministic (lowest id), preferring one with the sandbox image unless the
// caller asked for the bare host — the runtime resolveDaemonRuntime then
// picks for it. nil when none is connected.
func (h *Hub) DaemonForScratch(runtime string) *DaemonConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	ids := make([]string, 0, len(h.daemons))
	for id := range h.daemons {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	slices.Sort(ids)
	if runtime != runnerRuntimeDaemon {
		for _, id := range ids {
			if h.daemons[id].SandboxAvailable() {
				return h.daemons[id]
			}
		}
	}
	return h.daemons[ids[0]]
}

// AnyDaemon returns any single connected DaemonConn, or nil if none are
// registered. Used by HandlePostAssist as a fallback when the board has no
// default_daemon_id configured.
func (h *Hub) AnyDaemon() *DaemonConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, d := range h.daemons {
		return d
	}
	return nil
}

// GetAllDaemons returns a snapshot of all currently connected DaemonConns.
func (h *Hub) GetAllDaemons() []*DaemonConn {
	h.mu.RLock()
	defer h.mu.RUnlock()
	daemons := make([]*DaemonConn, 0, len(h.daemons))
	for _, d := range h.daemons {
		daemons = append(daemons, d)
	}
	return daemons
}
