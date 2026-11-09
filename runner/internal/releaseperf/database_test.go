package releaseperf

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func adminDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database tests")
	}
	return dsn
}

func TestCreateAndDropDisposableDatabase(t *testing.T) {
	ctx := context.Background()
	admin := adminDSN(t)
	name := "blerg_release_perf_test_fixture"
	dsn, err := CreateDisposableDatabase(ctx, admin, name)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "SELECT 1"); err != nil {
		t.Fatalf("expected the new database to be usable: %v", err)
	}
	pool.Close()

	// Creating again must drop-and-recreate rather than failing on an
	// existing database, so a stale leftover never blocks a fresh run.
	if _, err := CreateDisposableDatabase(ctx, admin, name); err != nil {
		t.Fatalf("expected re-creation of an existing disposable database to succeed, got: %v", err)
	}

	if err := DropDatabase(ctx, admin, name); err != nil {
		t.Fatal(err)
	}
}

func TestSettleDatabaseRunsVacuumAndCheckpoint(t *testing.T) {
	ctx := context.Background()
	admin := adminDSN(t)
	name := "blerg_release_perf_test_settle"
	dsn, err := CreateDisposableDatabase(ctx, admin, name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = DropDatabase(ctx, admin, name) }()
	if err := SettleDatabase(ctx, dsn); err != nil {
		t.Fatalf("expected VACUUM ANALYZE/CHECKPOINT to succeed on a fresh database, got: %v", err)
	}
}

func TestReadLocalAccountID(t *testing.T) {
	ctx := context.Background()
	admin := adminDSN(t)
	name := "blerg_release_perf_test_account"
	dsn, err := CreateDisposableDatabase(ctx, admin, name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = DropDatabase(ctx, admin, name) }()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `CREATE TABLE accounts (
		id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
		provider text NOT NULL,
		provider_subject text NOT NULL,
		UNIQUE (provider, provider_subject)
	)`); err != nil {
		t.Fatal(err)
	}
	var wantID string
	if err := pool.QueryRow(ctx, `INSERT INTO accounts (provider, provider_subject) VALUES ('local', 'release-perf') RETURNING id::text`).Scan(&wantID); err != nil {
		t.Fatal(err)
	}

	gotID, err := ReadLocalAccountID(ctx, pool, "release-perf")
	if err != nil {
		t.Fatalf("expected the seeded account's id, got error: %v", err)
	}
	if gotID != wantID {
		t.Fatalf("got account id %q, want %q", gotID, wantID)
	}

	if _, err := ReadLocalAccountID(ctx, pool, "nobody"); err == nil {
		t.Fatal("expected an error reading an account id for a username that was never created")
	}
}
