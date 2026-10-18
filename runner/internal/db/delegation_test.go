package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newUUID(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func mustDaemon(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	id := newUUID(t, pool)
	if err := db.UpsertDaemon(context.Background(), pool, id, "laptop-"+id[:8], "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	return id
}

func mustSession(t *testing.T, pool *pgxpool.Pool, daemon, account string) string {
	t.Helper()
	return mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: account})
}

func mustSessionAs(t *testing.T, pool *pgxpool.Pool, daemon string, o db.SessionOrigin) string {
	t.Helper()
	id := newUUID(t, pool)
	if err := db.InsertSessionAs(context.Background(), pool, id, daemon, "running", "/repos/x", "x", "t", "m", o); err != nil {
		t.Fatal(err)
	}
	return id
}

func endSession(t *testing.T, pool *pgxpool.Pool, id, status, reason string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`UPDATE sessions SET status = $2, end_reason = NULLIF($3, ''), ended_at = now() WHERE id = $1::uuid`, id, status, reason); err != nil {
		t.Fatal(err)
	}
}

func TestDelegationRequestLifecycle(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, "owner-1")
	r := db.DelegationRequest{ParentSessionID: parent, AccountID: "owner-1", DaemonID: daemon,
		IdempotencyKey: "k1", RequestHash: "h1", Request: json.RawMessage(`{"engine":"codex"}`),
		Offered: []string{"once", "session", "always", "deny"}, ExpiresAt: time.Now().Add(10 * time.Minute)}
	got, created, err := db.InsertDelegationRequest(ctx, pool, r)
	if err != nil || !created {
		t.Fatalf("insert: created=%v err=%v", created, err)
	}
	var body map[string]string
	if err := json.Unmarshal(got.Request, &body); err != nil || !reflect.DeepEqual(body, map[string]string{"engine": "codex"}) {
		t.Fatalf("request body: %s %v", got.Request, err)
	}
	if got.Status != "pending" || got.Choice != "" {
		t.Fatalf("row: %+v", got)
	}
	if !reflect.DeepEqual(got.Offered, r.Offered) {
		t.Fatalf("offered: %v", got.Offered)
	}
	again, created2, err := db.InsertDelegationRequest(ctx, pool, r)
	if err != nil || created2 || again.ID != got.ID {
		t.Fatalf("duplicate: created=%v id=%s/%s err=%v", created2, again.ID, got.ID, err)
	}
	byKey, err := db.GetDelegationRequestByKey(ctx, pool, parent, "k1")
	if err != nil || byKey == nil || byKey.ID != got.ID {
		t.Fatalf("by key: %+v %v", byKey, err)
	}
	if missing, err := db.GetDelegationRequest(ctx, pool, newUUID(t, pool)); err != nil || missing != nil {
		t.Fatalf("absent request: %+v %v", missing, err)
	}
	pending, err := db.ListPendingDelegationRequests(ctx, pool, parent)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending: %v %v", pending, err)
	}

	decide := func(account, status, choice string) (db.DelegationRequest, error) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		d, derr := db.DecideDelegationRequestTx(ctx, tx, got.ID, account, status, choice, "owner-1", "sid-1")
		if derr == nil || errors.Is(derr, db.ErrDelegationState) {
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
		return d, derr
	}
	if _, err := decide("someone-else", "allowed", "once"); !errors.Is(err, db.ErrDelegationNotFound) {
		t.Fatalf("wrong account: %v", err)
	}
	d, err := decide("owner-1", "allowed", "once")
	if err != nil || d.Status != "allowed" || d.Choice != "once" || d.DecidedBy != "owner-1" || d.DecidedSid != "sid-1" || d.DecidedAt == nil {
		t.Fatalf("decide: %+v %v", d, err)
	}
	d2, err := decide("owner-1", "denied", "")
	if !errors.Is(err, db.ErrDelegationState) || d2.Status != "allowed" {
		t.Fatalf("second decide: %+v %v", d2, err)
	}
	e, err := db.ExpireDelegationRequest(ctx, pool, got.ID, parent)
	if !errors.Is(err, db.ErrDelegationState) || e.Status != "allowed" {
		t.Fatalf("expire after allowed: %+v %v", e, err)
	}
	if _, err := db.ExpireDelegationRequest(ctx, pool, got.ID, newUUID(t, pool)); !errors.Is(err, db.ErrDelegationNotFound) {
		t.Fatalf("expire for another parent: %v", err)
	}

	child := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "owner-1", ParentSessionID: parent})
	if err := db.SetDelegationRequestStart(ctx, pool, got.ID, child, ""); err != nil {
		t.Fatal(err)
	}
	if cur, _ := db.GetDelegationRequest(ctx, pool, got.ID); cur.ChildSessionID != child || cur.StartError != "" {
		t.Fatalf("start: %+v", cur)
	}
	if err := db.SetDelegationRequestStart(ctx, pool, newUUID(t, pool), "", "x"); !errors.Is(err, db.ErrDelegationNotFound) {
		t.Fatalf("start of absent: %v", err)
	}

	// A denied request has no choice; a past-due one cannot be decided; sweeps expire the rest.
	deny := r
	deny.IdempotencyKey, deny.ExpiresAt = "k2", time.Now().Add(time.Minute)
	dr, _, _ := db.InsertDelegationRequest(ctx, pool, deny)
	tx, _ := pool.Begin(ctx)
	dd, err := db.DecideDelegationRequestTx(ctx, tx, dr.ID, "owner-1", "denied", "", "owner-1", "sid-1")
	if err != nil || dd.Status != "denied" || dd.Choice != "" {
		t.Fatalf("deny: %+v %v", dd, err)
	}
	_ = tx.Commit(ctx)

	late := r
	late.IdempotencyKey, late.ExpiresAt = "k3", time.Now().Add(-time.Second)
	lr, _, _ := db.InsertDelegationRequest(ctx, pool, late)
	tx, _ = pool.Begin(ctx)
	if cur, err := db.DecideDelegationRequestTx(ctx, tx, lr.ID, "owner-1", "allowed", "once", "owner-1", "sid-1"); !errors.Is(err, db.ErrDelegationState) || cur.Status != "pending" {
		t.Fatalf("decide past due: %+v %v", cur, err)
	}
	_ = tx.Rollback(ctx)
	stale, err := db.ExpireStaleDelegationRequests(ctx, pool)
	if err != nil || len(stale) != 1 || stale[0].ID != lr.ID || stale[0].Status != "expired" {
		t.Fatalf("stale sweep: %+v %v", stale, err)
	}

	other := r
	other.IdempotencyKey = "k4"
	or, _, _ := db.InsertDelegationRequest(ctx, pool, other)
	exp, err := db.ExpirePendingDelegationRequestsForParent(ctx, pool, parent)
	if err != nil || len(exp) != 1 || exp[0].ID != or.ID {
		t.Fatalf("expire for parent: %+v %v", exp, err)
	}
	// An expired request cannot be expired again.
	if _, err := db.ExpireDelegationRequest(ctx, pool, or.ID, parent); !errors.Is(err, db.ErrDelegationState) {
		t.Fatalf("re-expire: %v", err)
	}
}

func TestDelegationGrantMatchesExactly(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	otherDaemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, "owner-1")
	insert := func(g db.DelegationGrant) db.DelegationGrant {
		t.Helper()
		g.ParentSessionID, g.AccountID = parent, "owner-1"
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		out, err := db.InsertDelegationGrantTx(ctx, tx, g)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return out
	}
	repoGrant := insert(db.DelegationGrant{Engine: "codex", Runtime: "daemon", DaemonID: daemon, Repo: "blerg"})
	noRepoGrant := insert(db.DelegationGrant{Engine: "codex", Runtime: "daemon", DaemonID: daemon, NoRepo: true})
	cases := []struct {
		name                          string
		engine, runtime, daemonID, rp string
		noRepo                        bool
		want                          string
	}{
		{"exact repo", "codex", "daemon", daemon, "blerg", false, repoGrant.ID},
		{"other repo", "codex", "daemon", daemon, "other", false, ""},
		{"no repo", "codex", "daemon", daemon, "", true, noRepoGrant.ID},
		{"empty repo is not no_repo", "codex", "daemon", daemon, "", false, ""},
		{"other engine", "claude", "daemon", daemon, "blerg", false, ""},
		{"other runtime", "codex", "docker", daemon, "blerg", false, ""},
		{"other daemon", "codex", "daemon", otherDaemon, "blerg", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g, err := db.FindDelegationGrant(ctx, pool, parent, c.engine, c.runtime, c.daemonID, c.rp, c.noRepo)
			if err != nil {
				t.Fatal(err)
			}
			if c.want == "" && g != nil || c.want != "" && (g == nil || g.ID != c.want) {
				t.Fatalf("got %+v, want %q", g, c.want)
			}
		})
	}
	live, err := db.ListLiveDelegationGrants(ctx, pool, parent)
	if err != nil || len(live) != 2 || live[0].ID != repoGrant.ID {
		t.Fatalf("live: %+v %v", live, err)
	}
	if ok, _ := db.RevokeDelegationGrant(ctx, pool, repoGrant.ID, "someone-else"); ok {
		t.Fatal("another account revoked a grant")
	}
	if ok, err := db.RevokeDelegationGrant(ctx, pool, repoGrant.ID, "owner-1"); err != nil || !ok {
		t.Fatalf("revoke: %v %v", ok, err)
	}
	if ok, _ := db.RevokeDelegationGrant(ctx, pool, repoGrant.ID, "owner-1"); ok {
		t.Fatal("revoked twice")
	}
	if g, _ := db.FindDelegationGrant(ctx, pool, parent, "codex", "daemon", daemon, "blerg", false); g != nil {
		t.Fatalf("a revoked grant matched: %+v", g)
	}
	n, err := db.RevokeDelegationGrantsForParent(ctx, pool, parent)
	if err != nil || n != 1 {
		t.Fatalf("revoke for parent: %d %v", n, err)
	}
	if live, _ := db.ListLiveDelegationGrants(ctx, pool, parent); len(live) != 0 {
		t.Fatalf("live after revoke: %+v", live)
	}
}

func TestDelegationRulesLiveAndRevoke(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	other := mustDaemon(t, pool)
	a, err := db.InsertDelegationRule(ctx, pool, db.DelegationRule{AccountID: "owner-1", Engine: "codex", Runtime: "daemon", DaemonID: daemon, LocalOnly: true, CreatedVia: "card"})
	if err != nil || a.ID == "" || !strings.HasPrefix(a.DaemonName, "laptop-") || !a.LocalOnly || a.CreatedVia != "card" {
		t.Fatalf("insert: %+v %v", a, err)
	}
	tx, _ := pool.Begin(ctx)
	b, err := db.InsertDelegationRuleTx(ctx, tx, db.DelegationRule{AccountID: "owner-1", Engine: "claude", Runtime: "docker", DaemonID: other, CreatedVia: "settings"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertDelegationRule(ctx, pool, db.DelegationRule{AccountID: "owner-2", Engine: "codex", Runtime: "daemon", DaemonID: daemon, CreatedVia: "card"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertDelegationRule(ctx, pool, db.DelegationRule{AccountID: "owner-1", Engine: "codex", Runtime: "daemon", DaemonID: daemon, CreatedVia: "nonsense"}); err == nil {
		t.Fatal("created_via is not constrained")
	}
	list, err := db.ListDelegationRules(ctx, pool, "owner-1")
	if err != nil || len(list) != 2 || list[0].ID != a.ID || list[1].ID != b.ID {
		t.Fatalf("list: %+v %v", list, err)
	}
	cand, err := db.CandidateDelegationRules(ctx, pool, "owner-1", "codex", "daemon", daemon)
	if err != nil || len(cand) != 1 || cand[0].ID != a.ID {
		t.Fatalf("candidates: %+v %v", cand, err)
	}
	if c, _ := db.CandidateDelegationRules(ctx, pool, "owner-1", "codex", "docker", daemon); len(c) != 0 {
		t.Fatalf("runtime mismatch matched: %+v", c)
	}
	if ok, _ := db.RevokeDelegationRule(ctx, pool, a.ID, "owner-2"); ok {
		t.Fatal("another account revoked a rule")
	}
	if ok, err := db.RevokeDelegationRule(ctx, pool, a.ID, "owner-1"); err != nil || !ok {
		t.Fatalf("revoke: %v %v", ok, err)
	}
	if ok, _ := db.RevokeDelegationRule(ctx, pool, a.ID, "owner-1"); ok {
		t.Fatal("revoked twice")
	}
	if c, _ := db.CandidateDelegationRules(ctx, pool, "owner-1", "codex", "daemon", daemon); len(c) != 0 {
		t.Fatalf("a revoked rule is a candidate: %+v", c)
	}
	if list, _ := db.ListDelegationRules(ctx, pool, "owner-1"); len(list) != 1 || list[0].ID != b.ID {
		t.Fatalf("list after revoke: %+v", list)
	}
}

func TestChildSessionsAndDigest(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, "owner-1")
	if d, err := db.ReadChildrenDigest(ctx, pool, parent); err != nil || d.Count != 0 || d.MaxSeq != 0 || d.OpenAsks != 0 || d.MaxEndedAt != nil {
		t.Fatalf("empty digest: %+v %v", d, err)
	}
	c1 := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "owner-1", ParentSessionID: parent, DelegationVia: "rule:r1"})
	c2 := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "owner-1", ParentSessionID: parent, DelegationVia: "request:q1", KeepRunning: true})
	mustSession(t, pool, daemon, "owner-1") // not a child
	endSession(t, pool, c2, "stopped", "stopped_by_user")
	if _, err := db.CreateMessage(ctx, pool, c1, "ask", "may I?"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateMessage(ctx, pool, c1, "update", "fyi"); err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{c1, c1, c2} {
		if _, _, err := db.AppendAgentEvent(ctx, pool, id, newUUID(t, pool), "assistant_text", `{"i":`+string(rune('0'+i))+`}`); err != nil {
			t.Fatal(err)
		}
	}
	list, err := db.ListChildSessions(ctx, pool, parent)
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if list[0].ID != c1 || list[0].DelegationVia != "rule:r1" || list[0].OpenAsks != 1 || list[0].EndedAt != nil || list[0].KeepRunning || list[0].Title != "t" || list[0].Model != "m" {
		t.Fatalf("first child: %+v", list[0])
	}
	if list[1].ID != c2 || list[1].EndedAt == nil || !list[1].KeepRunning || list[1].Status != "stopped" || list[1].OpenAsks != 0 {
		t.Fatalf("second child: %+v", list[1])
	}
	d, err := db.ReadChildrenDigest(ctx, pool, parent)
	if err != nil || d.Count != 2 || d.OpenAsks != 1 || d.MaxSeq != 2 || d.MaxEndedAt == nil {
		t.Fatalf("digest: %+v %v", d, err)
	}
	if got := strings.Count(d.Statuses, ",") + 1; got != 2 || !strings.Contains(d.Statuses, "stopped") || !strings.Contains(d.Statuses, "running") {
		t.Fatalf("statuses: %q", d.Statuses)
	}
	if p, err := db.ParentOf(ctx, pool, c1); err != nil || p != parent {
		t.Fatalf("ParentOf child: %q %v", p, err)
	}
	if p, err := db.ParentOf(ctx, pool, parent); err != nil || p != "" {
		t.Fatalf("ParentOf parent: %q %v", p, err)
	}
	if p, err := db.ParentOf(ctx, pool, newUUID(t, pool)); err != nil || p != "" {
		t.Fatalf("ParentOf absent: %q %v", p, err)
	}
	stop, err := db.ChildrenToStop(ctx, pool, parent)
	if err != nil || len(stop) != 1 || stop[0] != c1 {
		t.Fatalf("to stop: %v %v", stop, err)
	}
	row, err := db.GetSession(ctx, pool, c2)
	if err != nil || row.ParentSessionID == nil || *row.ParentSessionID != parent || row.DelegationVia != "request:q1" || !row.KeepRunning || row.BoardID != nil {
		t.Fatalf("GetSession: %+v %v", row, err)
	}
	all, err := db.ListSessions(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range all {
		if s.ID == c1 {
			found = s.ParentSessionID != nil && *s.ParentSessionID == parent && s.DelegationVia == "rule:r1"
		}
	}
	if !found {
		t.Fatal("ListSessions did not carry the child fields")
	}
}

func TestChildLimitsCounters(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, "owner-1")
	otherParent := mustSession(t, pool, daemon, "owner-2")
	before := time.Now().Add(-time.Hour)

	a := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "owner-1", ParentSessionID: parent})
	mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "owner-1", ParentSessionID: parent})
	mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "owner-2", ParentSessionID: otherParent})
	endSession(t, pool, a, "stopped", "stopped_by_user")

	req := func(key, status string) {
		t.Helper()
		r, _, err := db.InsertDelegationRequest(ctx, pool, db.DelegationRequest{ParentSessionID: parent, AccountID: "owner-1", DaemonID: daemon,
			IdempotencyKey: key, RequestHash: key, Request: json.RawMessage(`{}`), Offered: []string{"deny"}, ExpiresAt: time.Now().Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		if status != "pending" {
			if _, err := pool.Exec(ctx, `UPDATE delegation_requests SET status = $2, choice = CASE WHEN $2 = 'allowed' THEN 'once' END WHERE id = $1::uuid`, r.ID, status); err != nil {
				t.Fatal(err)
			}
		}
	}
	req("pending", "pending")
	req("allowed", "allowed") // no child recorded yet: still counts
	allowedWithChild, _, _ := db.InsertDelegationRequest(ctx, pool, db.DelegationRequest{ParentSessionID: parent, AccountID: "owner-1", DaemonID: daemon,
		IdempotencyKey: "allowed-child", RequestHash: "allowed-child", Request: json.RawMessage(`{}`), Offered: []string{"deny"}, ExpiresAt: time.Now().Add(time.Minute)})
	if _, err := pool.Exec(ctx, `UPDATE delegation_requests SET status = 'allowed', choice = 'once', child_session_id = $2::uuid WHERE id = $1::uuid`, allowedWithChild.ID, a); err != nil {
		t.Fatal(err)
	}
	req("denied", "denied")
	req("expired", "expired")

	if n, err := db.CountRunningChildren(ctx, pool, parent); err != nil || n != 1 {
		t.Fatalf("running children: %d %v", n, err)
	}
	// Per parent: both children, the pending request and the allowed one with no child.
	if n, err := db.CountParentStarts(ctx, pool, parent); err != nil || n != 4 {
		t.Fatalf("parent starts: %d %v", n, err)
	}
	if n, err := db.CountAccountRunningChildren(ctx, pool, "owner-1"); err != nil || n != 1 {
		t.Fatalf("account running: %d %v", n, err)
	}
	// Per account: 2 children, plus requests pending, denied, expired or allowed without a child = 6.
	n, oldest, err := db.AccountStartsSince(ctx, pool, "owner-1", before)
	if err != nil || n != 6 || oldest == nil {
		t.Fatalf("account starts: %d %v %v", n, oldest, err)
	}
	if n, oldest, err := db.AccountStartsSince(ctx, pool, "owner-1", time.Now().Add(time.Hour)); err != nil || n != 0 || oldest != nil {
		t.Fatalf("account starts in the future: %d %v %v", n, oldest, err)
	}
	if n, _, _ := db.AccountStartsSince(ctx, pool, "owner-2", before); n != 1 {
		t.Fatalf("other account: %d", n)
	}
}

func TestTerminallyEnded(t *testing.T) {
	now := time.Now()
	reason := func(s string) *string { return &s }
	cases := []struct {
		name   string
		ended  *time.Time
		reason *string
		status string
		want   bool
	}{
		{"live", nil, nil, "running", false},
		{"ended without a reason", &now, nil, "stopped", false},
		{"reason without ended_at", nil, reason(db.EndReasonStoppedByUser), "running", false},
		{"stopped_by_user", &now, reason(db.EndReasonStoppedByUser), "stopped", true},
		{"stopped_by_agent", &now, reason(db.EndReasonStoppedByAgent), "stopped", true},
		{"auto_stopped", &now, reason(db.EndReasonAutoStopped), "stopped", true},
		{"process_exited", &now, reason(db.EndReasonProcessExited), "stopped", true},
		{"process_failed", &now, reason(db.EndReasonProcessFailed), "error", true},
		{"start_failed", &now, reason(db.EndReasonStartFailed), "error", true},
		{"parent_ended", &now, reason(db.EndReasonParentEnded), "stopped", true},
		{"daemon_lost still error", &now, reason(db.EndReasonDaemonLost), "error", false},
		{"daemon_lost cleared by a person", &now, reason(db.EndReasonDaemonLost), "stopped", true},
		{"daemon_unreported", &now, reason(db.EndReasonDaemonUnreported), "stopped", false},
		{"job_finished", &now, reason(db.EndReasonJobFinished), "stopped", false},
		{"job_failed", &now, reason(db.EndReasonJobFailed), "error", false},
		{"job_disappeared", &now, reason(db.EndReasonJobDisappeared), "error", false},
		{"not_resumed", &now, reason(db.EndReasonNotResumed), "stopped", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row := &db.SessionRow{EndedAt: c.ended, EndReason: c.reason, Status: c.status}
			if got := db.TerminallyEnded(row); got != c.want {
				t.Fatalf("TerminallyEnded = %v, want %v", got, c.want)
			}
		})
	}
	if db.TerminallyEnded(nil) {
		t.Fatal("nil row")
	}
	if !db.ValidEndReason(db.EndReasonParentEnded) {
		t.Fatal("parent_ended is not in the vocabulary")
	}
}

func TestOrphanedChildren(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	child := func(parent string, keep bool) string {
		return mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "owner-1", ParentSessionID: parent, KeepRunning: keep})
	}
	stopped := mustSession(t, pool, daemon, "owner-1")
	unreported := mustSession(t, pool, daemon, "owner-1")
	kept := mustSession(t, pool, daemon, "owner-1")
	clearedLost := mustSession(t, pool, daemon, "owner-1")
	lost := mustSession(t, pool, daemon, "owner-1")
	alive := mustSession(t, pool, daemon, "owner-1")
	endSession(t, pool, stopped, "stopped", "stopped_by_user")
	endSession(t, pool, unreported, "stopped", "daemon_unreported")
	endSession(t, pool, kept, "stopped", "stopped_by_user")
	endSession(t, pool, clearedLost, "stopped", "daemon_lost")
	endSession(t, pool, lost, "error", "daemon_lost")

	cStopped, cUnreported, cKept := child(stopped, false), child(unreported, false), child(kept, true)
	cCleared, cLost, cAlive := child(clearedLost, false), child(lost, false), child(alive, false)
	cEnded := child(stopped, false)
	endSession(t, pool, cEnded, "stopped", "stopped_by_user")
	notChild := mustSession(t, pool, daemon, "owner-1")

	ids := []string{cStopped, cUnreported, cKept, cCleared, cLost, cAlive, cEnded, notChild}
	got, err := db.OrphanedChildren(ctx, pool, ids)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{cStopped: true, cCleared: true}
	if len(got) != len(want) {
		t.Fatalf("orphans: %v", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("unexpected orphan %s in %v", id, got)
		}
	}
	if none, err := db.OrphanedChildren(ctx, pool, nil); err != nil || len(none) != 0 {
		t.Fatalf("no ids: %v %v", none, err)
	}
}

func TestInsertSessionAsKeepsParentOnRevive(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, "owner-1")
	child := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "owner-1", ParentSessionID: parent, DelegationVia: "grant:g1", KeepRunning: true})
	// The session_started upsert of a resumed session carries no origin.
	if err := db.InsertSessionAs(ctx, pool, child, daemon, "running", "/repos/x", "x", "t", "m", db.SessionOrigin{}); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetSession(ctx, pool, child)
	if err != nil || row.ParentSessionID == nil || *row.ParentSessionID != parent || row.DelegationVia != "grant:g1" || !row.KeepRunning {
		t.Fatalf("after revive: %+v %v", row, err)
	}
	// An ordinary session stays an ordinary one.
	plain, _ := db.GetSession(ctx, pool, parent)
	if plain.ParentSessionID != nil || plain.DelegationVia != "" || plain.KeepRunning {
		t.Fatalf("plain session: %+v", plain)
	}
	// The cluster insert ignores the child fields.
	cl := newUUID(t, pool)
	if err := db.InsertClusterSessionAs(ctx, pool, cl, daemon, "starting", "/x", "x", "t", "m", db.SessionOrigin{ParentSessionID: parent, DelegationVia: "rule:r", KeepRunning: true}); err != nil {
		t.Fatal(err)
	}
	if row, _ := db.GetSession(ctx, pool, cl); row.ParentSessionID != nil || row.DelegationVia != "" || row.KeepRunning {
		t.Fatalf("cluster session carried child fields: %+v", row)
	}
	// parent_session_id is a real foreign key.
	err = db.InsertSessionAs(ctx, pool, newUUID(t, pool), daemon, "running", "/x", "x", "t", "m", db.SessionOrigin{ParentSessionID: newUUID(t, pool)})
	if err == nil || errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("a child of a session that does not exist: %v", err)
	}
}

func TestDeletingAParentKeepsItsChildren(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, "owner-1")
	live := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "owner-1", ParentSessionID: parent})
	ended := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "owner-1", ParentSessionID: parent})
	endSession(t, pool, ended, "stopped", "stopped_by_user")
	if err := db.DeleteSession(ctx, pool, parent); err != nil {
		t.Fatalf("delete a parent with children: %v", err)
	}
	for _, id := range []string{live, ended} {
		row, err := db.GetSession(ctx, pool, id)
		if err != nil || row == nil || row.ParentSessionID != nil {
			t.Fatalf("child %s after parent delete: %+v %v", id, row, err)
		}
	}
}

func TestDelegationOnePendingPerBody(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, "owner-1")
	mk := func(key, hash string) db.DelegationRequest {
		return db.DelegationRequest{ParentSessionID: parent, AccountID: "owner-1", DaemonID: daemon, IdempotencyKey: key,
			RequestHash: hash, Request: json.RawMessage(`{}`), Offered: []string{"deny"}, ExpiresAt: time.Now().Add(time.Minute)}
	}
	first, created, err := db.InsertDelegationRequest(ctx, pool, mk("k1", "same"))
	if err != nil || !created {
		t.Fatal(created, err)
	}
	dup, created, err := db.InsertDelegationRequest(ctx, pool, mk("k2", "same"))
	if err != nil || created || dup.ID != first.ID {
		t.Fatalf("same body, other key: created=%v %+v %v", created, dup, err)
	}
	if _, created, err := db.InsertDelegationRequest(ctx, pool, mk("k3", "different")); err != nil || !created {
		t.Fatalf("different body: %v %v", created, err)
	}
	// Once the first is no longer pending, the same body may be requested again.
	if _, err := db.ExpireDelegationRequest(ctx, pool, first.ID, parent); err != nil {
		t.Fatal(err)
	}
	if again, created, err := db.InsertDelegationRequest(ctx, pool, mk("k4", "same")); err != nil || !created || again.ID == first.ID {
		t.Fatalf("after expiry: %v %+v %v", created, again, err)
	}
	// Concurrent inserts of one body with different keys: exactly one is created.
	const n = 8
	var wg sync.WaitGroup
	results := make(chan bool, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, c, err := db.InsertDelegationRequest(ctx, pool, mk(fmt.Sprintf("race-%d", i), "race"))
			if err != nil {
				t.Error(err)
			}
			results <- c
		}(i)
	}
	wg.Wait()
	close(results)
	won := 0
	for c := range results {
		if c {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("%d concurrent inserts created a row, want 1", won)
	}
}

func TestDecideDelegationRequestConcurrent(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, "owner-1")
	r, _, err := db.InsertDelegationRequest(ctx, pool, db.DelegationRequest{ParentSessionID: parent, AccountID: "owner-1", DaemonID: daemon,
		IdempotencyKey: "k", RequestHash: "h", Request: json.RawMessage(`{}`), Offered: []string{"once"}, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	const n = 6
	var wg sync.WaitGroup
	var won, lost atomic.Int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = tx.Rollback(ctx) }()
			_, err = db.DecideDelegationRequestTx(ctx, tx, r.ID, "owner-1", "allowed", "once", "owner-1", "sid")
			switch {
			case err == nil:
				if cerr := tx.Commit(ctx); cerr != nil {
					t.Error(cerr)
					return
				}
				won.Add(1)
			case errors.Is(err, db.ErrDelegationState):
				lost.Add(1)
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if won.Load() != 1 || lost.Load() != n-1 {
		t.Fatalf("won=%d lost=%d, want exactly one winner", won.Load(), lost.Load())
	}
}

func TestMarkInterruptedDelegationStarts(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, "owner-1")
	insert := func(key, status, decidedAgo string, child bool, startError string) string {
		t.Helper()
		r, _, err := db.InsertDelegationRequest(ctx, pool, db.DelegationRequest{ParentSessionID: parent, AccountID: "owner-1",
			DaemonID: daemon, IdempotencyKey: key, RequestHash: key, Request: json.RawMessage(`{}`),
			Offered: []string{"once", "deny"}, ExpiresAt: time.Now().Add(10 * time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		var childID any
		if child {
			childID = mustSession(t, pool, daemon, "owner-1")
		}
		if _, err := pool.Exec(ctx, `UPDATE delegation_requests SET status = $2, decided_at = now() - $3::interval,
			child_session_id = $4::uuid, start_error = NULLIF($5, '') WHERE id = $1::uuid`, r.ID, status, decidedAgo, childID, startError); err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	stuck := insert("stuck", "allowed", "3 minutes", false, "")
	insert("recent", "allowed", "30 seconds", false, "")
	old := insert("old", "allowed", "2 hours", false, "")
	insert("started", "allowed", "3 minutes", true, "")
	insert("failed", "allowed", "3 minutes", false, "boom")
	insert("denied", "denied", "3 minutes", false, "")
	got, err := db.MarkInterruptedDelegationStarts(ctx, pool, 2*time.Minute, "interrupted")
	if err != nil || len(got) != 2 {
		t.Fatalf("marked %+v %v", got, err)
	}
	marked := map[string]bool{}
	for _, r := range got {
		marked[r.ID] = r.StartError == "interrupted"
	}
	if !marked[stuck] || !marked[old] {
		t.Fatalf("missing interrupted starts: %+v", marked)
	}
	if again, err := db.MarkInterruptedDelegationStarts(ctx, pool, 2*time.Minute, "interrupted"); err != nil || len(again) != 0 {
		t.Fatalf("second pass %+v %v", again, err)
	}
}

func TestHostDaemonName(t *testing.T) {
	pool := connect(t)
	ctx := context.Background()
	daemon := mustDaemon(t, pool)
	name, ok, err := db.HostDaemonName(ctx, pool, daemon)
	if err != nil || !ok || !strings.HasPrefix(name, "laptop-") {
		t.Fatalf("host: %q %v %v", name, ok, err)
	}
	pod := newUUID(t, pool)
	if err := db.UpsertDaemon(ctx, pool, pod, "pod", "runner", ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := db.HostDaemonName(ctx, pool, pod); err != nil || ok {
		t.Fatalf("pod: %v %v", ok, err)
	}
	if _, ok, err := db.HostDaemonName(ctx, pool, newUUID(t, pool)); err != nil || ok {
		t.Fatalf("missing: %v %v", ok, err)
	}
}
