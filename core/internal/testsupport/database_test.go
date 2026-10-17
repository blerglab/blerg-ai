package testsupport

import (
	"context"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMain(m *testing.M) { os.Exit(Run(m)) }

// Template state persists across -count repetitions in one test process.
var templateBuilds atomic.Int32

func TestTemplateCopiesAreIndependentAndCleaned(t *testing.T) {
	_ = admin(t)
	migrate := func(ctx context.Context, p *pgxpool.Pool) error {
		templateBuilds.Add(1)
		_, err := p.Exec(ctx, "CREATE TABLE fixture (value text NOT NULL)")
		return err
	}
	ctx := context.Background()
	var names []string
	for i := 0; i < 2; i++ {
		t.Run("copy", func(t *testing.T) {
			pool := Pool(t, migrate)
			assertFixtureMarked(t, pool)
			names = append(names, pool.Config().ConnConfig.Database)
			reconnect, err := pgxpool.New(ctx, pool.Config().ConnString())
			if err != nil {
				t.Fatal("reconnect clone")
			}
			defer reconnect.Close()
			var actual string
			if err := reconnect.QueryRow(ctx, "SELECT current_database()").Scan(&actual); err != nil || actual != pool.Config().ConnConfig.Database {
				t.Fatal("connection string does not preserve isolated database identity")
			}

			var count int
			if err := pool.QueryRow(ctx, "SELECT count(*) FROM fixture").Scan(&count); err != nil || count != 0 {
				t.Fatalf("fresh copy count=%d: %v", count, err)
			}
			if _, err := pool.Exec(ctx, "INSERT INTO fixture VALUES ('local')"); err != nil {
				t.Fatal(err)
			}
		})
	}
	if templateBuilds.Load() != 1 {
		t.Fatalf("template built %d times", templateBuilds.Load())
	}
	if len(names) != 2 {
		t.Fatal("copies did not complete")
	}
	if names[0] == names[1] {
		t.Fatal("tests shared a database")
	}
	pool, err := pgxpool.NewWithConfig(ctx, admin(t))
	if err != nil {
		t.Fatal("open admin pool")
	}
	defer pool.Close()
	for _, name := range names {
		var exists bool
		if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname=$1)", name).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatal("test database survived subtest cleanup")
		}
	}
}

func TestEmptyDatabaseDoesNotReuseTemplate(t *testing.T) {
	cfg := Empty(t)
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal("open empty database")
	}
	defer pool.Close()
	var count int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("empty database has %d tables: %v", count, err)
	}
}

func TestCloneConfigKeepsDatabaseIdentityAndClearsSchemaOptions(t *testing.T) {
	for _, dsn := range []string{
		"postgres://test:fixture@127.0.0.1/admin?sslmode=disable&options=-csearch_path%3Dmissing&search_path=missing&pool_max_conns=3",
		"host=127.0.0.1 user=test password=fixture dbname=admin sslmode=disable options='-csearch_path=missing' search_path=missing pool_max_conns=3",
	} {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal("parse test fixture")
		}
		result, err := databaseConfig(cfg, "cx_core_clone")
		if err != nil {
			t.Fatal("rewrite clone configuration")
		}
		replay, err := pgxpool.ParseConfig(result.ConnString())
		if err != nil {
			t.Fatal("parse rewritten configuration")
		}
		for _, c := range []*pgxpool.Config{result, replay} {
			if c.ConnConfig.Database != "cx_core_clone" || c.ConnConfig.RuntimeParams["search_path"] != "public" || c.ConnConfig.RuntimeParams["options"] != "" {
				t.Fatal("clone configuration lost database/schema isolation")
			}
			if _, exists := c.ConnConfig.RuntimeParams["pool_max_conns"]; exists {
				t.Fatal("pool settings leaked into server runtime parameters")
			}
			if c.ConnConfig.Password != cfg.ConnConfig.Password {
				t.Fatal("connection credential changed")
			}
		}
	}
}
