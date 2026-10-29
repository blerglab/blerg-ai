package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func failedRunnerPod(reason string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{"name": "failed-agent", "uid": "pod-uid", "resourceVersion": "7"},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "runner", "resources": map[string]any{"limits": map[string]string{"memory": "4Gi"}}}}},
		"status":   map[string]any{"phase": "Failed", "containerStatuses": []any{map[string]any{"name": "runner", "state": map[string]any{"terminated": map[string]any{"exitCode": 137, "reason": reason, "message": "private fixture detail"}}}}},
	}
}

func TestDisconnectedPodTerminationExplainedAndLabeled(t *testing.T) {
	for _, reason := range []string{"OOMKilled", "Error", "Evicted"} {
		t.Run(reason, func(t *testing.T) {
			f := newReconcileFixture(t, "disconnected")
			pod := failedRunnerPod(reason)
			pod["metadata"].(map[string]any)["labels"] = map[string]string{"app": "blerg-runner-agent", "session": f.sessionID}
			if reason == "Evicted" {
				pod["status"].(map[string]any)["reason"] = reason
			}
			f.fake.setPod(f.sessionID, pod)
			// Neither the Job controller's failure condition nor the current operator
			// limit is the authority for this pod's actual termination or memory limit.
			f.api.hub.JobManager().MemLimit = "12Gi"
			f.api.reconcileClusterJobsOnce(context.Background())
			status, detail := f.status(t)
			if status != "disconnected" || !strings.Contains(detail, "stopped:") {
				t.Fatalf("%s: %q", status, detail)
			}
			if reason == "OOMKilled" && (!strings.Contains(detail, "limit 4 GiB") || !strings.Contains(detail, "BLERG_RUNNER_POD_MEM_LIMIT")) {
				t.Fatal(detail)
			}
			if !strings.Contains(detail, "retained workspace") || !strings.Contains(detail, "resume") {
				t.Fatal(detail)
			}
			if strings.Contains(detail, "private fixture") {
				t.Fatal("leaked raw status message")
			}
			events, err := db.ListAgentEventsTail(context.Background(), f.pool, f.sessionID, protocol.StartStageKind, 10)
			if err != nil || len(events) != 1 {
				t.Fatalf("events %d: %v", len(events), err)
			}
			var payload protocol.StartStagePayload
			if err := json.Unmarshal([]byte(events[0].Payload), &payload); err != nil || payload.Stages[0].Hint == "" {
				t.Fatal("missing recovery stage", err)
			}
			snapshot, err := clusterLaunchSnapshot(context.Background(), f.pool, f.sessionID)
			if err != nil || len(snapshot) != 1 || snapshot[0].Stage != "pod_terminated" || snapshot[0].Reason == "" {
				t.Fatal("missing safe termination receipt", snapshot, err)
			}
			f.api.reconcileClusterJobsOnce(context.Background())
			events, _ = db.ListAgentEventsTail(context.Background(), f.pool, f.sessionID, protocol.StartStageKind, 10)
			if len(events) != 1 {
				t.Fatal("duplicate failure events")
			}
			f.fake.mu.Lock()
			patches := f.fake.patchedPods
			f.fake.mu.Unlock()
			if len(patches) == 0 {
				t.Fatal("failed pod not labeled for monitoring")
			}
			metadata := patches[0]["metadata"].(map[string]any)
			labels := metadata["labels"].(map[string]any)
			if metadata["uid"] != "pod-uid" || metadata["resourceVersion"] != "7" || labels[podEndedLabel] != "true" {
				t.Fatal("missing identity preconditions or end label", patches[0])
			}
			if f.hits() != 0 {
				t.Fatal("resumable session fired completion")
			}
		})
	}
}

func TestPodTerminationDoesNotRewriteResumedSession(t *testing.T) {
	f := newReconcileFixture(t, "disconnected")
	rows, err := db.ListClusterSessionsToReconcile(context.Background(), f.pool)
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(context.Background(), `UPDATE sessions SET status='running',status_changed_at=now() WHERE id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	changed, err := db.RecordClusterPodFailure(context.Background(), f.pool, rows[0], "old pod died")
	if err != nil || changed {
		t.Fatalf("stale observation landed: %v %v", changed, err)
	}
}

func TestEndedPodLabelRejectsLiveAndForeignPods(t *testing.T) {
	for _, mode := range []string{"running", "foreign", "missing_uid", "already_labeled"} {
		t.Run(mode, func(t *testing.T) {
			f := &fakeK8s{}
			jm := newTestJobManager(t, f)
			raw := failedRunnerPod("OOMKilled")
			metadata := raw["metadata"].(map[string]any)
			metadata["labels"] = map[string]string{"app": "blerg-runner-agent", "session": "owned"}
			switch mode {
			case "running":
				raw["status"].(map[string]any)["phase"] = "Running"
			case "foreign":
				metadata["labels"].(map[string]string)["session"] = "another-session"
			case "missing_uid":
				delete(metadata, "uid")
			case "already_labeled":
				metadata["labels"].(map[string]string)[podEndedLabel] = "true"
			}
			pod := decodePod(t, raw)
			if err := jm.labelEndedPod(t.Context(), pod, "owned"); err != nil {
				t.Fatal(err)
			}
			if len(f.patchedPods) != 0 {
				t.Fatal("patched a live, foreign, unidentified or already labeled pod")
			}
		})
	}
}

func TestRunningPodTerminationRemainsResumable(t *testing.T) {
	f := newReconcileFixture(t, "running")
	f.fake.setJobStatus(f.jobName, map[string]any{"failed": 1})
	f.fake.setPod(f.sessionID, failedRunnerPod("OOMKilled"))
	f.api.reconcileClusterJobsOnce(t.Context())
	status, reason := f.status(t)
	if status != "disconnected" || !strings.Contains(reason, "ran out of memory") {
		t.Fatal(status, reason)
	}
	row, err := db.GetSession(t.Context(), f.pool, f.sessionID)
	if err != nil || row.EndedAt != nil || row.EndReason != nil {
		t.Fatal("pod death finalized retained session", row, err)
	}
	if f.hits() != 0 {
		t.Fatal("resumable session completed")
	}
}

func TestPodFailureClearsWhenResumeBegins(t *testing.T) {
	f := newReconcileFixture(t, "disconnected")
	if err := db.SetSessionError(t.Context(), f.pool, f.sessionID, "stopped: ran out of memory"); err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListClusterSessionsToReconcile(t.Context(), f.pool)
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	if err := db.TouchSessionStatusChanged(t.Context(), f.pool, f.sessionID); err != nil {
		t.Fatal(err)
	}
	_, reason := f.status(t)
	if reason != "" {
		t.Fatal("resuming still shows previous pod death", reason)
	}
	changed, err := db.RecordClusterPodFailure(t.Context(), f.pool, rows[0], "stale pod death")
	if err != nil || changed {
		t.Fatal("old poll rewrote new resume", changed, err)
	}
}

func TestStaleFailedJobDefersToOpenResume(t *testing.T) {
	f := newReconcileFixture(t, "running")
	rows, err := db.ListClusterSessionsToReconcile(t.Context(), f.pool)
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	f.fake.setJobStatus(f.jobName, map[string]any{"failed": 1})
	f.fake.setPod(f.sessionID, failedRunnerPod("OOMKilled"))
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET status='disconnected',status_changed_at=now() WHERE id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	// The reconcile list predates this new resume attempt. Its old failed Job
	// must not finalize the disconnected session while the replacement boots.
	f.api.hub.starts.begin(f.sessionID, clusterStartPlan("org/proj", nil))
	f.api.reconcileClusterSession(t.Context(), f.api.hub.JobManager(), rows[0])
	status, reason := f.status(t)
	if status != "disconnected" || reason != "" {
		t.Fatalf("old Job ended active resume: %s %q", status, reason)
	}
}

// Public conversation access never grants access to its owner's pod diagnostics.
func TestPodDeathDiagnosticsOwnerOnly(t *testing.T) {
	for _, private := range []bool{false, true} {
		t.Run(fmt.Sprintf("private=%v", private), func(t *testing.T) {
			f := newReconcileFixture(t, "disconnected")
			const owner = "pod-owner"
			if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id=$2,private=$3 WHERE id=$1`, f.sessionID, owner, private); err != nil {
				t.Fatal(err)
			}
			if private {
				f.api.hub.notePrivate(f.sessionID, owner)
			}
			browsers := []*BrowserConn{
				{ID: newUUID(), AccountID: owner, send: make(chan []byte, 64)},
				{ID: newUUID(), AccountID: "pod-viewer", send: make(chan []byte, 64)},
			}
			for _, b := range browsers {
				f.api.hub.RegisterBrowser(b)
				t.Cleanup(func() { f.api.hub.UnregisterBrowser(b.ID) })
			}
			f.fake.setPod(f.sessionID, failedRunnerPod("OOMKilled"))
			f.api.reconcileClusterJobsOnce(t.Context())
			row, err := db.GetSession(t.Context(), f.pool, f.sessionID)
			if err != nil {
				t.Fatal(err)
			}
			check := func(t *testing.T, raw string, diagnostic bool) {
				t.Helper()
				if diagnostic {
					if !strings.Contains(raw, "OOMKilled") || !strings.Contains(raw, "BLERG_RUNNER_POD_MEM_LIMIT") {
						t.Errorf("missing owner diagnostic: %s", raw)
					}
				} else {
					for _, hidden := range []string{"OOMKilled", "4 GiB", "BLERG_RUNNER_POD_MEM_LIMIT", "retained workspace"} {
						if strings.Contains(raw, hidden) {
							t.Errorf("foreign viewer received %q: %s", hidden, raw)
						}
					}
					if !strings.Contains(raw, "This session stopped.") {
						t.Errorf("missing generic stopped notice: %s", raw)
					}
				}
			}
			for _, b := range browsers {
				own := b.AccountID == owner
				t.Run(b.AccountID+"/snapshot", func(t *testing.T) {
					if private && !own {
						if len(b.send) != 0 {
							t.Fatal("private snapshot reached foreign browser")
						}
						return
					}
					select {
					case raw := <-b.send:
						check(t, string(raw), own)
					default:
						t.Fatal("no session snapshot")
					}
				})
				if private && !own {
					if canSeeAccount(b.AccountID, row) {
						t.Fatal("foreign private access")
					}
					continue
				}
				t.Run(b.AccountID+"/info", func(t *testing.T) {
					check(t, f.api.hub.sessionInfoFor(t.Context(), *row, b.AccountID).ErrorReason, own)
				})
				t.Run(b.AccountID+"/status", func(t *testing.T) {
					status, problem := f.api.SessionStatus(t.Context(), RunnerPrincipal{Kind: "human", Sub: b.AccountID}, f.sessionID)
					if problem != nil {
						t.Fatal(problem)
					}
					check(t, status["error_reason"].(string), own)
				})
				t.Run(b.AccountID+"/result", func(t *testing.T) {
					result, problem := f.api.Result(t.Context(), RunnerPrincipal{Kind: "human", Sub: b.AccountID}, f.sessionID)
					if problem != nil {
						t.Fatal(problem)
					}
					check(t, result.ErrorReason, own)
				})
				t.Run(b.AccountID+"/replay", func(t *testing.T) {
					replayAgentEvents(t.Context(), f.api.hub, b, f.pool, protocol.SubscribeAgentEvents{SessionID: f.sessionID})
					var event protocol.AgentEvent
					if err := json.Unmarshal(<-b.send, &event); err != nil {
						t.Fatal(err)
					}
					check(t, string(event.Payload), own)
				})
				t.Run(b.AccountID+"/live", func(t *testing.T) {
					events, err := db.ListAgentEventsTail(t.Context(), f.pool, f.sessionID, protocol.StartStageKind, 1)
					if err != nil || len(events) != 1 {
						t.Fatal(err)
					}
					raw, _ := json.Marshal(replayedAgentEvent(events[0]))
					// This is the final delivery projection shared by browser and live SSE.
					filtered, allowed := f.api.hub.desktopPayloadFor(t.Context(), b.AccountID, raw)
					if !allowed {
						t.Fatal("public event withheld")
					}
					check(t, string(filtered), own)
				})
			}
		})
	}
}

func TestResumeBroadcastClearsPodDeath(t *testing.T) {
	f := newReconcileFixture(t, "disconnected")
	if err := db.SetSessionError(t.Context(), f.pool, f.sessionID, "stopped: ran out of memory (OOMKilled)"); err != nil {
		t.Fatal(err)
	}
	b := &BrowserConn{ID: newUUID(), AccountID: "pod-viewer", send: make(chan []byte, 64)}
	f.api.hub.RegisterBrowser(b)
	t.Cleanup(func() { f.api.hub.UnregisterBrowser(b.ID) })
	if err := resumeClusterSessionMode(t.Context(), f.api.hub, f.pool, f.sessionID, "continue", "", "", false); err != nil {
		t.Fatal(err)
	}
	_, reason := f.status(t)
	if reason != "" {
		t.Fatal("database reason was not cleared", reason)
	}
	for len(b.send) > 0 {
		var frame protocol.BrowserSessionStarted
		if err := json.Unmarshal(<-b.send, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type == "session_started" && frame.Session.ID == f.sessionID && frame.Session.ErrorReason == "" {
			return
		}
	}
	t.Fatal("resume cleared database but sent no clearing session snapshot to connected browser")
}
