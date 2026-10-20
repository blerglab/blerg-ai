package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
	"github.com/jackc/pgx/v5/pgxpool"
)

func securityEndParent(t *testing.T, fx *delegFx) {
	t.Helper()
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='ended',ended_at=clock_timestamp(),end_reason='process_exited' WHERE id=$1`, fx.parent); err != nil {
		t.Fatal(err)
	}
}

func TestDelegationSecurityDeletedParentRetainsLineage(t *testing.T) {
	for _, keep := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "keep_running"}[keep], func(t *testing.T) {
			fx := newDelegFx(t)
			child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
			if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET keep_running=$2 WHERE id=$1`, child, keep); err != nil {
				t.Fatal(err)
			}
			if _, err := fx.pool.Exec(t.Context(), `DELETE FROM sessions WHERE id=$1`, fx.parent); err != nil {
				t.Fatal(err)
			}
			if fx.row(child).ParentSessionID != nil {
				t.Fatal("fixture did not exercise deleted-parent foreign-key clearing")
			}
			problem := parentResumeProblem(t.Context(), fx.pool, child)
			if keep && problem != nil || !keep && (problem == nil || problem.Status != http.StatusConflict) {
				t.Fatalf("deleted-parent policy keep=%v: %v", keep, problem)
			}
		})
	}
}

func TestDelegationSecurityHeldInputRespectsParentEnd(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	kept, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET keep_running=true WHERE id=$1`, kept); err != nil {
		t.Fatal(err)
	}
	fx.hub.away.begin(fx.dc.ID)
	for _, id := range []string{child, kept} {
		raw, err := json.Marshal(protocol.AgentUserMessage{Type: "agent_user_message", SessionID: id, Text: "held fixture message"})
		if err != nil || !fx.hub.away.hold(fx.dc.ID, id, raw) {
			t.Fatalf("hold fixture: %v", err)
		}
	}
	securityEndParent(t, fx)
	fx.hub.deliverHeld(t.Context(), fx.dc)
	seenKept := false
	for {
		select {
		case raw := <-fx.dc.send:
			var msg protocol.AgentUserMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				t.Fatal(err)
			}
			if msg.Type != "agent_user_message" {
				continue
			}
			if msg.SessionID == child {
				t.Error("reconnect replay dispatched held input to non-kept child of ended parent")
			}
			seenKept = seenKept || msg.SessionID == kept
		default:
			if !seenKept {
				t.Error("keep-running control lost its held message")
			}
			return
		}
	}
}

func TestDelegationSecurityDurableReplayAndManualMessageRefuseOrphan(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	fx.dc.SetInputReceipts(true)
	msg := protocol.AgentUserMessage{Type: "agent_user_message", SessionID: child, ClientID: newUUID(), Text: "queued fixture"}
	if fresh, err := acceptSessionInput(t.Context(), fx.hub, fx.pool, msg); err != nil || !fresh {
		t.Fatalf("queue fixture: fresh=%v err=%v", fresh, err)
	}
	securityEndParent(t, fx)
	replayPendingSessionInputs(t.Context(), fx.hub, fx.pool, fx.dc, []string{child})
	if _, problem := fx.api.SendMessage(t.Context(), RunnerPrincipal{Kind: "runner_key"}, child, "manual fixture", "runner"); problem == nil || problem.Status != http.StatusConflict {
		t.Fatalf("manual message admitted orphan: %v", problem)
	}
	handleBrowserAgentMessage(t.Context(), fx.hub, nil, fx.pool, protocol.AgentUserMessage{Type: "agent_user_message", SessionID: child, ClientID: newUUID(), Text: "browser fixture"})
	select {
	case raw := <-fx.dc.send:
		t.Fatalf("orphan input reached daemon: frame bytes=%d", len(raw))
	default:
	}
	if pending, err := db.HasPendingSessionInputs(t.Context(), fx.pool, child); err != nil || pending {
		t.Fatalf("orphan durable input remained pending: pending=%v err=%v", pending, err)
	}
}

// Hold the child row, let the handler pass its policy reads and block on the
// write, then end the parent. A guard performed only before the write loses.
func TestDelegationSecurityParentEndDuringNativeWrite(t *testing.T) {
	for _, started := range []bool{true, false} {
		t.Run(map[bool]string{true: "session_started", false: "state_changed"}[started], func(t *testing.T) {
			fx := newDelegFx(t)
			child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
			fx.hub.SetSessionOwner(child, fx.dc.ID)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			conn, err := fx.pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Release()
			tx, err := conn.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()
			if _, err := tx.Exec(ctx, `SELECT id FROM sessions WHERE id=$1 FOR UPDATE`, child); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if started {
					HandleSessionStarted(ctx, fx.hub, fx.pool, fx.dc.ID, protocol.SessionStarted{SessionID: child, Kind: "agent"})
					done <- nil
				} else {
					done <- HandleSessionStateChanged(ctx, fx.hub, fx.pool, protocol.SessionStateChanged{SessionID: child, Status: "idle"})
				}
			}()
			awaitLifecycle(t, func() bool {
				var waiting bool
				if err := fx.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)))`, int(conn.Conn().PgConn().PID())).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				return waiting
			})
			endConn, err := fx.pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer endConn.Release()
			parentDone := make(chan error, 1)
			go func() {
				_, err := endConn.Exec(ctx, `UPDATE sessions SET status='ended',ended_at=clock_timestamp(),end_reason='process_exited' WHERE id=$1`, fx.parent)
				parentDone <- err
			}()
			parentCommitted := false
			awaitLifecycle(t, func() bool {
				select {
				case err := <-parentDone:
					if err != nil {
						t.Fatal(err)
					}
					parentCommitted = true
					return true
				default:
				}
				var blocked bool
				if err := fx.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event_type='Lock')`, int(endConn.Conn().PgConn().PID())).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				return blocked
			})
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if !parentCommitted {
				if err := <-parentDone; err != nil {
					t.Fatal(err)
				}
				// A parent-share fence validly serializes the native write before
				// terminal end. The normal terminal hook must then catch that child.
				fx.api.onParentTerminalEnd(fx.parent)
				awaitLifecycle(t, func() bool { return derefOrEmpty(fx.row(child).EndReason) == db.EndReasonParentEnded })
			}
			if row := fx.row(child); derefOrEmpty(row.EndReason) != db.EndReasonParentEnded || !db.TerminallyEnded(row) {
				t.Fatalf("native write revived orphan after parent ended: status=%s reason=%s", row.Status, derefOrEmpty(row.EndReason))
			}
		})
	}
}

func TestDelegationSecurityHelloExpiresOmittedParentsButHeartbeatDoesNot(t *testing.T) {
	fx := newDelegFx(t)
	request := fx.ask(fx.parent, fx.parentTok, codexBody())
	_ = reconcileSessionsQueued(t.Context(), fx.hub, fx.pool, fx.dc.ID, []string{fx.parent}, nil, nil)
	if fx.request(request).Status != "pending" {
		t.Fatal("heartbeat expired pending request")
	}
	woke, release := fx.api.delegationWaiters.acquire(request)
	defer release()
	if err := fx.api.onDelegationDaemonHello(t.Context(), fx.dc.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-woke:
	default:
		t.Fatal("hello expiry did not wake approval waiter")
	}
	if fx.request(request).Status != "expired" || fx.decidedEvent(fx.parent, request) == nil {
		t.Fatal("hello failed to expire omitted pending parent and publish decision")
	}
}

func TestDelegationSecurityExpiredRetryCannotCreateFreshRequest(t *testing.T) {
	fx := newDelegFx(t)
	key := newUUID()
	code, first, _ := fx.postChild(fx.parent, fx.parentTok, codexBody(), key)
	if code != http.StatusAccepted {
		t.Fatalf("initial request: %d", code)
	}
	id := asString(first["request_id"])
	if err := fx.api.onDelegationDaemonHello(t.Context(), fx.dc.ID); err != nil {
		t.Fatal(err)
	}
	code, retry, _ := fx.postChild(fx.parent, fx.parentTok, codexBody(), key)
	if (code != http.StatusOK && code != http.StatusAccepted) || asString(retry["request_id"]) != id || asString(retry["status"]) != "expired" {
		t.Fatalf("expired retry changed identity/status: code=%d status=%s", code, asString(retry["status"]))
	}
	if fx.count(`SELECT count(*) FROM delegation_requests WHERE parent_session_id=$1`, fx.parent) != 1 {
		t.Fatal("expired idempotent retry created a fresh approval")
	}
}

func TestDelegationSecurityParentEndDuringDurableReplayLookup(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	fx.dc.SetInputReceipts(true)
	if _, err := acceptSessionInput(t.Context(), fx.hub, fx.pool, protocol.AgentUserMessage{Type: "agent_user_message", SessionID: child, ClientID: newUUID(), Text: "queued fixture"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	conn, err := fx.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `LOCK TABLE session_inputs IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		replayPendingSessionInputs(ctx, fx.hub, fx.pool, fx.dc, []string{child})
		close(done)
	}()
	awaitLifecycle(t, func() bool {
		var waiting bool
		if err := fx.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)))`, int(conn.Conn().PgConn().PID())).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		return waiting
	})
	securityEndParent(t, fx)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	<-done
	select {
	case raw := <-fx.dc.send:
		t.Fatalf("durable input dispatched after parent end during lookup: frame bytes=%d", len(raw))
	default:
	}
}

func TestDelegationSecurityAutomaticResumeBlocksOrphanButNotKeptChild(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, status: "idle"})
	kept, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, status: "idle"})
	for _, id := range []string{child, kept} {
		fx.hub.SetSessionOwner(id, fx.dc.ID)
		if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET engine='claude',keep_running=(id=$2) WHERE id=$1`, id, kept); err != nil {
			t.Fatal(err)
		}
		if err := db.SetLimitResume(t.Context(), fx.pool, id, true, newUUID()); err != nil {
			t.Fatal(err)
		}
		if _, err := fx.pool.Exec(t.Context(), `UPDATE session_limit_resume SET state='pending',turn_id='fixture-turn',reset_at=now()-interval '3 minutes',expires_at=now()+interval '1 hour' WHERE session_id=$1`, id); err != nil {
			t.Fatal(err)
		}
	}
	securityEndParent(t, fx)
	fx.api.checkLimitResumes(t.Context())
	state, err := db.GetLimitResume(t.Context(), fx.pool, child)
	if err != nil || state.State != "blocked" || state.Attempts != 0 {
		t.Fatalf("orphan continuation was not blocked: state=%+v err=%v", state, err)
	}
	seenKept := false
	for {
		select {
		case raw := <-fx.dc.send:
			var msg protocol.AgentUserMessage
			if err := json.Unmarshal(raw, &msg); err != nil {
				t.Fatal(err)
			}
			if msg.Type != "agent_user_message" {
				continue
			}
			if msg.SessionID == child {
				t.Fatal("automatic continuation admitted orphan")
			}
			seenKept = seenKept || msg.SessionID == kept
		default:
			if !seenKept {
				t.Fatal("keep-running control did not continue")
			}
			return
		}
	}
}

func TestDelegationSecurityTerminalCleanupRetriesBusyStartAndFindsLateChild(t *testing.T) {
	fx := newDelegFx(t)
	fx.api.delegationLife.lockWait = 500 * time.Millisecond
	fx.api.delegationLife.retryEvery = time.Millisecond
	var child string
	err := fx.api.withDelegationLock(t.Context(), "owner-1", func() error {
		securityEndParent(t, fx)
		fx.api.onParentTerminalEnd(fx.parent)
		// Force at least one bounded account-lock failure before insertion.
		// Cleanup must retry instead of silently abandoning the late child.
		time.Sleep(750 * time.Millisecond)
		child, _ = fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
		fx.hub.SetSessionOwner(child, fx.dc.ID)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	awaitLifecycle(t, func() bool {
		return derefOrEmpty(fx.row(child).EndReason) == db.EndReasonParentEnded && db.TerminallyEnded(fx.row(child))
	})
}

func TestDelegationSecurityHelloParentEndsDuringAskLookup(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	kept, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET keep_running=true WHERE id=$1`, kept); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	blocker, err := fx.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	tx, err := blocker.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `LOCK TABLE messages IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(fx.hub.ServeDaemon(delegDaemonToken, fx.pool))
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(protocol.DaemonHello{Type: "daemon_hello", Name: fx.dc.Name, Mode: "local", Token: delegDaemonToken,
		ProtocolVersion: "1", Welcome: true, RecoveryReady: true, ActiveSessions: []string{fx.parent, child, kept}}); err != nil {
		t.Fatal(err)
	}
	var welcome protocol.DaemonWelcome
	if err := json.Unmarshal(readFirstMessage(t, conn), &welcome); err != nil || welcome.Type != "daemon_welcome" || !welcome.RecoveryHold {
		t.Fatalf("negotiated recovery hold missing: %v", err)
	}
	// The real HELLO handler has passed reconciliation and is now waiting on
	// its ask snapshot. Ending the parent here defeats a snapshot-only guard.
	awaitLifecycle(t, func() bool {
		var waiting bool
		if err := fx.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1::int=ANY(pg_blocking_pids(pid)))`, int(blocker.Conn().PgConn().PID())).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		return waiting
	})
	securityEndParent(t, fx)
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	seenKill := false
	if err := conn.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("hello failed before readiness: %v", err)
		}
		var msg struct {
			Type      string `json:"type"`
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type == "kill_session" {
			if msg.SessionID == kept {
				t.Fatal("recovery killed keep-running control")
			}
			seenKill = seenKill || msg.SessionID == child
		}
		if msg.Type == "daemon_recovery_ready" {
			if !seenKill || derefOrEmpty(fx.row(child).EndReason) != db.EndReasonParentEnded {
				t.Fatal("ready overtook durable orphan end or its kill frame")
			}
			if db.TerminallyEnded(fx.row(kept)) {
				t.Fatal("keep-running control was finalized")
			}
			return
		}
	}
}

func TestDelegationSecurityPersistentApprovalRejectsEarlierParentEpoch(t *testing.T) {
	for _, selected := range []string{"session", "always"} {
		t.Run(selected, func(t *testing.T) {
			fx := newDelegFx(t)
			request := fx.ask(fx.parent, fx.parentTok, codexBody())
			now := time.Now()
			// Simulate a missed cleanup callback followed by a legitimate revival.
			// Current running status cannot authorize a request from the old epoch.
			if err := db.EndSessionStatus(t.Context(), fx.pool, fx.parent, "ended", &now, systemEnd(db.EndReasonProcessExited)); err != nil {
				t.Fatal(err)
			}
			if err := db.InsertSessionAs(t.Context(), fx.pool, fx.parent, fx.dc.ID, "running", "/repos/app", "app", "Parent", "", db.SessionOrigin{}); err != nil {
				t.Fatal(err)
			}
			if fx.row(fx.parent).DelegationEndedAt == nil {
				t.Fatal("revival erased durable parent boundary")
			}
			code, _ := fx.decide(request, fx.human("owner-1"), choice(selected))
			if code != http.StatusUnprocessableEntity {
				t.Errorf("old-epoch %s approval returned %d, want 422", selected, code)
			}
			if fx.count(`SELECT count(*) FROM delegation_grants WHERE parent_session_id=$1`, fx.parent) != 0 || fx.count(`SELECT count(*) FROM delegation_rules WHERE account_id=$1`, "owner-1") != 0 {
				t.Fatal("old request minted durable authorization in a later parent epoch")
			}
		})
	}
}

func TestDelegationSecurityInputGuardCancellationAndRuntimeMintWithOneMainConnection(t *testing.T) {
	fx := newDelegFx(t)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, runtime: runnerRuntimeCluster})
	cfg := fx.pool.Config().Copy()
	cfg.MaxConns = 1
	cfg.MinConns = 0
	single, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer single.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	err = withLimitResumeActionLock(ctx, single, child, func(action *pgxpool.Conn) error {
		return withParentInputGuard(ctx, single, child, func() error {
			// An inserted cancellation row takes a FK key-share on the child.
			// It must autocommit on the action connection, rather than remain
			// in the parent's guard transaction while runtime mint locks child.
			if err := db.CancelPendingLimitResume(ctx, action, child); err != nil {
				return err
			}
			runtime, messaging, err := db.MintRuntimeLaunch(ctx, single, child, "", time.Hour)
			if err != nil {
				return err
			}
			if runtime == "" || messaging == "" {
				t.Error("runtime launch did not return both credentials")
			}
			return nil
		})
	})
	if err != nil {
		t.Fatalf("guard/action cancellation blocked runtime mint with MaxConns=1: %v", err)
	}
}
