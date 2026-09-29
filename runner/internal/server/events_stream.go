package server

// The agent contract's push half (spec §3): the same transcript GET
// /api/runner/sessions/{id}/events pages through, delivered as Server-Sent
// Events so a broker learns what a session did as it happens instead of
// polling for it.
//
// SSE rather than a WebSocket because the contract is one-directional and
// read-only — a consumer wants the transcript, not a channel to talk back on —
// and because SSE's `Last-Event-ID` gives resumption for free over plain HTTP,
// which is what makes a dropped connection a non-event here.
//
// The stream is a poller over agent_events rather than a subscriber to the
// hub's live fan-out: the hub only carries events for sessions whose daemon is
// connected to THIS process, while the table is the durable record every
// session writes to. Polling is a little less immediate and a great deal more
// correct.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

const (
	// defaultSSEPollInterval is how often the stream looks for new events.
	// A second is below human perception for a transcript and cheap for the
	// database: one indexed range scan per subscriber per second.
	defaultSSEPollInterval = time.Second
	// defaultSSEKeepaliveInterval bounds how long a silent stream stays
	// silent. Proxies and clients cut idle connections (nginx's default
	// read timeout is 60 s), and a session can legitimately think for
	// minutes without emitting an event, so a comment goes out well inside
	// that window.
	defaultSSEKeepaliveInterval = 15 * time.Second
	// sseEventPageSize bounds one poll's read, matching the REST window.
	sseEventPageSize = 200
)

func (a *API) ssePoll() time.Duration {
	if a.ssePollInterval > 0 {
		return a.ssePollInterval
	}
	return defaultSSEPollInterval
}

func (a *API) sseKeepalive() time.Duration {
	if a.sseKeepaliveInterval > 0 {
		return a.sseKeepaliveInterval
	}
	return defaultSSEKeepaliveInterval
}

// sseStartCursor is the seq to resume after: `Last-Event-ID` if the client sent
// a usable one, else ?after_seq=, else the start of the transcript.
//
// The header wins because it is the client's own record of what it actually
// received — a browser EventSource sends it automatically on reconnect, without
// the application getting a chance to rewrite the query string.
func sseStartCursor(r *http.Request) int64 {
	if raw := strings.TrimSpace(r.Header.Get("Last-Event-ID")); raw != "" {
		if seq, err := strconv.ParseInt(raw, 10, 64); err == nil && seq >= 0 {
			return seq
		}
	}
	seq, err := strconv.ParseInt(r.URL.Query().Get("after_seq"), 10, 64)
	if err != nil || seq < 0 {
		return 0
	}
	return seq
}

// sseData renders an event payload as the single line an SSE `data:` field
// requires. Payloads come out of a jsonb column, so they are valid, compact and
// newline-free in practice — but the framing must not depend on that: a raw
// newline in a data field would silently split one event into two. Anything
// that is not valid JSON is sent as a JSON string, which is still something a
// consumer can parse.
func sseData(payload string) string {
	var buf bytes.Buffer
	if json.Valid([]byte(payload)) {
		if err := json.Compact(&buf, []byte(payload)); err == nil {
			return buf.String()
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return `""`
	}
	return string(raw)
}

// sseEventName sanitises an event kind for the `event:` field. Kinds are
// identifiers from the agent loop, but a newline here would corrupt the stream
// the same way one in a data field would.
func sseEventName(kind string) string {
	name := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, kind)
	if name == "" {
		return "message"
	}
	return name
}

// HandleRunnerEventStream serves GET /api/runner/sessions/{id}/events/stream.
func (a *API) HandleRunnerEventStream(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authRunner(w, r)
	if !ok {
		return
	}
	sessionID := r.PathValue("id")
	ctx := r.Context()

	// Authorize BEFORE any streaming header goes out, and answer a session this
	// principal may not see exactly as an unknown one — a stream is as much a
	// read of somebody else's session as the events endpoint is.
	if _, apiErr := a.requireSessionAccess(ctx, principal, sessionID); apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	// Resolve the session BEFORE any streaming header goes out: a 404 must be
	// an ordinary error response, not an event stream that happens to say
	// nothing. This doubles as the first terminal check.
	res, err := a.buildSessionResult(ctx, sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		// Without a flusher every write would sit in a buffer until the
		// handler returned, which for this handler is "never" — better to
		// fail loudly than to hand back a stream that never streams.
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	cursor := sseStartCursor(r)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// Tell nginx not to buffer: proxy buffering turns an event stream into a
	// single delayed response, which is the one failure mode a consumer
	// cannot work around.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	poll, keepalive := a.ssePoll(), a.sseKeepalive()
	lastWrite := time.Now()

	// sendEvents drains from the cursor, writing every event it finds. It
	// reports how many it wrote, and false if the client went away mid-write.
	sendEvents := func() (int, bool) {
		rows, err := db.ListAgentEvents(ctx, a.dbPool, sessionID, cursor, sseEventPageSize)
		if err != nil {
			// A transient query failure is no reason to drop a stream the
			// client may have been holding for hours: log it, and try again
			// on the next tick.
			log.Printf("runner event stream %s: event query: %v", sessionID, err)
			return 0, true
		}
		for _, ev := range rows {
			if _, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n",
				ev.Seq, sseEventName(ev.Kind), sseData(ev.Payload)); err != nil {
				return 0, false // client gone mid-write
			}
			cursor = ev.Seq
		}
		if len(rows) > 0 {
			flusher.Flush()
			lastWrite = time.Now()
		}
		return len(rows), true
	}

	for {
		n, ok := sendEvents()
		if !ok {
			return
		}
		switch {
		// A full page means there is probably more waiting: drain it without
		// pausing, so a long backlog replays at once rather than one page a
		// second.
		case n == sseEventPageSize:
			continue
		case n > 0:
			// Delivered something; fall through to the wait.
		default:
			// Caught up. Re-read the session so a state change since the last
			// poll is seen, and close the stream once the work is over.
			if res, err = a.buildSessionResult(ctx, sessionID); err == nil && res.Terminal {
				// One last drain: an event may have landed between the query
				// above and this one, and `end` must be the last thing a
				// consumer sees, never a way to lose the final events.
				if n, ok := sendEvents(); !ok {
					return
				} else if n > 0 {
					continue
				}
				body, err := json.Marshal(res)
				if err != nil {
					log.Printf("runner event stream %s: result encode: %v", sessionID, err)
					return
				}
				_, _ = fmt.Fprintf(w, "event: end\ndata: %s\n\n", body)
				flusher.Flush()
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

		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}
