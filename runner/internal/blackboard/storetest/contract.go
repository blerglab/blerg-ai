package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"strings"
	"testing"
	"time"
)

// StoreContract is shared with the real Postgres provider.
func StoreContract(t *testing.T, s blackboard.Store) {
	t.Run("provider admission", func(t *testing.T) { PartitionAdmissionContract(t, s) })
	t.Run("workspace admission", func(t *testing.T) { WorkspaceAdmissionContract(t, s) })
	t.Run("workspace event admission", func(t *testing.T) { WorkspaceEventAdmissionContract(t, s) })
	t.Run("linked workspace admission", func(t *testing.T) { LinkedWorkspaceAdmissionContract(t, s) })
	t.Helper()
	ctx := context.Background()
	r := blackboard.Record{ID: blackboard.NewID(), Owner: blackboard.Actor{ID: "owner", Kind: "human"}, Version: 1}
	if err := s.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx, r.ID)
	if err != nil || got.ParentID != nil {
		t.Fatalf("null parent: %+v %v", got, err)
	}
	e := blackboard.Event{ID: blackboard.NewID(), AuthenticatedActor: blackboard.Actor{ID: "worker", Kind: "agent"}, RecordedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Source: "agent", Kind: "assertion", Payload: []byte(`{"by":"person","claim":true}`)}
	if err = s.Update(ctx, r.ID, func(r *blackboard.Record) error { r.Events = append(r.Events, e); return nil }); err != nil {
		t.Fatal(err)
	}
	if err = s.Update(ctx, r.ID, func(r *blackboard.Record) error { r.Events[0].Kind = "plan_set"; return nil }); !errors.Is(err, blackboard.ErrImmutable) {
		t.Fatalf("history mutation: %v", err)
	}
	if err = s.Update(ctx, r.ID, func(r *blackboard.Record) error {
		r.Events[0].Payload = []byte(`{ "claim": true, "by": "person" }`)
		return nil
	}); err != nil {
		t.Fatalf("JSON value equality: %v", err)
	}
	if err = s.Update(ctx, r.ID, func(r *blackboard.Record) error {
		r.Revisions = append(r.Revisions, blackboard.Revision{Number: 1, Items: []blackboard.Item{{ID: blackboard.ItemID(blackboard.NewID()), Title: "Original"}}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = s.Update(ctx, r.ID, func(r *blackboard.Record) error { r.Revisions[0].Items[0].Title = "Rewrite"; return nil }); !errors.Is(err, blackboard.ErrImmutable) {
		t.Fatalf("revision mutation: %v", err)
	}
	if err = s.Update(ctx, r.ID, func(r *blackboard.Record) error { r.Revisions = nil; return nil }); !errors.Is(err, blackboard.ErrImmutable) {
		t.Fatalf("revision deletion: %v", err)
	}
	got, err = s.Load(ctx, r.ID)
	if err != nil || got.Events[0].AuthenticatedActor != e.AuthenticatedActor || got.Events[0].Source != "agent" {
		t.Fatalf("provenance lost: %+v %v", got, err)
	}
	if err = s.Update(ctx, r.ID, func(r *blackboard.Record) error { r.Name = "should roll back"; return blackboard.ErrInvalid }); !errors.Is(err, blackboard.ErrInvalid) {
		t.Fatal(err)
	}
	got, _ = s.Load(ctx, r.ID)
	if got.Name != "" {
		t.Fatal("failed callback persisted")
	}
	if _, err = s.Load(ctx, "missing"); !errors.Is(err, blackboard.ErrNotFound) {
		t.Fatal(err)
	}
	if err = s.Create(ctx, blackboard.Record{}); !errors.Is(err, blackboard.ErrInvalid) {
		t.Fatal(err)
	}
	t.Run("partition isolation", func(t *testing.T) {
		part, ok := s.(blackboard.PartitionStore)
		if !ok {
			t.Fatal("provider has no partition implementation")
		}
		pid := blackboard.ProposalID(blackboard.NewID())
		if err := s.Update(ctx, r.ID, func(r *blackboard.Record) error {
			r.Proposals = append(r.Proposals, blackboard.Proposal{ID: pid, Actor: e.AuthenticatedActor, Status: "pending", Changes: []blackboard.Change{{ID: "selected", Operation: "add", Item: blackboard.Item{Title: "idea"}}}})
			r.Events = append(r.Events, blackboard.Event{ID: blackboard.NewID(), Source: "person", Kind: "final_requested", AuthenticatedActor: r.Owner, RecordedAt: e.RecordedAt})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		core, err := part.LoadPartition(ctx, r.ID, blackboard.Partition{Kind: blackboard.PartitionCore})
		if err != nil || len(core.Events)+len(core.Proposals)+len(core.Revisions)+len(core.Snapshots) != 0 {
			t.Fatalf("history leaked into header: %+v %v", core, err)
		}
		plan, err := part.LoadPartition(ctx, r.ID, blackboard.Partition{Kind: blackboard.PartitionPlan})
		if err != nil || len(plan.Proposals) != 0 || len(plan.Events) != 1 || plan.Events[0].Source != "person" {
			t.Fatalf("unrelated agent rows in lifecycle view: %+v %v", plan, err)
		}
		selected, err := part.LoadPartition(ctx, r.ID, blackboard.Partition{Kind: blackboard.PartitionPlan, ProposalID: string(pid)})
		if err != nil || len(selected.Proposals) != 1 || selected.Proposals[0].ID != pid {
			t.Fatalf("selected proposal missing: %+v %v", selected, err)
		}
		if err := part.UpdatePartition(ctx, r.ID, blackboard.Partition{Kind: blackboard.PartitionEvent, ActorKind: "agent"}, func(r *blackboard.Record) error { r.Name = "agent changed plan"; return nil }); !errors.Is(err, blackboard.ErrImmutable) {
			t.Fatalf("agent partition changed header: %v", err)
		}
	})
}

// PartitionAdmissionContract exercises provider admission independently of the
// service's early refusal, so the SQL and memory boundaries cannot silently drift.
func PartitionAdmissionContract(t *testing.T, s blackboard.Store) {
	t.Helper()
	ps, ok := s.(blackboard.PartitionStore)
	if !ok {
		t.Fatal("provider has no partition implementation")
	}
	ctx := context.Background()
	actor := blackboard.Actor{ID: "worker", Kind: "agent"}
	for _, kind := range []string{"count", "bytes"} {
		t.Run(kind, func(t *testing.T) {
			r := blackboard.Record{ID: blackboard.NewID(), Owner: blackboard.Actor{ID: "owner", Kind: "human"}, Version: 1}
			if err := s.Create(ctx, r); err != nil {
				t.Fatal(err)
			}
			count := blackboard.MaxPendingAgentProposals
			size := 10
			if kind == "bytes" {
				count = 1
				size = blackboard.MaxPendingAgentProposalBytes
			}
			for n := 0; n < count; n++ {
				err := ps.UpdatePartition(ctx, r.ID, blackboard.Partition{Kind: blackboard.PartitionProposal, ActorKind: "agent", ActorID: actor.ID}, func(r *blackboard.Record) error {
					r.Proposals = append(r.Proposals, blackboard.Proposal{ID: blackboard.ProposalID(blackboard.NewID()), Status: "pending", Actor: actor, Changes: []blackboard.Change{{Item: blackboard.Item{Title: strings.Repeat("x", size)}}}})
					return nil
				})
				if kind == "bytes" {
					if !errors.Is(err, blackboard.ErrInvalid) {
						t.Fatalf("provider byte admission: %v", err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			}
			if kind == "count" {
				err := ps.UpdatePartition(ctx, r.ID, blackboard.Partition{Kind: blackboard.PartitionProposal, ActorKind: "agent", ActorID: actor.ID}, func(r *blackboard.Record) error {
					r.Proposals = append(r.Proposals, blackboard.Proposal{ID: blackboard.ProposalID(blackboard.NewID()), Status: "pending", Actor: actor})
					return nil
				})
				if !errors.Is(err, blackboard.ErrInvalid) {
					t.Fatalf("provider count admission: %v", err)
				}
			}
		})
	}
}

// Aggregate admission survives worker churn in both implementations.
func WorkspaceAdmissionContract(t *testing.T, s blackboard.Store) {
	t.Helper()
	ctx := context.Background()
	r := blackboard.Record{ID: blackboard.NewID(), WorkspaceID: "workspace", Owner: blackboard.Actor{ID: "owner", Kind: "human"}, Version: 1}
	if e := s.Create(ctx, r); e != nil {
		t.Fatal(e)
	}
	service := blackboard.Service{Store: s, BlackboardID: r.ID}
	changes := make([]blackboard.Change, 80)
	for i := range changes {
		changes[i] = blackboard.Change{Operation: "add", Item: blackboard.Item{Title: strings.Repeat("t", 200), Reason: strings.Repeat("r", 1000)}}
	}
	accepted := 0
	for i := 0; i < blackboard.MaxWorkspacePendingProposals; i++ {
		_, err := service.Propose(ctx, blackboard.Actor{ID: fmt.Sprintf("worker-%d", i), Kind: "agent"}, "", append([]blackboard.Change(nil), changes...))
		if errors.Is(err, blackboard.ErrInvalid) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		accepted++
	}
	if accepted == 0 || accepted >= blackboard.MaxWorkspacePendingProposals {
		t.Fatalf("aggregate byte cap did not bind: %d", accepted)
	}
	other := blackboard.Record{ID: blackboard.NewID(), WorkspaceID: r.WorkspaceID, Owner: r.Owner, Version: 1}
	if err := s.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := (blackboard.Service{Store: s, BlackboardID: other.ID}).Propose(ctx, blackboard.Actor{ID: "new-worker", Kind: "agent"}, "", append([]blackboard.Change(nil), changes...)); !errors.Is(err, blackboard.ErrInvalid) {
		t.Fatalf("new blackboard reset workspace proposal cap: %v", err)
	}
	if err := service.Apply(ctx, blackboard.Person{Actor: r.Owner, LoginID: "login"}, "", 0, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "person"}}}); err != nil {
		t.Fatalf("person spent aggregate capacity: %v", err)
	}
}

func WorkspaceEventAdmissionContract(t *testing.T, s blackboard.Store) {
	t.Helper()
	ctx := context.Background()
	r := blackboard.Record{ID: blackboard.NewID(), WorkspaceID: "event-workspace", Owner: blackboard.Actor{ID: "owner", Kind: "human"}, Version: 1}
	for i := 0; i < 5; i++ {
		r.Events = append(r.Events, blackboard.Event{ID: blackboard.NewID(), Source: "agent", Kind: "decision", AuthenticatedActor: blackboard.Actor{ID: fmt.Sprint(i), Kind: "agent"}, Payload: json.RawMessage(`{"text":"` + strings.Repeat("x", blackboard.MaxWorkspaceEventBytes/5+1024) + `"}`)})
	}
	if e := s.Create(ctx, r); e != nil {
		t.Fatal(e)
	}
	other := blackboard.Record{ID: blackboard.NewID(), WorkspaceID: r.WorkspaceID, Owner: r.Owner, Version: 1}
	if e := s.Create(ctx, other); e != nil {
		t.Fatal(e)
	}
	if _, e := (blackboard.Service{Store: s, BlackboardID: other.ID}).RecordEvent(ctx, blackboard.Actor{ID: "new-worker", Kind: "agent"}, blackboard.EventInput{Kind: "decision"}); !errors.Is(e, blackboard.ErrInvalid) {
		t.Fatalf("new blackboard reset workspace event cap: %v", e)
	}
}

func LinkedWorkspaceAdmissionContract(t *testing.T, s blackboard.Store) {
	t.Helper()
	ctx := context.Background()
	workspace := blackboard.NewID()
	r := blackboard.Record{ID: blackboard.NewID(), Owner: blackboard.Actor{ID: "owner", Kind: "human"}, Version: 1, Links: []blackboard.Link{{WorkspaceID: workspace}}}
	for range blackboard.MaxWorkspacePendingProposals {
		r.Proposals = append(r.Proposals, blackboard.Proposal{ID: blackboard.ProposalID(blackboard.NewID()), Actor: blackboard.Actor{ID: "worker", Kind: "agent"}, Status: "pending"})
	}
	if err := s.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	other := blackboard.Record{ID: blackboard.NewID(), Owner: r.Owner, Version: 1, WorkspaceID: workspace}
	if err := s.Create(ctx, other); err != nil {
		t.Fatal(err)
	}
	if _, err := (blackboard.Service{Store: s, BlackboardID: other.ID}).Propose(ctx, blackboard.Actor{ID: "new-worker", Kind: "agent"}, "", []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "idea"}}}); !errors.Is(err, blackboard.ErrInvalid) {
		t.Fatalf("linked workspace reset aggregate quota: %v", err)
	}
}
