package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestDelegationPersistentApprovalRechecksCurrentParent(t *testing.T) {
	for _, decision := range []string{"session", "always"} {
		for _, mode := range []string{"private", "sandbox", "owner", "ended", "child"} {
			t.Run(decision+"/"+mode, func(t *testing.T) {
				fx := newDelegFx(t)
				if _, err := fx.pool.Exec(context.Background(), `UPDATE sessions SET runtime='daemon' WHERE id=$1`, fx.parent); err != nil {
					t.Fatal(err)
				}
				body := codexBody()
				body["runtime"] = "daemon"
				id := fx.ask(fx.parent, fx.parentTok, body)
				sql := map[string]string{"private": `UPDATE sessions SET private=true WHERE id=$1`, "sandbox": `UPDATE sessions SET runtime='docker' WHERE id=$1`, "owner": `UPDATE sessions SET spawning_account_id='owner-2' WHERE id=$1`, "ended": `UPDATE sessions SET status='ended',ended_at=now(),end_reason='process_exited' WHERE id=$1`, "child": `UPDATE sessions SET delegation_via='request:old' WHERE id=$1`}[mode]
				if _, err := fx.pool.Exec(context.Background(), sql, fx.parent); err != nil {
					t.Fatal(err)
				}
				code, _ := fx.decide(id, fx.human("owner-1"), choice(decision))
				if code < 400 {
					t.Fatalf("stale persistent approval=%d", code)
				}
				if n := fx.count(`SELECT count(*) FROM delegation_rules`); n != 0 {
					t.Fatalf("stale rule created: %d", n)
				}
				if n := fx.count(`SELECT count(*) FROM delegation_grants`); n != 0 {
					t.Fatalf("stale grant created: %d", n)
				}
				if n := fx.count(`SELECT count(*) FROM sessions WHERE parent_session_id=$1`, fx.parent); n != 0 {
					t.Fatalf("stale child created: %d", n)
				}
			})
		}
	}
}

func TestDelegationDecisionAndRulesRejectAllTrailingJSON(t *testing.T) {
	fx := newDelegFx(t)
	id := fx.ask(fx.parent, fx.parentTok, codexBody())
	owner := fx.human("owner-1")
	for _, suffix := range []string{"]", "}", "{}", "false", " x"} {
		r := httptest.NewRequest(http.MethodPost, "/api/delegation-requests/"+id+"/decide", strings.NewReader(`{"choice":"once"}`+suffix))
		r.Header.Set("Authorization", "Bearer "+owner)
		w := httptest.NewRecorder()
		fx.mux.ServeHTTP(w, r)
		if w.Code != http.StatusUnprocessableEntity {
			t.Errorf("suffix %q: %d", suffix, w.Code)
		}
		r = httptest.NewRequest(http.MethodPost, "/api/delegation-rules", strings.NewReader(`{"engine":"codex","runtime":"daemon","daemon_id":"`+fx.dc.ID+`"}`+suffix))
		r.Header.Set("Authorization", "Bearer "+owner)
		w = httptest.NewRecorder()
		fx.mux.ServeHTTP(w, r)
		if w.Code != http.StatusUnprocessableEntity {
			t.Errorf("rule suffix %q: %d", suffix, w.Code)
		}
	}
	if n := fx.count(`SELECT count(*) FROM delegation_rules`); n != 0 {
		t.Fatal("malformed rule persisted")
	}
}
func TestDelegationLargeHoldCannotOverflow(t *testing.T) {
	if got := delegationHold("9223372036854775807"); got != 25*time.Second {
		t.Fatalf("overflowed hold=%v", got)
	}
}

func TestDelegationInterruptedRecoveryWinsBeforeQueuedWorker(t *testing.T) {
	fx := newDelegFx(t)
	ctx := t.Context()
	id := fx.ask(fx.parent, fx.parentTok, codexBody())
	release, err := fx.api.delegLocks.acquire(ctx, "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	owner := fx.human("owner-1")
	done := make(chan map[string]any, 1)
	go func() { _, out := fx.decide(id, owner, choice("once")); done <- out }()
	deadline := time.Now().Add(15 * time.Second)
	for fx.request(id).Status != "allowed" {
		if time.Now().After(deadline) {
			t.Fatal("allow not committed")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE delegation_requests SET decided_at=now()-interval '3 minutes' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if n := fx.api.maintainDelegations(ctx); n != 1 {
		t.Fatalf("sweep changed %d requests", n)
	}
	release()
	released = true
	var out map[string]any
	select {
	case out = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("queued worker stuck")
	}
	if asString(out["child_session_id"]) != "" || out["start_error"] != msgStartInterrupted {
		t.Fatalf("worker ignored recovery: %v", out)
	}
	fx.noSpawn()
	event := fx.decidedEvent(fx.parent, id)
	if event["start_error"] != msgStartInterrupted || asString(event["child_session_id"]) != "" {
		t.Fatalf("event contradicts terminal outcome: %v", event)
	}
}

func TestDelegationSweeperCannotFinalizeAnActiveWorker(t *testing.T) {
	fx := newDelegFx(t)
	ctx := t.Context()
	id := fx.ask(fx.parent, fx.parentTok, codexBody())
	blocker, err := fx.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err := blocker.Exec(ctx, `LOCK TABLE runner_idempotency IN SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	owner := fx.human("owner-1")
	done := make(chan map[string]any, 1)
	go func() { _, out := fx.decide(id, owner, choice("once")); done <- out }()
	control, err := pgx.ConnectConfig(ctx, fx.pool.Config().ConnConfig.Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = control.Close(context.Background()) }()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var acquired bool
		if err := control.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtext('delegation:owner-1'))`).Scan(&acquired); err != nil {
			t.Fatal(err)
		}
		if !acquired {
			break
		}
		if _, err := control.Exec(ctx, `SELECT pg_advisory_unlock(hashtext('delegation:owner-1'))`); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("worker never held account fence")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := fx.pool.Exec(ctx, `UPDATE delegation_requests SET decided_at=now()-interval '3 minutes' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if n := fx.api.maintainDelegations(ctx); n != 0 {
		t.Fatalf("active start reported interrupted: %d", n)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	select {
	case out = <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("active start stuck")
	}
	fx.spawn()
	if asString(out["child_session_id"]) == "" || asString(out["start_error"]) != "" {
		t.Fatalf("active start did not complete: %v", out)
	}
	event := fx.decidedEvent(fx.parent, id)
	if event["child_session_id"] != out["child_session_id"] || asString(event["start_error"]) != "" {
		t.Fatalf("event contradicts child: %v", event)
	}
}
