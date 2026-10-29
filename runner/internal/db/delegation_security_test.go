package db_test

import (
	"errors"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func reviewAllowedRequest(t *testing.T, pool *pgxpool.Pool, account string) db.DelegationRequest {
	t.Helper()
	daemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, account)
	r, _, err := db.InsertDelegationRequest(t.Context(), pool, db.DelegationRequest{ParentSessionID: parent, AccountID: account, DaemonID: daemon, IdempotencyKey: "review", RequestHash: "review", Offered: []string{"once"}, ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE delegation_requests SET status='allowed',decided_at=now()-interval '2 hours' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDelegationSuccessfulStartSurvivesDeletedChild(t *testing.T) {
	pool := connect(t)
	r := reviewAllowedRequest(t, pool, "owner-recorded")
	child := mustSession(t, pool, r.DaemonID, r.AccountID)
	if err := db.SetDelegationRequestStart(t.Context(), pool, r.ID, child, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `DELETE FROM sessions WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	marked, err := db.MarkInterruptedDelegationStarts(t.Context(), pool, 2*time.Minute, "interrupted")
	if err != nil || len(marked) != 0 {
		t.Fatalf("deleted successful child became interrupted: %+v %v", marked, err)
	}
	got, err := db.GetDelegationRequest(t.Context(), pool, r.ID)
	if err != nil || got == nil || got.ChildSessionID != "" || got.StartError != "the started session was deleted" {
		t.Fatalf("lost recorded success: %+v %v", got, err)
	}
	var recorded bool
	if err := pool.QueryRow(t.Context(), `SELECT start_recorded_at IS NOT NULL FROM delegation_requests WHERE id=$1`, r.ID).Scan(&recorded); err != nil || !recorded {
		t.Fatalf("success receipt not durable: recorded=%v err=%v", recorded, err)
	}
	if err := db.SetDelegationRequestStart(t.Context(), pool, r.ID, "", "late error"); !errors.Is(err, db.ErrDelegationState) {
		t.Fatalf("completed success overwritten: %v", err)
	}
}

func TestDelegationSweeperSkipsActiveWorkerAccount(t *testing.T) {
	pool := connect(t)
	active := reviewAllowedRequest(t, pool, "owner-active")
	other := reviewAllowedRequest(t, pool, "owner-abandoned")
	conn, err := pgx.ConnectConfig(t.Context(), pool.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(t.Context()) }()
	if _, err := conn.Exec(t.Context(), `SELECT pg_advisory_lock(hashtext('delegation:'||$1))`, active.AccountID); err != nil {
		t.Fatal(err)
	}
	marked, err := db.MarkInterruptedDelegationStarts(t.Context(), pool, 2*time.Minute, "interrupted")
	if err != nil || len(marked) != 1 || marked[0].ID != other.ID {
		t.Fatalf("worker not fenced or unrelated account blocked: %+v %v", marked, err)
	}
	if _, err := conn.Exec(t.Context(), `SELECT pg_advisory_unlock(hashtext('delegation:'||$1))`, active.AccountID); err != nil {
		t.Fatal(err)
	}
	marked, err = db.MarkInterruptedDelegationStarts(t.Context(), pool, 2*time.Minute, "interrupted")
	if err != nil || len(marked) != 1 || marked[0].ID != active.ID {
		t.Fatalf("abandoned worker not recovered: %+v %v", marked, err)
	}
	if err := db.SetDelegationRequestStart(t.Context(), pool, active.ID, "", "late fallback"); !errors.Is(err, db.ErrDelegationState) {
		t.Fatalf("late fallback overwrote recovery: %v", err)
	}
	current, err := db.GetDelegationRequest(t.Context(), pool, active.ID)
	if err != nil || current == nil || current.StartError != "interrupted" || current.StartRecordedAt == nil {
		t.Fatalf("recovery outcome lost: %+v %v", current, err)
	}
}

func TestDelegationDecisionChecksExpiryAfterAcquiringRow(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	parent := mustSession(t, pool, daemon, "owner-expiry")
	r, _, err := db.InsertDelegationRequest(t.Context(), pool, db.DelegationRequest{ParentSessionID: parent, AccountID: "owner-expiry", DaemonID: daemon, IdempotencyKey: "expiry", RequestHash: "expiry", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE delegation_requests SET expires_at=clock_timestamp()+interval '1 second' WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	blocker, err := pgx.ConnectConfig(t.Context(), pool.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Close(t.Context()) }()
	tx, err := blocker.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if _, err := tx.Exec(t.Context(), `SELECT id FROM delegation_requests WHERE id=$1 FOR UPDATE`, r.ID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		worker, err := pool.Begin(t.Context())
		if err != nil {
			result <- err
			return
		}
		defer func() { _ = worker.Rollback(t.Context()) }()
		_, err = db.DecideDelegationRequestTx(t.Context(), worker, r.ID, r.AccountID, "allowed", "once", r.AccountID, "sid")
		if err == nil {
			err = worker.Commit(t.Context())
		}
		result <- err
	}()
	deadline := time.After(30 * time.Second)
	for {
		var waiting bool
		// Poll from the pool: inside the blocker's transaction pg_stat_activity
		// stays the snapshot of its first read, so a worker that starts waiting
		// after that read would never be seen.
		if err := pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%delegation_requests%' AND pid<>pg_backend_pid())`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("decision finished before waiting for the row lock: %v", err)
		case <-deadline:
			t.Fatal("decision did not wait for row lock")
		case <-time.After(time.Millisecond):
		}
	}
	// Do not modify the locked row: an UPDATE's EPQ recheck after a competing
	// row change could hide the fact that its original expiry predicate was
	// evaluated before the lock wait. Only the separate lock-then-check closes it.
	if _, err := tx.Exec(t.Context(), `SELECT pg_sleep(GREATEST(0,EXTRACT(EPOCH FROM expires_at-clock_timestamp()))+0.02) FROM delegation_requests WHERE id=$1`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, db.ErrDelegationState) {
			t.Fatalf("expired decision error=%v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("decision did not finish")
	}
	if err := db.SetDelegationRequestStart(t.Context(), pool, r.ID, "", "not approved"); !errors.Is(err, db.ErrDelegationState) {
		t.Fatalf("pending request received a start receipt: %v", err)
	}
}

func TestLockDelegationParentPreservesPolicyAndFencesChanges(t *testing.T) {
	pool := connect(t)
	daemon := mustDaemon(t, pool)
	ancestor := mustSession(t, pool, daemon, "owner-policy")
	id := mustSessionAs(t, pool, daemon, db.SessionOrigin{SpawningAccount: "owner-policy", Private: true, ParentSessionID: ancestor, DelegationVia: "request:fixture"})
	if _, err := pool.Exec(t.Context(), `UPDATE sessions SET runtime='daemon',started_by_kind='session',ended_at=now(),end_reason='process_exited' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	r, err := db.LockDelegationParent(t.Context(), tx, id)
	if err != nil || r == nil {
		t.Fatalf("lock parent=%+v %v", r, err)
	}
	if r.ID != id || r.DaemonID != daemon || r.Status != "running" || r.SpawningAccountID == nil || *r.SpawningAccountID != "owner-policy" || r.Runtime == nil || *r.Runtime != "daemon" || !r.Private || r.ParentSessionID == nil || *r.ParentSessionID != ancestor || r.StartedByKind != "session" || r.DelegationVia != "request:fixture" || r.EndedAt == nil || r.EndReason == nil || *r.EndReason != "process_exited" {
		t.Fatalf("policy fields lost: %+v", r)
	}
	control, err := pgx.ConnectConfig(t.Context(), pool.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = control.Close(t.Context()) }()
	if _, err := control.Exec(t.Context(), `SET lock_timeout='100ms'`); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Exec(t.Context(), `UPDATE sessions SET private=false WHERE id=$1`, id); err == nil {
		t.Fatal("parent policy changed while decision fence held")
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := control.Exec(t.Context(), `UPDATE sessions SET private=false WHERE id=$1`, id); err != nil {
		t.Fatalf("parent fence not released: %v", err)
	}
	tx, err = pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(t.Context()) }()
	if r, err := db.LockDelegationParent(t.Context(), tx, newUUID(t, pool)); err != nil || r != nil {
		t.Fatalf("missing parent=%+v %v", r, err)
	}
}
