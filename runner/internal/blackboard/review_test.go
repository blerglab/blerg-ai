package blackboard

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type boardFunc func(context.Context, string, map[string]any, any) error

func (f boardFunc) Call(c context.Context, t string, a map[string]any, o any) error {
	return f(c, t, a, o)
}

func TestReviewForeignBoardSameNumber(t *testing.T) {
	ctx := context.Background()
	s, p := planFixture(t)
	_ = s.Store.Update(ctx, "b", func(r *Record) error {
		r.Links = []Link{{BoardID: "foreign"}}
		r.Milestones[0].ReleaseID = "release"
		return nil
	})
	b := &fakeBoard{cards: map[int]Card{1: boardCard(1, "next")}}
	gets := 0
	s.Board = boardFunc(func(c context.Context, tool string, a map[string]any, o any) error {
		if (tool == "blerg_card_get" || tool == "blerg_card_search") && a["board_id"] == "foreign" {
			gets++
			card := boardCard(1, "work")
			card.BoardID = "foreign"
			card.ReleaseID = stringPtr("other")
			var value any = card
			if tool == "blerg_card_search" {
				value = CardPage{Cards: []Card{card}}
			}
			raw, _ := json.Marshal(value)
			return json.Unmarshal(raw, o)
		}
		return b.Call(c, tool, a, o)
	})
	change := addChange("a", 1)
	change.Item.BoardID = "foreign"
	if err := s.Apply(ctx, p, "m", 0, []Change{addChange("b", 1), change}); err != nil {
		t.Fatal(err)
	}
	v, err := s.Slice(ctx, "m")
	if err != nil || len(v.Cards) != 2 || gets != 1 {
		t.Fatalf("foreign slice %+v gets=%d %v", v, gets, err)
	}
	if err = seedLegacyFrozenView(ctx, s, "m", "next"); err != nil {
		t.Fatal(err)
	}
	if gets != 3 {
		t.Fatalf("Final used wrong board: %d", gets)
	}
}
func TestReviewStaleRejectionAndBounds(t *testing.T) {
	ctx := context.Background()
	s, p := planFixture(t)
	id, err := s.Propose(ctx, Actor{"w", "agent"}, "m", []Change{addChange("a", 1)})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Apply(ctx, p, "m", 0, []Change{addChange("b", 2)}); err != nil {
		t.Fatal(err)
	}
	if err = s.Decide(ctx, p, "m", id, 0, nil); err != nil {
		t.Fatalf("stale rejection: %v", err)
	}
	change := addChange("c", 3)
	change.Item.Destination = strings.Repeat("x", 201)
	if _, err = s.Propose(ctx, p.Actor, "m", []Change{change}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unbounded destination")
	}
	for _, kind := range []string{"proposals", "events", "bytes"} {
		t.Run(kind, func(t *testing.T) {
			r := Record{ID: "bounded-" + kind, Owner: p.Actor, Version: 1}
			switch kind {
			case "proposals":
				r.Proposals = make([]Proposal, 1001)
			case "events":
				r.Events = make([]Event, MaxEvents+1)
			case "bytes":
				r.Vision = strings.Repeat("x", MaxRecordBytes)
			}
			err := s.Store.Create(ctx, r)
			if kind == "bytes" {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("core budget: %v", err)
				}
			} else if err != nil {
				t.Fatalf("history incorrectly charged to core %s: %v", kind, err)
			}
		})
	}
}
func TestReviewDateMovedAndFrozenGuards(t *testing.T) {
	ctx := context.Background()
	s, p := planFixture(t)
	input := EventInput{MilestoneID: "m", Kind: "date_moved", Payload: json.RawMessage(`{"target":"2027-04-05"}`)}
	for _, actor := range []Actor{{"other", "human"}, {"owner", "agent"}} {
		if _, err := s.RecordEvent(ctx, actor, input); !errors.Is(err, ErrForbidden) {
			t.Fatalf("date authority %v", err)
		}
	}
	if _, err := s.RecordEvent(ctx, p.Actor, input); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Store.Load(ctx, "b")
	if r.Milestones[0].Target == nil || r.Milestones[0].Target.Format("2006-01-02") != "2027-04-05" {
		t.Fatal("date not moved")
	}
	for _, stage := range []string{"Finalizing", "Final"} {
		t.Run(stage, func(t *testing.T) {
			_ = s.Store.Update(ctx, "b", func(r *Record) error {
				m := &r.Milestones[0]
				m.Stage = stage
				if stage == "Final" {
					now := time.Now()
					m.FrozenAt = &now
					m.Frozen = json.RawMessage(`{"counts":{"done":7},"cards":[]}`)
				}
				return nil
			})
			if _, err := s.RecordEvent(ctx, p.Actor, EventInput{MilestoneID: "m", Kind: "decision"}); !errors.Is(err, ErrFrozen) {
				t.Fatalf("event guard %v", err)
			}
			if stage == "Final" {
				v, err := s.Slice(ctx, "m")
				if err != nil || !v.Frozen || v.Counts["done"] != 7 {
					t.Fatalf("frozen slice %+v %v", v, err)
				}
			}
		})
	}
}
func TestReviewSnapshotRepairDedupeAndRevisionImmutability(t *testing.T) {
	ctx := context.Background()
	s, p := planFixture(t)
	_ = s.Store.Update(ctx, "b", func(r *Record) error { r.Milestones[0].ReleaseID = "release"; return nil })
	b := &fakeBoard{cards: map[int]Card{1: boardCard(1, "done"), 2: boardCard(2, "next")}, failCursor: true}
	s.Board = b
	if _, err := s.SnapshotDay(ctx, "m"); err == nil {
		t.Fatal("missing incomplete error")
	}
	b.failCursor = false
	snap, err := s.SnapshotDay(ctx, "m")
	if err != nil || !snap.Complete {
		t.Fatalf("repair %+v %v", snap, err)
	}
	calls := len(b.calls)
	if _, err = s.SnapshotDay(ctx, "m"); err != nil {
		t.Fatal(err)
	}
	if len(b.calls) != calls {
		t.Fatal("complete daily snapshot was observed again")
	}
	r, _ := s.Store.Load(ctx, "b")
	if len(r.Snapshots) != 1 {
		t.Fatal("duplicate snapshot")
	}
	if err = s.Apply(ctx, p, "m", 0, []Change{addChange("a", 1)}); err != nil {
		t.Fatal(err)
	}
	if err = s.Store.Update(ctx, "b", func(r *Record) error { r.Revisions[0].Items[0].Reason = "rewrite"; return nil }); !errors.Is(err, ErrImmutable) {
		t.Fatalf("revision mutable %v", err)
	}
	if _, err = s.RecordEvent(ctx, Actor{"viewer", "human"}, EventInput{Kind: "decision"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("viewer event %v", err)
	}
}

func TestReviewWriteBudgetsAreAtomic(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"proposals", "events"} {
		t.Run(kind, func(t *testing.T) {
			store := NewMemoryStore()
			owner := Actor{"owner", "human"}
			agent := Actor{"worker", "agent"}
			r := Record{ID: "budget", Owner: owner, Version: 1}
			if kind == "proposals" {
				for n := 0; n < MaxPendingAgentProposals; n++ {
					r.Proposals = append(r.Proposals, Proposal{ID: ProposalID(NewID()), Status: "pending", Actor: agent})
				}
			} else {
				for n := 0; n < MaxEvents; n++ {
					r.Events = append(r.Events, Event{ID: NewID(), Kind: "decision", Source: "agent", AuthenticatedActor: agent})
				}
			}
			if err := store.Create(ctx, r); err != nil {
				t.Fatal(err)
			}
			service := Service{Store: store, BlackboardID: r.ID}
			var err error
			if kind == "proposals" {
				_, err = service.Propose(ctx, agent, "", []Change{{Operation: "add", Item: Item{Title: "Overflow"}}})
			} else {
				_, err = service.RecordEvent(ctx, agent, EventInput{Kind: "decision"})
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("%s overflow: %v", kind, err)
			}
			after, _ := store.Load(ctx, r.ID)
			if len(after.Proposals) != len(r.Proposals) || len(after.Events) != len(r.Events) {
				t.Fatal("partial overflow persisted")
			}
		})
	}
}

func TestReviewChangeCapsCountUTF8Bytes(t *testing.T) {
	for _, field := range []string{"destination", "title", "reason"} {
		t.Run(field, func(t *testing.T) {
			s, p := planFixture(t)
			change := addChange("a", 1)
			switch field {
			case "destination":
				change.Item.Destination = strings.Repeat("雪", 67)
			case "title":
				change.Item.Title = strings.Repeat("雪", 67)
			case "reason":
				change.Item.Reason = strings.Repeat("雪", 334)
			}
			if _, err := s.Propose(context.Background(), p.Actor, "m", []Change{change}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("%s byte cap %v", field, err)
			}
		})
	}
}
