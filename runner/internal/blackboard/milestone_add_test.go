package blackboard

import (
	"errors"
	"strings"
	"testing"
)

func TestMilestoneAddProposal(t *testing.T) {
	s, p := planFixture(t)
	if err := s.Store.Update(t.Context(), "b", func(r *Record) error { r.Milestones[0].BoardID = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	c := thrustChange(t, `{"id":"open","operation":"milestone_add","milestone":{"name":"0.2.0","target":"2027-01-02","description":"Next work"}}`)
	pid, err := s.Propose(t.Context(), Actor{"main", "agent"}, "", []Change{c})
	if err != nil {
		t.Fatal(err)
	}
	r, _ := s.Store.Load(t.Context(), "b")
	if len(r.Milestones) != 1 {
		t.Fatal("proposal opened milestone")
	}
	if err = s.Decide(t.Context(), Person{Actor: p.Actor}, "", pid, 0, []ItemID{"open"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("login guard: %v", err)
	}
	if err = s.Decide(t.Context(), p, "", pid, 0, []ItemID{"open"}); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Store.Load(t.Context(), "b")
	if len(r.Milestones) != 2 || r.Milestones[1].Name != "0.2.0" || r.Milestones[1].Target == nil || r.Milestones[1].Stage != "" {
		t.Fatalf("missing milestone: %+v", r.Milestones)
	}
}

func TestMilestoneAddValidation(t *testing.T) {
	for _, raw := range []string{
		`{"operation":"milestone_add"}`,
		`{"operation":"milestone_add","milestone":{"name":" "}}`,
		`{"operation":"milestone_add","milestone":{"id":"chosen","name":"Next"}}`,
		`{"operation":"milestone_add","milestone":{"name":"Card #23"}}`,
		`{"operation":"milestone_add","milestone":{"name":"Next","target":"tomorrow"}}`,
		`{"operation":"milestone_add","milestone":{"name":"Next"},"item":{"title":"Hidden scope"}}`,
		`{"operation":"milestone_add","milestone":{"name":"Theme","board_id":"board"}}`,
		`{"operation":"milestone_add","milestone":{"name":"Next","board_id":"unknown"}}`,
		`{"operation":"milestone_add","milestone":{"name":"Next","release_id":"unknown"}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			s, _ := planFixture(t)
			s.Board = &fakeBoard{}
			// Make the fixture board explicitly linked, as production setup does.
			if err := s.Store.Update(t.Context(), "b", func(r *Record) error { r.Links = []Link{{BoardID: "board"}}; return nil }); err != nil {
				t.Fatal(err)
			}
			_, err := s.Propose(t.Context(), Actor{"main", "agent"}, "", []Change{thrustChange(t, raw)})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid milestone accepted: %v", err)
			}
		})
	}
}

func TestMilestoneAddReleaseAndIncomingScope(t *testing.T) {
	s, p := planFixture(t)
	s.Board = &fakeBoard{}
	if err := s.Store.Update(t.Context(), "b", func(r *Record) error { r.Links = []Link{{BoardID: "board"}}; return nil }); err != nil {
		t.Fatal(err)
	}
	c := thrustChange(t, `{"id":"open","operation":"milestone_add","milestone":{"name":"Next","release_id":"release"}}`)
	pid, err := s.Propose(t.Context(), Actor{"main", "agent"}, "", []Change{c})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Decide(t.Context(), p, "", pid, 0, []ItemID{"open"}); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Store.Load(t.Context(), "b")
	m := r.Milestones[1]
	if m.BoardID != "board" || m.ReleaseID != "release" {
		t.Fatalf("release link: %+v", m)
	}
	if err = s.Apply(t.Context(), p, "m", 0, []Change{addChange("card", 1)}); err != nil {
		t.Fatal(err)
	}
	move := Change{Operation: "move", Item: Item{BoardID: "board", CardNumber: 1, Reason: "Next release", Destination: m.ID}}
	if err = s.Apply(t.Context(), p, "m", 1, []Change{move}); err != nil {
		t.Fatal(err)
	}
	incoming := Change{Operation: "add", Item: Item{BoardID: "board", CardNumber: 1, Reason: "Next release"}}
	if err = s.Apply(t.Context(), p, m.ID, 0, []Change{incoming}); err != nil {
		t.Fatal(err)
	}
	r, _ = s.Store.Load(t.Context(), "b")
	if len(r.Items) != 2 || r.Items[0].Destination != m.ID || r.Items[1].MilestoneID != m.ID || r.Items[1].State != "planned" {
		t.Fatalf("incoming scope: %+v", r.Items)
	}
}

func TestMilestoneAddIdentifierErrorsNameExactField(t *testing.T) {
	for _, field := range []string{"board_id", "release_id"} {
		t.Run(field, func(t *testing.T) {
			s, _ := planFixture(t)
			c := Change{Operation: "milestone_add", Milestone: &MilestoneChange{Name: "Next", BoardID: "board"}}
			if field == "board_id" {
				c.Milestone.BoardID = strings.Repeat("x", 201)
			} else {
				c.Milestone.ReleaseID = strings.Repeat("x", 201)
			}
			_, err := s.Propose(t.Context(), Actor{"main", "agent"}, "", []Change{c})
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "milestone."+field+":") {
				t.Fatalf("expected exact field %s, got %v", field, err)
			}
		})
	}
}
