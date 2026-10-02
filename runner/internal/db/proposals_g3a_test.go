package db_test

// Round-2 fixes to the proposal store: paging, caps per session and cron, recording an outcome
// the database would reject, retention, and the age check of the approval.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestFinishProposalAlwaysRecordsTheTransition(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	for name, result := range map[string]json.RawMessage{
		"NUL escape":       json.RawMessage(`{"content":[{"type":"text","text":"a\u0000b"}]}`),
		"not json":         json.RawMessage(`<html>`),
		"truncated json":   json.RawMessage(`{"content":[`),
		"NUL in error":     json.RawMessage(`{"error":"bad\u0000thing"}`),
		"raw NUL in bytes": json.RawMessage("{\"error\":\"a\x00b\"}"),
	} {
		t.Run(name, func(t *testing.T) {
			p, err := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), 50)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.BeginProposalExecution(ctx, pool, "acct-a", p.ID, "acct-a"); err != nil {
				t.Fatal(err)
			}
			for _, state := range []string{db.ProposalDone} {
				got, err := db.FinishProposal(ctx, pool, p.ID, state, result)
				if err != nil {
					t.Fatalf("the transition must be recorded even when the result cannot be stored: %v", err)
				}
				if got.State != state || !json.Valid(got.Result) || !strings.Contains(string(got.Result), "result could not be stored") {
					t.Errorf("got %s / %s", got.State, got.Result)
				}
			}
		})
	}
	// A literal backslash followed by u0000 is ordinary text and is stored as given.
	p, _ := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), 50)
	_, _ = db.BeginProposalExecution(ctx, pool, "acct-a", p.ID, "acct-a")
	got, err := db.FinishProposal(ctx, pool, p.ID, db.ProposalFailed, json.RawMessage(`{"error":"a\\u0000b"}`))
	if err != nil || !strings.Contains(string(got.Result), "a") || strings.Contains(string(got.Result), "could not be stored") {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestApproveCASRefusesAStaleProposal(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	p, _ := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), 50)
	if _, err := pool.Exec(ctx, `UPDATE mcp_proposals SET created_at = now() - interval '8 days' WHERE id = $1`, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginProposalExecution(ctx, pool, "acct-a", p.ID, "acct-a"); !errors.Is(err, db.ErrProposalState) {
		t.Fatalf("a proposal past its expiry window but not yet swept must not start executing: %v", err)
	}
	if got, _ := db.GetOwnedProposal(ctx, pool, "acct-a", p.ID); got.State != db.ProposalPending {
		t.Errorf("state = %s", got.State)
	}
	fresh, _ := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), 50)
	if _, err := pool.Exec(ctx, `UPDATE mcp_proposals SET created_at = now() - interval '6 days 23 hours' WHERE id = $1`, fresh.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.BeginProposalExecution(ctx, pool, "acct-a", fresh.ID, "acct-a"); err != nil {
		t.Errorf("inside the window: %v", err)
	}
}

func TestPruneNeverDeletesOpenOrUnknownProposals(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	mk := func(state string) string {
		p, err := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), 500)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE mcp_proposals SET state = $2, decided_at = now() - interval '90 days',
			created_at = now() - interval '91 days' WHERE id = $1`, p.ID, state); err != nil {
			t.Fatal(err)
		}
		return p.ID
	}
	keep := map[string]string{
		"pending": mk("pending"), "executing": mk("executing"), "unknown": mk("unknown"),
	}
	gone := map[string]string{"done": mk("done"), "rejected": mk("rejected"), "expired": mk("expired"), "failed": mk("failed")}
	n, err := db.PruneDecidedProposals(ctx, pool, 30*24*time.Hour)
	if err != nil || n != 4 {
		t.Fatalf("pruned %d, %v; want exactly the 4 decided ones", n, err)
	}
	for state, id := range keep {
		if _, err := db.GetOwnedProposal(ctx, pool, "acct-a", id); err != nil {
			t.Errorf("%s proposal was pruned: %v", state, err)
		}
	}
	for state, id := range gone {
		if _, err := db.GetOwnedProposal(ctx, pool, "acct-a", id); !errors.Is(err, db.ErrProposalNotFound) {
			t.Errorf("%s proposal was not pruned: %v", state, err)
		}
	}
}

func TestListOpenIsUnpagedAndPendingFirst(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	var pending []string
	for range 5 {
		p, _ := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), 500)
		pending = append(pending, p.ID)
	}
	// 230 decided proposals, all NEWER than the open ones: a single capped, newest-first list
	// would bury every open one.
	for i := range 230 {
		p, _ := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), 500)
		if _, err := pool.Exec(ctx, `UPDATE mcp_proposals SET state = 'done', decided_at = now(), created_at = now() + make_interval(secs => $2) WHERE id = $1`, p.ID, float64(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	ex, _ := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), 500)
	unk, _ := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), 500)
	if _, err := pool.Exec(ctx, `UPDATE mcp_proposals SET state = 'executing' WHERE id = $1`, ex.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE mcp_proposals SET state = 'unknown' WHERE id = $1`, unk.ID); err != nil {
		t.Fatal(err)
	}
	_, _ = db.InsertProposal(ctx, pool, newProposal("acct-b", propConn1), 500) // another account

	open, err := db.ListOpenProposals(ctx, pool, "acct-a", "")
	if err != nil || len(open) != 7 {
		t.Fatalf("open = %d, %v; want 7", len(open), err)
	}
	for i, p := range open {
		if i < 5 && p.State != db.ProposalPending || p.State == db.ProposalDone {
			t.Errorf("row %d is %s: pending rows come first and decided ones never appear", i, p.State)
		}
	}
	if open[5].State != db.ProposalExecuting && open[6].State != db.ProposalExecuting {
		t.Error("the executing proposal is missing")
	}
	if only, _ := db.ListOpenProposals(ctx, pool, "acct-a", db.ProposalUnknown); len(only) != 1 || only[0].ID != unk.ID {
		t.Errorf("state filter = %+v", only)
	}
	_ = pending
}

func TestListDecidedPagesByCursor(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	ids := make([]string, 0, 25)
	for i := range 25 {
		p, _ := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), 500)
		st := "done"
		if i%5 == 0 {
			st = "rejected"
		}
		// Three rows share one created_at: the id breaks the tie, so nothing repeats or skips.
		ts := float64(100 - i/3)
		if _, err := pool.Exec(ctx, `UPDATE mcp_proposals SET state = $2, decided_at = now(), created_at = now() - make_interval(secs => $3::float8) WHERE id = $1`,
			p.ID, st, ts); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	open, _ := db.InsertProposal(ctx, pool, newProposal("acct-a", propConn1), 500)
	_, _ = db.InsertProposal(ctx, pool, newProposal("acct-b", propConn1), 500)

	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		rows, next, err := db.ListDecidedProposals(ctx, pool, "acct-a", "", cursor, 10)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, r := range rows {
			if seen[r.ID] {
				t.Fatalf("%s listed twice", r.ID)
			}
			if r.ID == open.ID || r.State == db.ProposalPending {
				t.Fatalf("an open proposal appeared among the decided ones")
			}
			seen[r.ID] = true
		}
		if next == "" {
			break
		}
		if len(rows) != 10 {
			t.Fatalf("a non-final page has %d rows", len(rows))
		}
		cursor = next
		if pages > 5 {
			t.Fatal("paging does not end")
		}
	}
	if len(seen) != 25 || pages != 3 {
		t.Errorf("saw %d rows in %d pages, want 25 in 3", len(seen), pages)
	}
	rej, next, err := db.ListDecidedProposals(ctx, pool, "acct-a", db.ProposalRejected, "", 100)
	if err != nil || len(rej) != 5 || next != "" {
		t.Errorf("rejected = %d, next %q, %v", len(rej), next, err)
	}
	if _, _, err := db.ListDecidedProposals(ctx, pool, "acct-a", "", "garbage", 10); !errors.Is(err, db.ErrProposalCursor) {
		t.Errorf("a malformed cursor: %v", err)
	}
	_ = ids
}

func TestInsertCapsPerSessionAndPerCron(t *testing.T) {
	pool := proposalTestPool(t)
	ctx := context.Background()
	var daemon string
	if err := pool.QueryRow(ctx, `INSERT INTO daemons (name, mode, repos_root) VALUES ('d', 'host', '/r') RETURNING id::text`).Scan(&daemon); err != nil {
		t.Fatal(err)
	}
	cron := "99999999-9999-4999-8999-999999999999"
	session := func(cronID string) string {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO sessions (daemon_id, project_path, repo, cron_id) VALUES ($1::uuid, '/p', 'r', NULLIF($2, '')::uuid) RETURNING id::text`,
			daemon, cronID).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	lim := db.ProposalLimits{Account: 50, Session: 3, Cron: 5}
	put := func(sid string) error {
		p := newProposal("acct-a", propConn1)
		p.SessionID = sid
		_, err := db.InsertProposalLimited(ctx, pool, p, lim)
		return err
	}
	s1, s2, s3 := session(cron), session(cron), session("")
	for range 3 {
		if err := put(s1); err != nil {
			t.Fatal(err)
		}
	}
	if err := put(s1); !errors.Is(err, db.ErrProposalSessionCap) {
		t.Fatalf("4th on one session: %v, want ErrProposalSessionCap", err)
	}
	if err := put(s2); err != nil {
		t.Fatalf("another session of the same cron: %v", err)
	}
	if err := put(s2); err != nil {
		t.Fatal(err)
	}
	if err := put(s2); !errors.Is(err, db.ErrProposalCronCap) {
		t.Fatalf("6th for the cron: %v, want ErrProposalCronCap", err)
	}
	if err := put(s3); err != nil {
		t.Fatalf("a session with no cron is not blocked by one: %v", err)
	}
	// Deciding one frees a slot.
	rows, _ := db.ListOpenProposals(ctx, pool, "acct-a", "")
	for _, r := range rows {
		if r.SessionID == s1 {
			_, _ = db.RejectProposal(ctx, pool, "acct-a", r.ID, "acct-a")
			break
		}
	}
	if err := put(s2); err != nil {
		t.Errorf("after a decision the cap should free up: %v", err)
	}
	// The account cap still applies and is reported as such.
	acct := db.ProposalLimits{Account: 2}
	for range 2 {
		if _, err := db.InsertProposalLimited(ctx, pool, newProposal("acct-z", propConn1), acct); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.InsertProposalLimited(ctx, pool, newProposal("acct-z", propConn1), acct); !errors.Is(err, db.ErrProposalCap) {
		t.Errorf("account cap: %v", err)
	}
}
