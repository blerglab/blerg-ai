package blackboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRound5WorkspaceProposalQuota(t *testing.T) {
	s, _ := planFixture(t)
	ctx := context.Background()
	for i := 0; i < MaxWorkspacePendingProposals; i++ {
		if _, e := s.Propose(ctx, Actor{ID: fmt.Sprint(i), Kind: "agent"}, "m", []Change{{Operation: "add", Item: Item{Title: "idea"}}}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.Propose(ctx, Actor{ID: "new-worker", Kind: "agent"}, "m", []Change{{Operation: "add", Item: Item{Title: "idea"}}}); !errors.Is(e, ErrInvalid) {
		t.Fatalf("aggregate cap reset by worker: %v", e)
	}
}

func TestRound5WorkspaceEventQuota(t *testing.T) {
	s, _ := planFixture(t)
	ctx := context.Background()
	if e := s.Store.Update(ctx, s.BlackboardID, func(r *Record) error {
		for i := 0; i < 5; i++ {
			r.Events = append(r.Events, Event{ID: NewID(), MilestoneID: "m", Source: "agent", Kind: "decision", AuthenticatedActor: Actor{ID: fmt.Sprint(i), Kind: "agent"}, Payload: json.RawMessage(`{"text":"` + strings.Repeat("x", MaxWorkspaceEventBytes/5+1024) + `"}`)})
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if _, e := s.RecordEvent(ctx, Actor{ID: "new-worker", Kind: "agent"}, EventInput{MilestoneID: "m", Kind: "decision"}); !errors.Is(e, ErrInvalid) {
		t.Fatalf("worker churn reset event aggregate: %v", e)
	}
}
