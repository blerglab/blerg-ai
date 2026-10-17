package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestCodexPlanQueuedWhileIdleCannotBecomeNextTurnPlan(t *testing.T) {
	a, em := codexPlanFixture()
	a.turn = ""
	a.planEpoch = 4
	a.events = make(chan struct{}, 1)
	native := codexPlanJSON(`[{"step":"Stale injected plan","status":"completed"}]`)
	line, _ := json.Marshal(map[string]any{"method": "turn/plan/updated", "params": native})
	a.read(bytes.NewReader(append(line, '\n')))
	a.mu.Lock()
	queued := a.backlog[0]
	a.turn = "fixture-turn"
	a.planEpoch = 5
	a.mu.Unlock()
	// The simulated next process turn is live, but this notification was read
	// before its start attempt. Merely matching its eventual IDs is insufficient.
	a.done = make(chan struct{})
	a.observePlan(queued.Params, queued.planEpoch)
	if len(em.snapshot()) != 0 {
		t.Fatal("idle queued plan became authoritative after next turn started")
	}
}

func TestCodexPlanPriorEpochCannotClearReusedTurnIdentity(t *testing.T) {
	a, em := codexPlanFixture()
	a.events = make(chan struct{}, 1)
	native := codexPlanJSON(`[]`)
	line, _ := json.Marshal(map[string]any{"method": "turn/plan/updated", "params": native})
	a.read(bytes.NewReader(append(line, '\n')))
	a.mu.Lock()
	queued := a.backlog[0]
	a.planEpoch++
	a.mu.Unlock()
	a.done = make(chan struct{})
	// A restarted/native fixture can reuse its turn text. The captured epoch
	// still prevents a previous turn's empty snapshot from clearing this one.
	a.observePlan(codexPlanJSON(`[{"step":"Current plan","status":"pending"}]`), 2)
	a.observePlan(queued.Params, queued.planEpoch)
	if events := em.snapshot(); len(events) != 1 || len(events[0].Payload.(protocol.PlanUpdatedPayload).Items) != 1 {
		t.Fatalf("old epoch cleared current plan: %+v", events)
	}
}

func TestCodexPlanChildEmptySnapshotCannotClearParent(t *testing.T) {
	a, em := codexPlanFixture()
	a.observePlan(codexPlanJSON(`[{"step":"Parent plan","status":"pending"}]`), 1)
	for _, child := range []string{"known-child", "foreign-child"} {
		a.d.children = map[string]*codexChild{"known-child": {}}
		native, _ := json.Marshal(map[string]any{"threadId": child, "turnId": "fixture-turn", "plan": []any{}})
		a.observePlan(native, 1)
	}
	if events := em.snapshot(); len(events) != 1 || len(events[0].Payload.(protocol.PlanUpdatedPayload).Items) != 1 {
		t.Fatalf("child snapshot cleared parent: %+v", events)
	}
}

func TestCodexPlanOversizedNotificationNeverEntersBacklog(t *testing.T) {
	a, em := codexPlanFixture()
	a.events = make(chan struct{}, 1)
	native := codexPlanJSON(`[{"step":"` + strings.Repeat("x", protocol.MaxPlanUpdatedBytes) + `","status":"pending"}]`)
	a.observePlan(native, 1)
	if len(em.snapshot()) != 0 {
		t.Fatal("oversized raw notification emitted plan")
	}
	line, _ := json.Marshal(map[string]any{"method": "turn/plan/updated", "params": native})
	a.read(io.LimitReader(bytes.NewReader(append(line, '\n')), int64(len(line)+1)))
	a.mu.Lock()
	count, held := len(a.backlog), a.backlogBytes
	a.mu.Unlock()
	if count != 0 || held != 0 {
		t.Fatalf("oversized notification retained in backlog: count=%d bytes=%d", count, held)
	}
}
