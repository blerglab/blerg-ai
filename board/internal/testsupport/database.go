// Package testsupport provides isolated PostgreSQL fixtures for Board tests.
package testsupport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var databases struct {
	sync.Mutex
	admin    *pgxpool.Pool
	config   *pgxpool.Config
	template string
	run      *fixtureRun
}

func identifier() string {
	var suffix [12]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		panic("cannot generate test database identity")
	}
	return "cx_board_" + hex.EncodeToString(suffix[:])
}

// databaseConfig also updates the serialized connection string used by tests
// that reconnect. Editing ConnConfig.Database alone leaves that string stale.
func databaseConfig(t *testing.T, base *pgxpool.Config, name string) *pgxpool.Config {
	t.Helper()
	dsn := base.ConnString()
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("parse test database URL failed")
		}
		parsed.Path, parsed.RawPath = "/"+name, ""
		query := parsed.Query()
		query.Set("dbname", name)
		query.Del("search_path")
		query.Del("options")
		parsed.RawQuery = query.Encode()
		dsn = parsed.String()
	} else {
		dsn += " dbname=" + name + " search_path=public options=''"
	}
	parsed, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("parse isolated test database configuration failed")
	}
	config := base.Copy()
	config.ConnConfig = parsed.ConnConfig
	return config
}

// Pool migrates one private template per package process, then copies it into a
// unique database per call. TEST_DATABASE_URL selects an admin-capable server
// connection; its database is never migrated, truncated, or dropped.
// Call Run from TestMain so the closed template is removed when tests finish.
func Pool(t *testing.T, migrate func(context.Context, *pgxpool.Pool) error) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB integration test")
	}
	databases.Lock()
	defer databases.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if databases.admin == nil {
		config, err := pgxpool.ParseConfig(url)
		if err != nil {
			t.Fatal("parse TEST_DATABASE_URL failed")
		}
		delete(config.ConnConfig.RuntimeParams, "search_path")
		delete(config.ConnConfig.RuntimeParams, "options")
		adminConfig := config.Copy()
		adminConfig.MaxConns = 2
		admin, err := pgxpool.NewWithConfig(ctx, adminConfig)
		if err != nil {
			t.Fatal("open test database administration pool failed")
		}
		run, err := startFixtureRun(ctx, config)
		if err != nil {
			admin.Close()
			t.Fatal("retain test database run lease failed")
		}
		name := identifier()
		if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE template0 STRATEGY WAL_LOG"); err != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, _ = admin.Exec(cleanupCtx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
			cleanupCancel()
			admin.Close()
			t.Fatalf("create test migration template: %v", err)
		}
		if err := run.mark(ctx, admin, name); err != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, _ = admin.Exec(cleanupCtx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
			cleanupCancel()
			admin.Close()
			t.Fatal("mark test migration template failed")
		}
		build := databaseConfig(t, config, name)
		pool, err := pgxpool.NewWithConfig(ctx, build)
		if err == nil {
			err = migrate(ctx, pool)
			pool.Close() // A template must have no connections while PostgreSQL copies it.
		}
		if err != nil {
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
			_, _ = admin.Exec(cleanupCtx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
			cleanupCancel()
			admin.Close()
			t.Fatalf("build test migration template: %v", err)
		}
		databases.admin, databases.config, databases.template, databases.run = admin, config, name, run
	}
	name := identifier()
	admin := databases.admin
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Errorf("drop isolated test database: %v", err)
		}
	})
	if _, err := databases.admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE "+pgx.Identifier{databases.template}.Sanitize()+" STRATEGY WAL_LOG"); err != nil {
		t.Fatalf("copy test database template: %v", err)
	}
	if err := databases.run.mark(ctx, admin, name); err != nil {
		t.Fatal("mark isolated test database failed")
	}
	config := databaseConfig(t, databases.config, name)
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open isolated test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// Run wraps a package's tests and removes its migration template even after a
// test failure. Test databases are removed earlier by each test's cleanup.
func Run(m *testing.M) int {
	code := m.Run()
	databases.Lock()
	defer databases.Unlock()
	if databases.admin != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		_, err := databases.admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{databases.template}.Sanitize()+" WITH (FORCE)")
		cancel()
		databases.admin.Close()
		if err != nil {
			fmt.Fprintln(os.Stderr, "remove test migration template failed")
			code = 1
		}
	}
	if err := closeFixtureRuns(); err != nil {
		fmt.Fprintln(os.Stderr, "release test database run lease failed")
		code = 1
	}
	return code
}
