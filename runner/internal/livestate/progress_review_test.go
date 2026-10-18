package livestate

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
)

func TestReviewProgressScopeLocalTaskIDsCannotMutateRoot(t *testing.T) {
	events := planEvents(
		create("same-call", "Root", ""), created("same-call", 1),
		planEv{"tool_call", `{"tool":"TaskCreate","call_id":"same-call","parent_call_id":"child","input":{"subject":"Child"}}`},
		planEv{"tool_result", `{"call_id":"same-call","parent_call_id":"child","output":"Task #9 created successfully: Child"}`},
		planEv{"tool_call", `{"tool":"TaskUpdate","call_id":"update","parent_call_id":"child","input":{"taskId":"9","status":"completed"}}`},
	)
	root, child := ProgressFor(events, "", 0), ProgressFor(events, "child", 0)
	if root == nil || root.Total != 1 || root.Completed != 0 || root.Label != "" {
		t.Fatalf("child changed root progress: %+v", root)
	}
	if child == nil || child.Total != 1 || child.Completed != 1 || child.Label != "Child" {
		t.Fatalf("child-local task IDs lost: %+v", child)
	}
}

func TestReviewProgressConflictingMarkersAndMalformedSnapshotPreserveRoot(t *testing.T) {
	events := planEvents(
		planEv{"progress_updated", `{"completed":2,"total":3,"label":"Root"}`},
		planEv{"plan_updated", `{"items":[{"text":"Foreign","status":"completed"}],"parent_call_id":"child-one","child_id":"child-two"}`},
		planEv{"plan_updated", `{"items":null}`},
		planEv{"progress_updated", `{"completed":99,"total":1,"label":"Invalid"}`},
	)
	root := ProgressFor(events, "", 0)
	if root == nil || root.Completed != 2 || root.Total != 3 || root.Label != "Root" {
		t.Fatalf("malformed child payload changed root: %+v", root)
	}
}

func TestReviewProgressFollowsVisibleChildLifetime(t *testing.T) {
	s := newScript()
	s.msg("chat", "go")
	s.sub("child", "Review")
	s.add("progress_updated", json.RawMessage(`{"child_id":"child","completed":1,"total":2,"label":"First run"}`))
	st := Build(s.events, s.opts("running"))
	if len(st.Subagents) != 1 || st.Subagents[0].Progress == nil {
		t.Fatal("live child progress missing")
	}
	s.subDone(agent.SubagentPayload{ChildID: "child", Summary: "done"})
	if len(Build(s.events, s.opts("idle")).Subagents) != 0 {
		t.Fatal("finished child still exposes progress")
	}
	s.resumed("resumed-child", "child", "Review", false)
	st = Build(s.events, s.opts("running"))
	if len(st.Subagents) != 1 || st.Subagents[0].Key != "resumed-child" || st.Subagents[0].Progress != nil {
		t.Fatal("resumed child inherited ended incarnation progress")
	}
	s.add("progress_updated", json.RawMessage(`{"parent_call_id":"resumed-child","completed":1,"total":4,"label":"New run"}`))
	st = Build(s.events, s.opts("running"))
	if st.Subagents[0].Progress == nil || st.Subagents[0].Progress.Label != "New run" {
		t.Fatal("resumed incarnation cannot report progress")
	}
}

func TestReviewProgressOrderingUsesSequenceNotClock(t *testing.T) {
	events := []Event{
		{Seq: 1, Kind: "progress_updated", Payload: json.RawMessage(`{"completed":1,"total":2,"label":"Old"}`), Ts: time.Unix(100, 0)},
		{Seq: 2, Kind: "progress_updated", Payload: json.RawMessage(`{"completed":2,"total":2,"label":"New"}`), Ts: time.Unix(1, 0)},
	}
	p := ProgressFor(events, "", 0)
	if p == nil || p.Label != "New" || p.Completed != 2 {
		t.Fatal("clock regression resurrected stale progress")
	}
}
