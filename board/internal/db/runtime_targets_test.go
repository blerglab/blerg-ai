package db_test

import (
	"github.com/blerglab/blerg-ai/board/internal/db"
	"os"
	"testing"
)

func TestRuntimeTargetDefaultCompatibility(t *testing.T) {
	p := testPool(t)
	b := mkBoard(t, p, db.BoardParams{})
	if b.RuntimeTargetID != nil {
		t.Fatalf("fresh default: %+v", b.RuntimeTargetID)
	}
	ctx := t.Context()
	if _, err := p.Exec(ctx, `UPDATE boards SET run_runtime='cluster',run_daemon_id='' WHERE id=$1`, b.ID); err != nil {
		t.Fatal(err)
	}
	var target string
	if err := p.QueryRow(ctx, `SELECT runtime_target_id FROM boards WHERE id=$1`, b.ID).Scan(&target); err != nil || target != "cluster" {
		t.Fatalf("old writer: %q %v", target, err)
	}
	if _, err := p.Exec(ctx, `UPDATE boards SET runtime_target_id='missing' WHERE id=$1`, b.ID); err == nil {
		t.Fatal("unknown target accepted")
	}
}

func TestRuntimeTargetUpgradeBackfill(t *testing.T) {
	p := testPool(t)
	cluster := mkBoard(t, p, db.BoardParams{})
	desktop := mkBoard(t, p, db.BoardParams{})
	implicit := mkBoard(t, p, db.BoardParams{})
	ctx := t.Context()
	if _, err := p.Exec(ctx, `DROP FUNCTION fill_board_runtime_target() CASCADE;
 DROP FUNCTION fill_board_session_runtime_target() CASCADE;
 ALTER TABLE boards DROP COLUMN runtime_target_id;
 ALTER TABLE runner_sessions DROP COLUMN runtime_target_id;`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE boards SET run_runtime='cluster' WHERE id=$1`, cluster.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, `UPDATE boards SET run_runtime='docker',run_daemon_id='desktop' WHERE id=$1`, desktop.ID); err != nil {
		t.Fatal(err)
	}
	card, err := db.CreateCard(ctx, p, implicit.ID, db.CardParams{Title: strp("Card")}, human)
	if err != nil {
		t.Fatal(err)
	}
	for _, rt := range []string{"", "cluster", "docker"} {
		if _, err := p.Exec(ctx, `INSERT INTO runner_sessions(card_id,board_id,runner,external_session_id,lifecycle,role,target_runtime) VALUES($1,$2,'fixture',$3,'running','worker',$3)`, card.Card.ID, implicit.ID, rt); err != nil {
			t.Fatal(err)
		}
	}
	migration, err := os.ReadFile("migrations/039_runtime_targets.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	var bad int
	if err := p.QueryRow(ctx, `SELECT count(*) FROM runner_sessions WHERE (target_runtime='cluster') IS DISTINCT FROM (runtime_target_id='cluster' AND runtime_target_id IS NOT NULL)`).Scan(&bad); err != nil || bad != 0 {
		t.Fatalf("session backfill mismatches: %d %v", bad, err)
	}
	got, err := db.GetBoard(ctx, p, cluster.ID)
	if err != nil || got.RuntimeTargetID == nil || *got.RuntimeTargetID != "cluster" {
		t.Fatalf("cluster backfill: %+v %v", got.RuntimeTargetID, err)
	}
	got, err = db.GetBoard(ctx, p, implicit.ID)
	if err != nil || got.RuntimeTargetID != nil {
		t.Fatalf("implicit board: %v %v", got.RuntimeTargetID, err)
	}
	got, err = db.GetBoard(ctx, p, desktop.ID)
	if err != nil || got.RuntimeTargetID != nil {
		t.Fatalf("desktop received cluster target: %+v %v", got.RuntimeTargetID, err)
	}
}

func TestRuntimeTargetOnlyExplicitClusterSessions(t *testing.T) {
	p := testPool(t)
	b := mkBoard(t, p, db.BoardParams{})
	c, err := db.CreateCard(t.Context(), p, b.ID, db.CardParams{Title: strp("Card")}, human)
	if err != nil {
		t.Fatal(err)
	}
	for _, rt := range []string{"", "cluster", "docker"} {
		var target *string
		err := p.QueryRow(t.Context(), `INSERT INTO runner_sessions(card_id,board_id,runner,external_session_id,lifecycle,role,target_runtime) VALUES($1,$2,'fixture',$3,'running','worker',$3) RETURNING runtime_target_id`, c.Card.ID, b.ID, rt).Scan(&target)
		if err != nil || (rt == "cluster" && (target == nil || *target != "cluster")) || (rt != "cluster" && target != nil) {
			t.Fatalf("runtime %q target %v: %v", rt, target, err)
		}
	}
}
