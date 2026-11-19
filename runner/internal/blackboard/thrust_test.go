package blackboard

import (
	"encoding/json"
	"errors"
	"testing"
)

func thrustChange(t *testing.T, raw string) Change {
	t.Helper()
	var c Change
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	return c
}
func TestThrustRefusalsAndHistory(t *testing.T) {
	s, p := planFixture(t)
	ctx := t.Context()
	if err := s.Store.Update(ctx, "b", func(r *Record) error {
		r.Goals = []Goal{{ID: "g", Name: "Goal"}}
		r.Thrusts[0].Name = "Original"
		r.Thrusts[0].GoalID = "g"
		r.Items = []Item{{ID: "card", ThrustID: "t", MilestoneID: "m", State: "planned"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	remove := thrustChange(t, `{"id":"remove","operation":"thrust_remove","thrust":{"id":"t","reason":"Unused"}}`)
	if _, err := s.Propose(ctx, Actor{"main", "agent"}, "m", []Change{remove}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nonempty: %v", err)
	}
	rename := thrustChange(t, `{"id":"rename","operation":"thrust_rename","thrust":{"id":"t","name":"Renamed","reason":"Clarity"}}`)
	pid, err := s.Propose(ctx, Actor{"main", "agent"}, "m", []Change{rename})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Person{{Actor: Actor{"other", "human"}, LoginID: "login"}, {Actor: p.Actor}, {Actor: Actor{"owner", "agent"}, LoginID: "login"}} {
		if err = s.Decide(ctx, bad, "m", pid, 0, []ItemID{"rename"}); !errors.Is(err, ErrForbidden) {
			t.Fatalf("authority: %v", err)
		}
	}
	if err = s.Decide(ctx, p, "m", pid, 1, []ItemID{"rename"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale: %v", err)
	}
	if err = s.Decide(ctx, p, "m", pid, 0, []ItemID{"rename"}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Store.Load(ctx, "b")
	history, _ := json.Marshal(before.Proposals[0])
	rename = thrustChange(t, `{"id":"again","operation":"thrust_rename","thrust":{"id":"t","name":"Final name","reason":"Clarity"}}`)
	pid, err = s.Propose(ctx, Actor{"main", "agent"}, "m", []Change{rename})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Decide(ctx, p, "m", pid, 1, []ItemID{"again"}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.Store.Load(ctx, "b")
	got, _ := json.Marshal(after.Proposals[0])
	if string(history) != string(got) {
		t.Fatal("history rewritten")
	}
	if after.Revisions[0].Thrusts[0].Name != "Renamed" {
		t.Fatal("historical revision lost its name")
	}
	if !containsName(history, "Original") {
		t.Fatalf("missing historical name: %s", history)
	}
}
func containsName(raw []byte, name string) bool {
	var q struct {
		Changes []struct {
			Thrust struct {
				PreviousName string `json:"previous_name"`
			} `json:"thrust"`
		} `json:"changes"`
	}
	_ = json.Unmarshal(raw, &q)
	return len(q.Changes) > 0 && q.Changes[0].Thrust.PreviousName == name
}

func TestThrustInvalidShapesAndScope(t *testing.T) {
	for _, raw := range []string{
		`{"operation":"thrust_add","thrust":{"name":"","goal_id":"g","reason":"Group"}}`,
		`{"operation":"thrust_add","thrust":{"id":"chosen","name":"New","goal_id":"g","reason":"Group"}}`,
		`{"operation":"thrust_add","thrust":{"name":"New","goal_id":"unknown","reason":"Group"}}`,
		`{"operation":"thrust_rename","thrust":{"id":"t","name":" ","reason":"Group"}}`,
		`{"operation":"thrust_rename","thrust":{"id":"t","name":"New","reason":""}}`,
		`{"operation":"thrust_merge","thrust":{"id":"t","destination":"t","reason":"Group"}}`,
		`{"operation":"thrust_merge","thrust":{"id":"t","destination":"other-scope","reason":"Group"}}`,
		`{"operation":"thrust_remove","thrust":{"id":"unknown","reason":"Group"}}`,
		`{"operation":"thrust_remove","thrust":{"id":"t","reason":"Group"},"item":{"title":"Hidden scope"}}`,
		`{"operation":"add","thrust":{"id":"t","reason":"Group"},"item":{"title":"Hidden scope"}}`,
		`{"operation":"thrust_remove"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			s, _ := planFixture(t)
			if err := s.Store.Update(t.Context(), "b", func(r *Record) error {
				r.Goals = []Goal{{ID: "g"}}
				r.Thrusts = append(r.Thrusts, Thrust{ID: "other-scope", MilestoneID: "different"})
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Propose(t.Context(), Actor{"main", "agent"}, "m", []Change{thrustChange(t, raw)}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid accepted: %v", err)
			}
		})
	}
}

func TestThrustSnapshotsIgnoreClaimedNames(t *testing.T) {
	s, _ := planFixture(t)
	if err := s.Store.Update(t.Context(), "b", func(r *Record) error { r.Thrusts[0].Name = "Real name"; return nil }); err != nil {
		t.Fatal(err)
	}
	c := thrustChange(t, `{"operation":"thrust_rename","thrust":{"id":"t","name":"New name","previous_name":"Forged","reason":"Clarity"}}`)
	if _, err := s.Propose(t.Context(), Actor{"main", "agent"}, "m", []Change{c}); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Store.Load(t.Context(), "b")
	if r.Proposals[0].Changes[0].Thrust.PreviousName != "Real name" {
		t.Fatal("trusted claimed name")
	}
}

func TestThrustProposalCopyIsolation(t *testing.T) {
	s, _ := planFixture(t)
	c := thrustChange(t, `{"operation":"thrust_rename","thrust":{"id":"t","name":"New name","reason":"Clarity"}}`)
	if _, err := s.Propose(t.Context(), Actor{"main", "agent"}, "m", []Change{c}); err != nil {
		t.Fatal(err)
	}
	c.Thrust.Name = "Changed after proposal"
	r, _ := s.Store.Load(t.Context(), "b")
	if r.Proposals[0].Changes[0].Thrust.Name != "New name" {
		t.Fatal("caller rewrote saved proposal")
	}
}
