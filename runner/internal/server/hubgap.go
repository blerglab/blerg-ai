package server

import (
	"encoding/json"
	"sync"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Gaps in a subscriber's live agent events.
//
// The hub never blocks on a slow subscriber: when one's buffer is full, an event meant for it is
// dropped (FanOutSessionOutput). A dropped persisted event used to be lost to that subscriber for
// good, because a client moves its cursor past whatever seq it sees next. Now the hub remembers the
// lowest seq it dropped for each subscriber, and the subscriber's forwarder, which drains the buffer,
// tells its client so: the websocket sends protocol.AgentEventsGap (the client fetches the events from
// from_seq on, deduplicating on client_event_id as usual), and the runner's SSE stream fetches them
// from the database itself and sends them inline (events_live.go).
//
// A transient event (a streaming delta, seq 0) that is dropped is not a gap: it is never stored, and
// the consolidated event that follows it carries the whole text.

type agentGaps struct {
	mu sync.Mutex
	m  map[string]map[string]int64 // session id → subscriber id → lowest dropped seq
}

func (g *agentGaps) note(sessionID, subscriberID string, seq int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.m == nil {
		g.m = make(map[string]map[string]int64)
	}
	subs := g.m[sessionID]
	if subs == nil {
		subs = make(map[string]int64)
		g.m[sessionID] = subs
	}
	if cur, ok := subs[subscriberID]; !ok || seq < cur {
		subs[subscriberID] = seq
	}
}

// take returns and forgets the lowest dropped seq for a subscriber (0: none).
func (g *agentGaps) take(sessionID, subscriberID string) int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	subs := g.m[sessionID]
	seq, ok := subs[subscriberID]
	if !ok {
		return 0
	}
	delete(subs, subscriberID)
	if len(subs) == 0 {
		delete(g.m, sessionID)
	}
	return seq
}

// FanOutAgentEvent sends a persisted agent event (data, with its seq) to every subscriber of the
// session. A subscriber whose buffer is full misses it, and the hub records the gap for its
// forwarder to report (TakeAgentEventGap). seq 0 (not persisted) is fanned out like any output.
func (h *Hub) FanOutAgentEvent(sessionID string, seq int64, data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for id, ch := range h.subscriptions[sessionID] {
		select {
		case ch <- data:
		default:
			if seq > 0 {
				h.gaps.note(sessionID, id, seq)
			}
		}
	}
}

// TakeAgentEventGap returns the lowest seq of a persisted agent event dropped for subscriberID since
// the last call (0: none dropped), and forgets it.
func (h *Hub) TakeAgentEventGap(sessionID, subscriberID string) int64 {
	return h.gaps.take(sessionID, subscriberID)
}

// agentEventsGapMessage is the websocket's gap notice for a browser.
func agentEventsGapMessage(sessionID string, fromSeq int64) []byte {
	raw, _ := json.Marshal(protocol.AgentEventsGap{Type: "agent_events_gap", SessionID: sessionID, FromSeq: fromSeq})
	return raw
}
