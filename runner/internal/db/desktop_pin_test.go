package db

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/testsupport"
)

func TestDaemonAuthorityShareLockSerializesRotation(t *testing.T) {
	pool := testsupport.Open(t, "TEST_DATABASE_URL", "pin-lock", RunMigrations)
	const id = "00000000-0000-4000-8000-00000000ab01"
	const gen = "00000000-0000-4000-8000-00000000ab02"
	const next = "00000000-0000-4000-8000-00000000ab03"
	binding := desktopauth.Binding{DesktopID: id, OwnerID: "fixture", Revision: 1}
	if err := BindDesktopConnection(t.Context(), pool, binding, "fixture", "host", "", gen); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	pin := DaemonAuthority{DaemonID: id, OwnerID: "fixture", Revision: 1, Generation: gen}
	if err := lockDaemonAuthorityTx(t.Context(), tx, pin); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	go func() { done <- BindDesktopConnection(ctx, pool, binding, "fixture", "host", "", next) }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, tx.Conn().PgConn().PID()).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("rotation committed before reconciliation: %v", err)
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("rotation did not block")
		}
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	stale, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stale.Rollback(context.Background()) }()
	if err := lockDaemonAuthorityTx(t.Context(), stale, pin); !errors.Is(err, ErrDesktopAuthority) {
		t.Fatalf("old generation accepted: %v", err)
	}
}

func TestDaemonAuthorityConcurrentRotationAndReconcileComplete(t *testing.T) {
	pool := testsupport.Open(t, "TEST_DATABASE_URL", "pin-race", RunMigrations)
	const id = "00000000-0000-4000-8000-00000000ab01"
	binding := desktopauth.Binding{DesktopID: id, OwnerID: "fixture", Revision: 1}
	gen := "00000000-0000-4000-8000-00000000ab02"
	if err := BindDesktopConnection(t.Context(), pool, binding, "fixture", "host", "", gen); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		next := fmt.Sprintf("00000000-0000-4000-8000-%012d", 100+i)
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
		done := make(chan error, 2)
		pin := DaemonAuthority{DaemonID: id, OwnerID: "fixture", Revision: 1, Generation: gen}
		go func() {
			tx, err := pool.Begin(ctx)
			if err != nil {
				done <- err
				return
			}
			defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
			err = lockDaemonAuthorityTx(ctx, tx, pin)
			if errors.Is(err, ErrDesktopAuthority) {
				done <- nil
				return
			}
			if err == nil {
				err = tx.Commit(ctx)
			}
			done <- err
		}()
		go func() { done <- BindDesktopConnection(ctx, pool, binding, "fixture", "host", "", next) }()
		for range 2 {
			if err := <-done; err != nil {
				cancel()
				t.Fatalf("iteration %d: %v", i, err)
			}
		}
		cancel()
		gen = next
	}
}

func TestTouchPinnedDaemonSkipsBusyAndStaleRows(t *testing.T) {
	pool := testsupport.Open(t, "TEST_DATABASE_URL", "pin-touch", RunMigrations)
	const id = "00000000-0000-4000-8000-00000000ab01"
	const gen = "00000000-0000-4000-8000-00000000ab02"
	const next = "00000000-0000-4000-8000-00000000ab03"
	binding := desktopauth.Binding{DesktopID: id, OwnerID: "fixture", Revision: 1}
	if err := BindDesktopConnection(t.Context(), pool, binding, "fixture", "host", "", gen); err != nil {
		t.Fatal(err)
	}
	pin := DaemonAuthority{DaemonID: id, OwnerID: "fixture", Revision: 1, Generation: gen}
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(t.Context(), `SELECT id FROM daemons WHERE id=$1 FOR SHARE`, id); err != nil {
		t.Fatal(err)
	}
	// A foreign-key check holds a key-share lock, which must not prevent a touch.
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	tx, err = pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `SELECT id FROM daemons WHERE id=$1 FOR KEY SHARE`, id); err != nil {
		t.Fatal(err)
	}
	if touched, err := TouchPinnedDaemon(t.Context(), pool, pin); err != nil || !touched {
		t.Fatalf("key share prevented touch: %v %v", touched, err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	tx, err = pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(t.Context(), `SELECT id FROM daemons WHERE id=$1 FOR SHARE`, id); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	touched, err := TouchPinnedDaemon(ctx, pool, pin)
	if err != nil || touched {
		t.Fatalf("busy row was not skipped: %v %v", touched, err)
	}
	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	touched, err = TouchPinnedDaemon(t.Context(), pool, pin)
	if err != nil || !touched {
		t.Fatalf("current row not touched: %v %v", touched, err)
	}
	if err := BindDesktopConnection(t.Context(), pool, binding, "fixture", "host", "", next); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `UPDATE daemons SET status='disconnected' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	touched, err = TouchPinnedDaemon(t.Context(), pool, pin)
	if err != nil || touched {
		t.Fatalf("stale generation touched daemon: %v %v", touched, err)
	}
	var status string
	if err := pool.QueryRow(t.Context(), `SELECT status FROM daemons WHERE id=$1`, id).Scan(&status); err != nil || status != "disconnected" {
		t.Fatalf("stale touch changed status=%s err=%v", status, err)
	}
}

func TestTouchPinnedLegacyDaemon(t *testing.T) {
	pool := testsupport.Open(t, "TEST_DATABASE_URL", "pin-touch-legacy", RunMigrations)
	const id = "00000000-0000-4000-8000-00000000ab01"
	const gen = "00000000-0000-4000-8000-00000000ab02"
	if err := UpsertDaemon(t.Context(), pool, id, "legacy", "host", ""); err != nil {
		t.Fatal(err)
	}
	revision, err := BindLegacyDesktop(t.Context(), pool, id, gen)
	if err != nil {
		t.Fatal(err)
	}
	touched, err := TouchPinnedDaemon(t.Context(), pool, DaemonAuthority{DaemonID: id, Revision: revision, Generation: gen, Legacy: true})
	if err != nil || !touched {
		t.Fatalf("legacy touch=%v err=%v", touched, err)
	}
}
