package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5"
)

const (
	scAcct  = "acct-1"
	scCron  = "0a0a0a0a-0a0a-4a0a-8a0a-0a0a0a0a0a01"
	scCron2 = "0a0a0a0a-0a0a-4a0a-8a0a-0a0a0a0a0a02"
)

func TestSessionCronID(t *testing.T) {
	pool := endTestPool(t)
	ctx := context.Background()
	s1, s2, s3 := "00000000-0000-4000-8000-0000000000a1", "00000000-0000-4000-8000-0000000000a2", "00000000-0000-4000-8000-0000000000a3"
	endTestSession(t, pool, s1, "running")
	endTestSession(t, pool, s2, "ended")
	endTestSession(t, pool, s3, "running") // not a cron session
	for _, id := range []string{s1, s2, s3} {
		if err := db.SetSessionSpawningAccount(ctx, pool, id, scAcct); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetSessionCronID(ctx, pool, s1, scCron); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionCronID(ctx, pool, s2, scCron); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionCronID(ctx, pool, "00000000-0000-4000-8000-0000000000ff", scCron); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("unknown session: err = %v, want ErrNoRows", err)
	}

	row, err := db.GetSession(ctx, pool, s1)
	if err != nil || row == nil || row.CronID == nil || *row.CronID != scCron {
		t.Fatalf("GetSession cron id = %+v, %v", row, err)
	}
	if row, _ := db.GetSession(ctx, pool, s3); row.CronID != nil {
		t.Errorf("non-cron session has cron id %v", *row.CronID)
	}
	all, err := db.ListSessions(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, r := range all {
		if r.ID == s1 && r.CronID != nil && *r.CronID == scCron {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("ListSessions did not carry the cron id (seen %d)", seen)
	}

	n, err := db.CountActiveCronSessions(ctx, pool, scAcct)
	if err != nil || n != 1 {
		t.Errorf("active cron sessions = %d, %v; want 1 (the ended one and the non-cron one do not count)", n, err)
	}
	if n, _ := db.CountActiveCronSessions(ctx, pool, "someone-else"); n != 0 {
		t.Errorf("another account sees %d", n)
	}
	act, err := db.ListActiveCronSessions(ctx, pool, "")
	if err != nil || len(act) != 1 || act[0].SessionID != s1 || act[0].CronID != scCron || act[0].MaxRuntimeSeconds != nil {
		t.Errorf("active = %+v, %v (no cron row exists, so no max runtime)", act, err)
	}
	if act, _ := db.ListActiveCronSessions(ctx, pool, scCron2); len(act) != 0 {
		t.Errorf("another cron's sessions = %+v", act)
	}
	if act, _ := db.ListActiveCronSessions(ctx, pool, scCron); len(act) != 1 {
		t.Errorf("this cron's sessions = %+v", act)
	}
}

func TestDeleteOrphanMCPGrants(t *testing.T) {
	pool := endTestPool(t)
	ctx := context.Background()
	live, dead, missing := "00000000-0000-4000-8000-0000000000b1", "00000000-0000-4000-8000-0000000000b2", "00000000-0000-4000-8000-0000000000b3"
	endTestSession(t, pool, live, "running")
	endTestSession(t, pool, dead, "ended")
	grant := func(sid string, hash byte) db.MCPGrant {
		return db.MCPGrant{
			SessionID: sid, ConnectionID: "00000000-0000-4000-8000-0000000000c1", Name: "calendar", AccountID: scAcct,
			Tools: map[string]db.MCPGrantTool{"echo": {Mode: "allow", Hash: "h"}}, URLSnapshot: "https://x.example/mcp",
			ProofKind: "token_id", ProofValue: "tok", TokenHash: []byte{hash, 1, 2, 3}, CallBudget: 10,
		}
	}
	young := "00000000-0000-4000-8000-0000000000b4"
	if err := db.InsertMCPGrants(ctx, pool, []db.MCPGrant{grant(live, 1), grant(dead, 2), grant(missing, 3), grant(young, 4)}); err != nil {
		t.Fatal(err)
	}
	// MINOR 28: a grant younger than the guard belongs to a session that may be resuming right now
	// (its row not yet back to a live status); only older ones are swept.
	if _, err := pool.Exec(ctx, `UPDATE session_mcp_grants SET created_at = now() - interval '11 minutes' WHERE session_id <> $1`, young); err != nil {
		t.Fatal(err)
	}
	n, err := db.DeleteOrphanMCPGrants(ctx, pool)
	if err != nil || n != 2 {
		t.Fatalf("deleted %d, %v; want 2 (the ended and the missing session; the young grant is spared)", n, err)
	}
	if g, _ := db.ListMCPGrantsForSession(ctx, pool, young); len(g) != 1 {
		t.Errorf("a grant younger than the guard was swept: %d", len(g))
	}
	if _, err := pool.Exec(ctx, `UPDATE session_mcp_grants SET created_at = now() - interval '11 minutes'`); err != nil {
		t.Fatal(err)
	}
	if n, err := db.DeleteOrphanMCPGrants(ctx, pool); err != nil || n != 1 {
		t.Fatalf("once aged, deleted %d, %v; want 1", n, err)
	}
	if g, _ := db.ListMCPGrantsForSession(ctx, pool, live); len(g) != 1 {
		t.Errorf("the live session lost its grant: %d", len(g))
	}
	if n, _ := db.DeleteOrphanMCPGrants(ctx, pool); n != 0 {
		t.Errorf("second pass deleted %d", n)
	}
}
