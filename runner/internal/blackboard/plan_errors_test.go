package blackboard

import (
	"errors"
	"strings"
	"testing"
)

func TestProposeRefusalsNameField(t *testing.T) {
	type refusal struct {
		name, field string
		actor       Actor
		mid         string
		changes     []Change
	}
	actor := Actor{"main", "agent"}
	cases := []refusal{
		{"actor", "actor.id", Actor{}, "m", []Change{addChange("a", 1)}},
		{"milestone length", "milestone_id", actor, strings.Repeat("x", 201), []Change{addChange("a", 1)}},
		{"no changes", "changes", actor, "m", nil},
		{"duplicate change", "changes.id", actor, "m", []Change{addChange("a", 1), addChange("a", 2)}},
		{"operation", "operation", actor, "m", []Change{{Operation: "invalid"}}},
		{"plain title", "item.title", actor, "m", []Change{{Operation: "add"}}},
		{"plain card number", "item.card_number", actor, "m", []Change{{Operation: "add", Item: Item{Title: "Work", CardNumber: 1}}}},
		{"card number", "item.card_number", actor, "m", []Change{addChange("a", 0)}},
		{"reason", "item.reason", actor, "m", []Change{{Operation: "remove", Item: Item{BoardID: "board", CardNumber: 1}}}},
		{"ownership", "item.id", actor, "m", []Change{{Operation: "remove", Item: Item{ID: "foreign"}}}},
		{"goal", "item.goal_id", actor, "m", []Change{{Operation: "add", Item: Item{Title: "Work", GoalID: "foreign"}}}},
		{"progress", "item.progress", actor, "m", []Change{{Operation: "add", Item: Item{Title: "Work", Progress: "foreign"}}}},
		{"thrust", "item.thrust_id", actor, "m", []Change{{Operation: "add", Item: Item{Title: "Work", ThrustID: "foreign"}}}},
		{"destination", "item.destination", actor, "m", []Change{{Operation: "move", Item: Item{BoardID: "board", CardNumber: 1, Reason: "Moved"}}}},
		{"link reason", "item.reason", actor, "m", []Change{{Operation: "link", Item: Item{BoardID: "other"}}}},
		{"link board", "item.board_id", actor, "m", []Change{{Operation: "link", Item: Item{Reason: "Track work"}}}},
		{"link shape", "item.title", actor, "m", []Change{{Operation: "link", Item: Item{BoardID: "other", Reason: "Track work", Title: "Hidden"}}}},
		{"unlinked", "item.board_id", actor, "m", []Change{{Operation: "add", Item: Item{BoardID: "foreign", CardNumber: 1, Reason: "Bug"}}}},
		{"thrust payload", "thrust", actor, "m", []Change{{Operation: "thrust_add"}}},
		{"thrust name", "thrust.name", actor, "m", []Change{thrustChange(t, `{"operation":"thrust_add","thrust":{"name":" ","reason":"Group"}}`)}},
		{"thrust reason", "thrust.reason", actor, "m", []Change{thrustChange(t, `{"operation":"thrust_add","thrust":{"name":"Group"}}`)}},
		{"thrust ownership", "thrust.id", actor, "m", []Change{thrustChange(t, `{"operation":"thrust_remove","thrust":{"id":"unknown","reason":"Group"}}`)}},
	}

	cases = append(cases,
		refusal{"long actor", "actor.id", Actor{ID: strings.Repeat("x", 201)}, "m", []Change{addChange("a", 1)}},
		refusal{"missing milestone", "milestone_id", actor, "missing", []Change{addChange("a", 1)}},
		refusal{"too many changes", "changes", actor, "m", make([]Change, 101)},
		refusal{"long change id", "changes.id", actor, "m", []Change{addChange(ItemID(strings.Repeat("x", 101)), 1)}},
		refusal{"long reason", "item.reason", actor, "m", []Change{{Operation: "add", Item: Item{Title: "Work", Reason: strings.Repeat("x", 1001)}}}},
		refusal{"long title", "item.title", actor, "m", []Change{{Operation: "add", Item: Item{Title: strings.Repeat("x", 201)}}}},
		refusal{"unexpected thrust", "thrust", actor, "m", []Change{thrustChange(t, `{"operation":"add","item":{"title":"Work"},"thrust":{"name":"Hidden"}}`)}},
		refusal{"unexpected milestone", "milestone", actor, "m", []Change{thrustChange(t, `{"operation":"add","item":{"title":"Work"},"milestone":{"name":"Hidden"}}`)}},
		refusal{"link batch", "changes", actor, "m", []Change{{Operation: "link", Item: Item{BoardID: "other", Reason: "Track work"}}, addChange("a", 1)}},
	)
	for _, tc := range []struct{ raw, field string }{
		{`{"operation":"thrust_add","thrust":{"id":"chosen","name":"Group","reason":"Group"}}`, "thrust.id"},
		{`{"operation":"thrust_add","thrust":{"name":"Group","reason":"Group"}}`, "thrust.goal_id"},
		{`{"operation":"thrust_add","thrust":{"name":"Group","goal_id":"missing","reason":"Group"}}`, "thrust.goal_id"},
		{`{"operation":"thrust_add","thrust":{"name":"Group","goal_id":"g","destination":"t","reason":"Group"}}`, "thrust.destination"},
		{`{"operation":"thrust_remove","thrust":{"id":"t","reason":"Group","goal_id":"g"}}`, "thrust.goal_id"},
		{`{"operation":"thrust_rename","thrust":{"id":"t","name":" ","reason":"Group"}}`, "thrust.name"},
		{`{"operation":"thrust_rename","thrust":{"id":"t","name":"Group","reason":"Group","destination":"t"}}`, "thrust.destination"},
		{`{"operation":"thrust_merge","thrust":{"id":"t","name":"Hidden","reason":"Group"}}`, "thrust.name"},
		{`{"operation":"thrust_merge","thrust":{"id":"t","destination":"t","reason":"Group"}}`, "thrust.destination"},
		{`{"operation":"thrust_remove","thrust":{"id":"t","name":"Hidden","reason":"Group"}}`, "thrust.name"},
		{`{"operation":"thrust_remove","thrust":{"id":"t","destination":"other","reason":"Group"}}`, "thrust.destination"},
		{`{"operation":"thrust_remove","thrust":{"id":"t","reason":"Group"},"item":{"title":"Hidden"}}`, "item"},
	} {
		cases = append(cases, refusal{tc.raw, tc.field, actor, "m", []Change{thrustChange(t, tc.raw)}})
	}
	for _, field := range []string{"destination", "board_id", "milestone_id", "goal_id", "thrust_id", "id"} {
		c := addChange("a", 1)
		value := strings.Repeat("x", 201)
		switch field {
		case "destination":
			c.Item.Destination = value
		case "board_id":
			c.Item.BoardID = value
		case "milestone_id":
			c.Item.MilestoneID = value
		case "goal_id":
			c.Item.GoalID = value
		case "thrust_id":
			c.Item.ThrustID = value
		case "id":
			c.Item.ID = ItemID(value)
		}
		cases = append(cases, refusal{"long " + field, "item." + field, actor, "m", []Change{c}})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := planFixture(t)
			_, err := s.Propose(t.Context(), tc.actor, tc.mid, tc.changes)
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.field+":") {
				t.Fatalf("want %s and ErrInvalid, got %v", tc.field, err)
			}
			if tc.name != "unlinked" && strings.Contains(err.Error(), "foreign") {
				t.Fatal("refusal echoed caller data")
			}
		})
	}
}

func TestProposeLifecycleAndBudgetRefusalsNameField(t *testing.T) {
	s, _ := planFixture(t)
	actor := Actor{"main", "agent"}
	if err := s.Store.Update(t.Context(), "b", func(r *Record) error { r.Milestones[0].Stage = "Finalizing"; return nil }); err != nil {
		t.Fatal(err)
	}
	_, err := s.Propose(t.Context(), actor, "m", []Change{addChange("a", 1)})
	if !errors.Is(err, ErrInvalid) || !errors.Is(err, ErrFrozen) || !strings.Contains(err.Error(), "milestone_id:") {
		t.Fatalf("frozen: %v", err)
	}
	s, _ = planFixture(t)
	for i := 0; i < MaxPendingAgentProposals; i++ {
		if _, err = s.Propose(t.Context(), actor, "m", []Change{addChange("a", 1)}); err != nil {
			t.Fatal(err)
		}
	}
	_, err = s.Propose(t.Context(), actor, "m", []Change{addChange("a", 1)})
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "changes:") {
		t.Fatalf("budget: %v", err)
	}
}
