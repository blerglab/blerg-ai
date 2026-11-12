package db_test

import (
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
)

func TestBlackboardPartialUpdateDoesNotDeleteUnselectedThrusts(t *testing.T) {
	for _, partition := range []bool{false, true} {
		name := "Update"
		if partition {
			name = "UpdatePartition"
		}
		t.Run(name, func(t *testing.T) {
			store, s, p, mid := partitionFixture(t)
			if err := store.Update(t.Context(), s.BlackboardID, func(r *blackboard.Record) error {
				r.Goals = []blackboard.Goal{{ID: "goal", Owner: p.Actor, Version: 1}}
				r.Thrusts = []blackboard.Thrust{{ID: "historical", MilestoneID: mid, GoalID: "goal", Name: "Historical theme", Owner: p.Actor, Version: 1}}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			// A trusted callback retaining only its current slice must not turn omission
			// into SQL deletion. No approved thrust removal accompanies this update.
			partial := func(r *blackboard.Record) error { r.Thrusts = nil; r.Name = "Current slice"; return nil }
			var err error
			if partition {
				err = store.UpdatePartition(t.Context(), s.BlackboardID, blackboard.Partition{Kind: blackboard.PartitionPlan, MilestoneID: mid}, partial)
			} else {
				err = store.Update(t.Context(), s.BlackboardID, partial)
			}
			if err != nil {
				t.Fatal(err)
			}
			var count int
			if err = store.Pool.QueryRow(t.Context(), `SELECT count(*) FROM thrusts WHERE blackboard_id=$1 AND id='historical'`, s.BlackboardID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("partial callback erased unselected thrust: %d %v", count, err)
			}
		})
	}
}

func TestBlackboardConcurrentThrustRemovalsKeepOtherScopes(t *testing.T) {
	store, s, p, mid := partitionFixture(t)
	if err := store.Update(t.Context(), s.BlackboardID, func(r *blackboard.Record) error {
		r.Goals = []blackboard.Goal{{ID: "goal", Owner: p.Actor, Version: 1}}
		r.Thrusts = []blackboard.Thrust{{ID: "historical", MilestoneID: mid, GoalID: "goal", Name: "Historical theme", Owner: p.Actor, Version: 1}}
		for _, scope := range []string{"next-a", "next-b"} {
			r.Milestones = append(r.Milestones, blackboard.Milestone{ID: scope, Owner: p.Actor, Version: 1})
			r.Thrusts = append(r.Thrusts, blackboard.Thrust{ID: scope, MilestoneID: scope, GoalID: "goal", Name: scope, Owner: p.Actor, Version: 1})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, scope := range []string{"next-a", "next-b"} {
		go func() {
			<-start
			results <- s.Apply(t.Context(), p, scope, 0, []blackboard.Change{{Operation: "thrust_remove", Thrust: &blackboard.ThrustChange{ID: scope, Reason: "Unused"}}})
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	r, err := store.Load(t.Context(), s.BlackboardID)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Thrusts) != 1 || r.Thrusts[0].ID != "historical" || len(r.Events) != 2 || len(r.Revisions) != 2 {
		t.Fatalf("concurrent scope loss: %+v", r)
	}
	var count int
	if err = store.Pool.QueryRow(t.Context(), `SELECT count(*) FROM thrusts WHERE blackboard_id=$1`, s.BlackboardID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("scope index: %d %v", count, err)
	}
}
