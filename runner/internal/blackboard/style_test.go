package blackboard

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestReleaseStyles(t *testing.T) {
	for _, tags := range [][]string{nil, {"v1.2.0", "v1.3.0-rc.1"}} {
		suggestion := DetectStyle(tags)
		if suggestion.Style.Name != "Versioned releases" || suggestion.HasHistory != (len(tags) > 0) {
			t.Fatalf("suggestion: %+v", suggestion)
		}
	}
	custom := ReleaseStyle{Name: "Quarterly goals", Stages: []ReleaseStage{{Key: "shipped", Label: "Delivered"}}}
	if err := custom.Validate(); err != nil {
		t.Fatal(err)
	}
	if custom.Label("shipped") != "Delivered" || DefaultStyle().Label("alpha") != "Alpha" {
		t.Fatal("labels must come from style data")
	}
	if err := (ReleaseStyle{Name: "Bad", Stages: []ReleaseStage{{Key: "plan_set", Label: "Approved"}}}).Validate(); err == nil {
		t.Fatal("style cannot confer plan authority")
	}
}

func TestCustomStyleEvents(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	style, _ := json.Marshal(ReleaseStyle{Name: "Research", Stages: []ReleaseStage{{Key: "submitted", Label: "Submitted"}}})
	if err := store.Create(ctx, Record{ID: "record", Owner: Actor{ID: "person", Kind: "human"}, Version: 1, Style: style, Milestones: []Milestone{{ID: "milestone", Version: 1}}}); err != nil {
		t.Fatal(err)
	}
	s := Service{Store: store, BlackboardID: "record"}
	if _, err := s.RecordEvent(ctx, Actor{ID: "worker", Kind: "agent"}, EventInput{MilestoneID: "milestone", Kind: "submitted"}); err != nil {
		t.Fatalf("configured event refused: %v", err)
	}
	if _, err := s.RecordEvent(ctx, Actor{ID: "worker", Kind: "agent"}, EventInput{MilestoneID: "milestone", Kind: "plan_set"}); err == nil {
		t.Fatal("custom style bypassed plan authority")
	}
}

func TestStyleLimitsLifecycleEvents(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	style, _ := json.Marshal(ContinuousStyle())
	if err := store.Create(ctx, Record{ID: "record", Owner: Actor{ID: "person", Kind: "human"}, Version: 1, Style: style, Milestones: []Milestone{{ID: "milestone", Version: 1}}}); err != nil {
		t.Fatal(err)
	}
	s := Service{Store: store, BlackboardID: "record"}
	if _, err := s.RecordEvent(ctx, Actor{ID: "worker", Kind: "agent"}, EventInput{MilestoneID: "milestone", Kind: "alpha", Payload: json.RawMessage(`{"commit":"abc123"}`)}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("continuous style accepted Alpha: %v", err)
	}
	if _, err := s.RecordEvent(ctx, Actor{ID: "worker", Kind: "agent"}, EventInput{MilestoneID: "milestone", Kind: "final"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("style bypassed the person-only terminal gate")
	}
	if ContinuousStyle().Label("final") != "Shipped" {
		t.Fatal("continuous completion must use the configured label")
	}
	if err := s.Final(ctx, Person{Actor: Actor{ID: "person", Kind: "human"}, LoginID: "login"}, "milestone", 0, ""); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
}
