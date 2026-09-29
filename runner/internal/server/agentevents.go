package server

import (
	"context"
	"encoding/json"
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
func forwardToSessionDaemon(h *Hub, bc *BrowserConn, sessionID string, msg any) {
	dc := h.FindDaemonForSession(sessionID)
	if dc == nil {
		log.Printf("browser %s: no daemon found for session %s", bc.ID, sessionID)
		return
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return
	}
	select {
	case dc.send <- raw:
	default:
		log.Printf("browser %s: daemon %s send channel full", bc.ID, dc.ID)
	}
}

// HandleAgentEvent processes one non-transient agent event from a daemon:
// persist (server-assigned seq, idempotent on client_event_id), ack back to
// the daemon, fan out to subscribed browsers, and apply side effects
// (model/effort mirroring, status transitions).
func HandleAgentEvent(ctx context.Context, h *Hub, pool *pgxpool.Pool, dc *DaemonConn, msg protocol.AgentEvent) {
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
	seq := msg.Seq
	if pool != nil {
		s, _, err := db.AppendAgentEvent(ctx, pool, msg.SessionID, msg.ClientEventID, msg.Kind, string(msg.Payload))
		if err != nil {
			log.Printf("agent_event append %s/%s: %v", msg.SessionID, msg.Kind, err)
			return // no ack — the daemon will re-send
		}
		seq = s
	}

	// Ack persistence so the daemon can drop it from its pending buffer.
	ackAgentEvent(dc, msg, seq)

	// Fan out to subscribed browsers with the assigned seq.
	msg.Seq = seq
	if raw, err := json.Marshal(msg); err == nil {
		h.FanOutSessionOutput(msg.SessionID, raw)
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
			h.BroadcastJSON(protocol.SessionMetaChanged{
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
	case turnDoneKind:
		// A one-shot session (start with auto_stop) is over as soon as its
		// first turn is recorded; every other session is untouched by this.
		// It runs after the append above, so the transcript the result reports
		// already contains the turn that ended the session.
		h.AutoStopOnTurnDone(msg.SessionID)
	}
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
	if _, ok := a.authBrowser(w, r); !ok {
		return
	}
	sessionID := r.PathValue("id")
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

	// Drain live events buffered during the DB query, then start forwarding.
drainLoop:
	for {
		select {
		case m := <-bufferChan:
			select {
			case bc.send <- m:
			case <-ctx.Done():
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
				select {
				case bc.send <- m:
				case <-subCtx.Done():
					return
				}
			case <-subCtx.Done():
				return
			}
		}
	}()
}

// replayAgentEvents sends a window of persisted events + replay_done marker.
func replayAgentEvents(ctx context.Context, _ *Hub, bc *BrowserConn, pool *pgxpool.Pool, msg protocol.SubscribeAgentEvents) {
	lastSeq := msg.AfterSeq
	hasMore := false
	if pool != nil {
		limit := msg.Limit
		if limit <= 0 {
			limit = 200
		}
		rows, err := db.ListAgentEvents(ctx, pool, msg.SessionID, msg.AfterSeq, limit)
		if err != nil {
			log.Printf("agent replay %s: %v", msg.SessionID, err)
		}
		for _, r := range rows {
			ev := protocol.AgentEvent{
				Type: "agent_event", SessionID: r.SessionID, ClientEventID: r.ClientEventID,
				Seq: r.Seq, Ts: r.Ts.UTC().Format(time.RFC3339Nano),
				Kind: r.Kind, Payload: json.RawMessage(r.Payload),
			}
			raw, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			select {
			case bc.send <- raw:
			case <-ctx.Done():
				return
			}
			lastSeq = r.Seq
		}
		hasMore = len(rows) == limit
	}
	if raw, err := json.Marshal(protocol.AgentEventsReplayDone{
		Type: "agent_events_replay_done", SessionID: msg.SessionID, LastSeq: lastSeq, HasMore: hasMore,
		ServerTime: time.Now().UTC().Format(time.RFC3339Nano),
	}); err == nil {
		select {
		case bc.send <- raw:
		case <-ctx.Done():
		}
	}
}

// HandleGetAgentEvents serves a window of a session's transcript over REST
// (bearer: daemon token). Runner pods use it to rebuild provider context on
// resume; ?after_seq= and ?limit= mirror the WS replay parameters.
func (a *API) HandleGetAgentEvents(w http.ResponseWriter, r *http.Request) {
	if !checkBearerToken(w, r, a.daemonToken) {
		return
	}
	sessionID := r.PathValue("id")
	afterSeq, _ := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if a.dbPool == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
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
			Kind: row.Kind, Payload: json.RawMessage(row.Payload),
		})
	}
	writeJSON(w, http.StatusOK, out)
}
