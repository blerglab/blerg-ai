package testsupport

import (
	"context"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMain(m *testing.M) { os.Exit(Run(m)) }

var fixtureMigrations atomic.Int32

func TestMigratedCopiesAreIsolated(t *testing.T) {
	migrate := func(ctx context.Context, pool *pgxpool.Pool) error {
		fixtureMigrations.Add(1)
		_, err := pool.Exec(ctx, "CREATE TABLE fixture (value text); INSERT INTO fixture VALUES ('template')")
		return err
	}
	names := make(chan string, 4)
	for range 4 {
		t.Run("independent copy", func(t *testing.T) {
			t.Parallel()
			pool := Pool(t, migrate)
			assertFixtureMarked(t, pool)
			var name, value string
			if err := pool.QueryRow(context.Background(), "SELECT current_database(), value FROM fixture").Scan(&name, &value); err != nil {
				t.Fatal(err)
			}
			names <- name
			reconnected, err := pgxpool.New(context.Background(), pool.Config().ConnString())
			if err != nil {
				t.Fatal("reconnect isolated test database failed")
			}
			defer reconnected.Close()
			var reconnectedName string
			if err := reconnected.QueryRow(context.Background(), "SELECT current_database()").Scan(&reconnectedName); err != nil {
				t.Fatal(err)
			}
			if reconnectedName != name {
				t.Fatal("serialized pool configuration escaped the isolated database")
			}
			if value != "template" {
				t.Fatalf("copy contains another test's writes: %q", value)
			}
			if _, err := pool.Exec(context.Background(), "UPDATE fixture SET value = 'changed'"); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Cleanup(func() {
		close(names)
		seen := make(map[string]bool)
		for name := range names {
			if seen[name] {
				t.Errorf("database reused: %s", name)
			}
			seen[name] = true
		}
		if len(seen) > 0 && fixtureMigrations.Load() != 1 {
			t.Errorf("migrated %d times, want once", fixtureMigrations.Load())
		}
		if len(seen) == 0 {
			return // The existing no-database skip contract also applies to these fixtures.
		}
		admin, err := pgxpool.New(context.Background(), os.Getenv("TEST_DATABASE_URL"))
		if err != nil {
			t.Fatal("open fixture cleanup verification pool")
		}
		defer admin.Close()
		for name := range seen {
			var exists bool
			if err := admin.QueryRow(context.Background(), "SELECT EXISTS (SELECT FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if exists {
				t.Errorf("isolated database survived test cleanup: %s", name)
			}
		}
	})
}

func TestDatabaseConfigRoundTripKeepsCloneAndPublicSchema(t *testing.T) {
	for name, dsn := range map[string]string{
		"URL":     "postgres://test:test@localhost/example?dbname=original&search_path=other&options=-c%20search_path%3Dother&pool_max_conns=3",
		"keyword": "host=localhost user=test password=test dbname=original search_path=other options='-c search_path=other' pool_max_conns=3",
	} {
		t.Run(name, func(t *testing.T) {
			base, err := pgxpool.ParseConfig(dsn)
			if err != nil {
				t.Fatal("parse fixture configuration")
			}
			clone := databaseConfig(t, base, "cx_board_fixture")
			roundTrip, err := pgxpool.ParseConfig(clone.ConnString())
			if err != nil {
				t.Fatal("parse serialized clone configuration")
			}
			for _, cfg := range []*pgxpool.Config{clone, roundTrip} {
				if cfg.ConnConfig.Database != "cx_board_fixture" {
					t.Fatal("configuration retained administrator database")
				}
				if path := cfg.ConnConfig.RuntimeParams["search_path"]; path != "" && path != "public" {
					t.Fatal("configuration restored caller search_path")
				}
				if cfg.ConnConfig.RuntimeParams["options"] != "" {
					t.Fatal("configuration restored caller options")
				}
				if _, leaked := cfg.ConnConfig.RuntimeParams["pool_max_conns"]; leaked {
					t.Fatal("pool configuration leaked into PostgreSQL runtime parameters")
				}
			}
		})
	}
}
