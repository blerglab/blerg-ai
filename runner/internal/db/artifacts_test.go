package db_test

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

// artifactTestPool is a multi-connection pool on its own schema with every migration applied.
func artifactTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database tests")
	}
	ctx := context.Background()
	const schema = "art_dbartifacts"
	boot, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema), fmt.Sprintf("CREATE SCHEMA %s", schema)} {
		if _, err := boot.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 8
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = boot.Exec(context.Background(), fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE", schema))
		boot.Close()
	})
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	return pool
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
