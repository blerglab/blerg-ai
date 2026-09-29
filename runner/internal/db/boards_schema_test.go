package db_test

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// TestMigration008Schema verifies that migration 008_boards.sql creates the
// expected tables, columns, and FK/ON DELETE rules for the ticket-board feature.
func TestMigration008Schema(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	setupSchema(t, pool)

	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	// ── 1. All new tables exist ───────────────────────────────────────────────
	tables := []string{
		"boards", "board_repos", "columns", "tickets",
		"ticket_repos", "ticket_tags", "ticket_dependencies",
		"ticket_events", "board_tokens",
	}
	for _, tbl := range tables {
		var exists bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables
				WHERE table_schema = current_schema()
				AND table_name = $1
			)
		`, tbl).Scan(&exists); err != nil {
			t.Fatalf("check table %s: %v", tbl, err)
		}
		if !exists {
			t.Errorf("expected table %q to exist", tbl)
		}
	}

	// ── 2. tickets.column_id is nullable ─────────────────────────────────────
	var isNullable string
	if err := pool.QueryRow(ctx, `
		SELECT is_nullable FROM information_schema.columns
		WHERE table_schema = current_schema()
		AND table_name = 'tickets' AND column_name = 'column_id'
	`).Scan(&isNullable); err != nil {
		t.Fatalf("check tickets.column_id nullability: %v", err)
	}
	if isNullable != "YES" {
		t.Errorf("tickets.column_id: is_nullable = %q, want YES", isNullable)
	}

	// ── 3. board_tokens.token_hash column exists ──────────────────────────────
	var tokenHashExists bool
	if err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema()
			AND table_name = 'board_tokens' AND column_name = 'token_hash'
		)
	`).Scan(&tokenHashExists); err != nil {
		t.Fatalf("check board_tokens.token_hash: %v", err)
	}
	if !tokenHashExists {
		t.Errorf("expected board_tokens.token_hash column to exist")
	}

	// ── 4. sessions gains board_id, ticket_id, assist columns ────────────────
	for _, col := range []string{"board_id", "ticket_id", "assist"} {
		var colExists bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = current_schema()
				AND table_name = 'sessions' AND column_name = $1
			)
		`, col).Scan(&colExists); err != nil {
			t.Fatalf("check sessions.%s: %v", col, err)
		}
		if !colExists {
			t.Errorf("expected sessions.%s column to exist", col)
		}
	}

	// ── 5. FK ON DELETE rules (pg_constraint.confdeltype) ────────────────────
	// confdeltype codes: 'a'=NO ACTION, 'r'=RESTRICT, 'c'=CASCADE, 'n'=SET NULL
	type fkCheck struct {
		table      string
		column     string
		wantAction string
		desc       string
	}
	checks := []fkCheck{
		// Board child tables cascade on board deletion.
		{"board_repos", "board_id", "c", "board_repos.board_id ON DELETE CASCADE"},
		{"columns", "board_id", "c", "columns.board_id ON DELETE CASCADE"},
		{"tickets", "board_id", "c", "tickets.board_id ON DELETE CASCADE"},
		// column_id is RESTRICT (live tickets block column deletion).
		{"tickets", "column_id", "r", "tickets.column_id ON DELETE RESTRICT"},
		// session_id is SET NULL (session end/delete doesn't destroy the ticket).
		{"tickets", "session_id", "n", "tickets.session_id ON DELETE SET NULL"},
		// Ticket child tables cascade on ticket deletion.
		{"ticket_repos", "ticket_id", "c", "ticket_repos.ticket_id ON DELETE CASCADE"},
		{"ticket_tags", "ticket_id", "c", "ticket_tags.ticket_id ON DELETE CASCADE"},
		{"ticket_dependencies", "ticket_id", "c", "ticket_dependencies.ticket_id ON DELETE CASCADE"},
		{"ticket_events", "ticket_id", "c", "ticket_events.ticket_id ON DELETE CASCADE"},
		// board_tokens cascades on board or session deletion.
		{"board_tokens", "board_id", "c", "board_tokens.board_id ON DELETE CASCADE"},
		{"board_tokens", "session_id", "c", "board_tokens.session_id ON DELETE CASCADE"},
	}
	for _, ck := range checks {
		var action string
		err := pool.QueryRow(ctx, `
			SELECT con.confdeltype
			FROM pg_constraint con
			JOIN pg_class rel ON rel.oid = con.conrelid
			JOIN pg_namespace ns ON ns.oid = rel.relnamespace
			JOIN pg_attribute att
			    ON att.attrelid = con.conrelid
			    AND att.attnum = con.conkey[1]
			WHERE ns.nspname = current_schema()
			AND rel.relname = $1
			AND att.attname = $2
			AND con.contype = 'f'
		`, ck.table, ck.column).Scan(&action)
		if err != nil {
			t.Errorf("check FK %s: %v", ck.desc, err)
			continue
		}
		if action != ck.wantAction {
			t.Errorf("FK %s: confdeltype = %q, want %q", ck.desc, action, ck.wantAction)
		}
	}

	// ── 6. Required indexes exist ─────────────────────────────────────────────
	type idxCheck struct {
		table string
		index string
	}
	idxChecks := []idxCheck{
		{"columns", "columns_board_id_rank_idx"},
		{"tickets", "tickets_board_id_column_id_rank_idx"},
		{"ticket_tags", "ticket_tags_tag_idx"},
	}
	for _, ic := range idxChecks {
		var idxExists bool
		if err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM pg_indexes
				WHERE schemaname = current_schema()
				AND tablename = $1
				AND indexname = $2
			)
		`, ic.table, ic.index).Scan(&idxExists); err != nil {
			t.Fatalf("check index %s on %s: %v", ic.index, ic.table, err)
		}
		if !idxExists {
			t.Errorf("expected index %q on table %q to exist", ic.index, ic.table)
		}
	}
}
