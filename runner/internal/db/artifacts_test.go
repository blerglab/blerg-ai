package db_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

// artifactTestPool is a multi-connection pool on its own schema with every migration applied.
func artifactTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return migratedPool(t, 8)
}

func TestArtifactsCapHoldsUnderConcurrentUploads(t *testing.T) {
	pool := artifactTestPool(t)
	ctx := context.Background()
	const daemon, session = "0d0d0d0d-0d0d-4d0d-8d0d-0d0d0d0d0d0d", "0e0e0e0e-0e0e-4e0e-8e0e-0e0e0e0e0e0e"
	if err := db.UpsertDaemon(ctx, pool, daemon, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	const capN, tries = 5, 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	okN, limitN := 0, 0
	for i := 0; i < tries; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := db.InsertArtifact(ctx, pool, db.ArtifactRow{
				ID: fmt.Sprintf("%032x", i), SessionID: session, Name: "a.txt", Size: 1, ContentType: "text/plain", SHA256: "x",
			}, capN)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				okN++
			case errors.Is(err, db.ErrArtifactLimit):
				limitN++
			default:
				t.Errorf("insert: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if okN != capN || limitN != tries-capN {
		t.Fatalf("inserted %d, refused %d; want %d and %d", okN, limitN, capN, tries-capN)
	}
	if n, _ := db.CountArtifacts(ctx, pool, session); n != capN {
		t.Fatalf("count = %d", n)
	}
	rows, err := db.ListArtifacts(ctx, pool, session)
	if err != nil || len(rows) != capN {
		t.Fatalf("list = %d, %v", len(rows), err)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].CreatedAt.After(rows[i-1].CreatedAt) {
			t.Errorf("not newest first: %v then %v", rows[i-1].CreatedAt, rows[i].CreatedAt)
		}
	}
	// A row belongs to its session: another session's id finds nothing.
	if got, err := db.GetArtifact(ctx, pool, "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f", rows[0].ID); err != nil || got != nil {
		t.Errorf("foreign session read = %v, %v", got, err)
	}
	// Deleting the session removes its rows.
	if err := db.DeleteSession(ctx, pool, session); err != nil {
		t.Fatal(err)
	}
	if n, _ := db.CountArtifacts(ctx, pool, session); n != 0 {
		t.Errorf("rows survived the session: %d", n)
	}
}

func TestArtifactsOriginCapsAreSeparateAndBytesAreCapped(t *testing.T) {
	pool := artifactTestPool(t)
	ctx := context.Background()
	const daemon, session = "0d0d0d0d-0d0d-4d0d-8d0d-0d0d0d0d0d0d", "0e0e0e0e-0e0e-4e0e-8e0e-0e0e0e0e0e0e"
	if err := db.UpsertDaemon(ctx, pool, daemon, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	row := func(i int, size int64, origin string) db.ArtifactRow {
		return db.ArtifactRow{ID: fmt.Sprintf("%032x", i), SessionID: session, Name: "a.txt", Size: size, ContentType: "text/plain", SHA256: "x", Origin: origin, UploadedBy: "acct-1"}
	}
	// One agent file fills an agent cap of 1, yet user files still go in.
	if _, err := db.InsertArtifact(ctx, pool, row(1, 10, ""), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertArtifact(ctx, pool, row(2, 10, ""), 1); !errors.Is(err, db.ErrArtifactLimit) {
		t.Fatalf("agent over cap: %v", err)
	}
	got, err := db.InsertArtifactCapped(ctx, pool, row(3, 60, db.OriginUser), 2, 100)
	if err != nil || got.Origin != db.OriginUser || got.UploadedBy != "acct-1" {
		t.Fatalf("user insert = %+v, %v", got, err)
	}
	if _, err := db.InsertArtifactCapped(ctx, pool, row(4, 41, db.OriginUser), 2, 100); !errors.Is(err, db.ErrArtifactBytesLimit) {
		t.Fatalf("over the byte total: %v", err)
	}
	if _, err := db.InsertArtifactCapped(ctx, pool, row(5, 40, db.OriginUser), 2, 100); err != nil {
		t.Fatalf("exactly the total: %v", err)
	}
	if _, err := db.InsertArtifactCapped(ctx, pool, row(6, 1, db.OriginUser), 2, 100); !errors.Is(err, db.ErrArtifactLimit) {
		t.Fatalf("over the user count: %v", err)
	}
	if n, b, err := db.CountArtifactsOrigin(ctx, pool, session, db.OriginUser); err != nil || n != 2 || b != 100 {
		t.Fatalf("user count = %d, %d, %v", n, b, err)
	}
	if n, _, _ := db.CountArtifactsOrigin(ctx, pool, session, db.OriginAgent); n != 1 {
		t.Fatalf("agent count = %d", n)
	}
	first, _ := db.GetArtifact(ctx, pool, session, fmt.Sprintf("%032x", 1))
	if first == nil || first.Origin != db.OriginAgent {
		t.Fatalf("default origin = %+v", first)
	}
}

// With no count cap (limit 0) a long-running session keeps accepting files past the old
// 100-file lifetime count; only the size total refuses.
func TestArtifactsWithoutCountCapAcceptPastOldLimit(t *testing.T) {
	pool := artifactTestPool(t)
	ctx := context.Background()
	const daemon, session = "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f", "1a1a1a1a-1a1a-4a1a-8a1a-1a1a1a1a1a1a"
	if err := db.UpsertDaemon(ctx, pool, daemon, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	row := func(i int, size int64) db.ArtifactRow {
		return db.ArtifactRow{ID: fmt.Sprintf("%032x", 1000+i), SessionID: session, Name: fmt.Sprintf("f%d.txt", i), Size: size, ContentType: "text/plain", SHA256: "x", Origin: db.OriginUser, UploadedBy: "acct-1"}
	}
	for i := range 120 {
		if _, err := db.InsertArtifactCapped(ctx, pool, row(i, 1), 0, 1000); err != nil {
			t.Fatalf("file %d refused without a count cap: %v", i, err)
		}
	}
	if _, err := db.InsertArtifactCapped(ctx, pool, row(500, 900), 0, 1000); !errors.Is(err, db.ErrArtifactBytesLimit) {
		t.Fatalf("the size total no longer refuses: %v", err)
	}
}

// BLG-470: a live file keeps one row, one id, one version, however many times it is republished.
func TestLiveArtifactRepublishNeverAddsAVersion(t *testing.T) {
	pool := artifactTestPool(t)
	ctx := context.Background()
	const daemon, session = "1b1b1b1b-1b1b-4b1b-8b1b-1b1b1b1b1b1b", "1c1c1c1c-1c1c-4c1c-8c1c-1c1c1c1c1c1c"
	if err := db.UpsertDaemon(ctx, pool, daemon, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	if existing, err := db.GetLiveArtifact(ctx, pool, session, db.OriginAgent, "chart.png"); err != nil || existing != nil {
		t.Fatalf("GetLiveArtifact before any publish = %v, %v; want nil, nil", existing, err)
	}
	first, err := db.InsertLiveArtifactCapped(ctx, pool, db.ArtifactRow{
		ID: fmt.Sprintf("%032x", 2000), SessionID: session, Name: "chart.png", Size: 10, ContentType: "image/png", SHA256: "a",
	}, 0, 0)
	if err != nil {
		t.Fatalf("first live publish: %v", err)
	}
	if first.Version != 1 || !first.Live {
		t.Fatalf("first live row = version %d, live %v; want 1, true", first.Version, first.Live)
	}
	for i, size := range []int64{20, 30, 40} {
		live, err := db.GetLiveArtifact(ctx, pool, session, db.OriginAgent, "chart.png")
		if err != nil || live == nil {
			t.Fatalf("GetLiveArtifact round %d: %v, %v", i, live, err)
		}
		updated, err := db.UpdateLiveArtifactContent(ctx, pool, session, live.ID, size, "image/png", fmt.Sprintf("sha-%d", i), 0, func() error { return nil })
		if err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
		if updated.ID != first.ID || updated.Version != 1 || updated.Size != size {
			t.Fatalf("round %d = id %s version %d size %d; want %s, 1, %d", i, updated.ID, updated.Version, updated.Size, first.ID, size)
		}
		if !updated.UpdatedAt.After(first.CreatedAt) {
			t.Fatalf("round %d: updated_at did not move forward", i)
		}
	}
	if n, err := db.CountArtifactVersions(ctx, pool, session, db.OriginAgent, "chart.png"); err != nil || n != 1 {
		t.Fatalf("version count after 3 republishes = %d, %v; want 1", n, err)
	}
	if n, err := db.CountArtifacts(ctx, pool, session); err != nil || n != 1 {
		t.Fatalf("artifact count after 3 republishes = %d, %v; want 1", n, err)
	}
}

// Switching an existing versioned file to live (an explicit `--live` publish on a name that
// already has non-live history) adds exactly one more version, marked live; every later
// `--live` republish of that name then updates that same row.
func TestLiveArtifactSwitchFromVersioned(t *testing.T) {
	pool := artifactTestPool(t)
	ctx := context.Background()
	const daemon, session = "1d1d1d1d-1d1d-4d1d-8d1d-1d1d1d1d1d1d", "1e1e1e1e-1e1e-4e1e-8e1e-1e1e1e1e1e1e"
	if err := db.UpsertDaemon(ctx, pool, daemon, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	v1, err := db.InsertArtifactCapped(ctx, pool, db.ArtifactRow{
		ID: fmt.Sprintf("%032x", 3000), SessionID: session, Name: "report.csv", Size: 5, ContentType: "text/csv", SHA256: "a",
	}, 0, 0)
	if err != nil {
		t.Fatalf("v1: %v", err)
	}
	v2, err := db.InsertLiveArtifactCapped(ctx, pool, db.ArtifactRow{
		ID: fmt.Sprintf("%032x", 3001), SessionID: session, Name: "report.csv", Size: 6, ContentType: "text/csv", SHA256: "b",
	}, 0, 0)
	if err != nil {
		t.Fatalf("switch to live: %v", err)
	}
	if v2.Version != v1.Version+1 || !v2.Live {
		t.Fatalf("switch row = version %d, live %v; want %d, true", v2.Version, v2.Live, v1.Version+1)
	}
	if n, err := db.CountArtifactVersions(ctx, pool, session, db.OriginAgent, "report.csv"); err != nil || n != 2 {
		t.Fatalf("version count after the switch = %d, %v; want 2 (the old version stays in history)", n, err)
	}
	if _, err := db.UpdateLiveArtifactContent(ctx, pool, session, v2.ID, 7, "text/csv", "c", 0, func() error { return nil }); err != nil {
		t.Fatalf("update after switch: %v", err)
	}
	if n, err := db.CountArtifactVersions(ctx, pool, session, db.OriginAgent, "report.csv"); err != nil || n != 2 {
		t.Fatalf("version count after an in-place update = %d, %v; want still 2", n, err)
	}
}

// The unique partial index refuses a second live row for the same (session, origin, name): the
// caller must look the existing one up with GetLiveArtifact and update it instead of inserting.
func TestLiveArtifactOnlyOneLiveRowPerName(t *testing.T) {
	pool := artifactTestPool(t)
	ctx := context.Background()
	const daemon, session = "1f1f1f1f-1f1f-4f1f-8f1f-1f1f1f1f1f1f", "2a2a2a2a-2a2a-4a2a-8a2a-2a2a2a2a2a2a"
	if err := db.UpsertDaemon(ctx, pool, daemon, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertLiveArtifactCapped(ctx, pool, db.ArtifactRow{
		ID: fmt.Sprintf("%032x", 4000), SessionID: session, Name: "chart.png", Size: 1, ContentType: "image/png", SHA256: "a",
	}, 0, 0); err != nil {
		t.Fatalf("first live insert: %v", err)
	}
	if _, err := db.InsertLiveArtifactCapped(ctx, pool, db.ArtifactRow{
		ID: fmt.Sprintf("%032x", 4001), SessionID: session, Name: "chart.png", Size: 2, ContentType: "image/png", SHA256: "b",
	}, 0, 0); err == nil {
		t.Fatal("a second live row for the same name was accepted")
	}
}

// ListLiveArtifacts feeds the startup sweep (BLG-470 round 3 #3) that checks every live row's
// bytes against disk: it must return every live row across every session, and never a plain
// version.
func TestListLiveArtifactsReturnsEveryLiveRowAcrossSessionsButNotPlainVersions(t *testing.T) {
	pool := artifactTestPool(t)
	ctx := context.Background()
	const daemon = "1e1e1e1e-1e1e-4e1e-8e1e-1e1e1e1e1e1e"
	const sessionA, sessionB = "1f1f1f1f-1f1f-4f1f-8f1f-1f1f1f1f1f2e", "1f1f1f1f-1f1f-4f1f-8f1f-1f1f1f1f1f2f"
	if err := db.UpsertDaemon(ctx, pool, daemon, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{sessionA, sessionB} {
		if err := db.InsertSession(ctx, pool, s, daemon, "running", "/repos/app", "app", "t", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.InsertLiveArtifactCapped(ctx, pool, db.ArtifactRow{
		ID: fmt.Sprintf("%032x", 4100), SessionID: sessionA, Name: "chart.png", Size: 1, ContentType: "image/png", SHA256: "a",
	}, 0, 0); err != nil {
		t.Fatalf("live insert A: %v", err)
	}
	if _, err := db.InsertLiveArtifactCapped(ctx, pool, db.ArtifactRow{
		ID: fmt.Sprintf("%032x", 4101), SessionID: sessionB, Name: "chart.png", Size: 1, ContentType: "image/png", SHA256: "b",
	}, 0, 0); err != nil {
		t.Fatalf("live insert B: %v", err)
	}
	if _, err := db.InsertArtifactCapped(ctx, pool, db.ArtifactRow{
		ID: fmt.Sprintf("%032x", 4102), SessionID: sessionA, Name: "plain.png", Size: 1, ContentType: "image/png", SHA256: "c",
	}, 0, 0); err != nil {
		t.Fatalf("plain insert: %v", err)
	}
	live, err := db.ListLiveArtifacts(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	gotIDs := map[string]bool{}
	for _, a := range live {
		if !a.Live {
			t.Fatalf("ListLiveArtifacts returned a non-live row: %+v", a)
		}
		gotIDs[a.ID] = true
	}
	for _, want := range []string{fmt.Sprintf("%032x", 4100), fmt.Sprintf("%032x", 4101)} {
		if !gotIDs[want] {
			t.Fatalf("ListLiveArtifacts = %v; want it to include %s", gotIDs, want)
		}
	}
	if gotIDs[fmt.Sprintf("%032x", 4102)] {
		t.Fatalf("ListLiveArtifacts included the plain versioned row")
	}
}

// UpdateLiveArtifactContent must hold its advisory lock across commitFile, not just across the row
// update: otherwise two concurrent republishes of the same live file can commit their rows in one
// order while their file commits (typically an os.Rename) land in the other order, leaving the row
// and the bytes on disk describing two different republishes.
func TestUpdateLiveArtifactContentHoldsTheLockAcrossCommitFile(t *testing.T) {
	pool := artifactTestPool(t)
	ctx := context.Background()
	const daemon, session = "2b2b2b2b-2b2b-4b2b-8b2b-2b2b2b2b2b2b", "2c2c2c2c-2c2c-4c2c-8c2c-2c2c2c2c2c2c"
	if err := db.UpsertDaemon(ctx, pool, daemon, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	live, err := db.InsertLiveArtifactCapped(ctx, pool, db.ArtifactRow{
		ID: fmt.Sprintf("%032x", 5000), SessionID: session, Name: "chart.png", Size: 1, ContentType: "image/png", SHA256: "seed",
	}, 0, 0)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	aEnteredCommitFile := make(chan struct{})
	releaseA := make(chan struct{})
	doneA := make(chan error, 1)
	go func() {
		_, err := db.UpdateLiveArtifactContent(ctx, pool, session, live.ID, 10, "image/png", "a", 0, func() error {
			close(aEnteredCommitFile)
			<-releaseA
			return nil
		})
		doneA <- err
	}()
	select {
	case <-aEnteredCommitFile:
	case <-time.After(5 * time.Second):
		t.Fatal("A never reached its file commit")
	}

	// B's request arrives while A is still inside its own commitFile (bytes written, row not yet
	// committed). If the lock covers commitFile, B must block until A finishes and commits; it
	// must not run its own commitFile first.
	bEnteredCommitFile := make(chan struct{}, 1)
	doneB := make(chan error, 1)
	go func() {
		_, err := db.UpdateLiveArtifactContent(ctx, pool, session, live.ID, 20, "image/png", "b", 0, func() error {
			bEnteredCommitFile <- struct{}{}
			return nil
		})
		doneB <- err
	}()

	select {
	case <-bEnteredCommitFile:
		t.Fatal("B committed its file while A's row update was still uncommitted: the lock does not cover the file commit")
	case <-time.After(300 * time.Millisecond):
		// expected: B is blocked behind A's advisory lock
	}

	close(releaseA)
	if err := <-doneA; err != nil {
		t.Fatalf("A: %v", err)
	}
	select {
	case <-bEnteredCommitFile:
	case <-time.After(5 * time.Second):
		t.Fatal("B never proceeded after A released the lock")
	}
	if err := <-doneB; err != nil {
		t.Fatalf("B: %v", err)
	}
}

// A failed commitFile (the rename did not land) must roll back the row update: the index must
// never describe bytes that are not actually on disk.
func TestUpdateLiveArtifactContentRollsBackWhenCommitFileFails(t *testing.T) {
	pool := artifactTestPool(t)
	ctx := context.Background()
	const daemon, session = "2d2d2d2d-2d2d-4d2d-8d2d-2d2d2d2d2d2d", "2e2e2e2e-2e2e-4e2e-8e2e-2e2e2e2e2e2e"
	if err := db.UpsertDaemon(ctx, pool, daemon, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	live, err := db.InsertLiveArtifactCapped(ctx, pool, db.ArtifactRow{
		ID: fmt.Sprintf("%032x", 6000), SessionID: session, Name: "chart.png", Size: 1, ContentType: "image/png", SHA256: "seed",
	}, 0, 0)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	commitErr := errors.New("rename failed")
	if _, err := db.UpdateLiveArtifactContent(ctx, pool, session, live.ID, 99, "image/png", "would-be-new", 0, func() error { return commitErr }); !errors.Is(err, commitErr) {
		t.Fatalf("UpdateLiveArtifactContent with a failing commitFile = %v; want it to wrap %v", err, commitErr)
	}
	after, err := db.GetLiveArtifact(ctx, pool, session, db.OriginAgent, "chart.png")
	if err != nil || after == nil {
		t.Fatalf("GetLiveArtifact after the failed commit: %v, %v", after, err)
	}
	if after.Size != live.Size || after.SHA256 != live.SHA256 {
		t.Fatalf("row after a failed commitFile = size %d sha %q; want the seed unchanged (size %d sha %q)", after.Size, after.SHA256, live.Size, live.SHA256)
	}
}

// UpdateLiveArtifactContent writes to a staging name and commits the row before renaming it into
// place (BLG-470 round 2 #5): a rename that fails transiently must be retried, within budget,
// before giving up — not treated as a permanent failure on the first try the way the always-fails
// case already covers in TestUpdateLiveArtifactContentRollsBackWhenCommitFileFails.
func TestUpdateLiveArtifactContentRetriesCommitFileBeforeGivingUp(t *testing.T) {
	pool := artifactTestPool(t)
	ctx := context.Background()
	const daemon, session = "2a2a2a2a-2a2a-4a2a-8a2a-2a2a2a2a2a2a", "2b2b2b2b-2b2b-4b2b-8b2b-2b2b2b2b2b2c"
	if err := db.UpsertDaemon(ctx, pool, daemon, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	live, err := db.InsertLiveArtifactCapped(ctx, pool, db.ArtifactRow{
		ID: fmt.Sprintf("%032x", 6500), SessionID: session, Name: "chart.png", Size: 1, ContentType: "image/png", SHA256: "seed",
	}, 0, 0)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Models os.Rename: a "disk" variable the first two attempts never touch (a failed rename is
	// idempotent — it does not partially move the file), and the third attempt lands.
	disk := "seed"
	attempts := 0
	commitFile := func() error {
		attempts++
		if attempts < 3 {
			return errors.New("transient rename failure")
		}
		disk = "new-sha"
		return nil
	}
	out, err := db.UpdateLiveArtifactContent(ctx, pool, session, live.ID, 42, "image/png", "new-sha", 0, commitFile)
	if err != nil {
		t.Fatalf("UpdateLiveArtifactContent with a rename that succeeds on the 3rd try: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("commitFile was called %d time(s); want exactly 3 (two transient failures, then success, all within the retry budget)", attempts)
	}
	if out.SHA256 != disk {
		t.Fatalf("row sha256 = %q but disk = %q; a successful retry must leave the row matching what actually landed on disk", out.SHA256, disk)
	}
	after, err := db.GetLiveArtifact(ctx, pool, session, db.OriginAgent, "chart.png")
	if err != nil || after == nil {
		t.Fatalf("GetLiveArtifact: %v, %v", after, err)
	}
	if after.SHA256 != "new-sha" {
		t.Fatalf("stored row sha256 = %q; want the update to have stuck, not been rolled back, since the rename landed within budget", after.SHA256)
	}
}

// A client disconnecting right after the row commits must not stop the restore from landing
// (BLG-470 round 3 #2): the row is already describing new bytes that the rename never wrote, and
// the request's own ctx being canceled is exactly the moment that matters most, not a reason to
// skip undoing it.
func TestUpdateLiveArtifactContentRestoresEvenWhenTheRequestContextIsCanceledAfterCommit(t *testing.T) {
	pool := artifactTestPool(t)
	const daemon, session = "2a2a2a2a-2a2a-4a2a-8a2a-2a2a2a2a2a2d", "2b2b2b2b-2b2b-4b2b-8b2b-2b2b2b2b2b2d"
	if err := db.UpsertDaemon(context.Background(), pool, daemon, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(context.Background(), pool, session, daemon, "running", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	live, err := db.InsertLiveArtifactCapped(context.Background(), pool, db.ArtifactRow{
		ID: fmt.Sprintf("%032x", 6600), SessionID: session, Name: "chart.png", Size: 1, ContentType: "image/png", SHA256: "seed",
	}, 0, 0)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	reqCtx, cancelReq := context.WithCancel(context.Background())
	commitFile := func() error {
		// Models the request's own ctx going away right as the rename starts failing — a client
		// that hung up the moment its connection broke, which is also when the row is already
		// committed to content nothing has actually written.
		cancelReq()
		return errors.New("rename failed")
	}
	if _, err := db.UpdateLiveArtifactContent(reqCtx, pool, session, live.ID, 99, "image/png", "would-be-new", 0, commitFile); err == nil {
		t.Fatal("want an error when every rename attempt fails")
	}
	after, err := db.GetLiveArtifact(context.Background(), pool, session, db.OriginAgent, "chart.png")
	if err != nil || after == nil {
		t.Fatalf("GetLiveArtifact: %v, %v", after, err)
	}
	if after.Size != live.Size || after.SHA256 != live.SHA256 {
		t.Fatalf("row after a failed commitFile with a canceled request ctx = size %d sha %q; want the seed restored (size %d sha %q) "+
			"— the restore must run on an uncancelable ctx, not the request's own", after.Size, after.SHA256, live.Size, live.SHA256)
	}
}

// LatestArtifactVersion must prefer the live row's version, whatever the highest version number
// among the name's rows is: a rolling update can leave an ordinary version numbered above the
// live row (BLG-470 #5, the old server does not know about `live`).
func TestLatestArtifactVersionPrefersTheLiveRowOverAHigherOrphanedVersion(t *testing.T) {
	pool := artifactTestPool(t)
	ctx := context.Background()
	const daemon, session = "2f2f2f2f-2f2f-4f2f-8f2f-2f2f2f2f2f2f", "3a3a3a3a-3a3a-4a3a-8a3a-3a3a3a3a3a3a"
	if err := db.UpsertDaemon(ctx, pool, daemon, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	// v1 (plain), then switch to live (v2), then an "old server" plain publish lands as v3 —
	// outranking the live row by number alone.
	if _, err := db.InsertArtifactCapped(ctx, pool, db.ArtifactRow{
		ID: fmt.Sprintf("%032x", 7001), SessionID: session, Name: "chart.png", Size: 1, ContentType: "image/png", SHA256: "v1",
	}, 0, 0); err != nil {
		t.Fatalf("v1: %v", err)
	}
	if _, err := db.InsertLiveArtifactCapped(ctx, pool, db.ArtifactRow{
		ID: fmt.Sprintf("%032x", 7002), SessionID: session, Name: "chart.png", Size: 2, ContentType: "image/png", SHA256: "v2-live",
	}, 0, 0); err != nil {
		t.Fatalf("switch to live: %v", err)
	}
	if _, err := db.InsertArtifactCapped(ctx, pool, db.ArtifactRow{
		ID: fmt.Sprintf("%032x", 7003), SessionID: session, Name: "chart.png", Size: 3, ContentType: "image/png", SHA256: "v3-orphan",
	}, 0, 0); err != nil {
		t.Fatalf("orphaned v3 (simulating an old server): %v", err)
	}
	latest, err := db.LatestArtifactVersion(ctx, pool, session, db.OriginAgent, "chart.png")
	if err != nil {
		t.Fatal(err)
	}
	if latest != 2 {
		t.Fatalf("LatestArtifactVersion = %d; want 2 (the live row), not 3 (the higher-numbered orphan)", latest)
	}
}

// Two (or more) concurrent first-time `--live` publishes of a brand new name must have exactly
// one winner, deterministically: the per-session advisory lock already serializes them, so the
// loser's re-check (inside the same lock as its own insert) must see the winner's committed row
// and fail cleanly with ErrArtifactAlreadyLive — not race into the unique index after the fact
// (BLG-470 round 2 #4).
func TestInsertLiveArtifactCappedConcurrentFirstPublishesHaveExactlyOneWinner(t *testing.T) {
	pool := artifactTestPool(t)
	ctx := context.Background()
	const daemon, session = "3d3d3d3d-3d3d-4d3d-8d3d-3d3d3d3d3d3d", "3e3e3e3e-3e3e-4e3e-8e3e-3e3e3e3e3e3e"
	if err := db.UpsertDaemon(ctx, pool, daemon, "laptop", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(ctx, pool, session, daemon, "running", "/repos/app", "app", "t", ""); err != nil {
		t.Fatal(err)
	}
	const n = 8
	var wg sync.WaitGroup
	oks, already := make(chan string, n), make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			row, err := db.InsertLiveArtifactCapped(ctx, pool, db.ArtifactRow{
				ID: fmt.Sprintf("%032x", 9000+i), SessionID: session, Name: "chart.png", Size: int64(i), ContentType: "image/png", SHA256: fmt.Sprintf("s%d", i),
			}, 0, 0)
			if err == nil {
				oks <- row.ID
				return
			}
			already <- err
		}(i)
	}
	wg.Wait()
	close(oks)
	close(already)
	var winners []string
	for id := range oks {
		winners = append(winners, id)
	}
	if len(winners) != 1 {
		t.Fatalf("%d concurrent first publishes succeeded; want exactly 1 (got %v)", len(winners), winners)
	}
	losers := 0
	for err := range already {
		losers++
		if !errors.Is(err, db.ErrArtifactAlreadyLive) {
			t.Errorf("a losing publish got %v; want ErrArtifactAlreadyLive (deterministic, not a race into the unique index)", err)
		}
	}
	if losers != n-1 {
		t.Fatalf("%d losers recorded; want %d", losers, n-1)
	}
	live, err := db.GetLiveArtifact(ctx, pool, session, db.OriginAgent, "chart.png")
	if err != nil || live == nil || live.ID != winners[0] {
		t.Fatalf("GetLiveArtifact = %v, %v; want the one winner (%s)", live, err, winners[0])
	}
}
