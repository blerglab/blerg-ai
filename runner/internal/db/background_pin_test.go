package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestPinnedBackgroundReconcileRefusesStaleGenerationAndRevision(t *testing.T) {
	for _, rotate := range []string{"generation", "revision"} {
		t.Run(rotate, func(t *testing.T) {
			pool := reelTestPool(t)
			sid := reelSession(t, pool)
			const daemon = "0d0d0d0d-0d0d-4d0d-8d0d-0d0d0d0d0d0e"
			const gen = "00000000-0000-4000-8000-00000000ab01"
			const next = "00000000-0000-4000-8000-00000000ab02"
			_, err := pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id='owner',ownership_revision=3,connection_generation=$2 WHERE id=$1`, daemon, gen)
			if err != nil {
				t.Fatal(err)
			}
			_, err = pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id='owner' WHERE id=$1`, sid)
			if err != nil {
				t.Fatal(err)
			}
			_, _, _, err = db.AppendBackgroundTaskEventAt(t.Context(), pool, sid, "00000000-0000-4000-8000-00000000ab03", "background_task_started", `{"task_id":"task","daemon_lifetime":"old"}`, time.Time{})
			if err != nil {
				t.Fatal(err)
			}
			pin := db.DaemonAuthority{DaemonID: daemon, OwnerID: "owner", Revision: 3, Generation: gen}
			var changes []db.BackgroundTaskReconciliation
			changes, err = db.ReconcileBackgroundTasksPinned(t.Context(), pool, pin, "", "old", map[string][]string{})
			if err != nil || len(changes) != 0 {
				t.Fatalf("current pin rejected: %v %v", changes, err)
			}
			if rotate == "generation" {
				_, err = pool.Exec(t.Context(), `UPDATE daemons SET connection_generation=$2 WHERE id=$1`, daemon, next)
			} else {
				_, err = pool.Exec(t.Context(), `UPDATE daemons SET ownership_revision=ownership_revision+1 WHERE id=$1`, daemon)
			}
			if err != nil {
				t.Fatal(err)
			}
			changes, err = db.ReconcileBackgroundTasksPinned(t.Context(), pool, pin, "", "new", map[string][]string{})
			if !errors.Is(err, db.ErrDesktopAuthority) || len(changes) != 0 {
				t.Fatalf("stale pin applied: %v %v", changes, err)
			}
			row, err := db.GetSession(t.Context(), pool, sid)
			if err != nil || row.BackgroundTasks != 1 {
				t.Fatalf("stale pin changed session: %+v %v", row, err)
			}
		})
	}
}

func TestPinnedBackgroundSkipsLockedSessionThenConverges(t *testing.T) {
	pool := reelTestPool(t)
	sid := reelSession(t, pool)
	const daemon = "0d0d0d0d-0d0d-4d0d-8d0d-0d0d0d0d0d0e"
	const gen = "00000000-0000-4000-8000-00000000ab01"
	if _, err := pool.Exec(t.Context(), `UPDATE daemons SET desktop_owner_id='owner',ownership_revision=3,connection_generation=$2 WHERE id=$1`, daemon, gen); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id='owner' WHERE id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := db.AppendBackgroundTaskEventAt(t.Context(), pool, sid, "00000000-0000-4000-8000-00000000ab03", "background_task_started", `{"task_id":"task","daemon_lifetime":"old"}`, time.Time{}); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(t.Context(), `SELECT id FROM sessions WHERE id=$1 FOR UPDATE`, sid); err != nil {
		t.Fatal(err)
	}
	pin := db.DaemonAuthority{DaemonID: daemon, OwnerID: "owner", Revision: 3, Generation: gen}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	changes, err := db.ReconcileBackgroundTasksPinned(ctx, pool, pin, "", "new", map[string][]string{})
	if err != nil || len(changes) != 0 {
		t.Fatalf("locked row not skipped: %v %v", changes, err)
	}
	if err = tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	changes, err = db.ReconcileBackgroundTasksPinned(t.Context(), pool, pin, "", "new", map[string][]string{})
	if err != nil || len(changes) != 1 || changes[0].Count != 0 {
		t.Fatalf("row did not converge: %v %v", changes, err)
	}
}
