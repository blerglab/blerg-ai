package db_test

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// A session's origin (owner, private, cron) is written in the insert itself, so
// the row is never visible without it.
func TestInsertSessionOrigin(t *testing.T) {
	pool := endTestPool(t)
	ctx := context.Background()
	const daemonID = "00000000-0000-0000-0000-0000000000e0"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "d", "local", "/r"); err != nil {
		t.Fatal(err)
	}
	const cron = "0a0a0a0a-0a0a-4a0a-8a0a-0a0a0a0a0b01"
	origin := db.SessionOrigin{SpawningAccount: "acct-x", Private: true, CronID: cron}

	insert := map[string]func(id string) error{
		"InsertSessionAs": func(id string) error {
			return db.InsertSessionAs(ctx, pool, id, daemonID, "starting", "/r/p", "p", "secret title", "", origin)
		},
		"InsertClusterSessionAs": func(id string) error {
			return db.InsertClusterSessionAs(ctx, pool, id, daemonID, "starting", "/w/p", "p", "secret title", "", origin)
		},
	}
	ids := map[string]string{
		"InsertSessionAs":        "00000000-0000-4000-8000-0000000000c1",
		"InsertClusterSessionAs": "00000000-0000-4000-8000-0000000000c2",
	}
	for name, fn := range insert {
		id := ids[name]
		if err := fn(id); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// The very first read of the row already shows the origin: there is no
		// window in which it is a plain, public, owner-less session.
		row, err := db.GetSession(ctx, pool, id)
		if err != nil || row == nil {
			t.Fatalf("%s: GetSession: %v", name, err)
		}
		if !row.Private || row.SpawningAccountID == nil || *row.SpawningAccountID != "acct-x" || row.CronID == nil || *row.CronID != cron {
			t.Errorf("%s: origin not written with the row: private=%v account=%v cron=%v", name, row.Private, row.SpawningAccountID, row.CronID)
		}
		owners, err := db.ListPrivateSessionOwners(ctx, pool)
		if err != nil || owners[id] != "acct-x" {
			t.Errorf("%s: ListPrivateSessionOwners = %v, %v", name, owners, err)
		}
	}
	if row, _ := db.GetSession(ctx, pool, ids["InsertClusterSessionAs"]); row == nil || row.Runtime == nil || *row.Runtime != "cluster" {
		t.Errorf("cluster insert lost runtime: %+v", row)
	}

	// A revive (a daemon's session_started re-sends the row with no origin)
	// never turns a private session public or erases its owner and cron.
	id := ids["InsertSessionAs"]
	if err := db.InsertSession(ctx, pool, id, daemonID, "running", "/r/p", "p", "secret title", ""); err != nil {
		t.Fatal(err)
	}
	row, _ := db.GetSession(ctx, pool, id)
	if row == nil || !row.Private || row.SpawningAccountID == nil || *row.SpawningAccountID != "acct-x" || row.CronID == nil {
		t.Errorf("a revive erased the origin: %+v", row)
	}

	// The zero origin is an ordinary session.
	plain := "00000000-0000-4000-8000-0000000000c3"
	if err := db.InsertSessionAs(ctx, pool, plain, daemonID, "starting", "/r/p", "p", "t", "", db.SessionOrigin{}); err != nil {
		t.Fatal(err)
	}
	if row, _ := db.GetSession(ctx, pool, plain); row == nil || row.Private || row.SpawningAccountID != nil || row.CronID != nil {
		t.Errorf("zero origin is not an ordinary session: %+v", row)
	}
}
