package server

import (
	"context"
	"github.com/blerglab/blerg-ai/runner/internal/activity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceMetricsOwnerIsolation(t *testing.T) {
	fx := newTopicFx(t)
	mux := http.NewServeMux()
	fx.api.RegisterMarkRoutes(mux)
	for _, tc := range []struct {
		owner string
		code  int
	}{{topicOwner, 200}, {topicOther, 404}} {
		req := httptest.NewRequest("GET", "/api/sessions/"+fx.sess+"/workspace/metrics", nil)
		req.Header.Set("Authorization", "Bearer "+fx.token(tc.owner, "human"))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != tc.code {
			t.Fatalf("%s: %d %s", tc.owner, w.Code, w.Body.String())
		}
		if tc.code == 200 && !strings.Contains(w.Body.String(), fx.sess) {
			t.Fatal(w.Body.String())
		}
	}
	req := httptest.NewRequest("GET", "/api/sessions/"+fx.sess+"/workspace/metrics?session=00000000-0000-4000-8000-000000000001", nil)
	req.Header.Set("Authorization", "Bearer "+fx.token(topicOwner, "human"))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatalf("other tree: %d %s", w.Code, w.Body.String())
	}
}

func TestWorkspaceMetricsOwnCredentialRegistersAndScopesRuns(t *testing.T) {
	fx := newTopicFx(t)
	path := "/api/sessions/" + fx.sess + "/workspace/metrics"
	body := map[string]any{"id": "gate-a", "suite": "runner-unit", "commit": "abcdef1", "state": "running", "progress": 0, "full_suite": true}
	if w := filingRequest(fx, http.MethodPost, path+"/runs", fx.otherTok, body); w.Code != 403 {
		t.Fatalf("foreign credential: %d %s", w.Code, w.Body.String())
	}
	if w := filingRequest(fx, http.MethodPost, path+"/runs", fx.sessTok, body); w.Code != 200 {
		t.Fatalf("register: %d %s", w.Code, w.Body.String())
	}
	w := filingRequest(fx, http.MethodGet, path, fx.sessTok, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"gate-a"`) || !strings.Contains(w.Body.String(), `"session_id":"`+fx.sess+`"`) {
		t.Fatalf("metrics: %d %s", w.Code, w.Body.String())
	}
	body["suite"] = "line\nbreak"
	if w := filingRequest(fx, http.MethodPost, path+"/runs", fx.sessTok, body); w.Code != 422 {
		t.Fatalf("invalid metadata: %d", w.Code)
	}
}

func TestWorkspaceTelemetryUsesPlanDeliveryOnce(t *testing.T) {
	fx := newPlanFx(t, "")
	child := newUUID()
	if err := db.InsertSession(t.Context(), fx.pool, child, topicDaemonID, "idle", "/repos/app", "app", "Child", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET parent_session_id=$2,kind='agent',spawning_account_id=$3,started_at=now()-interval '1 hour' WHERE id=$1`, child, fx.sess, topicOwner); err != nil {
		t.Fatal(err)
	}
	// Legacy root sessions are Main too; no launch manifest is required.
	fx.event("status_changed", `{"status":"running"}`)
	seq := fx.event("turn_done", `{"stop_reason":"end_turn"}`)
	fx.api.onPlanTurnEnded(fx.sess, seq, time.Now(), "end_turn")
	fx.api.planFlights.mu.Lock()
	done := fx.api.planFlights.done[fx.sess]
	fx.api.planFlights.mu.Unlock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("turn telemetry did not settle")
	}
	// Telemetry is independent of the plan flight; wait for its own bounded outbox write.
	deadline := time.Now().Add(time.Second)
	for {
		pending, err := db.PendingPlanFacts(context.Background(), fx.pool, fx.sess)
		if err != nil {
			t.Fatal(err)
		}
		ready := false
		for _, f := range pending {
			if strings.HasPrefix(f.Key, "telemetry:snapshot:") {
				ready = true
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("snapshot not queued")
		}
		time.Sleep(5 * time.Millisecond)
	}
	nudge := planCarryingInput(t, fx.api, fx.sess, true)
	if !strings.Contains(nudge, "Workspace "+fx.sess) || strings.Count(nudge, "Workspace ") != 1 {
		t.Fatalf("snapshot missing/duplicated: %q", nudge)
	}
	if again := planCarryingInput(t, fx.api, fx.sess, true); again != "" {
		t.Fatalf("snapshot repeated: %q", again)
	}
}

func TestWorkspaceSnapshotDeliveryBudget(t *testing.T) {
	metrics := activity.Workspace{SessionID: "00000000-0000-4000-8000-000000000001", GeneratedAt: time.Now()}
	for range 1000 {
		metrics.Workers = append(metrics.Workers, activity.Worker{SessionID: "00000000-0000-4000-8000-000000000002", Status: "running", AgeSeconds: 999999999, GateFraction: 1})
	}
	metrics.Machine = &activity.Machine{SessionID: metrics.SessionID, At: metrics.GeneratedAt, CPUs: 999999, Load1: 999999, DiskFreeBytes: 9999999999999999999}
	snapshot := metrics.Snapshot()
	delivered := planNudgeText([]planFact{{key: "telemetry:snapshot:17", text: snapshot}})
	if delivered != snapshot || strings.Contains(delivered, "\n") || len(delivered) >= 600 {
		t.Fatalf("snapshot delivery: %d bytes, %q", len(delivered), delivered)
	}
	t.Logf("snapshot delivery: %d bytes", len(delivered))
}

func TestTelemetryCannotBlockPlanFactsOrCarryingInput(t *testing.T) {
	fx := newPlanFx(t, "")
	fx.checklistSet("Finish validation")
	fx.checklistOp(map[string]any{"op": "tick", "index": 1})
	fx.event("status_changed", `{"status":"running"}`)
	fx.event("tool_call", `{"tool":"shell","call_id":"a","input":{"command":"go test ./..."}}`)
	seq := fx.event("turn_done", `{"stop_reason":"end_turn"}`)
	tx, err := fx.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(t.Context(), `LOCK session_turn_metrics IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	fx.api.onPlanTurnEnded(fx.sess, seq, time.Now(), "end_turn")
	fx.api.planFlights.mu.Lock()
	done := fx.api.planFlights.done[fx.sess]
	fx.api.planFlights.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("telemetry blocked plan check")
	}
	nudge := planCarryingInput(t, fx.api, fx.sess, true)
	if !strings.Contains(nudge, "checklist") {
		t.Fatalf("plan fact lost: %q", nudge)
	}
}
func TestNoWorkspaceSnapshotWithoutWorkers(t *testing.T) {
	fx := newPlanFx(t, "")
	row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	facts, err := fx.api.telemetryPlanFacts(t.Context(), row, 1)
	if err != nil || len(facts) != 0 {
		t.Fatalf("root-only facts: %+v %v", facts, err)
	}
}

func TestHeldWorkerFactsDropOnceTheWorkerFinished(t *testing.T) {
	for status, stale := range map[string]bool{"running": false, "idle": false, "stopped": true, "ended": true, "discarded": true, "failed": true} {
		children := []db.PlanChild{{ID: "w", Status: status}}
		if got := telemetryFactStale("telemetry:gates:w", children); got != stale {
			t.Errorf("%s: stale %v", status, got)
		}
	}
	if telemetryFactStale("telemetry:snapshot:7", []db.PlanChild{{ID: "7", Status: "failed"}}) {
		t.Error("a snapshot fact names no worker")
	}
}
