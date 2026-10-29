package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestClusterLaunchDiagnosticsNameStalledAndRefusedStages(t *testing.T) {
	f, spec := nativeFixture(t)
	j := newTestJobManager(t, f.fake)
	j.historyPool = f.pool
	p := &clusterLaunchProgress{j: j, session: spec.SessionID}
	p.next(t.Context(), "storage", "Allocating retained claim identity")
	stages, err := clusterLaunchSnapshot(t.Context(), f.pool, spec.SessionID)
	if err != nil || len(stages) != 1 || stages[0].StartedAt.IsZero() {
		t.Fatal("missing stage start", stages, err)
	}
	if problem := clusterLaunchProblem(stages, time.Now().Add(time.Minute), time.Second); !strings.Contains(problem, "stage storage") {
		t.Fatal("stuck stage not named", problem)
	}
	p.next(t.Context(), "writer_fence", "Waiting for old writer termination")
	failure := errClusterWorkspace
	p.finish(t.Context(), &failure)
	stages, err = clusterLaunchSnapshot(t.Context(), f.pool, spec.SessionID)
	if err != nil || len(stages) != 2 || stages[0].FinishedAt.IsZero() || stages[1].FinishedAt.IsZero() {
		t.Fatal("missing stage end", stages, err)
	}
	if problem := clusterLaunchProblem(stages, time.Now(), time.Hour); !strings.Contains(problem, "stage writer_fence") {
		t.Fatal("refused stage not named", problem)
	}
	// Unknown stage text never becomes diagnostic/log authority.
	persistStartStages(context.Background(), nil, f.pool, spec.SessionID, protocol.StartStagePayload{Stages: []protocol.StartStage{stage("private-token", "private", "failed", "fixture-secret")}})
	stages, _ = clusterLaunchSnapshot(t.Context(), f.pool, spec.SessionID)
	raw, _ := json.Marshal(stages)
	if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "fixture-secret") {
		t.Fatal("private stage data escaped safe diagnostics")
	}
}

func TestClusterBootstrapAndPreHelloFailureStages(t *testing.T) {
	for _, failure := range []bool{false, true} {
		pod := podJSON("Pending", scheduled(), waitingOn("PodInitializing"))
		init := map[string]any{"name": "workspace-storage", "state": map[string]any{"running": map[string]any{"startedAt": "now"}}}
		if failure {
			init["state"] = map[string]any{"terminated": map[string]any{"exitCode": 1, "reason": "private-value"}}
		}
		pod["status"].(map[string]any)["initContainerStatuses"] = []map[string]any{init}
		updates, _ := deriveClusterStages(decodePod(t, pod), stageContext{})
		boot, ok := stateOf(updates, "bootstrap")
		if !ok || (failure && boot.State != protocol.StageStateFailed) || (!failure && boot.State != protocol.StageStateActive) || strings.Contains(boot.Detail, "private-value") {
			t.Fatal("bootstrap not diagnosed", updates)
		}
	}
	pod := podJSON("Failed", scheduled(), map[string]any{"name": "runner", "state": map[string]any{"terminated": map[string]any{"exitCode": 1, "message": `{"stage":"storage","reason":"runtime_startup_refused","secret":"private"}`}}})
	_, fatal := deriveClusterStages(decodePod(t, pod), stageContext{})
	if fatal == nil || fatal.ID != "storage" || strings.Contains(fatal.Detail, "private") {
		t.Fatal("pre-hello storage refusal not diagnosed", fatal)
	}
}

func TestClusterLaunchRefusesUnknownCapacityAndCompetingJob(t *testing.T) {
	for _, admission := range []bool{true, false} {
		t.Run(map[bool]string{true: "capacity", false: "job_conflict"}[admission], func(t *testing.T) {
			fake := &fakeK8s{}
			j := newTestJobManager(t, fake)
			fallback := fake.handler()
			kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if admission && r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/jobs") {
					w.WriteHeader(http.StatusForbidden)
					_, _ = w.Write([]byte("fixture-secret"))
					return
				}
				if !admission && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/jobs") {
					w.WriteHeader(http.StatusConflict)
					return
				}
				fallback(w, r)
			}))
			defer kube.Close()
			j.BaseURL, j.Client = kube.URL, kube.Client()
			p := clusterPlanFixture()
			spec := SessionJobSpec{SessionID: p.SessionID, workspacePlan: &p, workspaceClaim: "fixture-workspace", Engine: "claude"}
			err := j.createSessionJob(spec)
			if err == nil || strings.Contains(err.Error(), "fixture-secret") {
				t.Fatal("unknown admission or competing Job reported success", err)
			}
			if admission && len(fake.createdSecrets) != 0 {
				t.Fatal("credentials allocated without capacity proof")
			}
			if !admission && len(fake.deletedSecrets) != 1 {
				t.Fatal("unused Secret was not cleaned up")
			}
		})
	}
}

// API bodies may echo credentials and manifests. Only the reason enum is useful
// launch status; the public error remains fixed and the POST is never retried.
func TestClusterQueuedStageRecordsKubernetesReason(t *testing.T) {
	for _, tc := range []struct {
		reason string
		status int
	}{
		{"AlreadyExists", http.StatusConflict}, {"Forbidden", http.StatusForbidden}, {"Invalid", http.StatusUnprocessableEntity}, {"Conflict", http.StatusConflict}, {"SchedulingPolicyRefused", 0},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			f, fixtureSpec := nativeFixture(t)
			j := newTestJobManager(t, f.fake)
			j.historyPool = f.pool
			if tc.status == 0 {
				j.ExcludedNodes = []string{"INVALID"}
			}
			fallback := f.fake.handler()
			var posts atomic.Int32
			kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/jobs") {
					posts.Add(1)
					w.WriteHeader(tc.status)
					_ = json.NewEncoder(w).Encode(map[string]any{"kind": "Status", "reason": tc.reason, "message": "fixture-private-token", "details": map[string]any{"name": "fixture-private-name"}})
					return
				}
				fallback(w, r)
			}))
			defer kube.Close()
			j.BaseURL, j.Client = kube.URL, kube.Client()
			p := clusterPlanFixture()
			// Use the persisted fixture session so snapshots exercise the real receipt.
			id := fixtureSpec.SessionID
			p.SessionID = id
			j.launchHub = f.api.hub
			beginStart(t.Context(), f.api.hub, f.pool, id, "cluster", clusterWorkspaceStartPlan())
			launchCtx := t.Context()
			if tc.reason == "Conflict" {
				bounded, cancel := context.WithTimeout(launchCtx, time.Second)
				defer cancel()
				launchCtx = bounded
			}
			err := j.createSessionJobContext(launchCtx, SessionJobSpec{SessionID: id, Engine: "claude", workspacePlan: &p, workspaceClaim: "fixture-claim"})
			if err == nil || err.Error() != errSessionJobCreate.Error() {
				t.Fatal("unsafe or missing refusal", err)
			}
			wantPosts := int32(1)
			if tc.status == 0 {
				wantPosts = 0
			}
			if tc.reason == "Conflict" {
				if launchCtx.Err() != context.DeadlineExceeded || posts.Load() < 2 {
					t.Fatal("persistent conflict did not retry through launch deadline", posts.Load(), launchCtx.Err())
				}
			} else if posts.Load() != wantPosts {
				t.Fatalf("submitted %d Jobs, expected %d", posts.Load(), wantPosts)
			}
			// The real consumer closes the attempt and revokes grants on a
			// synchronous refusal. That must preserve the more precise stage.
			f.api.markClusterStartFailed(t.Context(), id, "workspace preparation failed")
			stages, err := clusterLaunchSnapshot(t.Context(), f.pool, id)
			if err != nil {
				t.Fatal(err)
			}
			var queued clusterLaunchDiagnostic
			for _, s := range stages {
				if s.Stage == protocol.StageQueued {
					queued = s
				}
			}
			if queued.Outcome != protocol.StageStateFailed || queued.Reason != tc.reason {
				t.Fatalf("missing Kubernetes reason after terminal cleanup: %+v", queued)
			}
			raw, _ := json.Marshal(stages)
			if strings.Contains(string(raw), "fixture-private") {
				t.Fatal("API body escaped diagnostics")
			}
			if len(f.fake.deletedSecrets) != 1 {
				t.Fatal("unused Secret was not cleaned up")
			}
		})
	}
}
