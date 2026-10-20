package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func agentEventJSON(sessionID string, seq int64) []byte {
	raw, _ := json.Marshal(protocol.AgentEvent{
		Type: "agent_event", SessionID: sessionID, ClientEventID: newUUID(), Seq: seq,
		Ts: time.Now().UTC().Format(time.RFC3339Nano), Kind: "assistant_text", Payload: json.RawMessage(`{"text":"x","done":true}`),
	})
	return raw
}

// A persisted event dropped for a full subscriber is remembered, lowest seq first; a dropped
// transient (seq 0) is not a gap; unsubscribing forgets it.
func TestHubRecordsDroppedAgentEvents(t *testing.T) {
	h := NewHub()
	full := make(chan []byte, 1)
	roomy := make(chan []byte, 8)
	h.Subscribe("s", "slow", full)
	h.Subscribe("s", "fast", roomy)
	h.FanOutAgentEvent("s", 7, agentEventJSON("s", 7)) // fills slow
	h.FanOutAgentEvent("s", 0, []byte(`{"type":"agent_event","transient":true}`))
	h.FanOutAgentEvent("s", 9, agentEventJSON("s", 9))
	h.FanOutAgentEvent("s", 8, agentEventJSON("s", 8))
	if got := h.TakeAgentEventGap("s", "fast"); got != 0 {
		t.Errorf("fast subscriber gap = %d, want none", got)
	}
	if got := h.TakeAgentEventGap("s", "slow"); got != 8 {
		t.Errorf("slow subscriber gap = %d, want 8 (the lowest dropped seq)", got)
	}
	if got := h.TakeAgentEventGap("s", "slow"); got != 0 {
		t.Errorf("gap reported twice: %d", got)
	}
	h.FanOutAgentEvent("s", 10, agentEventJSON("s", 10))
	h.Unsubscribe("s", "slow")
	if got := h.TakeAgentEventGap("s", "slow"); got != 0 {
		t.Errorf("gap kept after unsubscribe: %d", got)
	}
}

// Over the websocket, a browser whose buffer overflowed is sent agent_events_gap with the first seq
// it missed, so it can fetch the hole instead of moving past it.
func TestSubscribeAgentEventsSendsGapNotice(t *testing.T) {
	h := NewHub()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bc := &BrowserConn{ID: "b1", send: make(chan []byte, 1), subs: make(map[string]context.CancelFunc)}
	handleSubscribeAgentEvents(ctx, h, bc, nil, protocol.SubscribeAgentEvents{
		Type: "subscribe_agent_events", SessionID: "s1",
	})
	// bc.send holds replay_done; the forwarder blocks on the first live event, the hub buffer (256)
	// takes the next ones and the rest are dropped.
	for seq := int64(1); seq <= 300; seq++ {
		h.FanOutAgentEvent("s1", seq, agentEventJSON("s1", seq))
	}
	deadline := time.After(15 * time.Second)
	var gapFrom int64
	var delivered []int64
	for gapFrom == 0 {
		select {
		case raw := <-bc.send:
			var env struct {
				Type    string `json:"type"`
				Seq     int64  `json:"seq"`
				FromSeq int64  `json:"from_seq"`
			}
			_ = json.Unmarshal(raw, &env)
			switch env.Type {
			case "agent_events_gap":
				if !strings.Contains(string(raw), `"session_id":"s1"`) {
					t.Fatalf("gap notice = %s", raw)
				}
				gapFrom = env.FromSeq
			case "agent_event":
				delivered = append(delivered, env.Seq)
			}
		case <-deadline:
			t.Fatalf("no gap notice; delivered %d events", len(delivered))
		}
	}
	if gapFrom < 2 || gapFrom > 300 {
		t.Fatalf("gap from %d, want a seq the browser did not get", gapFrom)
	}
	for _, s := range delivered {
		if s >= gapFrom {
			t.Fatalf("seq %d delivered before the gap notice from %d", s, gapFrom)
		}
	}
}
