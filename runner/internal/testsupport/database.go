// Package testsupport isolates PostgreSQL fixtures without migrating every test.
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

var databases = struct {
	sync.Mutex
	templates map[string]string
	cleanup   []func() error
}{templates: make(map[string]string)}

// Run removes process-local templates after all tests, including parallel tests.
// Every package using Open or Blank must call Run from its TestMain.
func Run(m *testing.M) int {
	code := m.Run()
	databases.Lock()
	defer databases.Unlock()
	for i := len(databases.cleanup) - 1; i >= 0; i-- {
		if err := databases.cleanup[i](); err != nil {
			fmt.Fprintln(os.Stderr, "remove test database template failed")
			code = 1
		}
	}
	if err := closeFixtureRuns(); err != nil {
		fmt.Fprintln(os.Stderr, "release test database run lease failed")
		code = 1
	}
	return code
}

func databaseName() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic("generate test database identity failed")
	}
	return "cx_runner_" + hex.EncodeToString(value[:])
}

func adminConfig(t testing.TB, env string) *pgxpool.Config {
	t.Helper()
	dsn := os.Getenv(env)
	if dsn == "" {
		t.Skip(env + " not set; skipping database tests")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("parse test database administrator configuration failed")
	}
	cfg.MaxConns = 2
	delete(cfg.ConnConfig.RuntimeParams, "search_path")
	delete(cfg.ConnConfig.RuntimeParams, "options")
	return cfg
}

func drop(cfg *pgxpool.Config, name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg.Copy())
	if err != nil {
		return err
	}
	defer pool.Close()
	_, err = pool.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
	return err
}

// Open migrates a uniquely named template once per key/configuration in this
// package process and clones it for each call. The configured administrator
// database is never changed. PostgreSQL's template connection restriction is
// met by closing the migration pool before the first copy; the mutex serializes
// template builds and copies. Other package processes use independent names.
func Open(t testing.TB, env, key string, migrate func(context.Context, *pgxpool.Pool) error) *pgxpool.Pool {
	t.Helper()
	if migrate == nil {
		t.Fatal("test migration callback must be set")
	}
	return open(t, env, key, migrate)
}

// Blank gives migration tests an empty independent database instead of a copy
// of the migrated template.
func Blank(t testing.TB, env string) *pgxpool.Pool {
	t.Helper()
	return open(t, env, "", nil)
}

func open(t testing.TB, env, key string, migrate func(context.Context, *pgxpool.Pool) error) *pgxpool.Pool {
	t.Helper()
	cfg := adminConfig(t, env)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	admin, err := pgxpool.NewWithConfig(ctx, cfg.Copy())
	if err != nil {
		t.Fatal("open test database administrator failed")
	}
	defer admin.Close()
	databases.Lock()
	defer databases.Unlock()
	run, err := startFixtureRun(ctx, cfg)
	if err != nil {
		t.Fatal("retain test database run lease failed")
	}
	template := "template0"
	if migrate != nil {
		identity := cfg.ConnString() + "\x00" + key
		template = databases.templates[identity]
		if template == "" {
			template = databaseName()
			// A failed CREATE response may arrive after PostgreSQL created it.
			databases.cleanup = append(databases.cleanup, func() error { return drop(cfg, template) })
			if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{template}.Sanitize()+" TEMPLATE template0 STRATEGY WAL_LOG"); err != nil {
				t.Fatal("create test migration template failed; database user needs CREATEDB")
			}
			if err := run.mark(ctx, admin, template); err != nil {
				t.Fatal("mark test migration template failed")
			}
			build := cfg.Copy()
			build.ConnConfig.Database = template
			pool, err := pgxpool.NewWithConfig(ctx, build)
			if err != nil {
				t.Fatal("open test migration template failed")
			}
			err = migrate(ctx, pool)
			pool.Close()
			if err != nil {
				t.Fatal("migrate test database template failed")
			}
			databases.templates[identity] = template
		}
	}
	name := databaseName()
	t.Cleanup(func() {
		if err := drop(cfg, name); err != nil {
			t.Error("remove isolated test database failed")
		}
	})
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE "+pgx.Identifier{template}.Sanitize()+" STRATEGY WAL_LOG"); err != nil {
		t.Fatal("copy isolated test database failed; database user needs CREATEDB")
	}
	if err := run.mark(ctx, admin, name); err != nil {
		t.Fatal("mark isolated test database failed")
	}
	cloned := cfg.Copy()
	// Preserve isolation for consumers that reconnect using Config().ConnString().
	dsn := cfg.ConnString()
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("parse test database URL failed")
		}
		parsed.Path = "/" + name
		query := parsed.Query()
		query.Set("dbname", name)
		query.Del("search_path")
		query.Del("options")
		parsed.RawQuery = query.Encode()
		dsn = parsed.String()
	} else {
		dsn += " dbname=" + name + " search_path=public options=''"
	}
	parsedConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("parse isolated test database configuration failed")
	}
	cloned.ConnConfig = parsedConfig.ConnConfig
	cloned.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, cloned)
	if err != nil {
		t.Fatal("open isolated test database failed")
	}
	t.Cleanup(pool.Close)
	return pool
}
