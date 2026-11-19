package blackboard

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type fakeBoard struct {
	cards      map[int]Card
	failCursor bool
	closed     bool
	calls      []string
}

func (b *fakeBoard) Call(ctx context.Context, tool string, args map[string]any, out any) error {
	b.calls = append(b.calls, tool)
	var v any
	switch tool {
	case "blerg_column_list":
		v = []Column{{ID: "done", Name: "Done", Terminal: true}, {ID: "work", Name: "In progress"}, {ID: "next", Name: "Ready"}}
	case "blerg_release_list":
		v = []Release{{ID: "release", BoardID: "board", Name: "Next"}}
	case "blerg_release_get":
		status := "planned"
		if b.closed {
			status = "released"
		}
		id := args["release_id"].(string)
		if id != "release" {
			status = "planned"
		}
		cards := []Card{}
		done := 0
		for _, c := range b.cards {
			if c.ReleaseID == nil || *c.ReleaseID != id {
				continue
			}
			cards = append(cards, c)
			if c.ColumnID != nil && *c.ColumnID == "done" {
				done++
			}
		}
		v = map[string]any{"release": Release{ID: id, BoardID: "board", Status: status}, "cards": cards, "done": done}
	case "blerg_release_close":
		b.closed = true
		v = map[string]any{"ok": true}
	case "blerg_card_search":
		page := CardPage{Cards: []Card{}}
		if numbers, ok := args["numbers"].([]int); ok {
			for _, n := range numbers {
				if c, ok := b.cards[n]; ok {
					page.Cards = append(page.Cards, c)
				}
			}
			raw, _ := json.Marshal(page)
			return json.Unmarshal(raw, out)
		}
		cursor, _ := args["cursor"].(string)
		if b.failCursor && cursor != "" {
			return errors.New("read failed")
		}
		for n := 1; n <= len(b.cards)+3; n++ {
			c, ok := b.cards[n]
			if !ok || c.ReleaseID == nil || *c.ReleaseID != "release" {
				continue
			}
			if cursor == "" && n > 1 {
				page.NextCursor = "next"
				continue
			}
			if cursor != "" && n == 1 {
				continue
			}
			page.Cards = append(page.Cards, c)
		}
		v = page
	case "blerg_card_get":
		if args["number"].(int) <= 0 {
			return ErrInvalid
		}
		c, ok := b.cards[args["number"].(int)]
		if !ok {
			return ErrNotFound
		}
		v = c
	default:
		return ErrInvalid
	}
	raw, _ := json.Marshal(v)
	return json.Unmarshal(raw, out)
}
func boardCard(n int, column string, tags ...string) Card {
	release := "release"
	return Card{BoardID: "board", Number: n, Title: "Title", ColumnID: &column, ReleaseID: &release, Tags: tags}
}
func TestStageMapping(t *testing.T) {
	columns := []Column{{ID: "done", Terminal: true}, {ID: "work", Name: "In progress"}}
	for _, tc := range []struct {
		card Card
		want string
	}{{boardCard(1, "done"), "done"}, {boardCard(1, "done", "to-test"), "to_test"}, {boardCard(1, "work", "to-test"), "to_test"}, {boardCard(1, "work"), "in_work"}, {boardCard(1, "next"), "next"}, {boardCard(1, "work", "needs-discussion"), "needs_you"}, {func() Card { c := boardCard(1, "next"); c.NeedsYou = true; return c }(), "needs_you"}} {
		if got := (Mapping{}).Stage(tc.card, columns); got != tc.want {
			t.Fatalf("stage %s want %s", got, tc.want)
		}
	}
}
func TestSliceMembershipDriftAndTombstone(t *testing.T) {
	s, p := planFixture(t)
	ctx := context.Background()
	_ = s.Store.Update(ctx, "b", func(r *Record) error { r.Milestones[0].ReleaseID = "release"; return nil })
	board := &fakeBoard{cards: map[int]Card{1: boardCard(1, "done"), 2: boardCard(2, "next")}}
	s.Board = board
	id, err := s.Propose(ctx, Actor{"w", "agent"}, "m", []Change{addChange("a", 1)})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Decide(ctx, p, "m", id, 0, []ItemID{"a"}); err != nil {
		t.Fatal(err)
	}
	v, err := s.Slice(ctx, "m")
	if err != nil || !v.Complete || v.Counts["unplanned"] != 1 || len(v.Cards) != 2 {
		t.Fatalf("unplanned: %+v %v", v, err)
	}
	c := board.cards[1]
	other := "next-release"
	c.ReleaseID = &other
	board.cards[1] = c
	v, err = s.Slice(ctx, "m")
	if err != nil || v.Cards[0].Membership.State != "moved_out" || !v.Cards[0].NeedsReason {
		t.Fatalf("moved: %+v %v", v, err)
	}
	c.ReleaseID = stringPtr("release")
	board.cards[1] = c
	if err = seedLegacyFrozenView(ctx, s, "m", "next-release"); err != nil {
		t.Fatal(err)
	}
	if !board.closed {
		t.Fatal("board not cut")
	}
	board.cards[1] = boardCard(1, "work", "changed")
	v, err = s.Slice(ctx, "m")
	if err != nil || v.Counts["done"] != 1 || !v.Cards[0].Drift {
		t.Fatalf("frozen drift: %+v %v", v, err)
	}
	delete(board.cards, 1)
	v, err = s.Slice(ctx, "m")
	if err != nil || !v.Cards[0].Deleted || v.Cards[0].Card.Title != "Title" || v.Counts["done"] != 1 {
		t.Fatalf("tombstone: %+v %v", v, err)
	}
}
func stringPtr(s string) *string { return &s }

func TestReviewReadsPreservePlanAndDeletedReleaseLessItems(t *testing.T) {
	ctx := context.Background()
	s, p := planFixture(t)
	b := &fakeBoard{cards: map[int]Card{1: boardCard(1, "next")}}
	s.Board = b
	if err := s.Apply(ctx, p, "m", 0, []Change{addChange("a", 1)}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Store.Load(ctx, "b")
	delete(b.cards, 1)
	v, err := s.Slice(ctx, "m")
	if err != nil || !v.Complete || len(v.Cards) != 1 || !v.Cards[0].Deleted {
		t.Fatalf("deleted slice: %+v %v", v, err)
	}
	if err := seedLegacyFrozenView(ctx, s, "m", ""); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Store.Load(ctx, "b")
	if !sameJSON(before.Items, after.Items) {
		t.Fatal("observation changed approved membership")
	}
}

func TestReviewBoardMovesNeverRewritePlan(t *testing.T) {
	ctx := context.Background()
	s, p := planFixture(t)
	_ = s.Store.Update(ctx, "b", func(r *Record) error { r.Milestones[0].ReleaseID = "release"; return nil })
	b := &fakeBoard{cards: map[int]Card{1: boardCard(1, "next"), 2: boardCard(2, "next")}}
	s.Board = b
	if err := s.Apply(ctx, p, "m", 0, []Change{addChange("a", 1)}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Store.Load(ctx, "b")
	for _, release := range []string{"elsewhere", "release"} {
		c := b.cards[1]
		c.ReleaseID = &release
		b.cards[1] = c
		if _, err := s.Slice(ctx, "m"); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := s.Store.Load(ctx, "b")
	if !sameJSON(before.Items, after.Items) || !sameJSON(before.Revisions, after.Revisions) {
		t.Fatal("GET rewrote plan")
	}
}

func TestReviewFinalPlainAndAuthority(t *testing.T) {
	ctx := context.Background()
	s, p := planFixture(t)
	_ = s.Store.Update(ctx, "b", func(r *Record) error { r.Milestones[0].ReleaseID = "release"; return nil })
	s.Board = &fakeBoard{cards: map[int]Card{}}
	if err := s.Apply(ctx, p, "m", 0, []Change{{Operation: "add", Item: Item{Title: "Plain"}}}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Person{{Actor: Actor{"other", "human"}, LoginID: "login"}, {Actor: Actor{"owner", "agent"}, LoginID: "login"}, {Actor: p.Actor}} {
		if err := s.Final(ctx, bad, "m", 1, ""); !errors.Is(err, ErrForbidden) {
			t.Fatalf("Final authority: %v", err)
		}
	}
	if err := s.Final(ctx, p, "m", 1, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("manual Final is retired, including for the owner: %v", err)
	}
}
func TestSliceIncompleteDoesNotInventMoves(t *testing.T) {
	s, _ := planFixture(t)
	ctx := context.Background()
	_ = s.Store.Update(ctx, "b", func(r *Record) error { r.Milestones[0].ReleaseID = "release"; return nil })
	s.Board = &fakeBoard{cards: map[int]Card{1: boardCard(1, "done"), 2: boardCard(2, "next")}, failCursor: true}
	v, err := s.Slice(ctx, "m")
	if err == nil || v.Complete {
		t.Fatal("partial read marked complete")
	}
	r, _ := s.Store.Load(ctx, "b")
	if len(r.Items) != 0 {
		t.Fatal("partial membership persisted")
	}
}
