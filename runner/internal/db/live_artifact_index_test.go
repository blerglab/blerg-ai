package db_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// The live-file unique index is built outside the migration transaction (which would hold
// session_artifacts writes, the old runner's included during a rolling update, for the whole
// build): it does not exist right after migrations, EnsureLiveArtifactIndex builds it, a second
// run is a no-op, and an invalid one (a failed build) is rebuilt.
func TestLiveArtifactIndexIsBuiltAndRepaired(t *testing.T) {
	ctx := context.Background()
	pool := unmigratedPool(t)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	state := func() (exists, valid bool) {
		t.Helper()
		err := pool.QueryRow(ctx, `SELECT true, i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
			WHERE c.relname = 'session_artifacts_live_name_idx' AND c.relnamespace = current_schema()::regnamespace`).Scan(&exists, &valid)
		if err != nil && err.Error() != "no rows in result set" {
			t.Fatal(err)
		}
		return exists, valid
	}
	if exists, _ := state(); exists {
		t.Fatal("a migration still builds the live artifact index inside the migration transaction")
	}
	for range 2 {
		if err := db.EnsureLiveArtifactIndex(ctx, pool); err != nil {
			t.Fatal(err)
		}
		if exists, valid := state(); !exists || !valid {
			t.Fatalf("after a build: exists %v valid %v", exists, valid)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE pg_index SET indisvalid = false WHERE indexrelid = 'session_artifacts_live_name_idx'::regclass`); err != nil {
		t.Fatal(err)
	}
	if err := db.EnsureLiveArtifactIndex(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if exists, valid := state(); !exists || !valid {
		t.Fatalf("an invalid index was not rebuilt: exists %v valid %v", exists, valid)
	}
	// The constraint it exists for works with the index in place.
	const daemon, session = "3b3b3b3b-3b3b-4b3b-8b3b-3b3b3b3b3b3b", "3c3c3c3c-3c3c-4c3c-8c3c-3c3c3c3c3c3c"
	if err := db.UpsertDaemon(ctx, pool, daemon, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertLiveArtifactCapped(ctx, pool, db.ArtifactRow{
		ID: fmt.Sprintf("%032x", 1), SessionID: session, Name: "chart.png", Size: 1, ContentType: "image/png", SHA256: "a",
	}, 0, 0); err != nil {
		t.Fatalf("first live insert: %v", err)
	}
	if _, err := db.InsertLiveArtifactCapped(ctx, pool, db.ArtifactRow{
		ID: fmt.Sprintf("%032x", 2), SessionID: session, Name: "chart.png", Size: 2, ContentType: "image/png", SHA256: "b",
	}, 0, 0); err == nil {
		t.Fatal("a second live row for the same name was accepted: the index is not enforcing uniqueness")
	}
}

// A server from before InsertLiveArtifactCapped's lock-protected check (or before this index
// existed at all) could have left duplicate live rows behind. Building the unique index over them
// would fail forever; EnsureLiveArtifactIndex must repair them first — keep the newest by
// updated_at, demote the rest to live=false — so the build then succeeds (BLG-470 round 2 #4).
func TestLiveArtifactIndexRepairsDuplicatesBeforeBuilding(t *testing.T) {
	ctx := context.Background()
	pool := unmigratedPool(t)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatal(err)
	}
	const daemon, session = "3f3f3f3f-3f3f-4f3f-8f3f-3f3f3f3f3f3f", "4a4a4a4a-4a4a-4a4a-8a4a-4a4a4a4a4a4a"
	if err := db.UpsertDaemon(ctx, pool, daemon, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	// Three duplicate live rows for the same name, inserted directly (the index does not exist
	// yet, so nothing stops this — exactly the state a pre-fix server could have left behind).
	for i, ts := range []string{"2026-11-01T00:00:00Z", "2026-11-03T00:00:00Z", "2026-11-02T00:00:00Z"} {
		if _, err := pool.Exec(ctx, `INSERT INTO session_artifacts (id, session_id, name, size, content_type, sha256, origin, version, live, updated_at)
			VALUES ($1, $2, 'chart.png', 1, 'image/png', $3, 'agent', 1, true, $4)`,
			fmt.Sprintf("%032x", 100+i), session, fmt.Sprintf("dup-%d", i), ts); err != nil {
			t.Fatalf("seed duplicate %d: %v", i, err)
		}
	}

	if err := db.EnsureLiveArtifactIndex(ctx, pool); err != nil {
		t.Fatalf("EnsureLiveArtifactIndex did not repair and build: %v", err)
	}

	live, err := db.GetLiveArtifact(ctx, pool, session, db.OriginAgent, "chart.png")
	if err != nil || live == nil {
		t.Fatalf("GetLiveArtifact after repair: %v, %v", live, err)
	}
	if live.ID != fmt.Sprintf("%032x", 101) { // i=1, the 2026-11-03 row: the newest updated_at
		t.Fatalf("the surviving live row = %s; want the one with the newest updated_at (%s)", live.ID, fmt.Sprintf("%032x", 101))
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM session_artifacts WHERE session_id = $1 AND live`, session).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d live rows remain after repair; want 1", n)
	}
	var valid bool
	if err := pool.QueryRow(ctx, `SELECT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = 'session_artifacts_live_name_idx' AND c.relnamespace = current_schema()::regnamespace`).Scan(&valid); err != nil {
		t.Fatal(err)
	}
	if !valid {
		t.Fatal("the index did not build successfully after the repair")
	}
}
