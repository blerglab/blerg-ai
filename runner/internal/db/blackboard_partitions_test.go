package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func partitionFixture(t *testing.T) (db.BlackboardStore, blackboard.Service, blackboard.Person, string) {
	t.Helper()
	pool := connect(t)
	store := db.BlackboardStore{Pool: pool}
	p := blackboard.Person{Actor: blackboard.Actor{ID: "owner", Kind: "human"}, LoginID: "login"}
	mid := blackboard.NewID()
	id := blackboard.NewID()
	if err := store.Create(context.Background(), blackboard.Record{ID: id, Owner: p.Actor, Version: 1, Milestones: []blackboard.Milestone{{ID: mid, Owner: p.Actor, Version: 1}}}); err != nil {
		t.Fatal(err)
	}
	return store, blackboard.Service{Store: store, BlackboardID: id}, p, mid
}
func TestBlackboardPartitionsIsolateLegacyAgentHistory(t *testing.T) {
	store, s, p, mid := partitionFixture(t)
	ctx := context.Background()
	agent := blackboard.Actor{ID: "agent", Kind: "agent"}
	// Trusted upgrade fixture: an already overfull pending partition must remain
	// recoverable. Its history is deliberately larger than the old JSON limit.
	bulk := make([]blackboard.Change, 80)
	for n := range bulk {
		bulk[n] = blackboard.Change{ID: blackboard.ItemID(fmt.Sprint(n)), Operation: "add", Item: blackboard.Item{Title: strings.Repeat("t", 200), Reason: strings.Repeat("r", 1000)}}
	}
	ids := []blackboard.ProposalID{}
	if err := store.Update(ctx, s.BlackboardID, func(r *blackboard.Record) error {
		r.Milestones[0].Stage = "Finalizing"
		r.Milestones[0].Frozen = json.RawMessage(`{"cards":[],"evidence":"` + strings.Repeat("x", blackboard.MaxRecordBytes+1024) + `"}`)
		for n := 0; n < 165; n++ {
			id := blackboard.ProposalID(blackboard.NewID())
			ids = append(ids, id)
			r.Proposals = append(r.Proposals, blackboard.Proposal{ID: id, MilestoneID: mid, Actor: agent, Status: "pending", Revision: 0, Changes: bulk})
		}
		r.Events = append(r.Events, blackboard.Event{ID: blackboard.NewID(), MilestoneID: mid, Kind: "decision", Source: "agent", AuthenticatedActor: agent, Payload: json.RawMessage(`{"text":"old"}`)})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var headerBytes int
	var headerHasHistory bool
	if err := store.Pool.QueryRow(ctx, `SELECT octet_length(record::text),record ?| ARRAY['events','proposals','revisions','snapshots'] FROM blackboards WHERE id=$1`, s.BlackboardID).Scan(&headerBytes, &headerHasHistory); err != nil || headerHasHistory || headerBytes > 4096 {
		t.Fatalf("aggregate header bytes=%d history=%v err=%v", headerBytes, headerHasHistory, err)
	}
	for _, op := range []struct {
		name string
		fn   func(context.Context) error
	}{
		{"legacy lock clear", func(c context.Context) error { return seedDBLegacyCut(c, s, mid, true) }},
		{"commit", func(c context.Context) error { return s.Decide(c, p, mid, ids[0], 0, []blackboard.ItemID{"0"}) }},
		{"reject", func(c context.Context) error { return s.Decide(c, p, mid, ids[1], 0, nil) }},
		{"Apply", func(c context.Context) error {
			return s.Apply(c, p, mid, 1, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "person"}}})
		}},
		{"legacy snapshot", func(c context.Context) error { return seedDBLegacyCut(c, s, mid, false) }},
	} {
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		err := op.fn(c)
		cancel()
		if err != nil {
			t.Fatalf("%s blocked by agent history: %v", op.name, err)
		}
	}
	got, err := store.Load(ctx, s.BlackboardID)
	if err != nil || len(got.Proposals) != 166 || got.Proposals[0].Status != "decided" || got.Proposals[1].Status != "rejected" || got.Milestones[0].FrozenAt == nil {
		t.Fatalf("recovery failed: %v", err)
	}
	usage, err := store.LoadPartition(ctx, s.BlackboardID, blackboard.Partition{Kind: blackboard.PartitionEvent, MilestoneID: mid, ActorID: "agent"})
	if err != nil || usage.Usage.Events != 1 {
		t.Fatalf("person lifecycle charged to agent partition: %+v %v", usage.Usage, err)
	}
	view, err := store.LoadPartition(ctx, s.BlackboardID, blackboard.Partition{Kind: blackboard.PartitionPlan, MilestoneID: mid})
	if err != nil || len(view.Proposals) != 0 || len(view.Events) != 0 {
		t.Fatalf("lifecycle view contains agent history: proposals=%d events=%d err=%v", len(view.Proposals), len(view.Events), err)
	}
}

type partitionBoard struct{}

func (partitionBoard) Call(_ context.Context, tool string, _ map[string]any, out any) error {
	col, release := "work", "release"
	card := blackboard.Card{BoardID: "board", Number: 1, ColumnID: &col, ReleaseID: &release}
	var v any
	switch tool {
	case "blerg_column_list":
		v = []blackboard.Column{{ID: col, Name: "In progress"}}
	case "blerg_card_search":
		v = blackboard.CardPage{Cards: []blackboard.Card{card}}
	case "blerg_card_get":
		v = card
	default:
		return blackboard.ErrInvalid
	}
	data, _ := json.Marshal(v)
	return json.Unmarshal(data, out)
}
func TestBlackboardUnchangedSliceTakesNoWriteLocks(t *testing.T) {
	store, s, _, mid := partitionFixture(t)
	ctx := context.Background()
	if err := store.Update(ctx, s.BlackboardID, func(r *blackboard.Record) error {
		r.Milestones[0].BoardID = "board"
		r.Milestones[0].ReleaseID = "release"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.Board = partitionBoard{}
	if _, err := s.Slice(ctx, mid); err != nil {
		t.Fatal(err)
	}
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, q := range []string{`SELECT id FROM blackboards WHERE id=$1 FOR UPDATE`, `SELECT id FROM milestones WHERE blackboard_id=$1 FOR UPDATE`, `SELECT kind FROM blackboard_partition_budgets WHERE blackboard_id=$1 FOR UPDATE`} {
		rows, err := tx.Query(ctx, q, s.BlackboardID)
		if err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	v, err := s.Slice(readCtx, mid)
	if err != nil || readCtx.Err() != nil || !v.Complete || len(v.Cards) != 1 {
		t.Fatalf("unchanged read attempted a lock/write: %+v %v context=%v", v, err, readCtx.Err())
	}
}
func TestBlackboardAgentRowsDoNotUpdatePlanHeader(t *testing.T) {
	store, s, _, mid := partitionFixture(t)
	ctx := context.Background()
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT id FROM blackboards WHERE id=$1 FOR NO KEY UPDATE`, s.BlackboardID); err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	agent := blackboard.Actor{ID: "agent", Kind: "agent"}
	if _, err = s.Propose(c, agent, mid, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "idea"}}}); err != nil {
		t.Fatalf("proposal touched header: %v", err)
	}
	if _, err = s.RecordEvent(c, agent, blackboard.EventInput{MilestoneID: mid, Kind: "decision"}); err != nil {
		t.Fatalf("event touched header: %v", err)
	}
}

func TestBlackboardPartitionMigrationPreservesRowsAndOrder(t *testing.T) {
	pool := unmigratedPool(t)
	ctx := context.Background()
	for _, file := range []string{"132_blackboard.sql", "133_blackboard_optional_levels.sql", "134_blackboard_review.sql"} {
		data, err := os.ReadFile("migrations/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	actor := blackboard.Actor{ID: "owner", Kind: "human"}
	mid := "milestone"
	item := blackboard.Item{BoardID: "board", CardNumber: 1, Reason: "before"}
	payload, _ := json.Marshal(item)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first := blackboard.Event{ID: "z", MilestoneID: mid, Source: "board", Kind: "planned", AuthenticatedActor: actor, RecordedAt: now, Payload: payload}
	item.Reason = "after"
	payload, _ = json.Marshal(item)
	last := first
	last.ID = "a"
	last.Payload = payload
	q1 := blackboard.Proposal{ID: "x", MilestoneID: mid, Actor: blackboard.Actor{ID: "worker", Kind: "agent"}, Status: "pending"}
	q2 := q1
	q2.ID = "y"
	r := blackboard.Record{ID: "legacy", Links: []blackboard.Link{{WorkspaceID: "linked-workspace"}}, Owner: actor, Version: 1, Milestones: []blackboard.Milestone{{ID: mid, Owner: actor, Version: 1, Stage: "Final", FrozenAt: &now, Frozen: json.RawMessage(`{"complete":true,"cards":[]}`)}}, Events: []blackboard.Event{first, last}, Proposals: []blackboard.Proposal{q1, q2},
		Revisions: []blackboard.Revision{{MilestoneID: mid, Number: 1, EventID: "revision", Items: []blackboard.Item{{ID: "plain", Title: "preserved"}}}},
		Snapshots: []blackboard.Snapshot{{MilestoneID: mid, Day: "2026-01-01", ObservedAt: now, Complete: true, Counts: map[string]int{"done": 2}}}}
	data, _ := json.Marshal(r)
	if _, err := pool.Exec(ctx, `INSERT INTO blackboards VALUES('legacy','human','owner',NULL,'{}',1,$1)`, data); err != nil {
		t.Fatal(err)
	}
	m, _ := json.Marshal(r.Milestones[0])
	owner, _ := json.Marshal(actor)
	if _, err := pool.Exec(ctx, `INSERT INTO milestones VALUES($1,'legacy',$2,NULL,NULL,NULL,'',NULL,1,$3)`, mid, owner, m); err != nil {
		t.Fatal(err)
	}
	for _, event := range []blackboard.Event{last, first} {
		data, _ := json.Marshal(event)
		if _, err := pool.Exec(ctx, `INSERT INTO plan_events VALUES($1,'legacy',$2,$3,$4,NULL,'board','planned',$5,$6)`, event.ID, mid, owner, now, event.Payload, data); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []blackboard.Proposal{q2, q1} {
		data, _ := json.Marshal(q)
		if _, err := pool.Exec(ctx, `INSERT INTO plan_proposals VALUES($1,'legacy',$2,0,'pending',$3)`, q.ID, mid, data); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO plan_revisions(blackboard_id,milestone_id,scope_id,revision,data) VALUES('legacy',$1,$1,1,$2)`, mid, mustJSON(t, r.Revisions[0])); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO milestone_snapshots VALUES('legacy',$1,'2026-01-01',$2,true,$3)`, mid, now, mustJSON(t, r.Snapshots[0])); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/135_blackboard_partitions.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"agent-a", "agent-b"} {
		event := blackboard.Event{ID: id, MilestoneID: mid, Source: "agent", Kind: "decision", AuthenticatedActor: blackboard.Actor{ID: id, Kind: "agent"}, RecordedAt: now, Payload: json.RawMessage(`{}`)}
		if _, err := pool.Exec(ctx, `INSERT INTO plan_events(id,blackboard_id,milestone_id,authenticated_actor,recorded_at,source,kind,payload,data) VALUES($1,'legacy',$2,$3,$4,'agent','decision','{}',$5)`, id, mid, mustJSON(t, event.AuthenticatedActor), now, mustJSON(t, event)); err != nil {
			t.Fatal(err)
		}
	}
	migration, err = os.ReadFile("migrations/136_blackboard_actor_budgets.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	extra, err := os.ReadFile("migrations/137_blackboard_workspace_budgets.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(extra)); err != nil {
		t.Fatal(err)
	}
	linked, err := os.ReadFile("migrations/138_blackboard_linked_workspace_budgets.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(linked)); err != nil {
		t.Fatal(err)
	}
	store := db.BlackboardStore{Pool: pool}
	got, err := store.Load(ctx, "legacy")
	if err != nil || len(got.Events) != 4 || got.Events[0].ID != "z" || got.Events[1].ID != "a" || len(got.Proposals) != 2 || got.Proposals[0].ID != "x" {
		t.Fatalf("migration reordered/lost history: %+v %v", got, err)
	}
	for _, id := range []string{"agent-a", "agent-b"} {
		view, err := store.LoadPartition(ctx, "legacy", blackboard.Partition{Kind: blackboard.PartitionEvent, MilestoneID: mid, ActorKind: "agent", ActorID: id})
		if err != nil || view.Usage.Events != 1 || view.Usage.EventBytes == 0 || view.Usage.WorkspaceEvents != 2 || view.Usage.WorkspaceEventBytes == 0 {
			t.Fatalf("migration lost actor usage: %s %+v %v", id, view.Usage, err)
		}
	}
	if !reflect.DeepEqual(got.Revisions, r.Revisions) || !reflect.DeepEqual(got.Snapshots, r.Snapshots) || got.Milestones[0].FrozenAt == nil || !got.Milestones[0].FrozenAt.Equal(now) {
		t.Fatalf("migration lost revisions/snapshots/frozen: %+v", got)
	}
	var cut map[string]any
	if err := json.Unmarshal(got.Milestones[0].Frozen, &cut); err != nil || cut["complete"] != true {
		t.Fatalf("frozen evidence lost: %v", err)
	}
	obs, err := store.LoadPartition(ctx, "legacy", blackboard.Partition{Kind: blackboard.PartitionObservation, MilestoneID: mid})
	if err != nil || len(obs.Events) != 1 || obs.Events[0].ID != "a" || obs.Usage.Events != 2 {
		t.Fatalf("latest observation/quota lost: %+v %v", obs, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE plan_events SET kind='changed' WHERE blackboard_id='legacy'`); err == nil {
		t.Fatal("migration dropped append-only trigger")
	}
}

func TestBlackboardOnlyPendingAgentRowsConsumeAdmission(t *testing.T) {
	store, s, p, mid := partitionFixture(t)
	ctx := context.Background()
	actor := blackboard.Actor{ID: "agent", Kind: "agent"}
	if err := store.Update(ctx, s.BlackboardID, func(r *blackboard.Record) error {
		for n := 0; n < 1000; n++ {
			r.Proposals = append(r.Proposals, blackboard.Proposal{ID: blackboard.ProposalID(blackboard.NewID()), Actor: actor, MilestoneID: mid, Status: "rejected"})
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Propose(ctx, actor, mid, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "new proposal"}}}); err != nil {
		t.Fatalf("rejected history spent admission: %v", err)
	}
	if err := s.Apply(ctx, p, mid, 0, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "person"}}}); err != nil {
		t.Fatalf("Apply spent pending admission: %v", err)
	}
}

type partitionAdmissionTrace struct{ entered chan struct{} }

func (t *partitionAdmissionTrace) TraceQueryStart(c context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.Contains(d.SQL, "INSERT INTO blackboard_partition_budgets") {
		select {
		case t.entered <- struct{}{}:
		default:
		}
	}
	return c
}
func (*partitionAdmissionTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func TestBlackboardQueuedAgentDoesNotHoldOffDerivedSnapshot(t *testing.T) {
	store, s, _, mid := partitionFixture(t)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `INSERT INTO blackboard_partition_budgets(blackboard_id,scope_id,kind,actor_id) VALUES($1,$2,'proposal','agent')`, s.BlackboardID, mid); err != nil {
		t.Fatal(err)
	}
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT kind FROM blackboard_partition_budgets WHERE blackboard_id=$1 AND scope_id=$2 AND kind='proposal' FOR UPDATE`, s.BlackboardID, mid); err != nil {
		t.Fatal(err)
	}
	trace := &partitionAdmissionTrace{entered: make(chan struct{}, 1)}
	cfg := store.Pool.Config()
	cfg.ConnConfig.Tracer = trace
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	agentCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() {
		writer := blackboard.Service{Store: db.BlackboardStore{Pool: pool}, BlackboardID: s.BlackboardID}
		_, err := writer.Propose(agentCtx, blackboard.Actor{ID: "agent", Kind: "agent"}, mid, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "queued"}}})
		done <- err
	}()
	guard, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	select {
	case <-trace.entered:
	case <-guard.Done():
		t.Fatal("writer never reached admission")
	}
	c, cutCancel := context.WithTimeout(ctx, 2*time.Second)
	err = seedDBLegacyCut(c, s, mid, false)
	cutCancel()
	if err != nil {
		t.Fatalf("queued agent held milestone against Final: %v", err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if !errors.Is(err, blackboard.ErrConflict) {
			t.Fatalf("queued agent did not yield immediately: %v", err)
		}
	case <-guard.Done():
		t.Fatal("queued writer did not resume")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
