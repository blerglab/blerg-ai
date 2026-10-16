package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLimitResumeCorrelationCancellationAndExpiry(t *testing.T) {
	p := endTestPool(t)
	ctx := context.Background()
	id := "00000000-0000-0000-0000-0000000000d1"
	endTestSession(t, p, id, "idle")
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	get := func() *db.LimitResume { t.Helper(); r, err := db.GetLimitResume(ctx, p, id); check(err); return r }
	at := time.Now().Add(-time.Hour)
	reset := time.Now().Add(-2 * time.Minute)
	check(db.TrackLimitResumeTurn(ctx, p, id, "turn-a", "human", at, 1))
	check(db.RecordLimitRefusal(ctx, p, id, "claude", "wrong-turn", &reset, 2, at))
	if get().ResetAt != nil {
		t.Fatal("wrong turn armed refusal")
	}
	check(db.RecordLimitRefusal(ctx, p, id, "claude", "turn-a", &reset, 2, at))
	if get().State != "disabled" {
		t.Fatal("disabled refusal enabled retry")
	}
	check(db.SetLimitResume(ctx, p, id, true, "auth"))
	r := get()
	if r.State != "pending" || r.ExpiresAt == nil {
		t.Fatalf("enable after refusal: %+v", r)
	}
	expiry := *r.ExpiresAt
	check(db.CancelPendingLimitResume(ctx, p, id))
	check(db.RecordLimitRefusal(ctx, p, id, "claude", "turn-a", &reset, 3, at))
	if get().State != "armed" {
		t.Fatal("late refusal bypassed cancellation")
	}
	check(db.TrackLimitResumeTurn(ctx, p, id, "turn-b", "human", time.Now().Add(time.Second), 4))
	check(db.RecordLimitRefusal(ctx, p, id, "claude", "turn-b", nil, 5, time.Now().Add(time.Second)))
	if get().State != "unknown" {
		t.Fatal("unknown reset scheduled")
	}
	check(db.RecordLimitRefusal(ctx, p, id, "claude", "turn-b", &reset, 6, time.Now().Add(time.Second)))
	check(db.SetLimitResumeState(ctx, p, id, "dispatched", "sent"))
	check(db.SetLimitResume(ctx, p, id, true, "auth"))
	check(db.SetLimitResume(ctx, p, id, true, "auth"))
	if get().State == "pending" {
		t.Fatal("toggle rearmed dispatched turn")
	}
	check(db.TrackLimitResumeTurn(ctx, p, id, "turn-c", "limit_resume", time.Now().Add(2*time.Second), 7))
	check(db.RecordLimitRefusal(ctx, p, id, "claude", "turn-c", &reset, 8, time.Now().Add(2*time.Second)))
	if get().ExpiresAt.After(expiry.Add(2 * time.Hour)) {
		t.Fatal("automatic retry extended retention")
	}
	check(db.CancelLimitResume(ctx, p, id))
	if get().Enabled {
		t.Fatal("explicit cancel retained enabled")
	}
}

func TestLimitResumeClaimIsSingleAndRejectsEndedSession(t *testing.T) {
	p := endTestPool(t)
	ctx := context.Background()
	id := "00000000-0000-0000-0000-0000000000d2"
	endTestSession(t, p, id, "idle")
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := p.Exec(ctx, `UPDATE sessions SET kind='agent' WHERE id=$1`, id)
	check(err)
	check(db.SetLimitResume(ctx, p, id, true, "auth"))
	at := time.Now().Add(-time.Hour)
	reset := time.Now().Add(-2 * time.Minute)
	check(db.TrackLimitResumeTurn(ctx, p, id, "turn-a", "human", at, 1))
	check(db.RecordLimitRefusal(ctx, p, id, "codex", "turn-a", &reset, 2, at))
	_, err = p.Exec(ctx, `UPDATE sessions SET ended_at=now() WHERE id=$1`, id)
	check(err)
	claimed, err := db.ClaimLimitResumeAttempt(ctx, p, id, "attempt-a")
	check(err)
	if claimed {
		t.Fatal("claimed ended session")
	}
	_, err = p.Exec(ctx, `UPDATE sessions SET ended_at=NULL WHERE id=$1`, id)
	check(err)
	claimed, err = db.ClaimLimitResumeAttempt(ctx, p, id, "attempt-a")
	check(err)
	if !claimed {
		t.Fatal("did not claim due session")
	}
	check(db.RecordLimitRefusal(ctx, p, id, "codex", "turn-a", &reset, 3, at))
	check(db.SetLimitResume(ctx, p, id, true, "auth"))
	check(db.SetLimitResume(ctx, p, id, true, "auth"))
	claimed, err = db.ClaimLimitResumeAttempt(ctx, p, id, "attempt-b")
	check(err)
	if claimed {
		t.Fatal("duplicate dispatch")
	}
	r, err := db.GetLimitResume(ctx, p, id)
	check(err)
	if r.DispatchedAt == nil || r.Attempts != 1 || r.State != "dispatched" {
		t.Fatalf("dispatch state: %+v", r)
	}
	originalExpiry := *r.ExpiresAt
	check(db.TrackLimitResumeTurn(ctx, p, id, "adoption-turn", "system", time.Now(), 4))
	check(db.TrackLimitResumeTurn(ctx, p, id, "retry-turn", "limit_resume", time.Now(), 5))
	check(db.RecordLimitRefusal(ctx, p, id, "codex", "retry-turn", &reset, 6, time.Now()))
	r, err = db.GetLimitResume(ctx, p, id)
	check(err)
	if r.Attempts != 1 || r.ExpiresAt == nil || !r.ExpiresAt.Equal(originalExpiry) {
		t.Fatalf("adoption reset retry budget or expiry: %+v", r)
	}

}

func TestLimitResumeLockDoesNotReserveMainPool(t *testing.T) {
	p := endTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := db.WithLimitResumeLock(ctx, p, "session-lock", func(conn *pgxpool.Conn) error {
		var one int
		if err := p.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
			return err
		}
		// Dedicated connections must inherit the test schema's search path.
		if _, err := db.GetLimitResume(ctx, conn, "00000000-0000-0000-0000-0000000000d3"); err != nil {
			return err
		}
		return db.WithLimitResumeLock(ctx, p, "global-lock", func(_ *pgxpool.Conn) error {
			return p.QueryRow(ctx, `SELECT 1`).Scan(&one)
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Hot paths take this lock for every session: it must reuse warm connections,
// not dial a new one per call.
func TestLimitResumeLockReusesConnections(t *testing.T) {
	p := endTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pids := map[int32]bool{}
	for i := 0; i < 20; i++ {
		err := db.WithLimitResumeLock(ctx, p, "reuse-lock", func(conn *pgxpool.Conn) error {
			var pid int32
			if err := conn.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				return err
			}
			pids[pid] = true
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(pids) > 2 {
		t.Fatalf("lock opened %d connections for 20 sequential calls", len(pids))
	}
}

// The event-ingest path waits briefly for a held lock instead of failing at
// once (which stalled the transcript until the daemon's 30 s resend).
func TestLimitResumeLockWaitBlocksBriefly(t *testing.T) {
	p := endTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- db.WithLimitResumeLock(ctx, p, "wait-lock", func(*pgxpool.Conn) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	if err := db.WithLimitResumeLockWait(ctx, p, "wait-lock", 100*time.Millisecond, func(*pgxpool.Conn) error { return nil }); !errors.Is(err, db.ErrLimitResumeBusy) {
		t.Fatalf("short wait on a held lock: %v", err)
	}
	time.AfterFunc(200*time.Millisecond, func() { close(release) })
	ran := false
	if err := db.WithLimitResumeLockWait(ctx, p, "wait-lock", 3*time.Second, func(*pgxpool.Conn) error { ran = true; return nil }); err != nil || !ran {
		t.Fatalf("blocking wait did not take the released lock: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// The wait setting does not leak into the pooled connection.
	err := db.WithLimitResumeLock(ctx, p, "wait-lock", func(conn *pgxpool.Conn) error {
		var timeout string
		if err := conn.QueryRow(ctx, `SHOW lock_timeout`).Scan(&timeout); err != nil {
			return err
		}
		if timeout != "0" {
			t.Errorf("lock_timeout leaked: %s", timeout)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
