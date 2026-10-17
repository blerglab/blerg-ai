package server

import (
	"context"
	"os"
	"testing"
)

func TestServerDatabaseFixturesKeepConcurrentConnectionsIsolated(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping database integration tests")
	}
	names := make(chan string, 2)
	t.Run("simultaneous fixtures", func(t *testing.T) {
		for range 2 {
			t.Run("fixture", func(t *testing.T) {
				t.Parallel()
				pool := connectSrvTestDB(t)
				ctx := context.Background()
				first, err := pool.Acquire(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer first.Release()
				second, err := pool.Acquire(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer second.Release()
				var database, schema string
				if err := first.QueryRow(ctx, "SELECT current_database(), current_schema()").Scan(&database, &schema); err != nil {
					t.Fatal(err)
				}
				if schema != "test_blerg_runner_srv" {
					t.Fatalf("first connection schema = %q", schema)
				}
				names <- database
				// Both fixtures intentionally create the same table. Sharing either the
				// administrator database or a schema would make the second CREATE fail.
				if _, err := first.Exec(ctx, "CREATE TABLE fixture_connection_marker (value int); INSERT INTO fixture_connection_marker VALUES (7)"); err != nil {
					t.Fatal(err)
				}
				var value int
				if err := second.QueryRow(ctx, "SELECT value FROM fixture_connection_marker").Scan(&value); err != nil || value != 7 {
					t.Fatalf("second connection cannot read its fixture: value=%d err=%v", value, err)
				}
			})
		}
	})
	if len(names) != 2 {
		t.Fatal("both concurrent fixtures must finish")
	}
	first, second := <-names, <-names
	if first == second {
		t.Fatal("concurrent fixtures shared a database")
	}
}
