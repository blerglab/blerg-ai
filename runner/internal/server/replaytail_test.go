package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// replayPage runs one subscribe through replayAgentEvents and returns the seqs it sent, in order,
// with the replay_done that closed it.
func replayPage(t *testing.T, f *reconcileFixture, msg protocol.SubscribeAgentEvents) ([]int64, protocol.AgentEventsReplayDone) {
	t.Helper()
	bc := &BrowserConn{ID: "b1", send: make(chan []byte, 1024)}
	msg.Type = "subscribe_agent_events"
	msg.SessionID = f.sessionID
	replayAgentEvents(context.Background(), f.api.hub, bc, f.pool, msg)
	close(bc.send)
	var seqs []int64
	var done protocol.AgentEventsReplayDone
	for raw := range bc.send {
		var head struct {
			Type string `json:"type"`
			Seq  int64  `json:"seq"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			t.Fatal(err)
		}
		switch head.Type {
		case "agent_event":
			seqs = append(seqs, head.Seq)
		case "agent_events_replay_done":
			if err := json.Unmarshal(raw, &done); err != nil {
				t.Fatal(err)
			}
		}
	}
	return seqs, done
}

func equalSeqs(a []int64, b ...int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A long transcript is shown from its end: the newest events at once, then the older ones in pages.
func TestReplayFromTheEndThenBackwards(t *testing.T) {
	f := newReconcileFixture(t, "idle")
	ctx := context.Background()
	for i := 1; i <= 10; i++ {
		if _, _, err := db.AppendAgentEvent(ctx, f.pool, f.sessionID, newUUID(), "user_message", `{"text":"x"}`); err != nil {
			t.Fatal(err)
		}
	}

	seqs, done := replayPage(t, f, protocol.SubscribeAgentEvents{Tail: 4})
	if !equalSeqs(seqs, 7, 8, 9, 10) {
		t.Fatalf("tail seqs = %v, want 7..10 oldest first", seqs)
	}
	if !done.Older || !done.HasOlder || done.FirstSeq != 7 || done.LastSeq != 10 || done.HasMore {
		t.Errorf("tail done = %+v, want older, has_older, first 7, last 10, no forward more", done)
	}

	seqs, done = replayPage(t, f, protocol.SubscribeAgentEvents{BeforeSeq: 7, Limit: 4})
	if !equalSeqs(seqs, 3, 4, 5, 6) || !done.Older || !done.HasOlder || done.FirstSeq != 3 {
		t.Errorf("before 7 = %v %+v, want 3..6 with more older", seqs, done)
	}

	seqs, done = replayPage(t, f, protocol.SubscribeAgentEvents{BeforeSeq: 3, Limit: 4})
	if !equalSeqs(seqs, 1, 2) || !done.Older || done.HasOlder || done.FirstSeq != 1 {
		t.Errorf("before 3 = %v %+v, want 1..2 and nothing older", seqs, done)
	}
}

// A transcript shorter than the tail comes whole, with nothing older to fetch; a forward replay is
// unchanged and does not claim to be a backward one.
func TestReplayTailOfAShortTranscriptAndForwardUnchanged(t *testing.T) {
	f := newReconcileFixture(t, "idle")
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		if _, _, err := db.AppendAgentEvent(ctx, f.pool, f.sessionID, newUUID(), "user_message", `{"text":"x"}`); err != nil {
			t.Fatal(err)
		}
	}
	seqs, done := replayPage(t, f, protocol.SubscribeAgentEvents{Tail: 200})
	if !equalSeqs(seqs, 1, 2, 3) || !done.Older || done.HasOlder || done.LastSeq != 3 {
		t.Errorf("short tail = %v %+v, want 1..3, older, nothing older", seqs, done)
	}
	seqs, done = replayPage(t, f, protocol.SubscribeAgentEvents{AfterSeq: 1})
	if !equalSeqs(seqs, 2, 3) || done.Older || done.HasOlder {
		t.Errorf("forward = %v %+v, want 2..3 and not marked older", seqs, done)
	}
}
