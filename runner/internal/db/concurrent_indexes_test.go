package db_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/topics"
)

// The rename rows' index is built without the migration transaction (which would hold agent_events
// writes for the build, the old runner's included during a rolling update): it appears valid after
// EnsureConcurrentIndexes, a second run is a no-op, and an invalid one (a failed build) is rebuilt.
func TestConcurrentIndexesAreBuiltAndRepaired(t *testing.T) {
	ctx := context.Background()
	pool := migratedPool(t, 2)
	state := func() (exists, valid bool) {
		t.Helper()
		err := pool.QueryRow(ctx, `SELECT true, i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
			WHERE c.relname = 'agent_events_session_renamed_idx' AND c.relnamespace = current_schema()::regnamespace`).Scan(&exists, &valid)
		if err != nil && err.Error() != "no rows in result set" {
			t.Fatal(err)
		}
		return exists, valid
	}
	if exists, _ := state(); exists {
		t.Fatal("a migration still builds the index inside the migration transaction")
	}
	for range 2 {
		if err := db.EnsureConcurrentIndexes(ctx, pool); err != nil {
			t.Fatal(err)
		}
		if exists, valid := state(); !exists || !valid {
			t.Fatalf("after a build: exists %v valid %v", exists, valid)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE pg_index SET indisvalid = false WHERE indexrelid = 'agent_events_session_renamed_idx'::regclass`); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureConcurrentIndexes(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if exists, valid := state(); !exists || !valid {
		t.Fatalf("an invalid index was not rebuilt: exists %v valid %v", exists, valid)
	}
	// The History read works with the index in place.
	if _, err := db.ListSessionRenamedEvents(ctx, pool, "00000000-0000-4000-8000-000000000001", 10); err != nil {
		t.Fatal(err)
	}
}

// Review N2: one runner builds or repairs at a time, under a session advisory lock on a connection of
// its own. While another holds it, a pass touches nothing (an index being built is invalid while it
// builds, and must never be dropped as a failed one) and says it is busy; concurrent builders all
// converge on one valid index.
func TestConcurrentIndexBuildersTakeTurns(t *testing.T) {
	ctx := context.Background()
	pool := migratedPool(t, 8)
	if err := db.EnsureConcurrentIndexes(ctx, pool); err != nil {
		t.Fatal(err)
	}
	oid := func() (o uint32) {
		t.Helper()
		if err := pool.QueryRow(ctx, `SELECT 'agent_events_session_renamed_idx'::regclass::oid`).Scan(&o); err != nil {
			t.Fatal(err)
		}
		return o
	}
	// Another runner holds the lock while its build is in progress: the index reads as invalid.
	if _, err := pool.Exec(ctx, `UPDATE pg_index SET indisvalid = false WHERE indexrelid = 'agent_events_session_renamed_idx'::regclass`); err != nil {
		t.Fatal(err)
	}
	before := oid()
	holder, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_lock(hashtext('blerg_runner_concurrent_indexes'))`); err != nil {
		t.Fatal(err)
	}
	busyCtx, cancel := context.WithTimeout(ctx, 20*time.Second) // a pass that does not skip would wait on the holder
	err = db.EnsureConcurrentIndexes(busyCtx, pool)
	cancel()
	if !errors.Is(err, db.ErrConcurrentIndexesBusy) {
		t.Fatalf("while another runner builds: %v", err)
	}
	var valid bool
	if err := pool.QueryRow(ctx, `SELECT indisvalid FROM pg_index WHERE indexrelid = $1`, before).Scan(&valid); err != nil {
		t.Fatalf("the other runner's index was dropped: %v", err)
	}
	if valid {
		t.Fatal("the other runner's index was touched")
	}
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_unlock(hashtext('blerg_runner_concurrent_indexes'))`); err != nil {
		t.Fatal(err)
	}
	holder.Release()

	// Four runners at once: each retries while busy; all finish and one valid index is left.
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() {
			for i := 0; i < 400; i++ {
				err := db.EnsureConcurrentIndexes(ctx, pool)
				if !errors.Is(err, db.ErrConcurrentIndexesBusy) {
					errs <- err
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			errs <- errors.New("never got the lock")
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = 'agent_events_session_renamed_idx' AND i.indisvalid`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("valid indexes after concurrent builders: %d %v", n, err)
	}
	// The lock was given back: a later pass gets it.
	if err := db.EnsureConcurrentIndexes(ctx, pool); err != nil {
		t.Fatalf("after: %v", err)
	}
}

// The History feed's strokes index is built the same way, and the feed's reads work over it.
func TestStrokesIndexIsBuilt(t *testing.T) {
	ctx := context.Background()
	pool := migratedPool(t, 2)
	if err := db.EnsureConcurrentIndexes(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var valid bool
	if err := pool.QueryRow(ctx, `SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = 'agent_events_strokes_idx' AND c.relnamespace = current_schema()::regnamespace`).Scan(&valid); err != nil || !valid {
		t.Fatalf("agent_events_strokes_idx: valid %v, %v", valid, err)
	}
	if _, err := db.ListStrokeEvents(ctx, pool, "00000000-0000-4000-8000-000000000001", 10); err != nil {
		t.Fatal(err)
	}
	if closed, _, open, err := db.AskStates(ctx, pool, "00000000-0000-4000-8000-000000000001"); err != nil || len(closed) != 0 || open != 0 {
		t.Fatalf("AskStates = %v %d %v", closed, open, err)
	}
}

// An index whose kind list changed is built again: the old definition's predicate no longer covers
// the read, so leaving it would leave the read unindexed without a word.
func TestConcurrentIndexWithAnOlderKindListIsRebuilt(t *testing.T) {
	ctx := context.Background()
	pool := migratedPool(t, 2)
	if _, err := pool.Exec(ctx, `CREATE INDEX agent_events_strokes_idx ON agent_events (session_id, seq) WHERE kind IN ('mark', 'ask')`); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureConcurrentIndexes(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var def string
	if err := pool.QueryRow(ctx, `SELECT pg_get_indexdef('agent_events_strokes_idx'::regclass)`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	for _, k := range topics.StrokeKinds {
		if !strings.Contains(def, "'"+k+"'") {
			t.Errorf("rebuilt index lacks %q: %s", k, def)
		}
	}
}

// topics.StrokeKinds and db.StrokeKindsSQL are the same list.
func TestStrokeKindsMatchTheIndex(t *testing.T) {
	var quoted []string
	for _, k := range topics.StrokeKinds {
		quoted = append(quoted, "'"+k+"'")
	}
	if want := "(" + strings.Join(quoted, ", ") + ")"; db.StrokeKindsSQL != want {
		t.Errorf("StrokeKindsSQL = %s, topics.StrokeKinds = %s", db.StrokeKindsSQL, want)
	}
}

// The right predicate on the wrong columns is still an obsolete definition (#121).
func TestConcurrentIndexWithWrongColumnsIsRebuilt(t *testing.T) {
	ctx := context.Background()
	pool := migratedPool(t, 2)
	if _, err := pool.Exec(ctx, `CREATE INDEX agent_events_strokes_idx ON agent_events (seq, session_id) WHERE kind IN `+db.StrokeKindsSQL); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureConcurrentIndexes(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var def string
	if err := pool.QueryRow(ctx, `SELECT pg_get_indexdef('agent_events_strokes_idx'::regclass)`).Scan(&def); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(def, "(session_id, seq)") {
		t.Fatalf("wrong index columns survived: %s", def)
	}
}
