package server

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCutBoardBinaryShared(t *testing.T) {
	var first string
	t.Run("first", func(t *testing.T) { first = buildCutBoardBinary(t) })
	t.Run("later", func(t *testing.T) {
		if _, err := os.Stat(first); err != nil {
			t.Fatalf("shared executable did not survive its first caller: %v", err)
		}
		if got := buildCutBoardBinary(t); got != first {
			t.Fatal("later caller rebuilt the shared executable")
		}
	})
}

func TestBoundProcessDatabaseLifetime(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	names := map[string]bool{}
	for range 2 {
		env := boundProcessDatabase(t, []string{"BLERG_BOUND_SUBJECT=plan"})
		cfg, stop := startBoundProcess(t, env)
		name := cfg["fixture_database"]
		if name == "" || names[name] {
			t.Fatal("sibling process fixtures must have distinct databases")
		}
		names[name] = true
		stop()
		config, err := pgxpool.ParseConfig(os.Getenv("TEST_DATABASE_URL"))
		if err != nil {
			t.Fatalf("parse fixture administrator: %T", err)
		}
		config.ConnConfig.Database = name
		pool, err := pgxpool.NewWithConfig(t.Context(), config)
		if err != nil {
			t.Fatalf("open stopped fixture database: %T", err)
		}
		var sessions int
		err = pool.QueryRow(t.Context(), "SELECT count(*) FROM test_blerg_runner_srv.sessions").Scan(&sessions)
		pool.Close()
		if err != nil {
			t.Fatalf("database must survive child shutdown until owning case cleanup: %T", err)
		}
		if sessions == 0 {
			t.Fatal("child did not populate parent-owned database")
		}
	}
}
