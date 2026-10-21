package server

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Block real reconciliation on a session row, rather than depending on a slow
// query or a sleep. Another daemon must still register, persist work and ack it.
func TestBackgroundBlockedDaemonDoesNotStallOtherDaemonEvents(t *testing.T) {
	fx := newLiveFx(t)
	h := fx.api.hub
	blocked := &DaemonConn{ID: topicDaemonID, BackgroundTaskLifetime: newUUID(), send: make(chan []byte, 8)}
	blocked.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	h.Register(blocked)
	if _, _, _, err := db.AppendBackgroundTaskEventAt(t.Context(), fx.pool, fx.sess, newUUID(), backgroundStartedKind, `{"task_id":"orphan","daemon_lifetime":"old-process"}`, time.Time{}); err != nil {
		t.Fatal(err)
	}
	otherID, otherSession := newUUID(), newUUID()
	if err := db.UpsertDaemon(t.Context(), fx.pool, otherID, "other-fixture", "local", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertSession(t.Context(), fx.pool, otherSession, otherID, "running", "/fixture", "fixture", "Fixture", ""); err != nil {
		t.Fatal(err)
	}
	tx, err := fx.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(t.Context(), `SELECT id FROM sessions WHERE id=$1 FOR UPDATE`, fx.sess); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		reconcileBackgroundTaskInventory(ctx, h, fx.pool, blocked, &protocol.BackgroundTaskInventory{Lifetime: blocked.BackgroundTaskLifetime, Sessions: map[string][]string{}})
	}()
	defer func() { _ = tx.Rollback(context.Background()); cancel(); <-done }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		err := fx.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, tx.Conn().PgConn().PID()).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("reconciliation never reached the blocked row")
		}
	}
	other := &DaemonConn{ID: otherID, BackgroundTaskLifetime: newUUID(), send: make(chan []byte, 8)}
	other.SetSandboxClones(true) // a current daemon: every sandbox in a clone of its own
	flowing := make(chan struct{})
	go func() {
		defer close(flowing)
		h.Register(other)
		HandleAgentEvent(ctx, h, fx.pool, other, protocol.AgentEvent{SessionID: otherSession, ClientEventID: newUUID(), Kind: backgroundStartedKind, Payload: json.RawMessage(`{"task_id":"other-task"}`)})
		HandleAgentEvent(ctx, h, fx.pool, other, protocol.AgentEvent{SessionID: otherSession, ClientEventID: newUUID(), Kind: "subagent_started", Payload: json.RawMessage(`{"child_id":"other-child"}`)})
	}()
	defer func() { _ = tx.Rollback(context.Background()); cancel(); <-flowing }()
	select {
	case <-flowing:
	case <-ctx.Done():
		t.Fatal("one daemon's blocked reconciliation stalled another daemon's event ingestion")
	}
	if len(other.send) != 2 {
		t.Fatalf("other daemon received %d acknowledgements, want 2", len(other.send))
	}
	var events int
	if err := fx.pool.QueryRow(ctx, `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind IN ('background_task_started','subagent_started')`, otherSession).Scan(&events); err != nil || events != 2 {
		t.Fatalf("other daemon events not durable: count=%d err=%v", events, err)
	}
	select {
	case <-done:
		t.Fatal("reconciliation unexpectedly stopped waiting for the row lock")
	default:
	}
}

func TestDaemonWorkFencePinsWaitersAndReclaimsEntries(t *testing.T) {
	h := NewHub()
	unlock := h.lockDaemonWork("same-daemon")
	defer unlock()
	h.mu.RLock()
	original := h.daemonWorkFences["same-daemon"]
	h.mu.RUnlock()
	acquired, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		done := h.lockDaemonWork("same-daemon")
		defer done()
		close(acquired)
		<-release
	}()
	releaseWaiter := sync.OnceFunc(func() { close(release) })
	defer func() { unlock(); releaseWaiter(); <-finished }()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		h.mu.RLock()
		refs := original.refs
		h.mu.RUnlock()
		if refs == 2 {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("waiter did not pin the daemon fence")
		}
	}
	select {
	case <-acquired:
		t.Fatal("same daemon acquired a held fence")
	default:
	}
	// A different daemon neither waits for this fence nor changes its identity.
	h.lockDaemonWork("different-daemon")()
	unlock()
	unlock() // explicit release followed by deferred cleanup is idempotent
	select {
	case <-acquired:
	case <-deadline.C:
		t.Fatal("waiter never acquired released fence")
	}
	h.mu.RLock()
	same := h.daemonWorkFences["same-daemon"] == original && original.refs == 1
	_, otherRemains := h.daemonWorkFences["different-daemon"]
	h.mu.RUnlock()
	if !same || otherRemains {
		t.Fatal("fence lost its waiter identity or leaked an unused entry")
	}
	releaseWaiter()
	<-finished
	h.mu.RLock()
	remaining := len(h.daemonWorkFences)
	h.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("unused daemon fences retained: %d", remaining)
	}
}
