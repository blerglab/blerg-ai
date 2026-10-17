package livestate

import (
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
)

func backgroundAdversarialStart(s *script, id, call, lifetime string) Event {
	return s.add("background_task_started", map[string]any{
		"task_id": id, "call_id": call, "kind": "bash", "daemon_lifetime": lifetime,
	})
}

func TestBackgroundReusedTaskIDSurvivesOlderCompletion(t *testing.T) {
	s := newScript()
	backgroundAdversarialStart(s, "reused", "old-call", "old-process")
	s.add("background_task_done", map[string]any{"task_id": "reused", "call_id": "old-call", "daemon_lifetime": "old-process", "status": "completed"})
	backgroundAdversarialStart(s, "reused", "new-call", "new-process")
	got := Build(s.events, s.opts("idle")).Tasks
	if len(got) != 1 || got[0].CallID != "new-call" {
		t.Fatalf("old completion erased newer task incarnation: %+v", got)
	}
}

func TestBackgroundTargetedOrphanCompletionLeavesNewerIncarnation(t *testing.T) {
	s := newScript()
	old := backgroundAdversarialStart(s, "reused", "old-call", "old-process")
	backgroundAdversarialStart(s, "reused", "new-call", "new-process")
	s.add("background_task_done", map[string]any{"task_id": "reused", "call_id": "old-call", "start_event_id": old.ID, "daemon_lifetime": "old-process", "status": "stopped"})
	got := Build(s.events, s.opts("idle")).Tasks
	if len(got) != 1 || got[0].CallID != "new-call" {
		t.Fatalf("targeted old orphan completion erased new task: %+v", got)
	}
}

func TestBackgroundForeignLifetimeCompletionCannotClearCurrentTask(t *testing.T) {
	s := newScript()
	backgroundAdversarialStart(s, "reused", "current-call", "current-process")
	s.add("background_task_done", map[string]any{"task_id": "reused", "call_id": "current-call", "daemon_lifetime": "old-process", "status": "stopped"})
	got := Build(s.events, s.opts("idle")).Tasks
	if len(got) != 1 || got[0].CallID != "current-call" {
		t.Fatalf("foreign lifetime completion erased current task: %+v", got)
	}
}

func TestBackgroundTargetedCompletionChecksTaskIdentity(t *testing.T) {
	s := newScript()
	start := backgroundAdversarialStart(s, "current", "current-call", "current-process")
	s.add("background_task_done", map[string]any{"task_id": "foreign", "call_id": "current-call", "start_event_id": start.ID, "daemon_lifetime": "current-process", "status": "stopped"})
	got := Build(s.events, s.opts("idle")).Tasks
	if len(got) != 1 || got[0].TaskID != "current" {
		t.Fatalf("inconsistent targeted completion erased another task: %+v", got)
	}
}

func TestOrphanChildTargetedCompletionCannotStopNewStart(t *testing.T) {
	s := newScript()
	old := s.sub("reused-child", "Old child")
	s.subDone(agent.SubagentPayload{ChildID: "reused-child", Background: true})
	s.sub("reused-child", "New child")
	s.add("subagent_done", map[string]any{"child_id": "reused-child", "start_event_id": old.ID, "summary": "the daemon restarted"})
	got := Build(s.events, s.opts("running")).Subagents
	if len(got) != 1 || got[0].Key != "reused-child" || got[0].Title != "New child" || got[0].Background {
		t.Fatalf("old orphan child completion erased or relabeled new child: %+v", got)
	}
}

func TestOrphanChildNewStartResetsOldFinalState(t *testing.T) {
	s := newScript()
	s.sub("reused-child", "Old child")
	s.subDone(agent.SubagentPayload{ChildID: "reused-child", Summary: "Old final result"})
	s.sub("reused-child", "New child")
	got := Build(s.events, s.opts("running")).Subagents
	if len(got) != 1 || got[0].Key != "reused-child" || got[0].Title != "New child" {
		t.Fatalf("new child inherited an earlier incarnation's final state: %+v", got)
	}
}
