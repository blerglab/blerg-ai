package server

import (
	"context"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func appendReplayFixture(t *testing.T, f *reconcileFixture, kind, payload string) int64 {
	t.Helper()
	seq, _, err := db.AppendAgentEvent(context.Background(), f.pool, f.sessionID, newUUID(), kind, payload)
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

func TestReplayTailRestoresReferencedChildLifecycle(t *testing.T) {
	f := newReconcileFixture(t, "idle")
	appendReplayFixture(t, f, "subagent_started", `{"child_id":"finished","summary":"Inspect fixture"}`)
	appendReplayFixture(t, f, "subagent_update", `{"child_id":"finished","summary":"Inspection complete"}`)
	appendReplayFixture(t, f, "subagent_done", `{"child_id":"finished","summary":"Done"}`)
	appendReplayFixture(t, f, "subagent_started", `{"child_id":"active","summary":"Still inspecting"}`)
	appendReplayFixture(t, f, "subagent_started", `{"child_id":"unreferenced"}`)
	appendReplayFixture(t, f, "user_message", `{"text":"Fixture gap"}`)
	appendReplayFixture(t, f, "assistant_text", `{"parent_call_id":"finished","text":"Delayed final report","done":true}`)
	appendReplayFixture(t, f, "assistant_text", `{"parent_call_id":"active","text":"Current progress"}`)
	seqs, done := replayPage(t, f, protocol.SubscribeAgentEvents{Tail: 2})
	if !equalSeqs(seqs, 1, 2, 3, 4, 7, 8) {
		t.Fatalf("tail lacks bounded lifecycle context: %v", seqs)
	}
	if done.FirstSeq != 7 || done.LastSeq != 8 || !done.HasOlder || done.HasMore {
		t.Fatalf("context changed the base page cursor: %+v", done)
	}
}

func TestReplayContextRespectsHistoricalUpperFenceAndDeduplicates(t *testing.T) {
	f := newReconcileFixture(t, "idle")
	appendReplayFixture(t, f, "subagent_started", `{"child_id":"child","summary":"Original task"}`)
	appendReplayFixture(t, f, "subagent_update", `{"child_id":"child","summary":"Earlier progress"}`)
	appendReplayFixture(t, f, "assistant_text", `{"parent_call_id":"child","text":"Earlier text"}`)
	appendReplayFixture(t, f, "subagent_done", `{"child_id":"child","summary":"Later completion"}`)
	seqs, done := replayPage(t, f, protocol.SubscribeAgentEvents{BeforeSeq: 4, Limit: 1})
	if !equalSeqs(seqs, 1, 2, 3) || done.FirstSeq != 3 || !done.HasOlder {
		t.Fatalf("historical context omitted state or leaked future completion: %v %+v", seqs, done)
	}
	seqs, done = replayPage(t, f, protocol.SubscribeAgentEvents{Tail: 2})
	if !equalSeqs(seqs, 1, 2, 3, 4) || done.FirstSeq != 3 || done.LastSeq != 4 {
		t.Fatalf("context duplicates a page row or changes cursors: %v %+v", seqs, done)
	}
}

func TestRunnerEventsLiveReplayContextDoesNotMoveResumeIDBackwards(t *testing.T) {
	api, srv, id := streamTestAPI(t, 0, 0)
	appendEvent(t, api, id, "subagent_started", `{"child_id":"child","summary":"Inspect fixture"}`)
	appendEvent(t, api, id, "subagent_done", `{"child_id":"child","summary":"Done"}`)
	appendEvent(t, api, id, "assistant_text", `{"text":"Gap"}`)
	appendEvent(t, api, id, "assistant_text", `{"parent_call_id":"child","text":"Delayed report","done":true}`)
	s := openSSEStream(t, srv, "/api/runner/sessions/"+id+"/events/live?after_seq=3", nil)
	for _, want := range []float64{1, 2} {
		_, data := s.expectFrame(t, "agent_event", false)
		if data["seq"] != want {
			t.Fatalf("context seq = %v, want %v", data["seq"], want)
		}
	}
	if seq, _ := s.expectFrame(t, "agent_event", true); seq != 4 {
		t.Fatalf("page event id = %d, want 4", seq)
	}
	_, done := s.expectFrame(t, "replay_done", false)
	if done["last_seq"] != float64(4) || done["has_more"] != false {
		t.Fatalf("forward cursor changed: %v", done)
	}
	tail := openSSEStream(t, srv, "/api/runner/sessions/"+id+"/events/live?tail=1&limit=1", nil)
	for _, want := range []float64{1, 2} {
		_, data := tail.expectFrame(t, "agent_event", false)
		if data["seq"] != want {
			t.Fatalf("tail context seq = %v, want %v", data["seq"], want)
		}
	}
	if seq, _ := tail.expectFrame(t, "agent_event", true); seq != 4 {
		t.Fatalf("tail event id = %d, want 4", seq)
	}
	_, done = tail.expectFrame(t, "replay_done", false)
	if done["first_seq"] != float64(4) || done["last_seq"] != float64(4) || done["has_older"] != true {
		t.Fatalf("tail cursor changed: %v", done)
	}
}

func TestReplayContextIgnoresMalformedOrOversizedReferences(t *testing.T) {
	page := []db.AgentEventRow{
		{Seq: 1, Kind: "assistant_text", Payload: `{"parent_call_id":12}`},
		{Seq: 2, Kind: "subagent_started", Payload: `{"child_id":{}}`},
		{Seq: 3, Kind: "assistant_text", Payload: `{"parent_call_id":"` + strings.Repeat("x", 513) + `"}`},
		{Seq: 4, Kind: "assistant_text", Payload: `not-json`},
	}
	// A nil pool proves these untrusted references never reach a database lookup.
	got := supplementReplayContext(context.Background(), nil, "fixture", page)
	if len(got) != len(page) {
		t.Fatal("malformed metadata changed the replay page")
	}
}
