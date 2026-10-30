package server

// Sessions paused by a daemon restart (migration 043).
//
// A daemon that hosts an agent session again after a restart picks up what the restart cut off by
// itself, except for an interactive session that was waiting for the person (and one it had
// already continued once). Such a session is reported with session_paused; the row keeps the
// reason (sessions.paused_reason = "restart") until the session next runs, ends or is revived
// (UpdateSessionStatus, EndSessionStatus, InsertSession), so the sessions list can count it next
// to the sessions waiting for input and offer to resume them all.
//
// Two more things follow from a restart:
//   - after each hello the server tells the daemon which of its sessions have a `blerg-runner ask`
//     open (open_asks): a recovery record from an older daemon cannot say so itself;
//   - the asks of a paused session are closed: the process that waited for their answers died with
//     the restart, and an answer must now reach the session as a message (answerAskFromChat would
//     otherwise take it as the answer to an ask nobody reads).

import (
	"context"
	"encoding/json"
	"log"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// sendOpenAsks tells a daemon which of the sessions it reported have an open ask. Best effort: a
// daemon that does not hear it continues a session whose record says nothing of a question.
func sendOpenAsks(ctx context.Context, dbPool *pgxpool.Pool, dc *DaemonConn, sessionIDs []string) bool {
	if dbPool == nil || len(sessionIDs) == 0 {
		return true
	}
	ids, err := db.SessionsWithOpenAsks(ctx, dbPool, sessionIDs)
	if err != nil {
		log.Printf("daemon %s: open asks: %v", dc.ID, err)
		return false
	}
	if ids == nil {
		ids = []string{}
	}
	raw, err := json.Marshal(protocol.OpenAsks{Type: "open_asks", SessionIDs: ids})
	if err != nil {
		return false
	}
	select {
	case dc.send <- raw:
		return true
	default:
		log.Printf("daemon %s: send buffer full — open_asks not sent", dc.ID)
		return false
	}
}

// handleSessionPaused records a daemon's report that a session it hosts again after a restart
// waits for a message, closes the session's asks (nobody waits for their answers any more) and
// tells the browsers. Only for the daemon's own session, and only while it is idle or waiting.
func handleSessionPaused(ctx context.Context, h *Hub, dbPool *pgxpool.Pool, dc *DaemonConn, msg protocol.SessionPaused) {
	if dbPool == nil || msg.Reason != protocol.PausedByRestart {
		return
	}
	row, err := db.GetSession(ctx, dbPool, msg.SessionID)
	if err != nil {
		log.Printf("daemon %s: session_paused %s: %v", dc.ID, msg.SessionID, err)
		return
	}
	if row == nil || row.DaemonID != dc.ID {
		log.Printf("daemon %s: session_paused for a session it does not host (%s) — ignored", dc.ID, msg.SessionID)
		return
	}
	set, err := db.SetSessionPausedWithDetail(ctx, dbPool, msg.SessionID, msg.Reason, msg.Detail)
	if err != nil {
		log.Printf("session_paused %s: %v", msg.SessionID, err)
		return
	}
	if !set {
		return // running again already, ended, or reported before
	}
	h.SessionPausedForDelegation(msg.SessionID)
	h.ChildActivity(msg.SessionID)
	detail := msg.Detail
	if detail == "" {
		detail = "waiting for a message"
	}
	h.ChildTurnEnded(msg.SessionID, childRestartPaused, detail)
	closed, err := db.CloseOpenAsksForSession(ctx, dbPool, msg.SessionID)
	if err != nil {
		log.Printf("session_paused %s: close asks: %v", msg.SessionID, err)
	}
	for _, m := range closed {
		h.SignalAskAnswered(m.ID)
		h.BroadcastJSON(protocol.MessageAnswered{Type: "message_answered", Message: messageRowToInfo(m)}) //nolint:contextcheck // broadcasts have independent bounded delivery checks and outlive this request
	}
	if err := recordSessionFailure(ctx, h, dbPool, msg.SessionID); err != nil {
		log.Printf("session pause alert: %v", err)
	}
	h.BroadcastJSON(protocol.SessionPausedChanged{Type: "session_paused_changed", SessionID: msg.SessionID, PausedReason: msg.Reason}) //nolint:contextcheck // broadcasts have independent bounded delivery checks and outlive this request
}

// clearPausedOnMessage drops a session's paused reason once a message for it has been accepted
// (delivered, or held for its daemon) and tells every browser, so a second tab or a reload does
// not offer to resume it again and send a second continue.
func clearPausedOnMessage(ctx context.Context, h *Hub, dbPool *pgxpool.Pool, sessionID string) {
	if dbPool == nil {
		return
	}
	cleared, err := db.ClearSessionPaused(ctx, dbPool, sessionID)
	if err != nil {
		log.Printf("clear paused %s: %v", sessionID, err)
		return
	}
	if cleared {
		h.BroadcastJSON(protocol.SessionPausedChanged{Type: "session_paused_changed", SessionID: sessionID}) //nolint:contextcheck // broadcasts have independent bounded delivery checks and outlive this request
	}
}
