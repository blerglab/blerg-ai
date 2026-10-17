package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestLimitResumeEligibility(t *testing.T) {
	for _, tc := range []struct {
		status, pause string
		background    int
		want          bool
	}{
		{"idle", "", 0, true}, {"disconnected", "", 0, true},
		{"running", "", 0, false}, {"waiting", "", 0, false},
		{"ended", "", 0, false}, {"idle", "restart", 0, false},
		{"idle", "", 1, false},
	} {
		row := &db.SessionRow{Kind: "agent", Status: tc.status, PausedReason: tc.pause, BackgroundTasks: tc.background}
		if got := limitResumeEligible(row); got != tc.want {
			t.Errorf("%+v: got %v", tc, got)
		}
	}
	if limitResumeEligible(&db.SessionRow{Kind: "tmux", Status: "idle"}) {
		t.Fatal("terminal eligible")
	}
}

func TestLimitResumePayloadValidation(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		payload string
		want    bool
	}{
		{`{"provider":"claude","turn_id":"turn-1"}`, true},
		{`{"provider":"codex","turn_id":"turn-1"}`, true},
		{`{"provider":"other","turn_id":"turn-1"}`, false},
		{`{"turn_id":"turn-1"}`, false},
		{`{"provider":"","turn_id":"turn-1"}`, false},
		{`{"provider":"claude"}`, false},
		{`{"provider":"claude","turn_id":"turn-1","reset_at":"2099-01-01T00:00:00Z"}`, false},
		{`broken`, false},
	} {
		_, ok := parseLimitRefusal([]byte(tc.payload), now)
		if ok != tc.want {
			t.Errorf("%s: %v", tc.payload, ok)
		}
	}
}

func TestLimitResumeIngestionAndDispatch(t *testing.T) {
	f := newIngestFixture(t)
	ctx := f.ctx
	// Concurrent workers open additional pooled connections. A one-connection SET
	// search_path from the shared fixture does not configure those connections.
	cfg := f.pool.Config().Copy()
	cfg.ConnConfig.RuntimeParams["search_path"] = "test_blerg_runner_srv"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f.pool = pool
	// A real opted-in agent session, with an idle connected runtime.
	_, err = f.pool.Exec(ctx, `UPDATE sessions SET kind='agent',engine='claude',spawning_account_id=$2 WHERE id=$1`, f.sessionID, newUUID())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SetLimitResume(ctx, f.pool, f.sessionID, true, newUUID()); err != nil {
		t.Fatal(err)
	}
	f.hub.Register(f.dc)
	f.hub.SetSessionOwner(f.sessionID, f.dc.ID)
	api := NewAPI(f.hub, f.pool, "", nil, "")
	at := time.Now().Add(-2 * time.Minute)
	reset := at.Add(-time.Minute)
	f.ingest("turn", "user_message", `{"turn_id":"turn-1","source":"chat"}`, at.Format(time.RFC3339Nano))
	raw, _ := json.Marshal(limitRefusal{Provider: "claude", TurnID: "turn-1", ResetAt: &reset})
	f.ingest("limit", "usage_limit", string(raw), at.Format(time.RFC3339Nano))
	state, err := db.GetLimitResume(ctx, f.pool, f.sessionID)
	if err != nil || state.State != "pending" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	// Duplicate ingestion does not create another attempt.
	f.ingest("limit", "usage_limit", string(raw), at.Format(time.RFC3339Nano))
	f.acks() // drain persistence acknowledgements
	var workers sync.WaitGroup
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); api.checkLimitResumes(ctx) }()
	}
	workers.Wait()
	var msg protocol.AgentUserMessage
	select {
	case raw := <-f.dc.send:
		if err = json.Unmarshal(raw, &msg); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("no continuation")
	}
	if msg.Source != limitResumeSource || msg.SessionID != f.sessionID {
		t.Fatalf("message=%+v", msg)
	}
	api.checkLimitResumes(ctx)
	select {
	case raw := <-f.dc.send:
		t.Fatalf("duplicate dispatch: %s", raw)
	default:
	}
	state, err = db.GetLimitResume(ctx, f.pool, f.sessionID)
	if err != nil || state.Attempts != 1 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestLimitResumeCancellationRejectsDelayedRefusal(t *testing.T) {
	f := newIngestFixture(t)
	if err := db.SetLimitResume(f.ctx, f.pool, f.sessionID, true, newUUID()); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Second)
	f.ingest("turn", "user_message", `{"turn_id":"old-turn","source":"chat"}`, at.Format(time.RFC3339Nano))
	if err := withCancelledLimitResume(f.ctx, f.pool, f.sessionID, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	reset := time.Now().Add(time.Hour)
	raw, _ := json.Marshal(limitRefusal{Provider: "claude", TurnID: "old-turn", ResetAt: &reset})
	f.ingest("late", "usage_limit", string(raw), at.Format(time.RFC3339Nano))
	state, err := db.GetLimitResume(f.ctx, f.pool, f.sessionID)
	if err != nil || state.State != "disabled" || state.Enabled {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestLimitedAutoStopWaitsForContinuation(t *testing.T) {
	api, pool, id := autoStopSession(t, true)
	ctx := context.Background()
	if err := db.SetLimitResume(ctx, pool, id, true, newUUID()); err != nil {
		t.Fatal(err)
	}
	if err := db.TrackLimitResumeTurn(ctx, pool, id, "turn-1", "chat", time.Now(), 1); err != nil {
		t.Fatal(err)
	}
	reset := time.Now().Add(time.Hour)
	if err := db.RecordLimitRefusal(ctx, pool, id, "claude", "turn-1", &reset, 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	turnDoneWithReason(t, api, pool, id, "error")
	if got := sessionStatus(t, pool, id); got != "running" {
		t.Fatalf("limited one-shot ended: %s", got)
	}
	if err := db.CancelLimitResume(ctx, pool, id); err != nil {
		t.Fatal(err)
	}
	api.autoStopOnTurnDone(id)
	if got := sessionStatus(t, pool, id); got != "ended" {
		t.Fatalf("cancelled one-shot still live: %s", got)
	}
}

func TestLimitResumeOwnerOnlyAndProofPrivate(t *testing.T) {
	f := newIngestFixture(t)
	api := NewAPI(f.hub, f.pool, "", nil, "")
	token := enableBrowserAuth(t, api)(coreAuthBrowserCap)
	if _, err := f.pool.Exec(f.ctx, `UPDATE sessions SET kind='agent',engine='claude',spawning_account_id='someone-else' WHERE id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	request := func(method, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/sessions/"+f.sessionID+"/limit-resume", strings.NewReader(body))
		req.SetPathValue("id", f.sessionID)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		api.HandleLimitResume(rec, req)
		return rec
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		if rec := request(method, `{"enabled":true}`); rec.Code != http.StatusNotFound {
			t.Fatalf("foreign %s: %d", method, rec.Code)
		}
	}
	if _, err := f.pool.Exec(f.ctx, `UPDATE sessions SET spawning_account_id='user-1' WHERE id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	if rec := request(http.MethodPut, `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing enabled: %d", rec.Code)
	}
	rec := request(http.MethodPut, `{"enabled":true}`)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), testSID) || strings.Contains(rec.Body.String(), "auth_session") {
		t.Fatalf("response: %d %s", rec.Code, rec.Body.String())
	}
	state, err := db.GetLimitResume(f.ctx, f.pool, f.sessionID)
	if err != nil || !state.Enabled || state.AuthSessionID != testSID {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

func TestAutomaticClusterResumeCannotFallbackToOperator(t *testing.T) {
	backend := &fakeK8s{}
	api, _, pool := clusterRunnerAPI(t, backend)
	ctx := context.Background()
	// The owner's credential has expired. Operator credentials exist, but an
	// automatic resume must not use them.
	resp, problem := api.StartSession(ctx, runnerPrincipal{Kind: runnerKeyPrincipalKind}, runnerStartRequest{Repo: "org/proj", Prompt: "work"}, "")
	if problem != nil {
		t.Fatal(problem)
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET status='disconnected',spawning_account_id='owner' WHERE id=$1`, resp.SessionID); err != nil {
		t.Fatal(err)
	}
	backend.mu.Lock()
	before := len(backend.created)
	backend.mu.Unlock()
	err := resumeClusterSessionStrict(ctx, api.hub, pool, resp.SessionID, limitResumePrompt, "owner", testSID)
	if err == nil {
		t.Fatal("automatic resume accepted without personal credential")
	}
	backend.mu.Lock()
	after := len(backend.created)
	backend.mu.Unlock()
	if after != before {
		t.Fatal("automatic resume created a pod with operator credentials")
	}
}

func TestLimitResumeWaitsForSleepingWorkstation(t *testing.T) {
	f := newIngestFixture(t)
	ctx := f.ctx
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET kind='agent',status='error',daemon_lost_at=now() WHERE id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	if err := db.SetLimitResume(ctx, f.pool, f.sessionID, true, newUUID()); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-3 * time.Minute)
	if err := db.TrackLimitResumeTurn(ctx, f.pool, f.sessionID, "sleeping-turn", "chat", at, 1); err != nil {
		t.Fatal(err)
	}
	reset := at.Add(-time.Minute)
	if err := db.RecordLimitRefusal(ctx, f.pool, f.sessionID, "claude", "sleeping-turn", &reset, 2, at); err != nil {
		t.Fatal(err)
	}
	api := NewAPI(f.hub, f.pool, "", nil, "")
	api.checkLimitResumes(ctx)
	state, err := db.GetLimitResume(ctx, f.pool, f.sessionID)
	if err != nil || state.State != "pending" || state.Attempts != 0 {
		t.Fatalf("sleeping host consumed retry: %+v %v", state, err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE sessions SET status='idle',daemon_lost_at=NULL WHERE id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	f.hub.Register(f.dc)
	f.hub.SetSessionOwner(f.sessionID, f.dc.ID)
	api.checkLimitResumes(ctx)
	select {
	case raw := <-f.dc.send:
		var msg protocol.AgentUserMessage
		if json.Unmarshal(raw, &msg) != nil || msg.ExpectedTurnID != "sleeping-turn" {
			t.Fatalf("wrong continuation: %s", raw)
		}
	default:
		t.Fatal("no continuation after host reconnected")
	}
}

// Sessions that never opted in (no session_limit_resume row) skip the lock and
// the upsert on every user_message; a refusal or a later opt-in recovers the
// turn from the transcript, so continuation still works.
func TestLimitResumeTracksTurnsLazily(t *testing.T) {
	for _, scenario := range []string{"untouched", "enable-after-message", "enable-after-refusal"} {
		t.Run(scenario, func(t *testing.T) {
			f := newIngestFixture(t)
			ctx := f.ctx
			if _, err := f.pool.Exec(ctx, `UPDATE sessions SET kind='agent',engine='claude' WHERE id=$1`, f.sessionID); err != nil {
				t.Fatal(err)
			}
			at := time.Now().Add(-2 * time.Minute)
			f.ingest("turn", "user_message", `{"turn_id":"turn-1","source":"chat"}`, at.Format(time.RFC3339Nano))
			var rows int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM session_limit_resume WHERE session_id=$1`, f.sessionID).Scan(&rows); err != nil {
				t.Fatal(err)
			}
			if rows != 0 {
				t.Fatal("a session without the option got a limit-resume row on user_message")
			}
			if scenario == "untouched" {
				return
			}
			reset := at.Add(-time.Minute)
			raw, _ := json.Marshal(limitRefusal{Provider: "claude", TurnID: "turn-1", ResetAt: &reset})
			if scenario == "enable-after-message" {
				if err := db.SetLimitResume(ctx, f.pool, f.sessionID, true, newUUID()); err != nil {
					t.Fatal(err)
				}
				f.ingest("limit", "usage_limit", string(raw), at.Format(time.RFC3339Nano))
			} else {
				f.ingest("limit", "usage_limit", string(raw), at.Format(time.RFC3339Nano))
				if err := db.SetLimitResume(ctx, f.pool, f.sessionID, true, newUUID()); err != nil {
					t.Fatal(err)
				}
			}
			state, err := db.GetLimitResume(ctx, f.pool, f.sessionID)
			if err != nil || state.State != "pending" || state.TurnID != "turn-1" {
				t.Fatalf("state=%+v err=%v", state, err)
			}
		})
	}
}

// In a pod, ingestion runs inside the runtime fence's 5 s deadline. A held
// limit-resume lock must not use all of it: the frame still commits, so the
// pod is not disconnected.
func TestLimitResumeIngestWaitLeavesTheFenceTimeToCommit(t *testing.T) {
	f := newIngestFixture(t)
	ctx := f.ctx
	if err := db.SetLimitResume(ctx, f.pool, f.sessionID, true, newUUID()); err != nil {
		t.Fatal(err)
	}
	held := make(chan struct{})
	release := make(chan struct{})
	holder := make(chan error, 1)
	go func() {
		holder <- db.WithLimitResumeLock(ctx, f.pool, f.sessionID, func(*pgxpool.Conn) error {
			close(held)
			<-release
			return nil
		})
	}()
	<-held
	msg := protocol.AgentEvent{SessionID: f.sessionID, Kind: "user_message", Ts: time.Now().Format(time.RFC3339Nano), Payload: json.RawMessage(`{"turn_id":"turn-1","source":"chat"}`)}
	var ingestErr error
	start := time.Now()
	fenceErr := db.WithRuntimeLaunchFence(ctx, f.pool, f.sessionID, func(fenced context.Context) error {
		ingestErr = ingestLimitResume(fenced, f.hub, f.pool, msg, 1)
		return nil
	})
	close(release)
	if err := <-holder; err != nil {
		t.Fatal(err)
	}
	if fenceErr != nil {
		t.Fatalf("the frame did not commit after %v: %v (ingest: %v)", time.Since(start), fenceErr, ingestErr)
	}
	if !errors.Is(ingestErr, db.ErrLimitResumeBusy) {
		t.Fatalf("ingest under a held lock: %v", ingestErr)
	}
}

func TestLimitResumeIngestWaitSkipsWithoutTimeLeft(t *testing.T) {
	if got := limitResumeIngestWaitFor(context.Background()); got != limitResumeIngestWait {
		t.Fatalf("no deadline: %v", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if got := limitResumeIngestWaitFor(ctx); got <= 0 || got > 1500*time.Millisecond {
		t.Fatalf("2 s left: %v", got)
	}
	short, cancelShort := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancelShort()
	if got := limitResumeIngestWaitFor(short); got > 0 {
		t.Fatalf("400 ms left: %v", got)
	}
}
