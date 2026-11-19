package blackboard

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestRound4ActorBudgets(t *testing.T) {
	s, _ := planFixture(t)
	ctx := context.Background()
	a := Actor{ID: "a", Kind: "agent"}
	b := Actor{ID: "b", Kind: "agent"}
	for i := 0; i < MaxPendingAgentProposals; i++ {
		if _, e := s.Propose(ctx, a, "m", []Change{{Operation: "add", Item: Item{Title: "idea"}}}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.Propose(ctx, b, "m", []Change{{Operation: "add", Item: Item{Title: "other"}}}); e != nil {
		t.Fatalf("proposal quota shared: %v", e)
	}
	if e := s.Store.Update(ctx, s.BlackboardID, func(r *Record) error {
		r.Events = append(r.Events, Event{ID: NewID(), MilestoneID: "m", Source: "agent", Kind: "decision", AuthenticatedActor: a, Payload: json.RawMessage(`{"text":"` + string(makeFilled(MaxPartitionEventBytes)) + `"}`)})
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RecordEvent(ctx, a, EventInput{MilestoneID: "m", Kind: "decision"}); !errors.Is(e, ErrInvalid) {
		t.Fatalf("actor a not capped: %v", e)
	}
	if _, e := s.RecordEvent(ctx, b, EventInput{MilestoneID: "m", Kind: "decision"}); e != nil {
		t.Fatalf("event quota shared: %v", e)
	}
}
func makeFilled(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'x'
	}
	return b
}
