package blackboard

import (
	"errors"
	"strings"
	"testing"
)

func TestPlanBoardLinkDecision(t *testing.T) {
	s, p := planFixture(t)
	ctx := t.Context()
	change := Change{ID: "link", Operation: "link", Item: Item{BoardID: "extra", Reason: "Track work"}}
	pid, err := s.Propose(ctx, Actor{"main", "agent"}, "", []Change{change})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := s.Store.Load(ctx, "b")
	if len(r.Links) != 0 {
		t.Fatal("proposal linked board")
	}
	if err = s.Decide(ctx, p, "", pid, 0, []ItemID{"link"}); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Store.Load(ctx, "b")
	if len(r.Links) != 1 || r.Links[0].BoardID != "extra" || r.PlanRevision != 1 {
		t.Fatalf("link: %+v", r)
	}
	pid, err = s.Propose(ctx, Actor{"main", "agent"}, "", []Change{{ID: "unlink", Operation: "unlink", Item: Item{BoardID: "extra", Reason: "No longer needed"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Decide(ctx, p, "", pid, 1, []ItemID{"unlink"}); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Store.Load(ctx, "b")
	if len(r.Links) != 1 || r.Links[0] != (Link{WorkspaceID: "b"}) || r.PlanRevision != 2 {
		t.Fatal("unlink not committed")
	}
}
func TestPlanUnlinkedBoardReason(t *testing.T) {
	s, _ := planFixture(t)
	_, err := s.Propose(t.Context(), Actor{"main", "agent"}, "", []Change{{Operation: "add", Item: Item{BoardID: "extra", CardNumber: 1, Reason: "Track work"}}})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "link board extra first") {
		t.Fatalf("reason: %v", err)
	}
}

func TestPlanUnlinkRechecksCurrentReferences(t *testing.T) {
	s, p := planFixture(t)
	ctx := t.Context()
	if err := s.Store.Update(ctx, "b", func(r *Record) error { r.Links = []Link{{WorkspaceID: "b", BoardID: "extra"}}; return nil }); err != nil {
		t.Fatal(err)
	}
	pid, err := s.Propose(ctx, Actor{"main", "agent"}, "", []Change{{ID: "unlink", Operation: "unlink", Item: Item{BoardID: "extra", Reason: "Stop tracking"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Store.Update(ctx, "b", func(r *Record) error {
		r.Items = append(r.Items, Item{ID: "card", BoardID: "extra", CardNumber: 1, State: "planned"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = s.Decide(ctx, p, "", pid, 0, []ItemID{"unlink"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("in-use unlink: %v", err)
	}
	r, _ := s.Store.Load(ctx, "b")
	if len(r.Links) != 1 || r.PlanRevision != 0 || r.Proposals[0].Status != "pending" {
		t.Fatal("partial unlink")
	}
}

func TestPlanBoardLinkValidation(t *testing.T) {
	for _, change := range []Change{
		{Operation: "link", Item: Item{Reason: "Missing board"}},
		{Operation: "link", Item: Item{BoardID: "extra"}},
		{Operation: "link", Item: Item{BoardID: "extra", Reason: "Track work", CardNumber: 1}},
		{Operation: "unlink", Item: Item{BoardID: "missing", Reason: "Stop tracking"}},
	} {
		s, _ := planFixture(t)
		if _, err := s.Propose(t.Context(), Actor{"main", "agent"}, "", []Change{change}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid change accepted: %+v %v", change, err)
		}
	}
}

func TestPlanPendingCardCannotCommitAfterUnlink(t *testing.T) {
	s, p := planFixture(t)
	ctx := t.Context()
	if err := s.Store.Update(ctx, "b", func(r *Record) error { r.Links = []Link{{WorkspaceID: "b", BoardID: "extra"}}; return nil }); err != nil {
		t.Fatal(err)
	}
	pid, err := s.Propose(ctx, Actor{"main", "agent"}, "", []Change{{ID: "card", Operation: "add", Item: Item{BoardID: "extra", CardNumber: 1, Reason: "Track work"}}})
	if err != nil {
		t.Fatal(err)
	}
	// A link decision in another milestone can change the global link without
	// advancing this proposal's revision.
	if err = s.Store.Update(ctx, "b", func(r *Record) error { r.Links = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	if err = s.Decide(ctx, p, "", pid, 0, []ItemID{"card"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unlinked card committed: %v", err)
	}
	r, _ := s.Store.Load(ctx, "b")
	if len(r.Items) != 0 || r.PlanRevision != 0 {
		t.Fatal("partial card commit")
	}
}
