package blackboard

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRound2AutomaticBudgetLeavesPersonLifecycle(t *testing.T) {
	ctx := context.Background()
	s, p := planFixture(t)
	first, err := s.Propose(ctx, Actor{"worker", "agent"}, "m", []Change{addChange("first", 1)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Propose(ctx, Actor{"worker", "agent"}, "m", []Change{addChange("second", 2)})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.Update(ctx, "b", func(r *Record) error {
		r.Milestones[0].Stage = "Finalizing"
		r.Milestones = append(r.Milestones, Milestone{ID: "other", Version: 1})
		r.Events = append(r.Events, Event{ID: "intent", Kind: "final_requested", MilestoneID: "m", Source: "person", AuthenticatedActor: p.Actor, Payload: json.RawMessage(`{"next_release_id":""}`)})
		r.Events = append(r.Events, Event{ID: "full-other-partition", MilestoneID: "other", Kind: "decision", Source: "agent", AuthenticatedActor: Actor{"worker", "agent"}, Payload: json.RawMessage(`{"text":"` + strings.Repeat("x", MaxPartitionEventBytes) + `"}`)})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordEvent(ctx, Actor{"worker", "agent"}, EventInput{MilestoneID: "other", Kind: "decision"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("full partition: %v", err)
	}
	if _, err = s.RecordEvent(ctx, Actor{"worker", "agent"}, EventInput{Kind: "decision"}); err != nil {
		t.Fatalf("unrelated partition: %v", err)
	}
	if _, err = s.RecordEvent(ctx, Actor{"worker", "agent"}, EventInput{MilestoneID: "m", Kind: "decision"}); !errors.Is(err, ErrFrozen) {
		t.Fatalf("Finalizing: %v", err)
	}
	if err = seedClearedLegacyLock(ctx, s, "m"); err != nil {
		t.Fatal(err)
	}
	if err = s.Decide(ctx, p, "m", first, 0, nil); err != nil {
		t.Fatal(err)
	}
	if err = s.Decide(ctx, p, "m", second, 0, []ItemID{"second"}); err != nil {
		t.Fatal(err)
	}
	s.Board = &fakeBoard{cards: map[int]Card{2: boardCard(2, "next")}}
	if err = seedLegacyFrozenView(ctx, s, "m", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Slice(ctx, "m"); err != nil {
		t.Fatal(err)
	}
}

type failedObservationStore struct{ Store }

func (s failedObservationStore) Update(context.Context, string, func(*Record) error) error {
	return errors.New("observation unavailable")
}
func TestRound2SliceIgnoresObservationFailure(t *testing.T) {
	ctx := context.Background()
	s, _ := planFixture(t)
	if err := s.Store.Update(ctx, "b", func(r *Record) error { r.Milestones[0].ReleaseID = "release"; return nil }); err != nil {
		t.Fatal(err)
	}
	s.Board = &fakeBoard{cards: map[int]Card{1: boardCard(1, "next")}}
	s.Store = failedObservationStore{s.Store}
	v, err := s.Slice(ctx, "m")
	if err != nil || !v.Complete || len(v.Cards) != 1 {
		t.Fatalf("read depends on observation: %+v %v", v, err)
	}
}
func TestRound2AutomaticByteReserve(t *testing.T) {
	ctx := context.Background()
	s, _ := planFixture(t)
	if err := s.Store.Update(ctx, "b", func(r *Record) error {
		r.Events = []Event{{Source: "agent", AuthenticatedActor: Actor{"worker", "agent"}, Payload: json.RawMessage(`{"text":"` + strings.Repeat("x", MaxPartitionEventBytes) + `"}`)}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordEvent(ctx, Actor{"worker", "agent"}, EventInput{Kind: "decision"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("byte reserve: %v", err)
	}
}
