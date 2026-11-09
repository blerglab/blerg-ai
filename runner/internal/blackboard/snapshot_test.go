package blackboard

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestDailySnapshotRestartGapAndIncomplete(t *testing.T) {
	ctx := context.Background()
	s, _ := planFixture(t)
	_ = s.Store.Update(ctx, "b", func(r *Record) error { r.Milestones[0].ReleaseID = "release"; return nil })
	clock := time.Date(2026, 1, 1, 23, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return clock }
	b := &fakeBoard{cards: map[int]Card{1: boardCard(1, "done"), 2: boardCard(2, "next")}, failCursor: true}
	s.Board = b
	snap, err := s.SnapshotDay(ctx, "m")
	if err == nil || snap.Complete {
		t.Fatal("incomplete read hidden")
	}
	b.failCursor = false
	restarted := Service{Store: s.Store, BlackboardID: "b", Board: b, Now: s.Now}
	if _, err = restarted.SnapshotDay(ctx, "m"); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(48 * time.Hour)
	if _, err = restarted.SnapshotDay(ctx, "m"); err != nil {
		t.Fatal(err)
	}
	_, samples, err := s.Timeline(ctx, "m")
	if err != nil || len(samples) != 2 || !samples[0].Complete || samples[1].Day != "2026-01-03" {
		t.Fatalf("invented days/duplicate: %+v %v", samples, err)
	}
}
func TestEventsNumberingProvenanceAndPerson(t *testing.T) {
	s, p := planFixture(t)
	ctx := context.Background()
	s.Now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
	input := EventInput{ID: "deploy-1", MilestoneID: "m", Kind: "alpha", Payload: json.RawMessage(`{"commit":"abc","by":"person","number":999}`)}
	// Client-supplied numbering is overwritten; claimed attribution stays an assertion.
	e, err := s.RecordEvent(ctx, Actor{"worker", "agent"}, input)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	_ = json.Unmarshal(e.Payload, &payload)
	if payload["number"] != float64(1) || e.Source != "agent" || e.AuthenticatedActor.Kind != "agent" {
		t.Fatalf("provenance %+v", e)
	}
	if _, err = s.RecordEvent(ctx, Actor{"worker", "agent"}, input); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	input.ID = "deploy-2"
	e, err = s.RecordEvent(ctx, Actor{"worker", "agent"}, input)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(e.Payload, &payload)
	if payload["number"] != float64(2) {
		t.Fatal("alpha number")
	}
	e, err = s.RecordEvent(ctx, p.Actor, EventInput{MilestoneID: "m", Kind: "decision", Payload: []byte(`{"reason":"A person's decision"}`)})
	if err != nil || e.Source != "person" || !e.RecordedAt.Equal(s.now()) {
		t.Fatalf("person event %+v %v", e, err)
	}
	if _, err = s.RecordEvent(ctx, Actor{"worker", "agent"}, EventInput{MilestoneID: "m", Kind: "plan_set"}); err == nil {
		t.Fatal("generic event established plan authority")
	}
}
