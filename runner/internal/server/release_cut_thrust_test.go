package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
)

// Called only by the real-process cut fixture: the parent performs the real
// bound approval, board cut and receipt finalization between these two phases.
func exerciseCutThrustHistory(ctx context.Context, fx *topicFx, id, phase string) error {
	store := fx.api.blackboardStore()
	p := blackboard.Person{Actor: blackboard.Actor{Kind: "human", ID: topicOwner}, LoginID: testSID}
	s := blackboard.Service{Store: store, BlackboardID: id}
	if phase == "cut_seed_thrusts" {
		if err := store.Update(ctx, id, func(r *blackboard.Record) error {
			r.Goals = append(r.Goals, blackboard.Goal{ID: "cut-goal", Name: "Reliable releases", Owner: r.Owner, Version: 1})
			r.Thrusts = append(r.Thrusts, blackboard.Thrust{ID: "cut-thrust", Name: "Original cut theme", MilestoneID: "cut-milestone", GoalID: "cut-goal", Owner: r.Owner, Version: 1})
			return nil
		}); err != nil {
			return err
		}
		return s.Apply(ctx, p, "cut-milestone", 0, []blackboard.Change{{Operation: "thrust_rename", Thrust: &blackboard.ThrustChange{ID: "cut-thrust", Name: "Cut theme", Reason: "Clarify scope"}}})
	}
	before, err := store.Load(ctx, id)
	if err != nil {
		return err
	}
	if before.Milestones[0].Stage != "Final" || before.Milestones[0].CutReceiptID == "" {
		return fmt.Errorf("cut did not finalize before next milestone")
	}
	history := func() (string, error) {
		var raw []byte
		err := fx.pool.QueryRow(ctx, `SELECT jsonb_build_object('events',(SELECT jsonb_agg(data ORDER BY ordinal) FROM plan_events WHERE blackboard_id=$1 AND milestone_id='cut-milestone'),'revisions',(SELECT jsonb_agg(data ORDER BY revision) FROM plan_revisions WHERE blackboard_id=$1 AND milestone_id='cut-milestone'),'proposals',(SELECT jsonb_agg(data ORDER BY ordinal) FROM plan_proposals WHERE blackboard_id=$1 AND milestone_id='cut-milestone'),'cut',(SELECT frozen FROM blackboard_cut_finalizations WHERE blackboard_id=$1 AND milestone_id='cut-milestone'))`, id).Scan(&raw)
		return string(raw), err
	}
	original, err := history()
	if err != nil {
		return err
	}
	if err = store.Update(ctx, id, func(r *blackboard.Record) error {
		r.Milestones = append(r.Milestones, blackboard.Milestone{ID: "next-milestone", Name: "Next release", Owner: r.Owner, Version: 1})
		for _, tid := range []string{"next-empty", "next-source", "next-destination"} {
			r.Thrusts = append(r.Thrusts, blackboard.Thrust{ID: tid, Name: tid, MilestoneID: "next-milestone", GoalID: "cut-goal", Owner: r.Owner, Version: 1})
		}
		return nil
	}); err != nil {
		return err
	}
	if err = s.Apply(ctx, p, "next-milestone", 0, []blackboard.Change{
		{Operation: "thrust_remove", Thrust: &blackboard.ThrustChange{ID: "next-empty", Reason: "Unused"}},
		{Operation: "thrust_merge", Thrust: &blackboard.ThrustChange{ID: "next-source", Destination: "next-destination", Reason: "Group related work"}},
	}); err != nil {
		return err
	}
	// Exercise a bounded partition write and an unrelated full-header write after
	// the next milestone's explicit removals, preserving the cut's SQL indexes.
	if err = store.UpdatePartition(ctx, id, blackboard.Partition{Kind: blackboard.PartitionPlan, MilestoneID: "next-milestone"}, func(r *blackboard.Record) error { r.Name = "Next release underway"; return nil }); err != nil {
		return err
	}
	after, err := store.Load(ctx, id)
	if err != nil {
		return err
	}
	if len(after.Thrusts) != 2 || after.Thrusts[0].Name != "Cut theme" {
		return fmt.Errorf("cut thrust lost after next milestone")
	}
	var count, total int
	if err = fx.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE id=ANY(ARRAY['cut-thrust','next-destination'])),count(*) FROM thrusts WHERE blackboard_id=$1`, id).Scan(&count, &total); err != nil {
		return err
	}
	if count != 2 || total != 2 {
		return fmt.Errorf("cut thrust index lost")
	}
	preserved, err := history()
	if err != nil {
		return err
	}
	if original != preserved {
		return fmt.Errorf("old cut history rewritten")
	}
	raw, _ := json.Marshal(after.Revisions[1].Thrusts)
	if string(raw) == "null" {
		return fmt.Errorf("cut revision missing thrust snapshot")
	}
	return nil
}
