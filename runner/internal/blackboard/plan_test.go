package blackboard

import (
	"context"
	"errors"
	"testing"
	"time"
)

func planFixture(t *testing.T) (Service, Person) {
	t.Helper()
	store := NewMemoryStore()
	p := Person{Actor: Actor{"owner", "human"}, LoginID: "login"}
	r := Record{ID: "b", Owner: p.Actor, Version: 1, Milestones: []Milestone{{ID: "m", BoardID: "board", Version: 1}}, Thrusts: []Thrust{{ID: "t", MilestoneID: "m", Version: 1}}}
	if err := store.Create(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return Service{Store: store, BlackboardID: "b"}, p
}
func addChange(id ItemID, n int) Change {
	return Change{ID: id, Operation: "add", Item: Item{BoardID: "board", CardNumber: n, ThrustID: "t", Reason: "Bug"}}
}
func TestPlanAuthorityAndSelection(t *testing.T) {
	ctx := context.Background()
	s, p := planFixture(t)
	id, err := s.Propose(ctx, Actor{"worker", "agent"}, "m", []Change{addChange("a", 1), addChange("b", 2)})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Person{{Actor: Actor{"owner", "agent"}, LoginID: "standing-grant"}, {Actor: Actor{"other", "human"}, LoginID: "login"}, {Actor: p.Actor}} {
		if err = s.Decide(ctx, bad, "m", id, 0, []ItemID{"a"}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("authority: %v", err)
		}
	}
	if err = s.Decide(ctx, p, "m", id, 0, []ItemID{"unknown"}); err == nil {
		t.Fatal("unbound selection accepted")
	}
	s.Now = func() time.Time { return time.Date(2040, 1, 1, 0, 0, 0, 0, time.UTC) }
	if err = s.Decide(ctx, p, "m", id, 0, []ItemID{"a"}); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Store.Load(ctx, "b")
	if len(r.Items) != 1 || r.Items[0].CardNumber != 1 || len(r.Revisions) != 1 || r.Events[0].AuthenticatedActor != p.Actor {
		t.Fatalf("selection/provenance: %+v", r)
	}
	if err = s.Decide(ctx, p, "m", id, 0, []ItemID{"b"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale: %v", err)
	}
}
func TestPlanAtomicFailure(t *testing.T) {
	ctx := context.Background()
	s, p := planFixture(t)
	id, err := s.Propose(ctx, Actor{"w", "agent"}, "m", []Change{addChange("a", 1), {ID: "b", Operation: "remove", Item: Item{BoardID: "board", CardNumber: 9, Reason: "Bug", Destination: "next"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Decide(ctx, p, "m", id, 0, []ItemID{"a", "b"}); err == nil {
		t.Fatal("invalid removal accepted")
	}
	r, _ := s.Store.Load(ctx, "b")
	if len(r.Items) != 0 || r.Proposals[0].Status != "pending" {
		t.Fatal("partial commit")
	}
}
func TestPlanRejectDoesNotSetPlan(t *testing.T) {
	s, p := planFixture(t)
	ctx := context.Background()
	id, err := s.Propose(ctx, Actor{"w", "agent"}, "m", []Change{addChange("a", 1)})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Decide(ctx, p, "m", id, 0, nil); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Store.Load(ctx, "b")
	if r.Milestones[0].Revision != 0 || len(r.Revisions) != 0 || r.Events[0].Kind != "proposal_rejected" || r.Proposals[0].Status != "rejected" {
		t.Fatalf("rejection set plan: %+v", r)
	}
}

func TestSoloPlainPlanHasNoUnusedLevels(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	owner := Person{Actor: Actor{"owner", "human"}, LoginID: "login"}
	if err := store.Create(ctx, Record{ID: "plain", Owner: owner.Actor, Version: 1, Goals: []Goal{{ID: "goal", Name: "Try an idea", Version: 1}}}); err != nil {
		t.Fatal(err)
	}
	s := Service{Store: store, BlackboardID: "plain"}
	changes := []Change{{Operation: "add", Item: Item{Title: "First interview", GoalID: "goal"}}, {Operation: "add", Item: Item{Title: "Read notes", GoalID: "goal"}}}
	if err := s.Apply(ctx, owner, "", 0, changes); err != nil {
		t.Fatal(err)
	}
	r, _ := store.Load(ctx, "plain")
	if len(r.Items) != 2 || len(r.Milestones) != 0 || len(r.Thrusts) != 0 || len(r.Links) != 0 || r.PlanRevision != 1 || r.Proposals[0].Status != "decided" {
		t.Fatalf("not minimal: %+v", r)
	}
	v, err := s.Slice(ctx, "")
	if err != nil || v.Counts["next"] != 2 || !v.Complete {
		t.Fatalf("plain slice %+v %v", v, err)
	}
	if err := s.Apply(ctx, Person{Actor: Actor{"owner", "agent"}, LoginID: "claimed"}, "", 1, changes); !errors.Is(err, ErrForbidden) {
		t.Fatalf("agent direct apply: %v", err)
	}
}
func TestPlanRejectsForeignItemIdentity(t *testing.T) {
	s, _ := planFixture(t)
	_, err := s.Propose(context.Background(), Actor{"w", "agent"}, "m", []Change{{Operation: "add", Item: Item{ID: "foreign", Title: "Claim an existing identity"}}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("foreign item ID accepted: %v", err)
	}
}

func TestWithdrawOwnPendingProposal(t *testing.T) {
	ctx := context.Background()
	s, _ := planFixture(t)
	actor := Actor{"main", "agent"}
	id, err := s.Propose(ctx, actor, "m", []Change{addChange("a", 1)})
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range []Actor{{"worker", "agent"}, {"main", "human"}} {
		if err = s.Withdraw(ctx, other, "m", id); !errors.Is(err, ErrForbidden) {
			t.Fatalf("foreign withdrawal: %v", err)
		}
	}
	if err = s.Withdraw(ctx, actor, "m", id); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Store.Load(ctx, "b")
	if r.Proposals[0].Status != "withdrawn" || len(r.Items) != 0 || r.PlanRevision != 0 {
		t.Fatal("withdrawal changed scope or left pending")
	}
	if err = s.Withdraw(ctx, actor, "m", id); !errors.Is(err, ErrConflict) {
		t.Fatalf("repeat: %v", err)
	}
}
