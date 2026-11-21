package db_test

import (
	"context"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestSessionBranchImmutableAndUnique(t *testing.T) {
	pool := artifactTestPool(t)
	ctx := context.Background()
	daemon := "10000000-0000-0000-0000-000000000001"
	if err := db.UpsertDaemon(ctx, pool, daemon, "test", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	ids := []string{"20000000-0000-0000-0000-000000000001", "20000000-0000-0000-0000-000000000002"}
	branches := []string{}
	for _, id := range ids {
		if err := db.InsertSession(ctx, pool, id, daemon, "starting", "/repos/test", "test", "Test", ""); err != nil {
			t.Fatal(err)
		}
		branch, err := db.AssignSessionBranch(ctx, pool, id, "dev/person/test")
		if err != nil {
			t.Fatal(err)
		}
		branches = append(branches, branch)
		again, err := db.AssignSessionBranch(ctx, pool, id, "changed/name")
		if err != nil || again != branch {
			t.Fatalf("reassigned %q to %q: %v", branch, again, err)
		}
		row, err := db.GetSession(ctx, pool, id)
		if err != nil || row.WorkBranch != branch {
			t.Fatalf("recorded branch = %#v, %v", row, err)
		}
	}
	if branches[0] == branches[1] {
		t.Fatalf("collision: %v", branches)
	}
	rows, err := db.ListSessions(ctx, pool)
	if err != nil || len(rows) != 2 {
		t.Fatalf("list = %v, %v", rows, err)
	}
	for _, row := range rows {
		if row.WorkBranch == "" {
			t.Fatal("list lost branch")
		}
	}
}

func TestSessionBranchReservationSurvivesDiscard(t *testing.T) {
	pool := artifactTestPool(t)
	ctx := context.Background()
	const daemon = "10000000-0000-0000-0000-000000000255"
	const first = "20000000-0000-0000-0000-000000000255"
	const second = "20000000-0000-0000-0000-000000000256"
	if err := db.UpsertDaemon(ctx, pool, daemon, "test", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	insert := func(id string) {
		t.Helper()
		if err := db.InsertSession(ctx, pool, id, daemon, "starting", "/repos/test", "test", "Test", ""); err != nil {
			t.Fatal(err)
		}
	}
	insert(first)
	one, err := db.AssignSessionBranch(ctx, pool, first, "dev/person/test", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteSession(ctx, pool, first); err != nil {
		t.Fatal(err)
	}
	insert(second)
	two, err := db.AssignSessionBranch(ctx, pool, second, "dev/person/test")
	if err != nil {
		t.Fatal(err)
	}
	if one == two {
		t.Fatalf("discard reused the old branch %q", one)
	}
	insert(first)
	restored, err := db.AssignSessionBranch(ctx, pool, first, "changed/name")
	if err != nil || restored != one {
		t.Fatalf("restored identity branch=%q, %v; want %q", restored, err, one)
	}
	row, err := db.GetSession(ctx, pool, first)
	if err != nil || row == nil || !row.WorkBranchCustom {
		t.Fatalf("reservation lost template intent: %+v, %v", row, err)
	}
}
