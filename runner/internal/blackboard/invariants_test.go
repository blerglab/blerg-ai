package blackboard

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestNonterminalDoneColumnCounts(t *testing.T) {
	columns := []Column{{ID: "done", Name: "Done"}, {ID: "next", Name: "Ready"}}
	v := SliceView{Counts: map[string]int{}}
	for n := 1; n <= 22; n++ {
		col := "next"
		if n <= 18 {
			col = "done"
		}
		c := boardCard(n, col)
		v.Cards = append(v.Cards, SliceCard{Card: c, Stage: (Mapping{}).Stage(c, columns)})
	}
	countSlice(&v)
	if v.Counts["next"] != 4 || v.Counts["done"] != 18 {
		t.Fatalf("22-card regression: counts=%v", v.Counts)
	}
}

func TestReleaseTagMembershipWithoutReleaseID(t *testing.T) {
	s, p := planFixture(t)
	ctx := t.Context()
	if err := s.Store.Update(ctx, "b", func(r *Record) error { r.Milestones[0].Name = "0.6.0"; return nil }); err != nil {
		t.Fatal(err)
	}
	c := boardCard(500, "next", "0.7.0")
	c.ReleaseID = nil
	s.Board = &fakeBoard{cards: map[int]Card{500: c}}
	change := addChange("member", 500)
	if err := s.Apply(ctx, p, "m", 0, []Change{change}); err != nil {
		t.Fatal(err)
	}
	v, err := s.Slice(ctx, "m")
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Cards) != 1 || v.Cards[0].Stage != "moved_out" || v.Cards[0].Membership.Destination != "0.7.0" || v.Cards[0].NeedsReason || v.Counts["next"] != 0 {
		t.Fatalf("retagged member: %+v", v)
	}
}

func TestReleaseTagAmbiguityPreservesMembership(t *testing.T) {
	item := Item{State: "planned", Reason: "Original scope"}
	c := boardCard(500, "next", "0.6.0", "0.7.0")
	got := releaseTagMembership(item, c, Milestone{Name: "0.6.0"})
	if got.State != "planned" || got.Reason != item.Reason {
		t.Fatalf("ambiguous tags changed scope: %+v", got)
	}
}

type invariantBoard struct{ fakeBoard }

func (b *invariantBoard) Call(ctx context.Context, tool string, args map[string]any, out any) error {
	if tool == "blerg_column_list" {
		raw, _ := json.Marshal([]Column{{ID: "done", Name: "Done"}, {ID: "next", Name: "Ready"}})
		return json.Unmarshal(raw, out)
	}
	tagFilter, _ := args["tag"].(string)
	if tool == "blerg_card_search" && tagFilter != "" {
		page := CardPage{Cards: []Card{}}
		for _, c := range b.cards {
			for _, tag := range c.Tags {
				if tag == args["tag"] {
					page.Cards = append(page.Cards, c)
					break
				}
			}
		}
		raw, _ := json.Marshal(page)
		return json.Unmarshal(raw, out)
	}
	return b.fakeBoard.Call(ctx, tool, args, out)
}

func TestSliceTwentyTwoNextRegression(t *testing.T) {
	s, p := planFixture(t)
	ctx := t.Context()
	if err := s.Store.Update(ctx, "b", func(r *Record) error { r.Milestones[0].Name = "0.6.0"; return nil }); err != nil {
		t.Fatal(err)
	}
	b := &invariantBoard{fakeBoard: fakeBoard{cards: map[int]Card{}}}
	s.Board = b
	changes := []Change{}
	for n := 1; n <= 22; n++ {
		col := "next"
		if n <= 18 {
			col = "done"
		}
		c := boardCard(n, col, "0.6.0")
		c.ReleaseID = nil
		b.cards[n] = c
		changes = append(changes, addChange(ItemID(fmt.Sprint(n)), n))
	}
	c := boardCard(500, "next", "0.7.0")
	c.ReleaseID = nil
	b.cards[500] = c
	changes = append(changes, addChange("retagged", 500))
	if err := s.Apply(ctx, p, "m", 0, changes); err != nil {
		t.Fatal(err)
	}
	unplanned := boardCard(501, "next", "0.6.0")
	unplanned.ReleaseID = nil
	b.cards[501] = unplanned
	v, err := s.Slice(ctx, "m")
	if err != nil {
		t.Fatal(err)
	}
	if v.Counts["done"] != 18 || v.Counts["next"] != 5 || v.Counts["moved_out"] != 1 || v.Counts["unplanned"] != 1 {
		t.Fatalf("counts: %v", v.Counts)
	}
	r, err := s.Store.Load(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range r.Events {
		var item Item
		if json.Unmarshal(e.Payload, &item) == nil && item.CardNumber == 500 && item.State == "moved_out" && item.Reason != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing release-tag observation evidence")
	}
}
