package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// activityTTL is how recently a browser must have reported user interaction for
// the user to count as "engaged". Within it, transitions surface as in-app
// toasts (client-side); beyond it, as OS push notifications. Mirrored by the
// frontend's ACTIVITY_TTL_MS.
const activityTTL = 3 * time.Minute

// validTransitions maps each status to the set of statuses it may transition to.
var validTransitions = map[string]map[string]bool{
	// starting → idle: an agent session with no initial prompt is ready and
	// waiting for its first message (the daemon says so right after
	// session_started, instead of leaving the row at "starting" until a
	// heartbeat reconciles it).
	"starting": {"running": true, "idle": true, "error": true},
	"running":  {"waiting": true, "idle": true, "stopped": true, "error": true},
	"waiting":  {"running": true, "stopped": true, "error": true},
	"idle":     {"running": true, "waiting": true, "stopped": true, "error": true},
	// disconnected (cluster pod died): resumable into any live state.
	"disconnected": {"starting": true, "running": true, "waiting": true, "idle": true, "stopped": true, "error": true},
	// terminal states — no outgoing transitions
	"stopped": {},
	"error":   {},
}

// isValidTransition reports whether transitioning from → to is permitted by the
// session state machine.
func isValidTransition(from, to string) bool {
	targets, known := validTransitions[from]
	if !known {
		return false
	}
	return targets[to]
}

// sessionNotification returns the push title and body to show when a session
// enters `status`, and ok=false for statuses that should stay silent. Only the
// "stop, the human is needed" states notify: waiting (a turn paused for input)
// and idle (a turn finished). label is the session's human name.
func sessionNotification(status, label string) (title, body string, ok bool) {
	switch status {
	case "waiting":
		return "Session waiting", fmt.Sprintf("%s is waiting for your input.", label), true
	case "idle":
		return "Session idle", fmt.Sprintf("%s finished its turn.", label), true
	default:
		return "", "", false
	}
}

// sessionLabel picks a human name for a session in notifications, preferring the
// title, then the repo, then the first segment of the UUID so a nameless session
// still reads sensibly instead of leaking a bare id.
func sessionLabel(title, repo, id string) string {
	if title != "" {
		return title
	}
	if repo != "" {
		return repo
	}
	if i := strings.IndexByte(id, '-'); i > 0 {
		return id[:i]
	}
	return id
}

// HandleSessionStarted handles a session_started message from a daemon.
// It inserts a new session row, appends a state_change event, and broadcasts
// to all browsers.
func HandleSessionStarted(ctx context.Context, hub *Hub, pool *pgxpool.Pool, daemonID string, msg protocol.SessionStarted) {
	if pool != nil {
		// A session the server already recorded as private (notePrivateInsert, before its row was
		// written) goes in private and owned, so a pre-create that failed cannot leave a public row
		// until MarkPrivate runs. An upsert only ever adds an origin.
		var origin db.SessionOrigin
		if owners, ok := hub.privateOwners([]string{msg.SessionID}); ok {
			if owner, private := owners[msg.SessionID]; private {
				origin = db.SessionOrigin{SpawningAccount: owner, Private: true}
			}
		}
		if err := db.InsertSessionAs(ctx, pool, msg.SessionID, daemonID, "starting",
			msg.ProjectPath, msg.Repo, msg.Title, msg.Model, origin); err != nil {
			log.Printf("InsertSession %s: %v", msg.SessionID, err)
		}
		if err := db.AppendSessionEvent(ctx, pool, msg.SessionID, "state_change",
			`{"status":"starting"}`, 0); err != nil {
			log.Printf("AppendSessionEvent session_started %s: %v", msg.SessionID, err)
		}
		if msg.Kind == "agent" {
			if err := db.SetSessionKind(ctx, pool, msg.SessionID, "agent"); err != nil {
				log.Printf("SetSessionKind %s: %v", msg.SessionID, err)
			}
		}
	}

	hub.SetSessionOwner(msg.SessionID, daemonID)
	if msg.Cols > 0 {
		hub.SetSessionPTYCols(msg.SessionID, msg.Cols)
	}

	// Where the session runs is decided by the server at spawn and lives on
	// the row, not in the daemon's session_started — read it back so the
	// first message the browser sees about this session already carries the
	// same runtime/kind labels the sessions list shows.
	kind := msg.Kind
	runtime := ""
	effort := "" // the launch effort, recorded on the row at spawn
	engine := ""
	cronID := "" // set when a cron started the session, so the badge shows from the first message
	if pool != nil {
		if row, err := db.GetSession(ctx, pool, msg.SessionID); err != nil {
			log.Printf("session_started GetSession %s: %v", msg.SessionID, err)
		} else if row != nil {
			if row.Runtime != nil {
				runtime = *row.Runtime
			}
			if row.Effort != nil {
				effort = *row.Effort
			}
			engine = derefOrEmpty(row.Engine)
			cronID = derefOrEmpty(row.CronID)
			if kind == "" {
				kind = row.Kind
			}
		}
	}

	// The engine is starting: every stage before it is behind the session.
	// Derived (not authoritative), so it never overrides a pod's own report.
	advanceStart(ctx, hub, pool, msg.SessionID, false,
		protocol.StartStage{ID: protocol.StageEngine, State: protocol.StageStateActive, Detail: "Starting the engine"})

	hub.BroadcastJSON(protocol.BrowserSessionStarted{
		Type: "session_started",
		Session: protocol.SessionInfo{
			ID:          msg.SessionID,
			DaemonID:    daemonID,
			Status:      "starting",
			ProjectPath: msg.ProjectPath,
			Repo:        msg.Repo,
			Title:       msg.Title,
			Model:       msg.Model,
			Effort:      effort,
			Engine:      engine,
			StartedAt:   time.Now().UTC().Format(time.RFC3339),
			Kind:        kind,
			Runtime:     runtime,
			CronID:      cronID,
		},
	})
}

// handleSessionNotFound is called when a session_state_changed message arrives
// for a session that has no DB row (i.e. a pre-start spawn failure where the
// daemon emits Status:"error" before session_started). For the "error" status
// we broadcast immediately so browsers can show the spawn failure. All other
// statuses are silently dropped: they should never arrive before session_started
// and there is nothing useful to do without a DB row.
func handleSessionNotFound(hub *Hub, msg protocol.SessionStateChanged) error {
	if msg.Status == "error" {
		hub.BroadcastJSON(msg)
		return nil
	}
	log.Printf("HandleSessionStateChanged: session %s not found, skipping", msg.SessionID)
	return nil
}

// HandleSessionStateChanged handles a session_state_changed message from a daemon.
// It validates the transition, updates Postgres, broadcasts to browsers, and —
// when the session enters waiting/idle and no one is actively engaged — sends an
// OS push notification (engaged users get an in-app toast from the broadcast).
func HandleSessionStateChanged(ctx context.Context, hub *Hub, pool *pgxpool.Pool, msg protocol.SessionStateChanged) error {
	// End attribution is the server's to state (filled from the row below);
	// a daemon never gets to put words in it.
	msg.EndReason, msg.EndedBy = "", nil
	var shown sessionEnd
	if pool != nil {
		current, err := db.GetSession(ctx, pool, msg.SessionID)
		if err != nil {
			return fmt.Errorf("GetSession %s: %w", msg.SessionID, err)
		}
		if current == nil {
			return handleSessionNotFound(hub, msg)
		}

		if !isValidTransition(current.Status, msg.Status) {
			log.Printf("HandleSessionStateChanged: invalid transition %s → %s for session %s, skipping",
				current.Status, msg.Status, msg.SessionID)
			return nil
		}

		// A cluster pod that reports "error" while its session is still
		// "starting" is saying the start failed (clone failure, missing
		// credential) and it is about to exit: nothing will ever revive that
		// session, and the reconciler does not look at "error" rows. So this
		// error is the session's end — ended_at, tokens revoked, completion
		// webhook — exactly like a Job that could not be created.
		finalStartFailure := current.Status == "starting" && msg.Status == "error" &&
			current.Runtime != nil && *current.Runtime == "cluster"
		var endedAt *time.Time
		if finalStartFailure {
			now := time.Now()
			endedAt = &now
		}
		// The endings this message is (see the completion rule below) are
		// attributed with the status: a start that failed, or a process the
		// daemon says is gone. A stop requested earlier keeps its own
		// attribution (first write wins). A plain "error" is revivable, not an
		// ending, so it records nothing.
		var end db.SessionEnd
		switch {
		case finalStartFailure:
			end = systemEnd(db.EndReasonStartFailed)
		case terminalSessionStatus(msg.Status) && msg.Status != "error":
			end = systemEnd(db.EndReasonProcessExited)
		}
		var updateErr error
		if end.Reason != "" {
			updateErr = db.EndSessionStatus(ctx, pool, msg.SessionID, msg.Status, endedAt, end)
		} else {
			updateErr = db.UpdateSessionStatus(ctx, pool, msg.SessionID, msg.Status, endedAt)
		}
		if updateErr != nil {
			return fmt.Errorf("UpdateSessionStatus %s → %s: %w", msg.SessionID, msg.Status, updateErr)
		}

		// Persist why a spawn/run failed so the row isn't left with a bare
		// "error" status once the daemon's one-shot broadcast is gone (trial
		// blocker 2 / R7).
		if msg.Status == "error" && msg.Message != nil && *msg.Message != "" {
			if err := db.SetSessionError(ctx, pool, msg.SessionID, *msg.Message); err != nil {
				log.Printf("SetSessionError %s: %v", msg.SessionID, err)
			}
		}

		// A terminal state the daemon reports is only a COMPLETION when the
		// daemon is saying the session is over. "stopped" is that: the process
		// is gone, so the session's credentials go with it and the broker is
		// owed its callback. "error" is not: reconcileSessions revives a
		// session the daemon still reports as alive (that is how a transient
		// engine failure or a WS blip heals), so notifying here would report a
		// completion for a session that is still going — and, because delivery
		// is claimed once per session, burn the one delivery the caller was
		// promised. A session that really is finished is finalised by the
		// daemon's session_ended, or by the reconciler's sweep.
		if terminalSessionStatus(msg.Status) && msg.Status != "error" {
			revokeSessionTokens(ctx, pool, msg.SessionID, "daemon reported "+msg.Status)
			hub.NotifyCompletion(msg.SessionID)
		} else if finalStartFailure {
			revokeSessionTokens(ctx, pool, msg.SessionID, "cluster start failed")
			hub.NotifyCompletion(msg.SessionID)
		}

		data, _ := json.Marshal(map[string]string{"status": msg.Status})
		if err := db.AppendSessionEvent(ctx, pool, msg.SessionID, "state_change",
			string(data), 0); err != nil {
			log.Printf("AppendSessionEvent state_change %s: %v", msg.SessionID, err)
		}

		// Leaving a start (fresh or resumed) closes its start attempt: live
		// means ready, error means the stage it was on failed.
		starting := current.Status == "starting" || current.Status == "disconnected"
		if starting && isLiveStatus(msg.Status) {
			markStartReady(ctx, hub, pool, msg.SessionID, true)
		} else if starting && msg.Status == "error" {
			reason := "The session failed to start"
			if msg.Message != nil && *msg.Message != "" {
				reason = *msg.Message
			}
			failStart(ctx, hub, pool, msg.SessionID, reason, "")
		}
		// A session that has just become ready and idle has not "finished a
		// turn" — it has not had one. No unread badge, no push for that.
		readyIdle := (current.Status == "starting" || current.Status == "disconnected") && msg.Status == "idle"

		// Set unread=true on genuine transitions into waiting or idle.
		// isValidTransition already rejected any self-loop (e.g. idle→idle), so
		// reaching here always means the status genuinely changed.
		newUnread := current.Unread
		if (msg.Status == "waiting" || msg.Status == "idle") && !readyIdle {
			newUnread = true
			if err := db.SetSessionUnread(ctx, pool, msg.SessionID, true); err != nil {
				log.Printf("SetSessionUnread %s: %v", msg.SessionID, err)
			}
		}
		// Carry the post-update unread value in the broadcast so clients update
		// status and unread badge atomically.
		msg.Unread = newUnread
		// The end attribution comes from the row, never from the daemon's
		// message: whatever the daemon put in these fields is replaced.
		shown = sessionEndFor(ctx, pool, msg.SessionID, msg.Status)

		// Send an OS push only when the user is away. If anyone has interacted
		// recently, they're engaged and the client shows an in-app toast instead.
		// Never for a private session: a subscription belongs to no account, so
		// the push could not be limited to the session's owner (privacy.go).
		if !hub.ActiveWithin(activityTTL) && !readyIdle && !current.Private {
			var title string
			if current.Title != nil {
				title = *current.Title
			}
			if pushTitle, pushBody, ok := sessionNotification(msg.Status, sessionLabel(title, current.Repo, current.ID)); ok {
				SendPush(pool, pushTitle, pushBody, "/sessions/"+msg.SessionID) //nolint:contextcheck // push delivery is best-effort background work with no tie to this request
			}
		}
	}

	broadcastWithEnd(hub, shown, func(endReason string, endedBy *protocol.EndedBy) any {
		m := msg
		m.EndReason, m.EndedBy = endReason, endedBy
		return m
	})

	return nil
}

// HandleSessionEnded handles a session_ended message from a daemon.
// It sets ended_at, updates the status, appends a state_change event, and
// broadcasts to browsers.
func HandleSessionEnded(ctx context.Context, hub *Hub, pool *pgxpool.Pool, msg protocol.SessionEnded) {
	finalStatus := "stopped"
	if msg.ExitCode != 0 {
		finalStatus = "error"
	}

	// A process exit with no stop asked for ended the session by itself. A
	// stop recorded when it was requested (DELETE, runner stop) is kept: this
	// exit is the stop taking effect.
	endReason := db.EndReasonProcessExited
	if msg.ExitCode != 0 {
		endReason = db.EndReasonProcessFailed
	}
	var shown sessionEnd

	if pool != nil {
		now := time.Now()
		if err := db.EndSessionStatus(ctx, pool, msg.SessionID, finalStatus, &now, systemEnd(endReason)); err != nil {
			log.Printf("UpdateSessionStatus session_ended %s: %v", msg.SessionID, err)
		}
		shown = sessionEndFor(ctx, pool, msg.SessionID, finalStatus)

		data, _ := json.Marshal(map[string]any{
			"status":    finalStatus,
			"exit_code": msg.ExitCode,
		})
		if err := db.AppendSessionEvent(ctx, pool, msg.SessionID, "state_change",
			string(data), 0); err != nil {
			log.Printf("AppendSessionEvent session_ended %s: %v", msg.SessionID, err)
		}

		// The ordinary end of a session (both "stopped" and a non-zero exit's
		// "error" are terminal), and so the ordinary webhook trigger.
		hub.NotifyCompletion(msg.SessionID)
	}

	broadcastWithEnd(hub, shown, func(endReason string, endedBy *protocol.EndedBy) any {
		return protocol.BrowserSessionEnded{
			Type:      "session_ended",
			SessionID: msg.SessionID,
			ExitCode:  msg.ExitCode,
			Signal:    msg.Signal,
			EndReason: endReason,
			EndedBy:   endedBy,
		}
	})

	// Close any open messages for this session and broadcast each one, so the
	// open-questions badge doesn't accrue permanent zombies from a dead session.
	if pool != nil {
		closed, err := db.CloseOpenMessagesForSession(ctx, pool, msg.SessionID)
		if err != nil {
			log.Printf("CloseOpenMessagesForSession session_ended %s: %v", msg.SessionID, err)
		}
		for _, row := range closed {
			hub.BroadcastJSON(protocol.MessageAnswered{
				Type:    "message_answered",
				Message: messageRowToInfo(row),
			})
		}
	}

	hub.UnsubscribeSession(msg.SessionID)
	hub.RemoveSessionOwner(msg.SessionID)

	// Best-effort: revoke board tokens and clear ticket binding for assist sessions.
	// Errors are logged but not fatal — the session is already ended.
	if pool != nil {
		if err := db.RevokeBoardTokensForSession(ctx, pool, msg.SessionID); err != nil {
			log.Printf("RevokeBoardTokensForSession session_ended %s: %v", msg.SessionID, err)
		}
		revokeSessionGrants(ctx, pool, msg.SessionID, "session_ended")
		if err := db.ClearTicketSessionBySession(ctx, pool, msg.SessionID); err != nil {
			log.Printf("ClearTicketSessionBySession session_ended %s: %v", msg.SessionID, err)
		}
	}
}

// HandleSessionMetaChanged handles a session_meta_changed message from a daemon.
// It updates the model/effort columns in Postgres and broadcasts to browsers.
func HandleSessionMetaChanged(ctx context.Context, hub *Hub, pool *pgxpool.Pool, msg protocol.SessionMetaChanged) {
	if pool != nil {
		if err := db.UpdateSessionMeta(ctx, pool, msg.SessionID, msg.Model, msg.Effort); err != nil {
			log.Printf("UpdateSessionMeta %s: %v", msg.SessionID, err)
		}
	}
	hub.BroadcastJSON(msg)
}

// HandleSessionOutput handles a session_output message from a daemon.
// It appends the output to Postgres and fans it out to subscribed browsers only.
func HandleSessionOutput(ctx context.Context, hub *Hub, pool *pgxpool.Pool, msg protocol.SessionOutput) {
	if pool != nil {
		if err := db.AppendSessionEvent(ctx, pool, msg.SessionID, "output",
			msg.Data, msg.Seq); err != nil {
			log.Printf("AppendSessionEvent output %s seq %d: %v", msg.SessionID, msg.Seq, err)
		}
	}

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("marshal session_output %s: %v", msg.SessionID, err)
		return
	}
	hub.FanOutSessionOutput(msg.SessionID, data)
}
