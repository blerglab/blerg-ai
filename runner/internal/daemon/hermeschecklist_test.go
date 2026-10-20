package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/checklist"
)

func TestHermesResumedChildrenRemainAssignableAtNativeIDLimit(t *testing.T) {
	r, em, _ := hermesSecurityRun(t)
	nativeID := strings.Repeat("n", 256)
	seen := map[string]bool{}
	previous := ""
	for run := 0; run < 3; run++ {
		raw, err := json.Marshal(map[string]any{"subagent_id": nativeID, "parent_id": r.sid, "model": "fixture-model"})
		if err != nil {
			t.Fatal(err)
		}
		r.child("subagent.start", raw)
		current := r.children[nativeID]
		if !checklist.ValidAssignee(checklist.Assignee{Kind: "subagent", ID: current.ChildID}) {
			t.Fatalf("run %d emitted an unassignable display ID of %d bytes", run, len(current.ChildID))
		}
		if seen[current.ChildID] {
			t.Fatal("resumed child reused a previous display lifetime")
		}
		seen[current.ChildID] = true
		if run > 0 && (!current.Resumed || current.ResumedFrom != previous) {
			t.Fatalf("run %d lost the previous display lifetime", run)
		}
		previous = current.ChildID
		r.child("subagent.progress", raw)
		r.child("subagent.complete", raw)
		if len(r.children) != 1 || !r.childDone[nativeID] {
			t.Fatal("native ownership map or completion identity changed")
		}
	}
	if countKind(em, "subagent_started") != 3 || countKind(em, "subagent_done") != 3 {
		t.Fatal("child lifecycle was lost or duplicated")
	}
	for _, event := range em.snapshot() {
		if child, ok := event.Payload.(agent.SubagentPayload); ok && !seen[child.ChildID] {
			t.Fatal("progress or completion used a different display identity")
		}
	}
}
