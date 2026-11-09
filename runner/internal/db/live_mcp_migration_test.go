package db_test

import (
	"os"
	"testing"
)

func TestLiveMCPMigrationCanBeReapplied(t *testing.T) {
	pool := connect(t)
	body, err := os.ReadFile("migrations/155_live_mcp_decision_outbox.sql")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := pool.Exec(t.Context(), string(body)); err != nil {
			t.Fatal(err)
		}
	}
	var triggers int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM pg_trigger WHERE tgrelid='bound_asks'::regclass AND tgname='live_mcp_decision_enqueue'`).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if triggers != 1 {
		t.Fatalf("decision outbox triggers: %d", triggers)
	}
}
