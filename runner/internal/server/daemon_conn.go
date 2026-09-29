package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
)

var daemonUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Allow connections from any origin; the caller controls access via the
	// daemon token.
	CheckOrigin: func(_ *http.Request) bool { return true },
}

// Read limits on the daemon socket (gorilla closes the connection with 1009
// when a frame is larger). Variables only so tests can shrink them.
var (
	// daemonHelloReadLimit bounds daemon_hello, which is read before the
	// token is checked. A worst-case legitimate hello — 2,000 checked-out
	// folders and 1,000 remotes with long names, 500 active sessions, and
	// every engine that can report a list at the full per-engine cap with
	// maximum-length fields — measures ~0.75 MB
	// (TestDaemonReadLimitFitsWorstCaseHello); 4 MiB is well above that.
	daemonHelloReadLimit int64 = 4 << 20
	// daemonMessageReadLimit bounds every later message. It must fit the
	// largest legitimate one, which is not a heartbeat but a full
	// session_scrollback (tmux keeps 50,000 lines, captured with escape
	// sequences, each ESC JSON-escaped to six bytes — tens of MB for a busy
	// wide pane) or an agent_event carrying a large tool result.
	daemonMessageReadLimit int64 = 64 << 20
)

// envelope is used to peek at the "type" field of an incoming message before
// full decoding.
type envelope struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
}

// runnerBoundSession is the one session a runner pod may speak for. A runner
// pod (mode "runner", named "runner-<session id>" by internal/runner) hosts
// exactly one session, but authenticates with the shared daemon token — so
// without this binding any pod could report status, events or start stages
// for any other session. "" for ordinary daemons, which host many.
func runnerBoundSession(mode, name string) string {
	if mode == "runner" && strings.HasPrefix(name, "runner-") {
		return strings.TrimPrefix(name, "runner-")
	}
	return ""
}

// boundSessionIDs keeps only the ids a daemon may report (see
// runnerBoundSession); ordinary daemons' lists pass through untouched.
func boundSessionIDs(mode, name string, ids []string) []string {
	bound := runnerBoundSession(mode, name)
	if bound == "" {
		return ids
	}
	out := make([]string, 0, 1)
	for _, id := range ids {
		if strings.EqualFold(id, bound) {
			out = append(out, id)
		} else {
			log.Printf("runner %s reported foreign session %s — ignored", name, id)
		}
	}
	return out
}

// newUUID returns a random UUID v4 string.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ServeDaemon returns an http.HandlerFunc that upgrades the connection to
// WebSocket, validates the daemon hello, and registers the daemon with the hub.
func (h *Hub) ServeDaemon(daemonToken string, dbPool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wsConn, err := daemonUpgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("daemon ws upgrade error: %v", err)
			return
		}

		// Bound every frame the daemon can make the server buffer: the hello
		// tightly (it is read before the token is checked), the rest of the
		// connection at the size of the largest legitimate message.
		wsConn.SetReadLimit(daemonHelloReadLimit)

		// ── Step 1: read the hello message ────────────────────────────────────
		_ = wsConn.SetReadDeadline(time.Now().Add(15 * time.Second))
		_, raw, err := wsConn.ReadMessage()
		if err != nil {
			log.Printf("daemon hello read error: %v", err)
			_ = wsConn.Close()
			return
		}
		_ = wsConn.SetReadDeadline(time.Time{}) // clear deadline

		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil || env.Type != "daemon_hello" {
			_ = wsConn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(4400, "expected daemon_hello"))
			_ = wsConn.Close()
			return
		}

		var hello protocol.DaemonHello
		if err := json.Unmarshal(raw, &hello); err != nil {
			_ = wsConn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(4400, "malformed daemon_hello"))
			_ = wsConn.Close()
			return
		}

		// Normalise name to lowercase so "WSL-DESKTOP" and "wsl-desktop" resolve
		// to the same DB row.
		hello.Name = strings.ToLower(hello.Name)

		// ── Step 2: validate token ─────────────────────────────────────────────
		if !tokenEqual(hello.Token, daemonToken) {
			_ = wsConn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(4401, "unauthorized"))
			_ = wsConn.Close()
			return
		}

		// ── Step 3: assign a stable daemon ID ────────────────────────────────
		// Look up by name so reconnecting daemons reuse the same UUID, keeping
		// existing session rows associated with this daemon.
		// The connection outlives the upgrade request, so its work keeps the
		// request's values but not its cancellation.
		ctx := context.WithoutCancel(r.Context())
		daemonID := ""
		if dbPool != nil {
			existing, err := db.GetDaemonByName(ctx, dbPool, hello.Name)
			if err != nil {
				log.Printf("lookup daemon by name %q: %v", hello.Name, err)
			} else if existing != nil {
				daemonID = existing.ID
			}
		}
		if daemonID == "" {
			daemonID = newUUID()
		}

		// ── Step 4: upsert daemon in Postgres ─────────────────────────────────
		if dbPool != nil {
			if err := db.UpsertDaemon(ctx, dbPool, daemonID, hello.Name, hello.Mode, hello.ReposRoot); err != nil {
				log.Printf("upsert daemon %s: %v", daemonID, err)
			}
		}

		// ── Step 5: reconcile active_sessions ───────────────────────────────────────
		// Mark sessions active in the DB but absent from the daemon's list as stopped.
		if dbPool != nil {
			reconcileSessions(ctx, h, dbPool, daemonID, boundSessionIDs(hello.Mode, hello.Name, hello.ActiveSessions), nil)
		}

		// ── Step 6: build DaemonConn and register ─────────────────────────────
		checkedOut := hello.CheckedOutRepos
		if checkedOut == nil {
			checkedOut = []string{}
		}
		dc := &DaemonConn{
			ID:        daemonID,
			Name:      hello.Name,
			Mode:      hello.Mode,
			ReposRoot: hello.ReposRoot,
			Version:   hello.Version,
			send:      make(chan []byte, 256),
		}
		dc.SetCheckedOutRepos(checkedOut)
		dc.SetRepoOrigins(hello.RepoOrigins, hello.RepoRemotes)
		dc.SetGitProviders(hello.GitProviders)
		dc.SetCloneFrom(hello.CloneFrom)
		dc.SetAllowHostCredentialClone(hello.AllowHostCredentialClone)
		dc.SetSandboxAvailable(hello.SandboxAvailable)
		dc.SetHostClaude(hello.ClaudeCLIAvailable, hello.AnthropicKeySet, hello.SandboxClaudeCredential)
		dc.SetAvailableEngines(hello.AvailableEngines)
		dc.SetEngineModels(hello.EngineModels)
		h.Register(dc)

		// Store wsConn on the stack so goroutines below close over it.
		conn := wsConn
		conn.SetReadLimit(daemonMessageReadLimit)

		// ── Step 7: start read / write goroutines ─────────────────────────────
		go daemonWritePump(dc, conn)
		go daemonReadPump(h, dc, conn, dbPool) //nolint:gosec,contextcheck // the read pump outlives the upgrade request (hijacked websocket); it ends when the connection closes; contextcheck: a websocket read pump has no request context to inherit; the disconnect cleanup deliberately uses its own
	}
}

// daemonWritePump drains dc.send and writes outbound messages to the daemon.
func daemonWritePump(dc *DaemonConn, conn *websocket.Conn) {
	defer func() { _ = conn.Close() }()
	for msg := range dc.send {
		if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
			log.Printf("daemon write pump %s: %v", dc.ID, err)
			return
		}
	}
}

// daemonReadPump reads inbound messages from the daemon, handles heartbeats,
// and cleans up on disconnect.
func daemonReadPump(h *Hub, dc *DaemonConn, conn *websocket.Conn, dbPool *pgxpool.Pool) {
	// Create one context for the lifetime of the read pump rather than
	// allocating a new one on every heartbeat message.
	ctx := context.Background()

	defer func() {
		close(dc.send)
		_ = conn.Close()
		handleDaemonDisconnect(h, dc, dbPool)
	}()

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure,
				websocket.CloseNoStatusReceived,
			) {
				log.Printf("daemon read pump %s: %v", dc.ID, err)
			}
			return
		}

		var env envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			continue
		}
		// A runner pod speaks for its own session only.
		if bound := runnerBoundSession(dc.Mode, dc.Name); bound != "" && env.Type != "daemon_heartbeat" &&
			!strings.EqualFold(env.SessionID, bound) {
			log.Printf("runner %s: dropping %s for foreign session %q", dc.Name, env.Type, env.SessionID)
			continue
		}

		switch env.Type {
		case "daemon_heartbeat":
			var hb protocol.DaemonHeartbeat
			hbOK := json.Unmarshal(raw, &hb) == nil
			if hbOK {
				dc.SetSandboxAvailable(hb.SandboxAvailable)
				dc.SetHostClaude(hb.ClaudeCLIAvailable, hb.AnthropicKeySet, hb.SandboxClaudeCredential)
				dc.SetCheckedOutRepos(hb.CheckedOutRepos)
				dc.SetRepoOrigins(hb.RepoOrigins, hb.RepoRemotes)
				dc.SetAvailableEngines(hb.AvailableEngines)
				// Absent = unchanged: the daemon only resends its model
				// lists when one changed.
				if hb.EngineModels != nil {
					dc.SetEngineModels(hb.EngineModels)
				}
				// Absent (an older daemon) = unchanged.
				if hb.ReposRoot != "" {
					applyReposRoot(ctx, dbPool, dc, hb.ReposRoot)
				}
			}
			if dbPool != nil {
				if err := db.SetDaemonStatus(ctx, dbPool, dc.ID, "connected"); err != nil {
					log.Printf("heartbeat update %s: %v", dc.ID, err)
				}
				if hbOK {
					reconcileSessions(ctx, h, dbPool, dc.ID, boundSessionIDs(dc.Mode, dc.Name, hb.ActiveSessions), hb.SessionStates)
				}
			}

		case "repos_root_result":
			var msg protocol.ReposRootResult
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("daemon %s malformed repos_root_result: %v", dc.ID, err)
				continue
			}
			handleReposRootResult(ctx, h, dbPool, dc, msg)

		case "session_started":
			var msg protocol.SessionStarted
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("daemon %s malformed session_started: %v", dc.ID, err)
				continue
			}
			HandleSessionStarted(ctx, h, dbPool, dc.ID, msg)

		case "session_state_changed":
			var msg protocol.SessionStateChanged
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("daemon %s malformed session_state_changed: %v", dc.ID, err)
				continue
			}
			if err := HandleSessionStateChanged(ctx, h, dbPool, msg); err != nil {
				log.Printf("daemon %s session_state_changed %s: %v", dc.ID, msg.SessionID, err)
			}

		case "session_ended":
			var msg protocol.SessionEnded
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("daemon %s malformed session_ended: %v", dc.ID, err)
				continue
			}
			HandleSessionEnded(ctx, h, dbPool, msg)

		case "session_meta_changed":
			var msg protocol.SessionMetaChanged
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("daemon %s malformed session_meta_changed: %v", dc.ID, err)
				continue
			}
			HandleSessionMetaChanged(ctx, h, dbPool, msg)

		case "session_output":
			var msg protocol.SessionOutput
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("daemon %s malformed session_output: %v", dc.ID, err)
				continue
			}
			HandleSessionOutput(ctx, h, dbPool, msg)

		case "agent_event":
			var msg protocol.AgentEvent
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("daemon %s malformed agent_event: %v", dc.ID, err)
				continue
			}
			if msg.Transient {
				// Streaming deltas: fan out only — no DB, no ack.
				h.FanOutSessionOutput(msg.SessionID, raw)
				continue
			}
			HandleAgentEvent(ctx, h, dbPool, dc, msg)

		case "session_scrollback":
			var msg protocol.SessionScrollback
			if err := json.Unmarshal(raw, &msg); err != nil {
				log.Printf("daemon %s malformed session_scrollback: %v", dc.ID, err)
				continue
			}
			// Snapshot, not part of the stream — fan out to subscribers only, no
			// DB recording. (FanOutSessionOutput is a generic subscriber fan-out.)
			if data, err := json.Marshal(msg); err == nil {
				h.FanOutSessionOutput(msg.SessionID, data)
			}

		default:
			log.Printf("daemon %s unknown message type: %s", dc.ID, env.Type)
		}
	}
}

// isActiveStatus reports whether a session status represents a live session.
func isActiveStatus(status string) bool {
	return status == "running" || status == "idle" || status == "waiting" || status == "starting"
}

// revokeSessionTokens revokes a session's credentials (per-session messaging
// token, assist board token) at a genuinely final end — one after which the
// session's process will never run again: the daemon's session_ended, a
// user-requested delete of a session no daemon owns, the assist abort, and
// reconcile finding a session the daemon no longer knows about. Not on a
// daemon disconnect (the session usually resumes). Best-effort, logged.
func revokeSessionTokens(ctx context.Context, dbPool *pgxpool.Pool, sessionID, why string) {
	if dbPool == nil {
		return
	}
	if err := db.RevokeBoardTokensForSession(ctx, dbPool, sessionID); err != nil {
		log.Printf("revoke tokens %s (%s): %v", sessionID, why, err)
	}
}

// reconcileDesiredStatus computes the status a session should have, given the
// daemon's heartbeat report. It returns (status, true) if the session must be
// updated, or ("", false) to leave it unchanged.
//
//   - Not reported but active in DB → the daemon no longer owns it → "stopped".
//   - Not reported and already terminal → no change.
//   - Reported with a detector state (running/idle/waiting) → adopt it. This is
//     what self-heals a dropped state change: the daemon re-asserts the truth
//     every heartbeat and the server converges to it.
//   - Reported without a usable detector state → revive a terminal session to
//     "running", otherwise keep the current active status (don't clobber a
//     known idle/waiting just because the detector hasn't reported yet).
func reconcileDesiredStatus(current string, reported bool, reportedState string) (string, bool) {
	if !reported {
		if isActiveStatus(current) {
			return "stopped", true
		}
		return "", false
	}
	desired := "running"
	if isActiveStatus(reportedState) {
		desired = reportedState
	} else if isActiveStatus(current) {
		desired = current
	}
	if desired == current {
		return "", false
	}
	return desired, true
}

// setSessionStatus authoritatively updates a session's status: it writes the DB,
// appends a state_change event, and broadcasts to browsers. Unlike
// HandleSessionStateChanged it does not enforce the transition state machine —
// it is used by reconciliation and disconnect handling, which are authoritative
// about session liveness (e.g. reviving a session out of a terminal state when
// its daemon reconnects).
// final says whether this write is the session's real end, and so whether it
// may fire the completion webhook. It is NOT the same question as "is the
// status terminal": a daemon WS drop marks a desktop session "error" and
// reconcileSessions revives it minutes later when the daemon reattaches, so
// notifying there would report a completion for a session that is still going
// — and, because delivery is claimed once per session, would burn the one
// delivery the caller was promised. Only writes that genuinely end a session
// (reconcile's "stopped", the orphan cleanup) pass true.
func setSessionStatus(ctx context.Context, h *Hub, dbPool *pgxpool.Pool, sessionID, status string, endedAt *time.Time, message *string, final bool) { //nolint:unparam // final is a deliberate decision at each call site (see above); no current caller ends a session through this path
	setSessionStatusEnd(ctx, h, dbPool, sessionID, status, endedAt, message, final, db.SessionEnd{})
}

// setSessionStatusEnd is setSessionStatus for a write that ends the session
// and knows why: end (non-zero) is recorded with the status in one statement,
// first write wins (db.EndSessionStatus). A zero end is exactly
// setSessionStatus. Any live status clears an earlier attribution — a session
// the daemon reports alive again has not ended, whatever was recorded before.
func setSessionStatusEnd(ctx context.Context, h *Hub, dbPool *pgxpool.Pool, sessionID, status string, endedAt *time.Time, message *string, final bool, end db.SessionEnd) {
	var err error
	if end.Reason != "" {
		err = db.EndSessionStatus(ctx, dbPool, sessionID, status, endedAt, end)
	} else {
		err = db.UpdateSessionStatus(ctx, dbPool, sessionID, status, endedAt)
	}
	if err != nil {
		log.Printf("setSessionStatus %s → %s: %v", sessionID, status, err)
		return
	}
	// A revive into a live status drops the end attribution inside
	// UpdateSessionStatus's own statement (migration 018) — not here, where a
	// session_ended landing between two writes would lose its reason.
	// Any transition away from "error" must drop the stale reason — a
	// revived session (e.g. reconcileSessions healing a transient disconnect
	// back to "running") is no longer explained by whatever failed before.
	// This is the one place that clears it so every consumer (session card,
	// SessionDetail, Task 15's status endpoint) reads a reason only while the
	// session is actually in "error".
	if status != "error" {
		if err := db.ClearSessionError(ctx, dbPool, sessionID); err != nil {
			log.Printf("ClearSessionError %s: %v", sessionID, err)
		}
		// The same transition is what "the daemon came back" looks like:
		// reconcileSessions revives the session to running/idle/waiting, so
		// the lost-daemon marker must go with the stale error, or the sweep
		// that finalises abandoned sessions would later end a live one.
		if err := db.ClearSessionDaemonLost(ctx, dbPool, sessionID); err != nil {
			log.Printf("ClearSessionDaemonLost %s: %v", sessionID, err)
		}
	}
	// A start still in progress in this process ends here too (tracker-only:
	// this is a hot path, so no history lookup).
	if isLiveStatus(status) {
		markStartReady(ctx, h, dbPool, sessionID, false)
	} else if status == "error" || status == "stopped" {
		reason := "The session ended before it was ready"
		if message != nil && *message != "" {
			reason = *message
		}
		failStart(ctx, h, dbPool, sessionID, reason, "")
	}
	// Deliberately no token revocation here: a status written through this
	// helper is not necessarily final. handleDaemonDisconnect marks sessions
	// "error" on any WS drop (laptop sleep, network blip, daemon restart),
	// and on reconnect the daemon reattaches the still-running pane and
	// reconcileSessions revives the row — the process keeps the token it was
	// spawned with, so revoking on that transition would 401 every later
	// `blerg-runner ask`. Callers that know an end is final revoke
	// themselves (revokeSessionTokens).
	data, _ := json.Marshal(map[string]string{"status": status})
	if err := db.AppendSessionEvent(ctx, dbPool, sessionID, "state_change", string(data), 0); err != nil {
		log.Printf("setSessionStatus append %s: %v", sessionID, err)
	}
	// Read the current unread value from the DB so reconcile/disconnect broadcasts
	// never accidentally clear a session's unread badge. UpdateSessionStatus does
	// not touch the unread column, so the stored value is still authoritative.
	var unread bool
	var shown sessionEnd
	if row, err := db.GetSession(ctx, dbPool, sessionID); err != nil {
		log.Printf("setSessionStatus getSession %s: %v", sessionID, err)
	} else if row != nil {
		unread = row.Unread
		shown = rowEnd(row)
	}
	// Reconciliation can also be how a session ends — a session its daemon no
	// longer reports is "stopped" for good — so a final terminal write owes
	// the broker a webhook too.
	if final && terminalSessionStatus(status) {
		h.NotifyCompletion(sessionID)
	}
	broadcastWithEnd(h, shown, func(endReason string, endedBy *protocol.EndedBy) any {
		return protocol.SessionStateChanged{
			Type:      "session_state_changed",
			SessionID: sessionID,
			Status:    status,
			Message:   message,
			Unread:    unread,
			EndReason: endReason,
			EndedBy:   endedBy,
		}
	})
}

// reconcileSessions makes the server's view of daemonID's sessions match the
// daemon's reported active set. It always (re)asserts hub ownership for reported
// sessions so input/kill routing works after a reconnect or server restart —
// reattached sessions never re-send session_started. Sessions active in the DB
// but absent from the report are stopped; sessions the daemon reports alive but
// the DB has in a terminal state (e.g. errored by a transient disconnect) are
// revived, because the daemon is authoritative about what is actually running.
func reconcileSessions(ctx context.Context, h *Hub, dbPool *pgxpool.Pool, daemonID string, activeSessions []string, states map[string]string) {
	active := make(map[string]struct{}, len(activeSessions))
	for _, id := range activeSessions {
		active[id] = struct{}{}
		h.SetSessionOwner(id, daemonID)
	}

	sessions, err := db.ListSessionsByDaemon(ctx, dbPool, daemonID)
	if err != nil {
		log.Printf("reconcile sessions for daemon %s: %v", daemonID, err)
		return
	}
	now := time.Now()
	for _, s := range sessions {
		_, reported := active[s.ID]
		// A session the daemon still reports alive, long after a stop was
		// requested for it, did not stop: drop the pending attribution so a
		// later, unrelated ending is not blamed on that request. Its status
		// may never change, so this cannot wait for a transition.
		if reported && s.EndReason != nil && !terminalSessionStatus(s.Status) {
			if _, err := db.ExpirePendingSessionEnd(ctx, dbPool, s.ID); err != nil {
				log.Printf("reconcile %s: expire pending end: %v", s.ID, err)
			}
		}
		desired, change := reconcileDesiredStatus(s.Status, reported, states[s.ID])
		if !change {
			continue
		}
		var endedAt *time.Time
		var end db.SessionEnd
		if desired == "stopped" {
			// The daemon is connected and does not report this session: its
			// process is gone for good, so its credentials go with it.
			endedAt = &now
			end = systemEnd(db.EndReasonDaemonUnreported)
			revokeSessionTokens(ctx, dbPool, s.ID, "reconcile: unreported by daemon")
		}
		setSessionStatusEnd(ctx, h, dbPool, s.ID, desired, endedAt, nil, desired == "stopped", end)
	}
}

// handleDaemonDisconnect updates DB state and broadcasts daemon_disconnected to
// all browsers after a daemon's connection is lost. It first claims the
// disconnect via UnregisterIfCurrent: if a reconnect has already replaced this
// connection, the cleanup is stale and its sessions live on under the new
// connection, so it does nothing.
func handleDaemonDisconnect(h *Hub, dc *DaemonConn, dbPool *pgxpool.Pool) {
	// Requests waiting on this connection can never be answered now.
	h.abandonReposRootRequests(dc)
	if !h.UnregisterIfCurrent(dc) {
		return
	}

	ctx := context.Background()
	affectedSessionIDs := []string{}

	if dbPool != nil {
		// Mark daemon as disconnected.
		if err := db.SetDaemonStatus(ctx, dbPool, dc.ID, "disconnected"); err != nil {
			log.Printf("set daemon %s disconnected: %v", dc.ID, err)
		}

		// Error-out active sessions owned by this daemon. If the daemon restarts
		// and reattaches (tmux-backed sessions), reconcileSessions revives them.
		sessions, err := db.ListSessionsByDaemon(ctx, dbPool, dc.ID)
		if err != nil {
			log.Printf("list sessions on disconnect %s: %v", dc.ID, err)
		} else {
			now := time.Now()
			errMsg := "daemon disconnected"
			for _, s := range sessions {
				if !isActiveStatus(s.Status) {
					continue
				}
				if dc.Mode == "runner" {
					// Pod death is not session death: the session resumes when
					// a new Job re-connects (or is re-created on demand).
					setSessionStatus(ctx, h, dbPool, s.ID, "disconnected", nil, nil, false)
				} else {
					// Revivable, so not final — but remember WHEN the daemon
					// was lost, so the reconciler can finalise the session if
					// it never comes back.
					setSessionStatus(ctx, h, dbPool, s.ID, "error", &now, &errMsg, false)
					if err := db.SetSessionDaemonLost(ctx, dbPool, s.ID); err != nil {
						log.Printf("SetSessionDaemonLost %s: %v", s.ID, err)
					}
				}
				affectedSessionIDs = append(affectedSessionIDs, s.ID)
			}
		}
	}

	// Broadcast daemon_disconnected regardless of DB availability.
	h.BroadcastJSON(protocol.DaemonDisconnected{
		Type:               "daemon_disconnected",
		DaemonID:           dc.ID,
		AffectedSessionIDs: affectedSessionIDs,
	})
}
