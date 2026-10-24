package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// forwardToSessionDaemon routes a control message to the daemon owning a
// session (send_input pattern).
func forwardToSessionDaemon(h *Hub, bc *BrowserConn, sessionID string, msg any) bool {
	dc := h.FindDaemonForSession(sessionID)
	if dc == nil {
		log.Printf("browser %s: no daemon found for session %s", bc.ID, sessionID)
		return false
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return false
	}
	if h.trySendToDaemon(dc, raw) {
		return true
	}
	log.Printf("browser %s: daemon %s send channel full", bc.ID, dc.ID)
	return false
}

// RefreshSessionTools tells the daemon running sessionID that the session's MCP tool definitions
// changed (an accept, an auto-accept, a cron re-pin), so its agent lists them again at the next
// idle point. Best effort: false when no daemon holds the session (a resume will re-list anyway)
// or its queue is full.
func (h *Hub) RefreshSessionTools(sessionID string) bool {
	dc := h.FindDaemonForSession(sessionID)
	if dc == nil {
		return false
	}
	raw, err := json.Marshal(protocol.RefreshSessionTools{Type: "refresh_session_tools", SessionID: sessionID})
	if err != nil {
		return false
	}
	// trySendToDaemon (reposroot.go) checks the connection is still current and recovers from a
	// send on a channel the read pump closed: this runs from accept handlers and cron goroutines.
	if !h.trySendToDaemon(dc, raw) {
		log.Printf("refresh tools %s: daemon %s gone or send channel full", sessionID, dc.ID)
		return false
	}
	return true
}

// appendAgentEvent stores an agent event (a variable so a test can make the database fail).
var appendAgentEvent = db.AppendAgentEventAt

// The agent event kinds a background task's start and end are recorded as (the Claude Code driver's
// claudebackground.go); the session row counts the tasks running from them.
const (
	backgroundStartedKind = "background_task_started"
	backgroundDoneKind    = "background_task_done"
)

// HandleAgentEvent processes one non-transient agent event from a daemon:
// persist (server-assigned seq, idempotent on client_event_id), ack back to
// the daemon, fan out to subscribed browsers, and apply side effects
// (model/effort mirroring, status transitions).
func HandleAgentEvent(ctx context.Context, h *Hub, pool *pgxpool.Pool, dc *DaemonConn, msg protocol.AgentEvent) {
	if pool != nil {
		blocked, err := db.SessionDiscardBlocked(ctx, pool, msg.SessionID)
		if err != nil {
			return
		} // retryable storage failure; never guess permission
		if blocked {
			ackAgentEvent(dc, msg, 0)
			return
		}
	}
	progressChild := ""
	if msg.Kind == protocol.UsageUpdatedKind {
		usage, ok := protocol.DecodeUsageUpdated(msg.Payload)
		if !ok {
			ackAgentEvent(dc, msg, 0)
			return
		}
		msg.Payload, _ = json.Marshal(usage)
		msg.Transient = false
	}
	if msg.Kind == protocol.PlanUpdatedKind {
		plan, ok := protocol.DecodePlanUpdated(msg.Payload)
		if !ok {
			ackAgentEvent(dc, msg, 0)
			return
		}
		msg.Payload, _ = json.Marshal(plan)
		progressChild, _ = protocol.ProgressScope(plan.ParentCallID, plan.ChildID)
		msg.Transient = false
	}
	if msg.Kind == protocol.ProgressUpdatedKind {
		progress, ok := protocol.DecodeProgressUpdated(msg.Payload)
		if !ok {
			ackAgentEvent(dc, msg, 0)
			return
		}
		progressChild, _ = protocol.ProgressScope(progress.ParentCallID, progress.ChildID)
		msg.Payload, _ = json.Marshal(progress)
		msg.Transient = false
	}
	if progressChild != "" && pool == nil {
		ackAgentEvent(dc, msg, 0)
		return
	}
	isBackground := msg.Kind == backgroundStartedKind || msg.Kind == backgroundDoneKind
	isSubagent := msg.Kind == "subagent_started" || msg.Kind == "subagent_update" || msg.Kind == "subagent_done"
	backgroundCount := 0

	if isBackground {
		var valid bool
		msg.Payload, valid = backgroundTaskPayload(msg.Payload, dc.BackgroundTaskLifetime)
		if !valid {
			ackAgentEvent(dc, msg, 0)
			return
		}
		msg.Transient = false
	}
	if isSubagent {
		var p map[string]json.RawMessage
		if json.Unmarshal(msg.Payload, &p) != nil || p == nil {
			ackAgentEvent(dc, msg, 0)
			return
		}
		var childID string
		if json.Unmarshal(p["child_id"], &childID) != nil || !protocol.ValidBackgroundTaskID(childID) {
			ackAgentEvent(dc, msg, 0)
			return
		}
		stripBackgroundTaskMarkers(p)
		msg.Payload, _ = json.Marshal(p)
		msg.Transient = false
	}
	if msg.Kind == protocol.CapabilitiesKind {
		// Shown to users as-is: re-check it here rather than trusting the
		// sender (see protocol.SanitizeCapabilities). A payload that isn't
		// even the right shape is dropped — and acked, so the daemon stops
		// resending it.
		var p protocol.CapabilitiesPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.Printf("agent_event %s: dropping malformed capabilities: %v", msg.SessionID, err)
			ackAgentEvent(dc, msg, 0)
			return
		}
		raw, err := json.Marshal(protocol.SanitizeCapabilities(p))
		if err != nil {
			return
		}
		msg.Payload = raw
	}
	if msg.Kind == protocol.MCPStatusKind {
		var payload protocol.MCPStatusPayload
		if json.Unmarshal(msg.Payload, &payload) != nil {
			ackAgentEvent(dc, msg, 0)
			return
		}
		clean := protocol.SanitizeMCPStatus(payload)
		if clean.Engine == "" || clean.Since == "" {
			ackAgentEvent(dc, msg, 0)
			return
		}
		msg.Payload, _ = json.Marshal(clean)
		msg.Transient = false // Inventory snapshots must be sanitized and durable, never raw deltas.
	}
	// Fence only the owning connection check and durable work append. Later
	// acknowledgements, input settlement and notifications do not hold it.
	unlockWork := func() {}
	if isBackground || isSubagent || progressChild != "" {
		unlockWork = h.lockDaemonWork(dc.ID)
		defer unlockWork()
		if current := h.GetDaemon(dc.ID); current != dc && (current != nil || dc.workRegistered) {
			return
		}
	}
	seq := msg.Seq
	// Without a database every event counts as new; with one, only an event this call stored does.
	inserted := pool == nil
	// An older daemon may still send a NUL escape, which jsonb refuses; store U+FFFD instead of
	// refusing the event for good (protocol.StripNUL).
	msg.Payload = protocol.StripNUL(msg.Payload)
	// The event's time is when the daemon saw it happen, not when it reached us (a resend after
	// an outage would otherwise carry the reconnect time), within limits (eventTime). The live
	// fan-out carries the same value the row stores, so a reload shows the same time.
	limitEvent := msg // cancellation uses the original engine time, never the display-time clamp
	at := eventTime(msg.Ts, time.Now())
	msg.Ts = at.UTC().Format(time.RFC3339Nano)
	if pool != nil {
		// In order (ingestorder.go): while an earlier event of this session failed to store, a later
		// one waits, unacknowledged, for the daemon to re-send both in order. It stays in the
		// daemon's buffer meanwhile; a buffer at its cap (pendingCap) stalls the session's engine
		// rather than dropping anything.
		if !h.ingest.admit(msg.SessionID, msg.ClientEventID) {
			return
		}
		var s int64
		var fresh bool
		var err error
		switch {
		case progressChild != "":
			s, fresh, err = db.AppendChildProgressAt(ctx, pool, msg.SessionID, msg.ClientEventID, msg.Kind, string(msg.Payload), progressChild, dc.ID, at)
		case isBackground:
			s, fresh, backgroundCount, err = db.AppendBackgroundTaskEventAt(ctx, pool, msg.SessionID, msg.ClientEventID, msg.Kind, string(msg.Payload), at, dc.ID)
		case isSubagent:
			s, fresh, err = db.AppendSubagentEventAt(ctx, pool, msg.SessionID, msg.ClientEventID, msg.Kind, string(msg.Payload), at, dc.ID)
		default:
			s, fresh, err = appendAgentEvent(ctx, pool, msg.SessionID, msg.ClientEventID, msg.Kind, string(msg.Payload), at)
		}
		if errors.Is(err, db.ErrBackgroundTaskAuthority) {
			h.ingest.stored(msg.SessionID, msg.ClientEventID) // This event is permanently refused, not retryable.
			ackAgentEvent(dc, msg, 0)
			return
		}
		if err != nil {
			log.Printf("agent_event append %s/%s: %v", msg.SessionID, msg.Kind, err)
			h.ingest.failed(msg.SessionID, msg.ClientEventID, err)
			return // no ack — the daemon will re-send
		}
		seq, inserted = s, fresh
	}

	unlockWork()

	if err := settleSessionInput(ctx, pool, msg); err != nil {
		h.ingest.failed(msg.SessionID, msg.ClientEventID, err)
		return
	}

	// Retry scheduling is durable before acknowledgement, including a replay
	// whose event append succeeded but whose scheduling write failed.
	if err := ingestLimitResume(ctx, h, pool, limitEvent, seq); err != nil {
		log.Printf("limit resume ingest %s: %v", msg.SessionID, err)
		h.ingest.failed(msg.SessionID, msg.ClientEventID, err)
		return
	}

	if pool != nil {
		h.ingest.stored(msg.SessionID, msg.ClientEventID)
	}
	// Ack persistence so the daemon can drop it from its pending buffer. A duplicate is acked
	// too: it is stored, and the daemon must stop re-sending it.
	ackAgentEvent(dc, msg, seq)
	if !inserted {
		// A re-sent event already recorded (its ack was lost, or the daemon reconnected): it
		// was fanned out and its side effects applied when it was stored. Applying them again
		// would let a stale status_changed{running} flip an idle row back, or an old
		// model_changed restore the old model.
		return
	}

	// Fan out to subscribed browsers with the assigned seq.
	switch msg.Kind {
	case "turn_done", "subagent_done", "ask", "user_message", "tool_call", "tool_result", protocol.UsageUpdatedKind:
		h.ChildActivity(msg.SessionID)
	}
	msg.Seq = seq
	if raw, err := json.Marshal(msg); err == nil {
		h.FanOutAgentEvent(msg.SessionID, seq, raw)
	}

	// Side effects.
	switch msg.Kind {
	case "model_changed":
		var p struct {
			Model  string `json:"model"`
			Effort string `json:"effort"`
		}
		if err := json.Unmarshal(msg.Payload, &p); err == nil && pool != nil {
			if err := db.UpdateSessionModelEffort(ctx, pool, msg.SessionID, p.Model, p.Effort); err != nil {
				log.Printf("agent model mirror %s: %v", msg.SessionID, err)
			}
			h.BroadcastJSON(protocol.SessionMetaChanged{ //nolint:contextcheck // broadcasts have independent bounded delivery checks and outlive this request
				Type: "session_meta_changed", SessionID: msg.SessionID, Model: p.Model, Effort: p.Effort,
			})
		}
	case "status_changed":
		var p struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(msg.Payload, &p); err == nil && p.Status != "" {
			if err := HandleSessionStateChanged(ctx, h, pool, protocol.SessionStateChanged{
				Type: "session_state_changed", SessionID: msg.SessionID, Status: p.Status,
			}); err != nil {
				log.Printf("agent status %s → %s: %v", msg.SessionID, p.Status, err)
			}
		}
	case protocol.StartStageKind:
		// The pod's own report of a start stage (connect/clone/engine):
		// already persisted and fanned out above; the tracker learns it so
		// the server's derived updates don't contradict it.
		var p protocol.StartStagePayload
		if err := json.Unmarshal(msg.Payload, &p); err == nil {
			h.starts.merge(msg.SessionID, p.Stages, true)
		}
	case backgroundStartedKind, backgroundDoneKind:
		if pool != nil {
			h.BroadcastJSON(protocol.SessionBackgroundChanged{ //nolint:contextcheck // broadcasts have independent bounded delivery checks and outlive this request
				Type: "session_background_changed", SessionID: msg.SessionID, BackgroundTasks: backgroundCount,
			})
		}
	case turnDoneKind:
		// A one-shot session (start with auto_stop) is over as soon as its
		// first turn is recorded; every other session is untouched by this.
		// It runs after the append above, so the transcript the result reports
		// already contains the turn that ended the session.
		h.AutoStopOnTurnDone(msg.SessionID)
		// A child's parent is told its turn ended (child_turn_notes.go); one the parent or the
		// person interrupted is not news to them.
		var p struct {
			StopReason string `json:"stop_reason"`
		}
		_ = json.Unmarshal(msg.Payload, &p)
		switch p.StopReason {
		case "interrupted":
		case "error":
			h.ChildTurnEnded(msg.SessionID, childTurnFailed, "")
		default:
			h.ChildTurnEnded(msg.SessionID, childTurnFinished, "")
		}
		// The session's own plan is checked against what the turn did (plansync.go).
		h.PlanTurnEnded(msg.SessionID, seq, at, p.StopReason)
	case "ask":
		// The agent asked the person something: that question's own notice says so, and a
		// deferred idle notice (notices.go) would only repeat it.
		h.CancelIdleNotice(msg.SessionID)
	}

	// Work the session ran has ended: an idle session waiting on it may now be done (notices.go).
	if msg.Kind == backgroundDoneKind || msg.Kind == "subagent_done" {
		noticeOnWorkEnded(ctx, h, pool, msg.SessionID)
	}
}

// eventTimeSkew bounds how far a daemon-reported event time may sit from the server's clock. A
// daemon's clock can be wrong; an event is never stored as happening more than this far in the
// past or the future.
const eventTimeSkew = 10 * time.Minute

// eventTime is the time an agent event is stored with: the daemon's ts (RFC 3339) clamped to
// eventTimeSkew around now, or now when the ts is missing or unreadable.
func eventTime(ts string, now time.Time) time.Time {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if ts == "" || err != nil {
		return now
	}
	if lo := now.Add(-eventTimeSkew); t.Before(lo) {
		return lo
	}
	if hi := now.Add(eventTimeSkew); t.After(hi) {
		return hi
	}
	return t
}

// ackAgentEvent tells the daemon an event is settled (persisted, or
// deliberately dropped) so it leaves its resend buffer.
func ackAgentEvent(dc *DaemonConn, msg protocol.AgentEvent, seq int64) {
	if ack, err := json.Marshal(protocol.AgentEventAck{
		Type: "agent_event_ack", SessionID: msg.SessionID, ClientEventID: msg.ClientEventID, Seq: seq,
	}); err == nil {
		select {
		case dc.send <- ack:
		default:
			log.Printf("agent_event ack %s: daemon %s send channel full", msg.SessionID, dc.ID)
		}
	}
}

// HandleGetCapabilities is GET /api/sessions/{id}/capabilities (browser
// session): the latest capabilities event of a session — what it has
// loaded — as {capabilities, seq, ts}, or {"capabilities": null} when none
// was reported. The transcript carries the same events, but a browser
// replays only the first window of one, so the Skills & plugins panel asks
// for the latest directly.
func (a *API) HandleGetCapabilities(w http.ResponseWriter, r *http.Request) {
	err := a.sessionReadFence(r.Context(), r.PathValue("id"), func(ctx context.Context) error {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Second))
		a.snapshotHandleGetCapabilities(w, r.WithContext(ctx))
		return nil
	})
	if err != nil {
		writeError(w, 409, "session source changed; refresh")
	}
}

func (a *API) snapshotHandleGetCapabilities(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("id")
	if !a.browserCanSee(r.Context(), principal.Sub, sessionID) || !a.hub.automaticDesktopEventVisible(r.Context(), principal.Sub, sessionID) {
		// Same answer as for a session that has no capabilities report, so
		// the route is not an oracle for which private sessions exist.
		writeJSON(w, http.StatusOK, map[string]any{"capabilities": nil})
		return
	}
	out := map[string]any{"capabilities": nil}
	if a.dbPool != nil {
		rows, err := db.ListAgentEventsTail(r.Context(), a.dbPool, sessionID, protocol.CapabilitiesKind, 1)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "list error")
			return
		}
		if len(rows) == 1 {
			out = map[string]any{
				"capabilities": json.RawMessage(rows[0].Payload),
				"seq":          rows[0].Seq,
				"ts":           rows[0].Ts.UTC().Format(time.RFC3339Nano),
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSubscribeAgentEvents mirrors handleSubscribeSession's ordering
// (subscribe → replay from DB → drain buffered live events → forwarder) so a
// browser never misses events between replay and live streaming. Agent
// sessions have no PTY focus semantics, so no focus stealing here.
func handleSubscribeAgentEvents(ctx context.Context, h *Hub, bc *BrowserConn, pool *pgxpool.Pool, msg protocol.SubscribeAgentEvents) {
	bc.subMu.Lock()
	if _, exists := bc.subs[msg.SessionID]; exists {
		bc.subMu.Unlock()
		// Already live-subscribed; still serve the requested replay window
		// (lazy-loading older pages goes through this path).
		replayAgentEvents(ctx, h, bc, pool, msg)
		return
	}
	bc.subMu.Unlock()

	bufferChan := make(chan []byte, 256)
	h.Subscribe(msg.SessionID, bc.ID, bufferChan)

	replayAgentEvents(ctx, h, bc, pool, msg)

	// forward delivers one buffered live message, then — if the hub had to drop a persisted event
	// for this browser because the buffer was full — the gap notice (hubgap.go), so the browser
	// fetches what it missed instead of moving past it.
	forward := func(ctx context.Context, m []byte) bool {
		select {
		case bc.send <- m:
		case <-ctx.Done():
			return false
		}
		if from := h.TakeAgentEventGap(msg.SessionID, bc.ID); from > 0 {
			select {
			case bc.send <- agentEventsGapMessage(msg.SessionID, from):
			case <-ctx.Done():
				return false
			}
		}
		return true
	}

	// Drain live events buffered during the DB query, then start forwarding.
drainLoop:
	for {
		select {
		case m := <-bufferChan:
			if !forward(ctx, m) {
				h.Unsubscribe(msg.SessionID, bc.ID)
				return
			}
		default:
			break drainLoop
		}
	}
	subCtx, subCancel := context.WithCancel(ctx)
	bc.subMu.Lock()
	bc.subs[msg.SessionID] = subCancel
	bc.subMu.Unlock()
	go func() {
		defer subCancel()
		for {
			select {
			case m := <-bufferChan:
				if !forward(subCtx, m) {
					return
				}
			case <-subCtx.Done():
				return
			}
		}
	}()
}

// replayAgentEvents sends a window of persisted events + replay_done marker.
// Forward (the default) is the oldest events after AfterSeq. Tail or BeforeSeq
// read backwards from the end of the transcript instead: a long transcript is
// shown from its newest events at once and the older ones follow in pages.
func replayAgentEvents(ctx context.Context, h *Hub, bc *BrowserConn, pool *pgxpool.Pool, msg protocol.SubscribeAgentEvents) {
	repairEndedSessionInputs(ctx, h, pool, msg.SessionID)
	rows, done := replayAgentEventsPage(ctx, pool, msg)
	// Pending acceptance may precede the requested tail: include it so a fresh page can
	// show every message still waiting, even when the current turn has a long transcript.
	if pool != nil && msg.BeforeSeq == 0 {
		if pending, err := db.PendingSessionInputs(ctx, pool, msg.SessionID); err == nil {
			for _, input := range pending {
				raw, _ := json.Marshal(replayedAgentEvent(input.Event))
				select {
				case bc.send <- raw:
				case <-ctx.Done():
					return
				}
			}
		}
	}

	for _, r := range rows {
		event := replayedAgentEvent(r)
		event.Payload = h.desktopEventPayload(ctx, bc.AccountID, r.SessionID, r.Kind, r.Payload)
		raw, err := json.Marshal(event)
		if err != nil {
			continue
		}
		select {
		case bc.send <- raw:
		case <-ctx.Done():
			return
		}
	}
	done.ServerTime = time.Now().UTC().Format(time.RFC3339Nano)
	if raw, err := json.Marshal(done); err == nil {
		select {
		case bc.send <- raw:
		case <-ctx.Done():
		}
	}
}

// replayAgentEventsPage reads the window a subscribe asks for and builds its replay_done marker
// (ServerTime left for the sender to stamp). Shared with the runner contract's live stream
// (events_live.go), so the two surfaces page a transcript identically.
func replayAgentEventsPage(ctx context.Context, pool *pgxpool.Pool, msg protocol.SubscribeAgentEvents) ([]db.AgentEventRow, protocol.AgentEventsReplayDone) {
	done := protocol.AgentEventsReplayDone{
		Type: "agent_events_replay_done", SessionID: msg.SessionID, LastSeq: msg.AfterSeq,
	}
	var rows []db.AgentEventRow
	if pool != nil {
		var err error
		switch {
		case msg.Tail > 0 || msg.BeforeSeq > 0:
			limit := msg.Limit
			if msg.Tail > 0 {
				limit = msg.Tail
			}
			var more bool
			rows, more, err = db.ListAgentEventsBefore(ctx, pool, msg.SessionID, msg.BeforeSeq, limit)
			done.Older, done.HasOlder = true, more
			if len(rows) > 0 {
				done.FirstSeq = rows[0].Seq
				if msg.Tail > 0 {
					done.LastSeq = rows[len(rows)-1].Seq
				}
			}
		default:
			limit := msg.Limit
			if limit <= 0 {
				limit = 200
			}
			rows, err = db.ListAgentEvents(ctx, pool, msg.SessionID, msg.AfterSeq, limit)
			done.HasMore = len(rows) == limit
			if len(rows) > 0 {
				done.LastSeq = rows[len(rows)-1].Seq
			}
		}
		if err != nil {
			log.Printf("agent replay %s: %v", msg.SessionID, err)
		}
		rows = supplementReplayContext(ctx, pool, msg.SessionID, rows)
	}
	return rows, done
}

// replayedAgentEvent is a persisted row as the browser receives it.
func replayedAgentEvent(r db.AgentEventRow) protocol.AgentEvent {
	return protocol.AgentEvent{
		Type: "agent_event", SessionID: r.SessionID, ClientEventID: r.ClientEventID,
		Seq: r.Seq, Ts: r.Ts.UTC().Format(time.RFC3339Nano),
		Kind: r.Kind, Payload: json.RawMessage(r.Payload),
	}
}

// HandleGetAgentEvents serves a window of a session's transcript over REST
// (bearer: daemon token). Runner pods use it to rebuild provider context on
// resume; ?after_seq= and ?limit= mirror the WS replay parameters.
func (a *API) HandleGetAgentEvents(w http.ResponseWriter, r *http.Request) {
	err := a.sessionReadFence(r.Context(), r.PathValue("id"), func(ctx context.Context) error {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Second))
		a.snapshotHandleGetAgentEvents(w, r.WithContext(ctx))
		return nil
	})
	if err != nil {
		writeError(w, 409, "session source changed; refresh")
	}
}

func (a *API) snapshotHandleGetAgentEvents(w http.ResponseWriter, r *http.Request) {
	sessionID := r.PathValue("id")
	viewer := a.boardDesktopViewer(r)
	if !a.isDaemonToken(a.rawBearer(r)) {
		bound, err := a.authenticateRuntimeToken(r)
		if err != nil || bound.ID != sessionID {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if bound != nil && viewer == "" {
			viewer = derefOrEmpty(bound.SpawningAccountID)
		}
	}
	afterSeq, _ := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if a.dbPool == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	repairEndedSessionInputs(r.Context(), a.hub, a.dbPool, sessionID)
	rows, err := db.ListAgentEvents(r.Context(), a.dbPool, sessionID, afterSeq, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list error")
		return
	}
	out := make([]protocol.AgentEvent, 0, len(rows))
	for _, row := range rows {
		out = append(out, protocol.AgentEvent{
			Type: "agent_event", SessionID: row.SessionID, ClientEventID: row.ClientEventID,
			Seq: row.Seq, Ts: row.Ts.UTC().Format(time.RFC3339Nano),
			Kind: row.Kind, Payload: a.hub.desktopEventPayload(r.Context(), viewer, row.SessionID, row.Kind, row.Payload),
		})
	}
	writeJSON(w, http.StatusOK, out)
}
