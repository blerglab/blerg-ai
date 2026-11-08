package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard/storetest"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestBlackboardStoreContract(t *testing.T) {
	pool := connect(t)
	s := db.BlackboardStore{Pool: pool}
	storetest.StoreContract(t, s)
	ctx := context.Background()
	r := blackboard.Record{ID: blackboard.NewID(), Owner: blackboard.Actor{ID: "owner", Kind: "human"}, Version: 1}
	if err := s.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, r.ID, func(r *blackboard.Record) error {
		r.Events = append(r.Events, blackboard.Event{ID: blackboard.NewID(), Kind: "assertion", Source: "agent"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE plan_events SET kind='plan_set' WHERE blackboard_id=$1`, r.ID); err == nil {
		t.Fatal("SQL history mutation accepted")
	}
	var refs int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.constraint_column_usage WHERE table_name='sessions' AND constraint_name IN (SELECT constraint_name FROM information_schema.table_constraints WHERE table_name='blackboards')`).Scan(&refs); err != nil || refs != 0 {
		t.Fatalf("session cascade: %d %v", refs, err)
	}
	got, err := s.Load(ctx, r.ID)
	if err != nil || got.ParentID != nil || len(got.Events) != 1 {
		t.Fatalf("round trip: %+v %v", got, err)
	}
}

func TestBlackboardPlainPlanAndJSONBHistory(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	store := db.BlackboardStore{Pool: pool}
	owner := blackboard.Person{Actor: blackboard.Actor{ID: "owner", Kind: "human"}, LoginID: "login"}
	r := blackboard.Record{ID: blackboard.NewID(), Owner: owner.Actor, Version: 1, WorkspaceID: "workspace"}
	if err := store.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	s := blackboard.Service{Store: store, BlackboardID: r.ID}
	for revision := 0; revision < 2; revision++ {
		if err := s.Apply(ctx, owner, "", revision, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "Try an idea"}}}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.Load(ctx, r.ID)
	if err != nil || len(got.Items) != 2 || got.PlanRevision != 2 || len(got.Milestones) != 0 || len(got.Links) != 0 {
		t.Fatalf("plain plan %+v %v", got, err)
	}
}

func TestBlackboardOptionalMigrationUpgradesNumberedPlan(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	var nullable string
	if err := pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='plan_items' AND column_name='card_number'`).Scan(&nullable); err != nil || nullable != "YES" {
		t.Fatalf("card remains required: %s %v", nullable, err)
	}
	var scope bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='plan_revisions' AND column_name='scope_id')`).Scan(&scope); err != nil || !scope {
		t.Fatalf("missing project scope %v", err)
	}
}

func TestBlackboardChildIdentityCannotCrossRecords(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	s := db.BlackboardStore{Pool: pool}
	r := blackboard.Record{ID: blackboard.NewID(), Owner: blackboard.Actor{ID: "one", Kind: "human"}, Version: 1, Goals: []blackboard.Goal{{ID: blackboard.NewID(), Owner: blackboard.Actor{ID: "one", Kind: "human"}, Name: "Original", Version: 1}}}
	if err := s.Create(ctx, r); err != nil {
		t.Fatal(err)
	}
	second := blackboard.Clone(r)
	second.ID = blackboard.NewID()
	second.Owner.ID = "two"
	second.Goals[0].Name = "Overwrite"
	if err := s.Create(ctx, second); !errors.Is(err, blackboard.ErrConflict) {
		t.Fatalf("cross-record identity: %v", err)
	}
	if _, err := s.Load(ctx, second.ID); !errors.Is(err, blackboard.ErrNotFound) {
		t.Fatal("partial foreign record persisted")
	}
}

func TestBlackboardMigrationPreservesExistingRevision(t *testing.T) {
	pool := unmigratedPool(t)
	ctx := context.Background()
	original, err := os.ReadFile("migrations/132_blackboard.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(original)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO blackboards VALUES('b','human','owner',NULL,'{}',1,'{}');INSERT INTO milestones VALUES('m','b','{}','board','release',NULL,'Opened',NULL,1,'{}');INSERT INTO plan_revisions VALUES('b','m',1,'{"revision":1}')`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/133_blackboard_optional_levels.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var scope, payload string
	if err = pool.QueryRow(ctx, `SELECT scope_id,data::text FROM plan_revisions WHERE blackboard_id='b' AND revision=1`).Scan(&scope, &payload); err != nil || scope != "m" || payload != `{"revision": 1}` {
		t.Fatalf("revision changed: %s %s %v", scope, payload, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE plan_revisions SET data='{}' WHERE blackboard_id='b'`); err == nil {
		t.Fatal("append-only trigger was not restored")
	}
}

func TestBlackboardReviewEventScopeAndSnapshotTriggers(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	store := db.BlackboardStore{Pool: pool}
	owner := blackboard.Actor{ID: "owner", Kind: "human"}
	ids := []string{blackboard.NewID(), blackboard.NewID()}
	for _, id := range ids {
		r := blackboard.Record{ID: id, Owner: owner, Version: 1, Milestones: []blackboard.Milestone{{ID: blackboard.NewID(), Owner: owner, Version: 1}}}
		if err := store.Create(ctx, r); err != nil {
			t.Fatal(err)
		}
		service := blackboard.Service{Store: store, BlackboardID: id}
		if _, err := service.RecordEvent(ctx, owner, blackboard.EventInput{ID: "same-external-id", Kind: "decision"}); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM plan_events WHERE blackboard_id=ANY($1) AND id='same-external-id'`, ids).Scan(&count); err != nil || count != 2 {
		t.Fatalf("event identity collision %d %v", count, err)
	}
	r, _ := store.Load(ctx, ids[0])
	mid := r.Milestones[0].ID
	if err := store.Update(ctx, r.ID, func(r *blackboard.Record) error {
		r.Snapshots = append(r.Snapshots, blackboard.Snapshot{MilestoneID: mid, Day: "2027-01-01", ObservedAt: time.Date(2027, 1, 1, 1, 0, 0, 0, time.UTC), Complete: false, Counts: map[string]int{}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, r.ID, func(r *blackboard.Record) error {
		r.Snapshots[0].Complete = true
		r.Snapshots[0].ObservedAt = r.Snapshots[0].ObservedAt.Add(time.Hour)
		return nil
	}); err != nil {
		t.Fatalf("repair %v", err)
	}
	for _, query := range []string{
		`UPDATE milestone_snapshots SET data='{}' WHERE blackboard_id=$1`,
		`UPDATE milestone_snapshots SET day=day+1 WHERE blackboard_id=$1`,
		`DELETE FROM milestone_snapshots WHERE blackboard_id=$1`,
		`DELETE FROM plan_events WHERE blackboard_id=$1`,
	} {
		if _, err := pool.Exec(ctx, query, r.ID); err == nil {
			t.Fatalf("history trigger allowed %s", query)
		}
	}
	// Prove unrelated writes don't touch existing relational rows. A rejecting SQL
	// trigger makes rewriting even identical proposal rows observable.
	service := blackboard.Service{Store: store, BlackboardID: r.ID}
	if err := service.Apply(ctx, blackboard.Person{Actor: owner, LoginID: "login"}, "", 0, []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "Approved"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, r.ID, func(r *blackboard.Record) error { r.Revisions[0].Items[0].Title = "Rewrite history"; return nil }); !errors.Is(err, blackboard.ErrImmutable) {
		t.Fatalf("SQL record revision guard: %v", err)
	}
	if _, err := service.Propose(ctx, owner, "", []blackboard.Change{{Operation: "add", Item: blackboard.Item{Title: "Idea"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TRIGGER proposals_no_rewrite BEFORE UPDATE ON plan_proposals FOR EACH ROW EXECUTE FUNCTION blackboard_history_immutable()`); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(ctx, r.ID, func(r *blackboard.Record) error { r.Name = "Rename"; return nil }); err != nil {
		t.Fatalf("unrelated write re-upserted history: %v", err)
	}
}

func TestBlackboardReviewMigrationRecoversCollidingEvents(t *testing.T) {
	pool := unmigratedPool(t)
	ctx := context.Background()
	for _, file := range []string{"132_blackboard.sql", "133_blackboard_optional_levels.sql"} {
		data, err := os.ReadFile("migrations/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = pool.Exec(ctx, string(data)); err != nil {
			t.Fatal(err)
		}
	}
	owner := blackboard.Actor{ID: "owner", Kind: "human"}
	event := blackboard.Event{ID: "external", AuthenticatedActor: owner, RecordedAt: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), Kind: "decision", Source: "person"}
	rawEvent, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		record := blackboard.Record{ID: id, Owner: owner, Version: 1, Events: []blackboard.Event{event}}
		data, e := json.Marshal(record)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, `INSERT INTO blackboards VALUES($1,'human','owner',NULL,'{}',1,$2)`, id, data); e != nil {
			t.Fatal(e)
		}
	}
	if _, err = pool.Exec(ctx, `INSERT INTO plan_events VALUES('external','one','','{}','2027-01-01',NULL,'person','decision','{}',$1)`, rawEvent); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("migrations/134_blackboard_review.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(data)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM plan_events WHERE id='external'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("lost colliding event: %d %v", count, err)
	}
}
