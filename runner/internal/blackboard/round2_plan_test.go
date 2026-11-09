package blackboard

import (
	"context"
	"errors"
	"testing"
)

func TestRound2FrozenProposalRejectsOnly(t *testing.T) {
	ctx := context.Background()
	s, p := planFixture(t)
	id, err := s.Propose(ctx, Actor{"worker", "agent"}, "m", []Change{addChange("item", 1)})
	if err != nil {
		t.Fatal(err)
	}
	s.Board = &fakeBoard{cards: map[int]Card{}}
	if err = seedLegacyFrozenView(ctx, s, "m", ""); err != nil {
		t.Fatal(err)
	}
	if err = s.Decide(ctx, p, "m", id, 0, []ItemID{"item"}); !errors.Is(err, ErrFrozen) {
		t.Fatalf("commit after freeze: %v", err)
	}
	if err = s.Decide(ctx, p, "m", id, 0, nil); err != nil {
		t.Fatalf("reject after freeze: %v", err)
	}
	r, _ := s.Store.Load(ctx, "b")
	if r.Proposals[0].Status != "rejected" || len(r.Revisions) != 0 {
		t.Fatal("rejection changed plan")
	}
}
