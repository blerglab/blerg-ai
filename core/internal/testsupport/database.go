// Package testsupport provides isolated Postgres databases for integration tests.
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
	templates map[string]string
	cleanup   []func() error
}

// Run removes process-local templates after all tests (including parallel tests)
// finish. Every package using Pool or Empty must call this from TestMain.
func Run(m *testing.M) int {
	code := m.Run()
	databases.Lock()
	defer databases.Unlock()
	for i := len(databases.cleanup) - 1; i >= 0; i-- {
		if err := databases.cleanup[i](); err != nil {
			fmt.Fprintln(os.Stderr, "test database cleanup failed")
			code = 1
		}
	}
	if err := closeFixtureRuns(); err != nil {
		fmt.Fprintln(os.Stderr, "release test database run lease failed")
		code = 1
	}
	return code
}

func name() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("test database random identifier failed")
	}
	return "cx_core_" + hex.EncodeToString(b[:])
}

func admin(t testing.TB) *pgxpool.Config {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test database configuration")
	}
	cfg.MaxConns = 2
	delete(cfg.ConnConfig.RuntimeParams, "search_path")
	delete(cfg.ConnConfig.RuntimeParams, "options")
	return cfg
}

func drop(cfg *pgxpool.Config, database string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	p, err := pgxpool.NewWithConfig(ctx, cfg.Copy())
	if err != nil {
		return err
	}
	defer p.Close()
	_, err = p.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{database}.Sanitize()+" WITH (FORCE)")
	return err
}

// Empty creates an unmigrated database for tests that exercise migrations.
func Empty(t testing.TB) *pgxpool.Config { return clone(t, nil) }

// Pool clones a process-local template migrated once by migrate. Its returned
// pool and database are removed by t.Cleanup. No connection stays on the template.
func Pool(t testing.TB, migrate func(context.Context, *pgxpool.Pool) error) *pgxpool.Pool {
	t.Helper()
	cfg := clone(t, migrate)
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal("open isolated test database")
	}
	t.Cleanup(p.Close)
	return p
}

func clone(t testing.TB, migrate func(context.Context, *pgxpool.Pool) error) *pgxpool.Config {
	t.Helper()
	cfg := admin(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	p, err := pgxpool.NewWithConfig(ctx, cfg.Copy())
	if err != nil {
		t.Fatal("open test database administrator")
	}
	defer p.Close()
	databases.Lock()
	defer databases.Unlock()
	run, err := startFixtureRun(ctx, cfg)
	if err != nil {
		t.Fatal("retain test database run lease failed")
	}
	template := "template0"
	if migrate != nil {
		// The full config string separates independent database servers/configurations.
		key := cfg.ConnString()
		if databases.templates == nil {
			databases.templates = make(map[string]string)
		}
		template = databases.templates[key]
		if template == "" {
			template = name()
			databases.cleanup = append(databases.cleanup, func() error { return drop(cfg, template) })
			if _, err := p.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{template}.Sanitize()+" TEMPLATE template0 STRATEGY WAL_LOG"); err != nil {
				t.Fatal("create test database template")
			}
			if err := run.mark(ctx, p, template); err != nil {
				t.Fatal("mark test migration template failed")
			}
			tc := cfg.Copy()
			tc.ConnConfig.Database = template
			tp, err := pgxpool.NewWithConfig(ctx, tc)
			if err != nil {
				t.Fatal("open test database template")
			}
			err = migrate(ctx, tp)
			tp.Close()
			if err != nil {
				t.Fatal("migrate test database template")
			}
			databases.templates[key] = template
		}
	}
	database := name()
	t.Cleanup(func() {
		if err := drop(cfg, database); err != nil {
			t.Error("drop isolated test database")
		}
	})
	if _, err := p.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{database}.Sanitize()+" TEMPLATE "+pgx.Identifier{template}.Sanitize()+" STRATEGY WAL_LOG"); err != nil {
		t.Fatal("clone isolated test database")
	}
	if err := run.mark(ctx, p, database); err != nil {
		t.Fatal("mark isolated test database failed")
	}
	result, err := databaseConfig(cfg, database)
	if err != nil {
		t.Fatal("parse isolated test database configuration")
	}
	return result
}

func databaseConfig(cfg *pgxpool.Config, database string) (*pgxpool.Config, error) {
	cloneConfig := cfg.Copy()
	// ConnString must also identify the clone: tests reconnect using that string.
	dsn := cfg.ConnString()
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return nil, err
		}
		u.Path = "/" + database
		q := u.Query()
		q.Set("dbname", database)
		q.Del("options")
		q.Set("search_path", "public")
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else {
		// Generated database names contain only safe ASCII identifier characters.
		dsn += " dbname=" + database + " search_path=public options=''"
	}
	parsed, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cloneConfig.ConnConfig = parsed.ConnConfig
	return cloneConfig, nil
}
