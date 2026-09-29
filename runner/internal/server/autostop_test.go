package server

// One-shot sessions (agent contract v1 addendum): a session started with
// auto_stop:true is stopped by the runner as soon as its first turn finishes,
// so a fire-and-forget caller gets a terminal result and a completion webhook
// without having to stop the session itself.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// autoStopSession starts a cluster session through the shared contract entry
// point and returns the API, the pool and the new session id.
func autoStopSession(t *testing.T, autoStop bool) (*API, *pgxpool.Pool, string) {
	t.Helper()
	api, _, pool := clusterRunnerAPI(t, &fakeK8s{})
	resp, apiErr := api.StartSession(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind},
		runnerStartRequest{Repo: "org/proj", Title: "T", Prompt: "do one thing", AutoStop: autoStop}, "")
	if apiErr != nil {
		t.Fatalf("StartSession: %v", apiErr)
	}
	// The session is "starting" until the runtime reports in; a turn_done can
	// only follow a session that is actually running.
	ctx := context.Background()
	if err := db.UpdateSessionStatus(ctx, pool, resp.SessionID, "running", nil); err != nil {
		t.Fatal(err)
	}
	return api, pool, resp.SessionID
}

// turnDone feeds one turn_done event in over the data plane, exactly as the
// pod's daemon connection does.
func turnDone(t *testing.T, api *API, pool *pgxpool.Pool, sessionID string) {
	t.Helper()
	turnDoneWithReason(t, api, pool, sessionID, "end_turn")
}

// turnDoneWithReason is turnDone with the stop reason spelled out — a turn that
// died on a provider error ends with "error" and must stop a one-shot session
// exactly as a clean turn does.
func turnDoneWithReason(t *testing.T, api *API, pool *pgxpool.Pool, sessionID, stopReason string) {
	t.Helper()
	dc := &DaemonConn{ID: newUUID(), send: make(chan []byte, 8)}
	api.hub.Register(dc)
	api.hub.SetSessionOwner(sessionID, dc.ID)
	HandleAgentEvent(context.Background(), api.hub, pool, dc, protocol.AgentEvent{
		Type: "agent_event", SessionID: sessionID, ClientEventID: newUUID(),
		Ts: "2026-09-25T00:00:00Z", Kind: "turn_done",
		Payload: json.RawMessage(`{"stop_reason":"` + stopReason + `"}`),
	})
}

// deletedJobNames is fakeK8s.deleted read under its lock.
func deletedJobNames(f *fakeK8s) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

func sessionStatus(t *testing.T, pool *pgxpool.Pool, sessionID string) string {
	t.Helper()
	row, err := db.GetSession(context.Background(), pool, sessionID)
	if err != nil || row == nil {
		t.Fatalf("GetSession: %v (row %v)", err, row)
	}
	return row.Status
}

// The flag is persisted by start and reported by both the status and the
// result endpoints, so a caller can see which mode a session is in.
func TestAutoStopPersistedAndReported(t *testing.T) {
	api, pool, sessionID := autoStopSession(t, true)
	ctx := context.Background()

	row, err := db.GetSession(ctx, pool, sessionID)
	if err != nil || row == nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !row.AutoStop {
		t.Error("auto_stop was not persisted on the session row")
	}
	p := runnerPrincipal{Kind: runnerKeyPrincipalKind}
	status, apiErr := api.SessionStatus(ctx, p, sessionID)
	if apiErr != nil {
		t.Fatalf("SessionStatus: %v", apiErr)
	}
	if status["auto_stop"] != true {
		t.Errorf("status auto_stop = %v, want true", status["auto_stop"])
	}
	res, apiErr := api.Result(ctx, p, sessionID)
	if apiErr != nil {
		t.Fatalf("Result: %v", apiErr)
	}
	if !res.AutoStop {
		t.Error("result auto_stop = false, want true")
	}
}

// A default (interactive) session reports the flag as false and is NOT ended
// by a turn finishing — that is the whole point of the default.
func TestTurnDoneLeavesInteractiveSessionRunning(t *testing.T) {
	api, pool, sessionID := autoStopSession(t, false)

	res, apiErr := api.Result(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, sessionID)
	if apiErr != nil {
		t.Fatalf("Result: %v", apiErr)
	}
	if res.AutoStop {
		t.Error("result auto_stop = true for a session started without it")
	}

	turnDone(t, api, pool, sessionID)
	if got := sessionStatus(t, pool, sessionID); got != "running" {
		t.Errorf("status after turn_done = %q, want running", got)
	}
}

// The first turn_done ends an auto-stop session for good: terminal result,
// status "ended", the Job deleted exactly once, session tokens revoked, and
// the completion webhook fired. A second turn_done changes nothing.
func TestTurnDoneEndsAutoStopSessionOnce(t *testing.T) {
	f := &fakeK8s{}
	api, _, pool := clusterRunnerAPI(t, f)
	api.webhookBackoff = []time.Duration{0, 0, 0}
	// A real receiver, so "the webhook follows" is observed end to end rather
	// than asserted about an internal call.
	delivered := make(chan string, 4)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered <- r.Header.Get("X-Blerg-Session")
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(receiver.Close)
	allowLoopbackDelivery(t, receiver.URL)

	ctx := context.Background()
	resp, apiErr := api.StartSession(ctx, runnerPrincipal{Kind: runnerKeyPrincipalKind},
		runnerStartRequest{Repo: "org/proj", Title: "T", Prompt: "one thing", AutoStop: true}, "")
	if apiErr != nil {
		t.Fatalf("StartSession: %v", apiErr)
	}
	sessionID := resp.SessionID
	if err := db.UpdateSessionStatus(ctx, pool, sessionID, "running", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionCallback(ctx, pool, sessionID, receiver.URL, ""); err != nil {
		t.Fatal(err)
	}

	turnDone(t, api, pool, sessionID)

	if got := sessionStatus(t, pool, sessionID); got != "ended" {
		t.Fatalf("status after turn_done = %q, want ended", got)
	}
	res, apiErr := api.Result(ctx, runnerPrincipal{Kind: runnerKeyPrincipalKind}, sessionID)
	if apiErr != nil {
		t.Fatalf("Result: %v", apiErr)
	}
	if !res.Terminal || res.Lifecycle != "ended" {
		t.Errorf("result = lifecycle %q terminal %v, want ended/true", res.Lifecycle, res.Terminal)
	}
	if res.EndedAt == nil {
		t.Error("ended_at must be set by an auto-stop")
	}
	select {
	case got := <-delivered:
		if got != sessionID {
			t.Errorf("webhook delivered for %q, want %s", got, sessionID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no completion webhook was delivered for the auto-stopped session")
	}
	deleted := deletedJobNames(f)
	if len(deleted) != 1 {
		t.Errorf("job deletes = %v, want exactly one", deleted)
	}

	// A second turn_done (a re-delivered event, or a session that somehow
	// produces another turn) must not stop anything a second time. The Job
	// delete count is the synchronous record of how many stops actually ran.
	turnDone(t, api, pool, sessionID)
	if got := deletedJobNames(f); len(got) != 1 {
		t.Errorf("job deletes after a second turn_done = %v, want still one", got)
	}
	select {
	case got := <-delivered:
		t.Errorf("a second completion webhook was delivered (%s); delivery is once per session", got)
	default:
	}
}

// A session that was already stopped by hand is skipped: the flag does not
// resurrect a second stop when a late turn_done arrives.
func TestTurnDoneSkipsAlreadyStoppedSession(t *testing.T) {
	f := &fakeK8s{}
	api, _, pool := clusterRunnerAPI(t, f)
	ctx := context.Background()
	resp, apiErr := api.StartSession(ctx, runnerPrincipal{Kind: runnerKeyPrincipalKind},
		runnerStartRequest{Repo: "org/proj", Title: "T", Prompt: "one thing", AutoStop: true}, "")
	if apiErr != nil {
		t.Fatalf("StartSession: %v", apiErr)
	}
	sessionID := resp.SessionID
	if _, apiErr := api.Stop(ctx, runnerPrincipal{Kind: runnerKeyPrincipalKind}, sessionID); apiErr != nil {
		t.Fatalf("Stop: %v", apiErr)
	}
	before := deletedJobNames(f)

	turnDone(t, api, pool, sessionID)

	if got := deletedJobNames(f); len(got) != len(before) {
		t.Errorf("job deletes = %v, want no second stop after an explicit one (%v)", got, before)
	}
}

// A one-shot session whose turn ends in a provider error is ended too: the
// agent loop emits turn_done{stop_reason:"error"} on that path (loop.go), and
// a caller waiting on the callback must be told the work is over rather than
// left holding a session that will never speak again.
func TestFailedTurnEndsAutoStopSession(t *testing.T) {
	api, pool, sessionID := autoStopSession(t, true)

	turnDoneWithReason(t, api, pool, sessionID, "error")

	if got := sessionStatus(t, pool, sessionID); got != "ended" {
		t.Fatalf("status after a failed turn = %q, want ended", got)
	}
	res, apiErr := api.Result(context.Background(), runnerPrincipal{Kind: runnerKeyPrincipalKind}, sessionID)
	if apiErr != nil {
		t.Fatalf("Result: %v", apiErr)
	}
	if !res.Terminal {
		t.Errorf("result terminal = false after a failed turn on a one-shot session")
	}
}
