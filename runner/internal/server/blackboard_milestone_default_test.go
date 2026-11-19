package server

import (
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"testing"
)

func TestNewestOpenMilestoneDefault(t *testing.T) {
	r := blackboard.Record{Milestones: []blackboard.Milestone{{ID: "old"}, {ID: "new"}, {ID: "cutting", Stage: "Finalizing"}, {ID: "shipped", Stage: "Final"}}}
	got, err := agentMilestone(r, "")
	if err != nil || got != "new" {
		t.Fatalf("default = %q, %v; want new", got, err)
	}
	explicit, err := agentMilestone(r, "old")
	if err != nil || explicit != "old" {
		t.Fatalf("explicit selector = %q, %v", explicit, err)
	}
}
