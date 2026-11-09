package blackboard

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRound2ForeignBoardStaysPlanned(t *testing.T) {
	ctx := context.Background()
	s, p := planFixture(t)
	if err := s.Store.Update(ctx, "b", func(r *Record) error {
		r.Links = []Link{{BoardID: "foreign"}}
		r.Milestones[0].ReleaseID = "release"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	change := addChange("item", 1)
	change.Item.BoardID = "foreign"
	if err := s.Apply(ctx, p, "m", 0, []Change{change}); err != nil {
		t.Fatal(err)
	}
	b := &fakeBoard{cards: map[int]Card{}}
	s.Board = boardFunc(func(c context.Context, tool string, a map[string]any, o any) error {
		if tool == "blerg_card_get" || tool == "blerg_card_search" && a["numbers"] != nil {
			card := boardCard(1, "work")
			card.BoardID = "foreign"
			card.ReleaseID = stringPtr("foreign-release")
			var value any = card
			if tool == "blerg_card_search" {
				value = CardPage{Cards: []Card{card}}
			}
			raw, _ := json.Marshal(value)
			return json.Unmarshal(raw, o)
		}
		return b.Call(c, tool, a, o)
	})
	for _, final := range []bool{false, true} {
		if final {
			if err := seedLegacyFrozenView(ctx, s, "m", ""); err != nil {
				t.Fatal(err)
			}
		}
		v, err := s.Slice(ctx, "m")
		if err != nil || len(v.Cards) != 1 || v.Cards[0].Stage != "in_work" || v.Cards[0].Membership.State == "moved_out" {
			t.Fatalf("foreign planned: %+v %v", v, err)
		}
	}
}
