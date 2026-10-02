package db_test

// File versions (migration 033, db/artifacts.go): publishing a name that already exists in the
// session, from the same origin, makes the next version of it.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// versionFixture is a pool with a daemon and one running session.
func versionFixture(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	pool := artifactTestPool(t)
	ctx := context.Background()
	const daemon, session = "0d0d0d0d-0d0d-4d0d-8d0d-0d0d0d0d0d0d", "0e0e0e0e-0e0e-4e0e-8e0e-0e0e0e0e0e0e"
	if err := db.UpsertDaemon(ctx, pool, daemon, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	return pool, session
}

func vrow(session string, i int, name, origin string) db.ArtifactRow {
	return db.ArtifactRow{ID: fmt.Sprintf("%032x", i), SessionID: session, Name: name, Size: 1, ContentType: "text/plain", SHA256: "x", Origin: origin}
}

func TestArtifactVersionsAreNumberedPerNameAndOrigin(t *testing.T) {
	pool, session := versionFixture(t)
	ctx := context.Background()
	ins := func(i int, name, origin string) db.ArtifactRow {
		t.Helper()
		r, err := db.InsertArtifact(ctx, pool, vrow(session, i, name, origin), 50)
		if err != nil {
			t.Fatalf("insert %d %s/%s: %v", i, origin, name, err)
		}
		return r
	}
	a1 := ins(1, "report.md", db.OriginAgent)
	if a1.Version != 1 || a1.Previous != 0 {
		t.Fatalf("first = v%d previous %d, want v1 previous 0", a1.Version, a1.Previous)
	}
	a2 := ins(2, "report.md", db.OriginAgent)
	if a2.Version != 2 || a2.Previous != 1 {
		t.Fatalf("second = v%d previous %d, want v2 previous 1", a2.Version, a2.Previous)
	}
	if o := ins(3, "other.md", db.OriginAgent); o.Version != 1 {
		t.Errorf("a different name = v%d, want v1", o.Version)
	}
	// The same name from a person is its own sequence.
	if u := ins(4, "report.md", db.OriginUser); u.Version != 1 || u.Previous != 0 {
		t.Errorf("same name, user origin = v%d previous %d, want v1", u.Version, u.Previous)
	}
	if a3 := ins(5, "report.md", db.OriginAgent); a3.Version != 3 || a3.Previous != 2 {
		t.Errorf("third agent = v%d previous %d", a3.Version, a3.Previous)
	}
	got, err := db.GetArtifact(ctx, pool, session, a2.ID)
	if err != nil || got == nil || got.Version != 2 {
		t.Fatalf("GetArtifact = %+v, %v; want version 2", got, err)
	}
	rows, err := db.ListArtifacts(ctx, pool, session)
	if err != nil || len(rows) != 5 {
		t.Fatalf("list = %d, %v", len(rows), err)
	}
}

// The number is stored: deleting an earlier version never renumbers the rest.
func TestArtifactVersionsAreStoredNotRecomputed(t *testing.T) {
	pool, session := versionFixture(t)
	ctx := context.Background()
	var ids []string
	for i := 1; i <= 2; i++ {
		r, err := db.InsertArtifact(ctx, pool, vrow(session, i, "a.txt", ""), 50)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	if ok, err := db.DeleteArtifact(ctx, pool, session, ids[0]); err != nil || !ok {
		t.Fatalf("delete v1: %v %v", ok, err)
	}
	v2, _ := db.GetArtifact(ctx, pool, session, ids[1])
	if v2 == nil || v2.Version != 2 {
		t.Fatalf("v2 after deleting v1 = %+v", v2)
	}
	next, err := db.InsertArtifact(ctx, pool, vrow(session, 3, "a.txt", ""), 50)
	if err != nil || next.Version != 3 || next.Previous != 2 {
		t.Fatalf("next = %+v, %v; want v3 previous 2", next, err)
	}
	// Deleting the newest frees its number: the next publish is v3 again.
	if _, err := db.DeleteArtifact(ctx, pool, session, next.ID); err != nil {
		t.Fatal(err)
	}
	again, err := db.InsertArtifact(ctx, pool, vrow(session, 4, "a.txt", ""), 50)
	if err != nil || again.Version != 3 {
		t.Fatalf("after deleting the newest = %+v, %v; want v3", again, err)
	}
}

func TestArtifactVersionsConcurrentPublishesGetDistinctNumbers(t *testing.T) {
	pool, session := versionFixture(t)
	ctx := context.Background()
	const n = 12
	var wg sync.WaitGroup
	versions := make(chan int, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := db.InsertArtifact(ctx, pool, vrow(session, i+1, "same.txt", ""), 50)
			if err != nil {
				t.Errorf("insert: %v", err)
				return
			}
			versions <- r.Version
		}(i)
	}
	wg.Wait()
	close(versions)
	seen := map[int]bool{}
	for v := range versions {
		if seen[v] {
			t.Errorf("version %d handed out twice", v)
		}
		seen[v] = true
	}
	for v := 1; v <= n; v++ {
		if !seen[v] {
			t.Errorf("version %d missing: want 1..%d", v, n)
		}
	}
}

func TestArtifactVersionCapRefusesTheTwentyFirst(t *testing.T) {
	pool, session := versionFixture(t)
	ctx := context.Background()
	for i := 1; i <= db.MaxArtifactVersions; i++ {
		if _, err := db.InsertArtifact(ctx, pool, vrow(session, i, "big.txt", ""), 50); err != nil {
			t.Fatalf("version %d: %v", i, err)
		}
	}
	if _, err := db.InsertArtifact(ctx, pool, vrow(session, 100, "big.txt", ""), 50); !errors.Is(err, db.ErrArtifactVersionLimit) {
		t.Fatalf("21st version: %v, want ErrArtifactVersionLimit", err)
	}
	// Another name, and the same name from the other origin, are unaffected.
	if _, err := db.InsertArtifact(ctx, pool, vrow(session, 101, "small.txt", ""), 50); err != nil {
		t.Errorf("another name: %v", err)
	}
	if _, err := db.InsertArtifact(ctx, pool, vrow(session, 102, "big.txt", db.OriginUser), 50); err != nil {
		t.Errorf("same name, user origin: %v", err)
	}
	// Deleting one frees a place; the new one continues the sequence (v21).
	if _, err := db.DeleteArtifact(ctx, pool, session, fmt.Sprintf("%032x", 1)); err != nil {
		t.Fatal(err)
	}
	r, err := db.InsertArtifact(ctx, pool, vrow(session, 103, "big.txt", ""), 50)
	if err != nil || r.Version != 21 {
		t.Fatalf("after a delete = %+v, %v; want v21", r, err)
	}
	if n, err := db.CountArtifactVersions(ctx, pool, session, db.OriginAgent, "big.txt"); err != nil || n != db.MaxArtifactVersions {
		t.Fatalf("CountArtifactVersions = %d, %v", n, err)
	}
}

// Migration 033 numbers the rows that already exist, per (session, origin, name), oldest first,
// and running it again changes nothing (not even after a version was deleted).
func TestArtifactVersionBackfillNumbersExistingDuplicatesByTime(t *testing.T) {
	pool, session := versionFixture(t)
	ctx := context.Background()
	// Rows as they were before the column: every one at the default version 1.
	seed := func(id, name, origin, at string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO session_artifacts (id, session_id, name, size, content_type, sha256, origin, created_at)
			VALUES ($1, $2, $3, 1, 'text/plain', 'x', $4, $5::timestamptz)`, id, session, name, origin, at); err != nil {
			t.Fatal(err)
		}
	}
	seed("b", "r.md", "agent", "2026-09-01T10:00:00Z")
	seed("a", "r.md", "agent", "2026-09-01T09:00:00Z") // older, though inserted later
	seed("c", "r.md", "agent", "2026-09-01T11:00:00Z")
	seed("d", "r.md", "user", "2026-09-01T12:00:00Z")
	seed("e", "x.md", "agent", "2026-09-01T12:00:00Z")
	seed("t1", "tie.md", "agent", "2026-09-01T13:00:00Z") // same instant: the id breaks the tie
	seed("t2", "tie.md", "agent", "2026-09-01T13:00:00Z")
	sqlText, err := os.ReadFile("migrations/033_artifact_version.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(sqlText)); err != nil {
		t.Fatalf("migration: %v", err)
	}
	want := map[string]int{"a": 1, "b": 2, "c": 3, "d": 1, "e": 1, "t1": 1, "t2": 2}
	check := func(when string) {
		t.Helper()
		for id, v := range want {
			got, err := db.GetArtifact(ctx, pool, session, id)
			if err != nil || got == nil || got.Version != v {
				t.Errorf("%s: %s = %+v, %v; want version %d", when, id, got, err, v)
			}
		}
	}
	check("after the backfill")
	// Delete the oldest, then run the migration once more: nothing is renumbered.
	if _, err := db.DeleteArtifact(ctx, pool, session, "a"); err != nil {
		t.Fatal(err)
	}
	delete(want, "a")
	if _, err := pool.Exec(ctx, string(sqlText)); err != nil {
		t.Fatalf("migration again: %v", err)
	}
	check("after running it again")
	next, err := db.InsertArtifact(ctx, pool, db.ArtifactRow{ID: "n1", SessionID: session, Name: "r.md", ContentType: "text/plain", SHA256: "x"}, 50)
	if err != nil || next.Version != 4 {
		t.Fatalf("next after the backfill = %+v, %v; want v4", next, err)
	}
}
