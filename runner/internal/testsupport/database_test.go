package testsupport

import (
	"context"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMain(m *testing.M) { os.Exit(Run(m)) }

var fixtureTemplateBuilds atomic.Int32

func TestTemplateCopiesAreIndependentAndCleaned(t *testing.T) {
	_ = adminConfig(t, "TEST_DATABASE_URL")
	migrate := func(ctx context.Context, pool *pgxpool.Pool) error {
		fixtureTemplateBuilds.Add(1)
		_, err := pool.Exec(ctx, "CREATE TABLE fixture (value text NOT NULL)")
		return err
	}
	var mu sync.Mutex
	var names []string
	t.Run("parallel copies", func(t *testing.T) {
		for range 4 {
			t.Run("copy", func(t *testing.T) {
				t.Parallel()
				pool := Open(t, "TEST_DATABASE_URL", "fixture", migrate)
				assertFixtureMarked(t, pool)
				ctx := context.Background()
				mu.Lock()
				names = append(names, pool.Config().ConnConfig.Database)
				mu.Unlock()
				reconnect, err := pgxpool.New(ctx, pool.Config().ConnString())
				if err != nil {
					t.Fatal("reconnect isolated test database failed")
				}
				defer reconnect.Close()
				var actual string
				if err := reconnect.QueryRow(ctx, "SELECT current_database()").Scan(&actual); err != nil || actual != pool.Config().ConnConfig.Database {
					t.Fatal("reconnection escaped isolated database")
				}
				var count int
				if err := reconnect.QueryRow(ctx, "SELECT count(*) FROM fixture").Scan(&count); err != nil || count != 0 {
					t.Fatalf("fresh copy contains %d rows: %v", count, err)
				}
				if _, err := pool.Exec(ctx, "INSERT INTO fixture VALUES ('local')"); err != nil {
					t.Fatal(err)
				}
			})
		}
	})
	if fixtureTemplateBuilds.Load() != 1 {
		t.Fatalf("template migrated %d times, want once", fixtureTemplateBuilds.Load())
	}
	if len(names) != 4 {
		t.Fatalf("created %d copies, want 4", len(names))
	}
	seen := map[string]bool{}
	admin, err := pgxpool.NewWithConfig(context.Background(), adminConfig(t, "TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal("open test administrator failed")
	}
	defer admin.Close()
	for _, name := range names {
		if seen[name] {
			t.Fatal("parallel tests shared a database")
		}
		seen[name] = true
		var exists bool
		if err := admin.QueryRow(context.Background(), "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname=$1)", name).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			t.Fatal("isolated database survived its test cleanup")
		}
	}
}

func TestBlankDatabaseStartsUnmigrated(t *testing.T) {
	pool := Blank(t, "TEST_DATABASE_URL")
	var tables int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("blank database has %d tables: %v", tables, err)
	}
}

func TestTemplateKeysKeepDifferentSchemasApart(t *testing.T) {
	first := Open(t, "TEST_DATABASE_URL", "first", func(ctx context.Context, pool *pgxpool.Pool) error {
		_, err := pool.Exec(ctx, "CREATE TABLE first_fixture (id int)")
		return err
	})
	second := Open(t, "TEST_DATABASE_URL", "second", func(ctx context.Context, pool *pgxpool.Pool) error {
		_, err := pool.Exec(ctx, "CREATE TABLE second_fixture (id int)")
		return err
	})
	for _, pool := range []*pgxpool.Pool{first, second} {
		var count int
		if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'").Scan(&count); err != nil || count != 1 {
			t.Fatalf("isolated template contains %d tables: %v", count, err)
		}
	}
}

// A developer may configure a default search path in the administrator DSN.
// It must not be restored when the isolated database's ConnString is rebuilt.
func TestConfiguredSearchPathDoesNotEscapeTemplateSchema(t *testing.T) {
	_ = adminConfig(t, "TEST_DATABASE_URL")
	dsn := os.Getenv("TEST_DATABASE_URL")
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("parse fixture database URL")
		}
		query := parsed.Query()
		query.Set("search_path", "cx_missing_schema")
		query.Set("pool_max_conns", "3")
		query.Set("options", "-c search_path=cx_missing_schema")
		parsed.RawQuery = query.Encode()
		dsn = parsed.String()
	} else {
		dsn += " search_path=cx_missing_schema options='-c search_path=cx_missing_schema' pool_max_conns=3"
	}
	t.Setenv("CX_RUNNER_DATABASE_FIXTURE", dsn)
	pool := Open(t, "CX_RUNNER_DATABASE_FIXTURE", "search-path-fixture", func(ctx context.Context, pool *pgxpool.Pool) error {
		_, err := pool.Exec(ctx, "CREATE TABLE fixture (id int)")
		return err
	})
	var schema string
	if err := pool.QueryRow(context.Background(), "SELECT current_schema()").Scan(&schema); err != nil || schema != "public" {
		t.Fatalf("isolated schema = %q: %v", schema, err)
	}
	var count int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM fixture").Scan(&count); err != nil || count != 0 {
		t.Fatalf("read isolated template table count=%d: %v", count, err)
	}
}
