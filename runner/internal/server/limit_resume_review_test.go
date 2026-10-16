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
)

func TestReviewOldLimitedTurnDoneDoesNotKillQueuedManualContinuation(t *testing.T) {
	api, pool, id := autoStopSession(t, true)
	ctx := context.Background()
	if err := db.SetLimitResume(ctx, pool, id, true, testSID); err != nil {
		t.Fatal(err)
	}
	if err := db.TrackLimitResumeTurn(ctx, pool, id, "limited-turn", "chat", time.Now().Add(-time.Minute), 1); err != nil {
		t.Fatal(err)
	}
	reset := time.Now().Add(time.Hour)
	if err := db.RecordLimitRefusal(ctx, pool, id, "claude", "limited-turn", &reset, 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	// A manual continuation has been accepted, but the runtime has not yet
	// consumed it and emitted its new user_message. The old turn then ends.
	if err := withCancelledPendingLimitResume(ctx, pool, id, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	turnDoneWithReason(t, api, pool, id, "error")
	if got := sessionStatus(t, pool, id); got != "running" {
		t.Fatalf("old turn killed queued continuation: %s", got)
	}
	if err := db.TrackLimitResumeTurn(ctx, pool, id, "manual-turn", "chat", time.Now().Add(time.Second), 100); err != nil {
		t.Fatal(err)
	}
	turnDoneWithReason(t, api, pool, id, "end_turn")
	if got := sessionStatus(t, pool, id); got != "ended" {
		t.Fatalf("new completed turn did not auto-stop: %s", got)
	}
}

func TestReviewManualTurnFenceIsBoundedAndDoesNotSuppressDisable(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "expired fence", true: "disabled preference"}[disabled], func(t *testing.T) {
			api, pool, id := autoStopSession(t, true)
			ctx := context.Background()
			if err := db.SetLimitResume(ctx, pool, id, true, testSID); err != nil {
				t.Fatal(err)
			}
			if err := db.TrackLimitResumeTurn(ctx, pool, id, "old-turn", "chat", time.Now(), 1); err != nil {
				t.Fatal(err)
			}
			if err := db.CancelPendingLimitResume(ctx, pool, id); err != nil {
				t.Fatal(err)
			}
			if disabled {
				if err := db.CancelLimitResume(ctx, pool, id); err != nil {
					t.Fatal(err)
				}
			} else if _, err := pool.Exec(ctx, `UPDATE session_limit_resume SET cancelled_at=now()-interval '3 minutes' WHERE session_id=$1`, id); err != nil {
				t.Fatal(err)
			}
			turnDoneWithReason(t, api, pool, id, "error")
			if got := sessionStatus(t, pool, id); got != "ended" {
				t.Fatalf("fence prevented normal stop: %s", got)
			}
		})
	}
}

func TestReviewDisableResumeDoesNotStopRunningAutoStop(t *testing.T) {
	f := newIngestFixture(t)
	api := NewAPI(f.hub, f.pool, "", nil, "")
	token := enableBrowserAuth(t, api)(coreAuthBrowserCap)
	if _, err := f.pool.Exec(f.ctx, `UPDATE sessions SET kind='agent',engine='claude',spawning_account_id='user-1',auto_stop=true,status='running' WHERE id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	if err := db.SetLimitResume(f.ctx, f.pool, f.sessionID, true, testSID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE session_limit_resume SET state='pending',reset_at=now()+interval '1 hour',expires_at=now()+interval '8 days' WHERE session_id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/sessions/"+f.sessionID+"/limit-resume", strings.NewReader(`{"enabled":false}`))
	req.SetPathValue("id", f.sessionID)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	api.HandleLimitResume(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable: %d %s", rec.Code, rec.Body.String())
	}
	if got := sessionStatus(t, f.pool, f.sessionID); got != "running" {
		t.Fatalf("disable killed running session: %s", got)
	}
}

func TestReviewCancelledBacklogCannotRearmAfterDisplayTimeClamp(t *testing.T) {
	f := newIngestFixture(t)
	if err := db.SetLimitResume(f.ctx, f.pool, f.sessionID, true, testSID); err != nil {
		t.Fatal(err)
	}
	// A cancellation two hours ago must fence an unseen three-hour-old backlog turn.
	if _, err := f.pool.Exec(f.ctx, `UPDATE session_limit_resume SET cancelled_at=now()-interval '2 hours',cancelled_turn_id='known-turn',turn_id='known-turn' WHERE session_id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-3 * time.Hour)
	f.ingest("backlogged-turn", "user_message", `{"turn_id":"unseen-old-turn","source":"chat"}`, at.Format(time.RFC3339Nano))
	reset := time.Now().Add(time.Hour)
	raw, _ := json.Marshal(limitRefusal{Provider: "claude", TurnID: "unseen-old-turn", ResetAt: &reset})
	f.ingest("backlogged-refusal", "usage_limit", string(raw), at.Format(time.RFC3339Nano))
	state, err := db.GetLimitResume(f.ctx, f.pool, f.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if state.State == "pending" || state.TurnID == "unseen-old-turn" {
		t.Fatalf("stale backlog rearmed continuation: %+v", state)
	}
}

func TestReviewRetryPersistenceFailureBlocksLaterTurnDone(t *testing.T) {
	f := newIngestFixture(t)
	if err := db.SetLimitResume(f.ctx, f.pool, f.sessionID, true, testSID); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	f.ingest("current-turn", "user_message", `{"turn_id":"current-turn","source":"chat"}`, at.Format(time.RFC3339Nano))
	f.acks()
	// Fail only scheduling, after the transcript append succeeds.
	if _, err := f.pool.Exec(f.ctx, `CREATE FUNCTION review_fail_limit_schedule() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.provider='claude' THEN RAISE EXCEPTION 'injected scheduling failure'; END IF; RETURN NEW; END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(f.ctx, `CREATE TRIGGER review_fail_limit_schedule BEFORE UPDATE ON session_limit_resume FOR EACH ROW EXECUTE FUNCTION review_fail_limit_schedule()`); err != nil {
		t.Fatal(err)
	}
	reset := at.Add(time.Hour)
	raw, _ := json.Marshal(limitRefusal{Provider: "claude", TurnID: "current-turn", ResetAt: &reset})
	f.ingest("failed-refusal", "usage_limit", string(raw), at.Format(time.RFC3339Nano))
	called := 0
	f.hub.SetAutoStopper(func(string) { called++ })
	f.ingest("later-turn-done", "turn_done", `{"stop_reason":"error"}`, at.Format(time.RFC3339Nano))
	if called != 0 {
		t.Fatal("turn_done overtook failed retry persistence")
	}
	if got := f.acks(); len(got) != 0 {
		t.Fatalf("unpersisted scheduling acknowledged: %v", got)
	}
	if _, err := f.pool.Exec(f.ctx, `DROP TRIGGER review_fail_limit_schedule ON session_limit_resume`); err != nil {
		t.Fatal(err)
	}
	f.ingest("failed-refusal", "usage_limit", string(raw), at.Format(time.RFC3339Nano))
	f.ingest("later-turn-done", "turn_done", `{"stop_reason":"error"}`, at.Format(time.RFC3339Nano))
	if called != 1 {
		t.Fatalf("turn_done not resumed after scheduling recovered: %d", called)
	}
	state, err := db.GetLimitResume(f.ctx, f.pool, f.sessionID)
	if err != nil || state.State != "pending" {
		t.Fatalf("recovered scheduling: %+v %v", state, err)
	}
}
