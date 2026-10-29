package db_test

import (
	"os"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// TestDropAgentRulesMigrationOnAnExistingDatabase: an installation that has agent_rules (009) with
// rows in it loses the table when 089 runs, and the migration run is idempotent afterwards.
func TestDropAgentRulesMigrationOnAnExistingDatabase(t *testing.T) {
	pool := connect(t)
	create, err := os.ReadFile("migrations/009_agent_dataplane.sql")
	if err != nil {
		t.Fatal(err)
	}
	ddl := string(create)
	at := strings.Index(ddl, "CREATE TABLE IF NOT EXISTS agent_rules")
	if at < 0 {
		t.Fatal("009 no longer creates agent_rules")
	}
	ddl = ddl[at:]
	if _, err := pool.Exec(t.Context(), ddl); err != nil {
		t.Fatalf("recreate agent_rules as 009 made it: %v", err)
	}
	if _, err := pool.Exec(t.Context(), `INSERT INTO agent_rules (project, content, enabled) VALUES ('proj', 'always run make test', true), ('proj', 'pending', false);
		DELETE FROM schema_migrations WHERE filename='089_drop_agent_rules.sql'`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.RunMigrations(t.Context(), pool); err != nil {
			t.Fatal(err)
		}
	}
	var table *string
	if err := pool.QueryRow(t.Context(), `SELECT to_regclass('agent_rules')::text`).Scan(&table); err != nil {
		t.Fatal(err)
	}
	if table != nil {
		t.Fatalf("agent_rules survived 089: %s", *table)
	}
	var recorded bool
	if err := pool.QueryRow(t.Context(), `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE filename='089_drop_agent_rules.sql')`).Scan(&recorded); err != nil || !recorded {
		t.Fatalf("089 not recorded: %v", err)
	}
}
