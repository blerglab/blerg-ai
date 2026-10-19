package server

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func lifecycleFixture(t *testing.T) (*delegFx, string, string, string) {
	t.Helper()
	fx := newDelegFx(t)
	request := fx.ask(fx.parent, fx.parentTok, codexBody())
	fx.grant(fx.parent, "codex", "docker", "app", false)
	child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	kept, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET keep_running=true WHERE id=$1`, kept); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{fx.parent, child, kept} {
		fx.hub.SetSessionOwner(id, fx.dc.ID)
	}
	return fx, child, kept, request
}

func awaitLifecycle(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("delegation lifecycle did not settle")
		}
		time.Sleep(time.Millisecond)
	}
}

func assertParentCleanup(t *testing.T, fx *delegFx, child, kept, request string) {
	t.Helper()
	awaitLifecycle(t, func() bool {
		row := fx.row(child)
		return row.EndReason != nil && *row.EndReason == db.EndReasonParentEnded &&
			fx.request(request).Status == "expired" &&
			fx.decidedEvent(fx.parent, request) != nil &&
			fx.count(`SELECT count(*) FROM delegation_grants WHERE parent_session_id=$1 AND revoked_at IS NULL`, fx.parent) == 0
	})
	if !db.TerminallyEnded(fx.row(child)) || db.TerminallyEnded(fx.row(kept)) {
		t.Fatal("child end or keep-running policy was lost")
	}
	if ev := fx.decidedEvent(fx.parent, request); ev["status"] != "expired" {
		t.Fatalf("expired request event: %v", ev)
	}
	found := false
	for {
		select {
		case raw := <-fx.dc.send:
			var msg protocol.KillSession
			if err := json.Unmarshal(raw, &msg); err != nil {
				t.Fatal(err)
			}
			if msg.Type == "kill_session" {
				if msg.SessionID == kept {
					t.Fatal("kept child received kill")
				}
				found = found || msg.SessionID == child
			}
		default:
			if !found {
				t.Fatal("child never received kill")
			}
			return
		}
	}
}

func TestTerminalEndStopsChildrenRevokesGrantsExpiresRequests(t *testing.T) {
	fx, child, kept, request := lifecycleFixture(t)
	HandleSessionEnded(t.Context(), fx.hub, fx.pool, protocol.SessionEnded{SessionID: fx.parent})
	assertParentCleanup(t, fx, child, kept, request)
}

func TestPersonStopOfParentDoesTheSame(t *testing.T) {
	fx, child, kept, request := lifecycleFixture(t)
	if _, err := fx.api.Stop(t.Context(), runnerPrincipal{Kind: "runner_key"}, fx.parent); err != nil {
		t.Fatal(err)
	}
	assertParentCleanup(t, fx, child, kept, request)
}

func TestStateOnlyParentExitCleansChildren(t *testing.T) {
	fx, child, kept, request := lifecycleFixture(t)
	if err := HandleSessionStateChanged(t.Context(), fx.hub, fx.pool, protocol.SessionStateChanged{SessionID: fx.parent, Status: "stopped"}); err != nil {
		t.Fatal(err)
	}
	if !db.TerminallyEnded(fx.row(fx.parent)) {
		t.Fatal("state-only process exit was not a durable real end")
	}
	assertParentCleanup(t, fx, child, kept, request)
}

func TestPauseExpiresRequestsButKeepsChildrenAndGrants(t *testing.T) {
	fx, child, kept, request := lifecycleFixture(t)
	handleSessionPaused(t.Context(), fx.hub, fx.pool, fx.dc, protocol.SessionPaused{SessionID: fx.parent, Reason: protocol.PausedByRestart})
	awaitLifecycle(t, func() bool {
		return fx.request(request).Status == "expired" && fx.decidedEvent(fx.parent, request) != nil
	})
	for _, id := range []string{child, kept} {
		if db.TerminallyEnded(fx.row(id)) {
			t.Fatal("pause ended child")
		}
	}
	if fx.count(`SELECT count(*) FROM delegation_grants WHERE parent_session_id=$1 AND revoked_at IS NULL`, fx.parent) != 1 {
		t.Fatal("pause revoked grant")
	}
	if ev := fx.decidedEvent(fx.parent, request); ev["status"] != "expired" {
		t.Fatalf("pause missing decision event: %v", ev)
	}
	HandleSessionStarted(t.Context(), fx.hub, fx.pool, fx.dc.ID, protocol.SessionStarted{SessionID: fx.parent, Kind: "agent"})
	grant, err := db.FindDelegationGrant(t.Context(), fx.pool, fx.parent, "codex", "docker", fx.dc.ID)
	if err != nil || grant == nil {
		t.Fatalf("restart erased a surviving session grant: %v", err)
	}
}

func TestReconcileObservationDoesNotExpireDelegation(t *testing.T) {
	fx, child, kept, request := lifecycleFixture(t)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET started_at=now()-interval '3 minutes' WHERE id=$1`, fx.parent); err != nil {
		t.Fatal(err)
	}
	reconcileSessionsQueued(t.Context(), fx.hub, fx.pool, fx.dc.ID, []string{child, kept}, nil, nil)
	parent := fx.row(fx.parent)
	if db.TerminallyEnded(parent) || derefOrEmpty(parent.EndReason) != db.EndReasonDaemonUnreported {
		t.Fatal("missing inventory was treated as a real parent end")
	}
	if fx.request(request).Status != "pending" || db.TerminallyEnded(fx.row(child)) || db.TerminallyEnded(fx.row(kept)) {
		t.Fatal("ordinary reconcile changed delegation lifetimes")
	}
	if fx.count(`SELECT count(*) FROM delegation_grants WHERE parent_session_id=$1 AND revoked_at IS NULL`, fx.parent) != 1 {
		t.Fatal("ordinary reconcile revoked a session grant")
	}
}

func TestRealEndPromotesEarlierRecoverableParentReason(t *testing.T) {
	for _, reason := range []string{db.EndReasonDaemonUnreported, db.EndReasonDaemonLost} {
		t.Run(reason, func(t *testing.T) {
			fx, child, kept, request := lifecycleFixture(t)
			if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='error',ended_at=now(),end_reason=$2 WHERE id=$1`, fx.parent, reason); err != nil {
				t.Fatal(err)
			}
			HandleSessionEnded(t.Context(), fx.hub, fx.pool, protocol.SessionEnded{SessionID: fx.parent, ExitCode: 1})
			row := fx.row(fx.parent)
			if !db.TerminallyEnded(row) || derefOrEmpty(row.EndReason) != db.EndReasonProcessFailed {
				t.Fatalf("recoverable reason masked real exit: %v", row.EndReason)
			}
			assertParentCleanup(t, fx, child, kept, request)
		})
	}
}

func TestOrphanedRecoverableAndQueuedChildrenCannotRevive(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "queued"}[queued], func(t *testing.T) {
			fx := newDelegFx(t)
			child, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent})
			ctx := context.Background()
			if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET status='stopped',ended_at=now(),end_reason='daemon_unreported' WHERE id=$1`, child); err != nil {
				t.Fatal(err)
			}
			if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET status='ended',ended_at=now(),end_reason='process_exited' WHERE id=$1`, fx.parent); err != nil {
				t.Fatal(err)
			}
			active, queue := []string{child}, []string(nil)
			if queued {
				active, queue = nil, []string{child}
			}
			stopped := reconcileSessionsQueued(ctx, fx.hub, fx.pool, fx.dc.ID, active, queue, nil)
			if !slices.Contains(stopped, child) || derefOrEmpty(fx.row(child).EndReason) != db.EndReasonParentEnded {
				t.Fatalf("orphan revived or escaped kill: %v, reason %v", stopped, fx.row(child).EndReason)
			}
			HandleSessionStarted(ctx, fx.hub, fx.pool, fx.dc.ID, protocol.SessionStarted{SessionID: child, Kind: "agent"})
			if derefOrEmpty(fx.row(child).EndReason) != db.EndReasonParentEnded {
				t.Fatal("late start erased parent end")
			}
		})
	}
}
