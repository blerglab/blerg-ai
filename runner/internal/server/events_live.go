package server

// The live stream (B in the chat-package design): GET /api/runner/sessions/{id}/events/live is
// what the runner's own browser gets over its websocket — a replay page, then every persisted
// event as it is recorded, the typing deltas that are never persisted, and the session's status
// as it changes — offered as Server-Sent Events to the credential that started the session.
//
// It is a subscriber to the hub, not a poller over agent_events like events/stream: the deltas
// and status changes only exist on the hub. The price is that the hub carries a session's output
// only while its daemon (or pod) is connected to THIS process; the polling stream stays for the
// consumers that want the durable record and nothing more.
//
// Frames, each `event: <type>` + `data: <json>`, with `id: <seq>` on persisted events only:
//
//	agent_event   the protocol.AgentEvent JSON (persisted, with seq; or transient:true)
//	replay_done   protocol.AgentEventsReplayDone, once, after the opening page
//	status        {session_id, status, message?, end_reason?, ended_by?}: once at open, then on change
//	end           the result body, when the session is over; the stream then closes
//
// Query: after_seq=N replays forward from a cursor; tail=1 replays the newest `limit` events and
// marks the page older (has_older, first_seq) for a client that pages backwards from there, as the
// websocket's Tail does. A `: keepalive` comment goes out every 15 s of silence.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// liveStatus is the status frame: the session's status in the browser's vocabulary (starting,
// running, waiting, idle, disconnected, stopped, ended, error), with the end attribution a
// browser would be shown when there is one.
type liveStatus struct {
	SessionID string            `json:"session_id"`
	Status    string            `json:"status"`
	Message   *string           `json:"message,omitempty"`
	EndReason string            `json:"end_reason,omitempty"`
	EndedBy   *protocol.EndedBy `json:"ended_by,omitempty"`
}

// liveStreamBuffer is how many hub messages a slow consumer may fall behind by before the hub
// drops some, as it does for a browser. Broadcasts for every session on the install land here
// (they are filtered below), so it is roomier than one session's output needs.
const liveStreamBuffer = 512

// HandleRunnerEventsLive serves GET /api/runner/sessions/{id}/events/live.
func (a *API) HandleRunnerEventsLive(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authRunner(w, r)
	if !ok {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "no database")
		return
	}
	sessionID := r.PathValue("id")
	ctx := r.Context()
	// Authorize before any streaming header goes out, answering a session this principal may
	// not see exactly as an unknown one — an ordinary 404, never an empty stream.
	row, apiErr := a.requireSessionAccess(ctx, principal, sessionID)
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	page := protocol.SubscribeAgentEvents{SessionID: sessionID}
	q := r.URL.Query()
	if seq, err := strconv.ParseInt(q.Get("after_seq"), 10, 64); err == nil && seq > 0 {
		page.AfterSeq = seq
	}
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 400 {
		page.Limit = n
	} else {
		page.Limit = 200
	}
	if tail, _ := strconv.ParseBool(q.Get("tail")); tail {
		page.Tail = page.Limit
	}

	// Subscribe BEFORE the replay, as the browser does, so nothing recorded between the page and
	// the live feed is lost; an event that lands in both is deduplicated by the client on its
	// client_event_id, exactly as over the websocket.
	//
	// Session output is fanned out per subscription, but a status change is broadcast to
	// browsers, so this stream watches as a browser would: one registration under one id, which
	// also means a session going private mid-stream evicts it like any other foreign watcher.
	// The account is what lets a private session's own owner keep receiving its broadcasts.
	id := "live-" + newUUID()
	feed := make(chan []byte, liveStreamBuffer)
	a.hub.Subscribe(sessionID, id, feed)
	a.hub.RegisterBrowser(&BrowserConn{ID: id, AccountID: principal.spawningAccountID(), send: feed})
	defer a.hub.Unsubscribe(sessionID, id)
	defer a.hub.UnregisterBrowser(id)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	lastWrite := time.Now()
	// frame writes one record (no id line when seq is 0) and reports whether the client is still there.
	frame := func(seq int64, event string, data []byte) bool {
		if seq > 0 {
			if _, err := fmt.Fprintf(w, "id: %d\n", seq); err != nil {
				return false
			}
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, sseData(string(data))); err != nil {
			return false
		}
		flusher.Flush()
		lastWrite = time.Now()
		return true
	}
	status := func(s liveStatus) bool {
		raw, err := json.Marshal(s)
		if err != nil {
			return false
		}
		return frame(0, "status", raw)
	}
	// end sends the result; the stream is over either way.
	end := func() {
		res, err := a.buildSessionResult(ctx, sessionID)
		if err != nil {
			log.Printf("runner live stream %s: result: %v", sessionID, err)
			return
		}
		if raw, err := json.Marshal(res); err == nil {
			frame(0, "end", raw)
		}
	}

	// The opening page, its marker, and where the session stands.
	rows, done := replayAgentEventsPage(ctx, a.dbPool, page)
	for _, ev := range rows {
		raw, err := json.Marshal(replayedAgentEvent(ev))
		if err != nil {
			continue
		}
		if !frame(ev.Seq, "agent_event", raw) {
			return
		}
	}
	done.ServerTime = time.Now().UTC().Format(time.RFC3339Nano)
	if raw, err := json.Marshal(done); err == nil {
		if !frame(0, "replay_done", raw) {
			return
		}
	}
	reason, by := endAttribution(row, principal.spawningAccountID())
	if !status(liveStatus{SessionID: sessionID, Status: row.Status, EndReason: reason, EndedBy: by}) {
		return
	}
	if terminalSessionStatus(row.Status) {
		end()
		return
	}

	keepalive := a.sseKeepalive()
	tick := time.NewTicker(keepalive)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-feed:
			var env struct {
				Type      string            `json:"type"`
				SessionID string            `json:"session_id"`
				Seq       int64             `json:"seq"`
				Status    string            `json:"status"`
				Message   *string           `json:"message"`
				EndReason string            `json:"end_reason"`
				EndedBy   *protocol.EndedBy `json:"ended_by"`
			}
			if json.Unmarshal(m, &env) != nil || env.SessionID != sessionID {
				continue // every other session's broadcasts, and anything that is not JSON
			}
			switch env.Type {
			case "agent_event":
				if !frame(env.Seq, "agent_event", m) {
					return
				}
			case "session_state_changed":
				if !status(liveStatus{SessionID: sessionID, Status: env.Status, Message: env.Message,
					EndReason: env.EndReason, EndedBy: env.EndedBy}) {
					return
				}
				if terminalSessionStatus(env.Status) {
					end()
					return
				}
			}
		case <-tick.C:
			// A broadcast this stream was not registered for in time, or one the hub dropped,
			// must not leave a consumer holding a stream for a session that is long over: the
			// row is the record, so it is re-read on the keepalive beat.
			if cur, err := db.GetSession(ctx, a.dbPool, sessionID); err == nil && cur != nil && terminalSessionStatus(cur.Status) {
				reason, by := endAttribution(cur, principal.spawningAccountID())
				status(liveStatus{SessionID: sessionID, Status: cur.Status, EndReason: reason, EndedBy: by})
				end()
				return
			}
			if time.Since(lastWrite) >= keepalive {
				if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
					return
				}
				flusher.Flush()
				lastWrite = time.Now()
			}
		}
	}
}
