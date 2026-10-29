package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// All objects are this fixture's fakes. A successful DELETE removes the fake
// writer; GET/list must also prove its absence before terminal cleanup succeeds.
type cleanupKube struct {
	mu                           sync.Mutex
	job, claim, denyPods         bool
	deletes, secrets, foreground int
}

func (k *cleanupKube) manager(t *testing.T, id string) *JobManager {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k.mu.Lock()
		defer k.mu.Unlock()
		switch {
		case strings.Contains(r.URL.Path, "/persistentvolumeclaims/"):
			if k.claim {
				_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"uid": "ambiguous-claim"}})
			} else {
				w.WriteHeader(404)
			}
		case strings.HasSuffix(r.URL.Path, "/pods"):
			if k.denyPods {
				w.WriteHeader(403)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
		case strings.Contains(r.URL.Path, "/secrets/") && r.Method == "DELETE":
			k.secrets++
			w.WriteHeader(404)
		case strings.Contains(r.URL.Path, "/jobs/") && r.Method == "DELETE":
			var body struct {
				Propagation   string `json:"propagationPolicy"`
				Preconditions struct {
					UID string `json:"uid"`
				} `json:"preconditions"`
			}
			if r.ContentLength != 0 {
				_ = json.NewDecoder(r.Body).Decode(&body)
			}
			if body.Propagation == "Foreground" {
				if body.Preconditions.UID != "owned-job" {
					t.Error("missing exact Job UID precondition")
				}
				k.foreground++
			}
			k.deletes++
			k.job = false
			w.WriteHeader(200)
		case strings.Contains(r.URL.Path, "/jobs/") && r.Method == "GET":
			if !k.job {
				w.WriteHeader(404)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"uid": "owned-job", "labels": map[string]string{"app": "blerg-runner-agent", "session": id}}})
		default:
			t.Errorf("unexpected Kubernetes operation %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	t.Cleanup(srv.Close)
	return &JobManager{BaseURL: srv.URL, Namespace: "fixture", Client: srv.Client()}
}
func cleanupBrowser(t *testing.T, api *API, owner string) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	api.coreAuth = newTestCoreAuthClient(t, pub, "core-1")
	return mintRunnerToken(t, priv, "core-1", identity.Claims{Sub: owner, Kind: "human", Sid: testSID, Aud: coreAuthAudience, Caps: []string{coreAuthBrowserCap, coreAuthRunnerCap}, ExpiresAt: time.Now().Add(time.Hour).Unix()})
}
func cleanupBrowserRequest(t *testing.T, api *API, id, owner, mode string) int {
	t.Helper()
	method := "DELETE"
	if mode == "discard" {
		method = "POST"
	}
	req := httptest.NewRequest(method, "/api/sessions/"+id, nil)
	req.SetPathValue("id", id)
	req.Header.Set("Authorization", "Bearer "+cleanupBrowser(t, api, owner))
	rec := httptest.NewRecorder()
	if mode == "discard" {
		api.handleSessionDiscard(rec, req)
	} else {
		api.HandleDeleteSession(rec, req)
	}
	return rec.Code
}
func TestClusterWorkspaceTrustedCleanupStates(t *testing.T) {
	for _, mode := range []string{"stop_twice", "natural_exit", "never_allocated", "never_allocated_delete", "never_allocated_terminal", "recoverable_delete", "terminal_discard"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			var f *reconcileFixture
			var p clusterworkspace.Plan
			if strings.HasPrefix(mode, "never_allocated") {
				var spec SessionJobSpec
				f, spec = nativeFixture(t)
				p = clusterPlanFixture()
				p.SessionID = spec.SessionID
				p.Manifest.OwnerID = spec.SpawningAccountID
				p.Runtime.Generation = 0
				if err := db.InsertSessionWorkspace(ctx, f.pool, p.SessionID, p.Manifest); err != nil {
					t.Fatal(err)
				}
			} else {
				f, _, p = reviewWorkspace(t)
			}
			kube := &cleanupKube{job: !strings.HasPrefix(mode, "never_allocated")}
			f.api.hub.SetJobManager(kube.manager(t, p.SessionID))
			runtimeRaw, _, err := db.MintRuntimeLaunch(ctx, f.pool, p.SessionID, "", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			runtimeToken, err := db.ValidateRuntimeToken(ctx, f.pool, runtimeRaw)
			if err != nil {
				t.Fatal(err)
			}
			f.api.clusterStopTimeout = 100 * time.Millisecond
			var connected *DaemonConn
			expected := append([]protocol.WorkspaceMemberResult{}, p.PreviousResults...)
			if !strings.HasPrefix(mode, "never_allocated") {
				expected[0].Pending = false
				expected[1].PushError = "final refusal"
				if err := persistClusterWorkspaceReport(ctx, f.pool, p.SessionID, p.Runtime, 2, expected, mode == "stop_twice"); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "natural_exit" || mode == "terminal_discard" {
				HandleSessionEnded(ctx, f.api.hub, f.pool, protocol.SessionEnded{SessionID: p.SessionID})
			}
			if mode == "never_allocated_terminal" {
				at := time.Now().Add(-time.Minute)
				if err := db.EndSessionStatus(ctx, f.pool, p.SessionID, "error", &at, systemEnd(db.EndReasonStartFailed)); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "recoverable_delete" {
				at := time.Now().Add(-time.Minute)
				if err := db.EndSessionStatus(ctx, f.pool, p.SessionID, "error", &at, systemEnd(db.EndReasonDaemonLost)); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "recoverable_delete" || mode == "terminal_discard" {
				dc := &DaemonConn{ID: newUUID(), Name: "runner-" + p.SessionID, Mode: "runner", BoundSessionID: p.SessionID, RuntimeTokenID: runtimeToken.ID, WorkspaceRuntime: p.Runtime, send: make(chan []byte, 4)}
				connected = dc
				f.api.hub.Register(dc)
				f.api.hub.SetSessionOwner(p.SessionID, dc.ID)
			}
			before, err := db.GetSession(ctx, f.pool, p.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "recoverable_delete", "never_allocated_delete":
				if code := cleanupBrowserRequest(t, f.api, p.SessionID, p.Manifest.OwnerID, "delete"); code != 204 {
					t.Fatal("recoverable DELETE cleanup failed", code)
				}
			case "terminal_discard":
				if code := cleanupBrowserRequest(t, f.api, p.SessionID, p.Manifest.OwnerID, "discard"); code != 200 {
					t.Fatal("terminal owner discard failed", code)
				}
			default:
				if _, err := f.api.Stop(ctx, RunnerPrincipal{Kind: runnerKeyPrincipalKind}, p.SessionID); err != nil {
					t.Fatal("trusted cleanup failed", err)
				}
			}
			if connected != nil && len(connected.send) != 0 {
				t.Fatal("cleanup required another report from terminal/recoverable runtime")
			}
			after, err := db.GetSession(ctx, f.pool, p.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if !db.TerminallyEnded(after) {
				t.Fatal("cleanup did not confirm terminal state", after)
			}
			if db.TerminallyEnded(before) && (before.Status != after.Status || !reflect.DeepEqual(before.EndedAt, after.EndedAt) || !reflect.DeepEqual(before.EndReason, after.EndReason) || !reflect.DeepEqual(before.EndedByKind, after.EndedByKind) || !reflect.DeepEqual(before.EndedByAccount, after.EndedByAccount)) {
				t.Fatal("cleanup rewrote original end attribution")
			}
			if mode == "terminal_discard" && after.ArchivedAt == nil {
				t.Fatal("discard failed to archive after cleanup")
			}
			if mode == "recoverable_delete" && derefOrEmpty(after.EndReason) != db.EndReasonStoppedByUser {
				t.Fatal("recoverable observation masked explicit stop", after.EndReason)
			}
			if mode == "stop_twice" {
				if _, err := f.api.Stop(ctx, RunnerPrincipal{Kind: runnerKeyPrincipalKind}, p.SessionID); err != nil {
					t.Fatal("authorized stop retry failed", err)
				}
				again, _ := db.GetSession(ctx, f.pool, p.SessionID)
				if !reflect.DeepEqual(after.EndedAt, again.EndedAt) || !reflect.DeepEqual(after.EndReason, again.EndReason) {
					t.Fatal("stop retry rewrote terminal attribution")
				}
			}
			workspace, err := db.GetSessionWorkspace(ctx, f.pool, p.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "recoverable_delete" {
				for i, v := range workspace.Results {
					if !v.Pending || !strings.Contains(v.SyncError, "unacknowledged") || v.Head != expected[i].Head {
						t.Fatal("unacknowledged writer presented as successful settlement", v)
					}
				}
			} else if !reflect.DeepEqual(workspace.Results, expected) {
				t.Fatal("cleanup rewrote durable results", workspace.Results, expected)
			}
			if !strings.HasPrefix(mode, "never_allocated") {
				if err := persistClusterWorkspaceResults(ctx, f.pool, p.SessionID, p.Runtime, 99, p.PreviousResults); err == nil {
					t.Fatal("trusted cleanup relaxed terminal runtime result guards")
				}
			}
			var active int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM board_tokens WHERE session_id=$1 AND revoked_at IS NULL`, p.SessionID).Scan(&active); err != nil || active != 0 {
				t.Fatal("cleanup left token authority", active, err)
			}
			kube.mu.Lock()
			defer kube.mu.Unlock()
			if kube.job || kube.deletes < 1 || kube.secrets < 1 {
				t.Fatal("Job/Secret cleanup was skipped", kube.deletes, kube.secrets)
			}
			if (mode == "natural_exit" || mode == "recoverable_delete" || mode == "terminal_discard") && kube.foreground < 1 {
				t.Fatal("no physical writer fence before cleanup")
			}
		})
	}
}

func TestClusterWorkspaceTrustedCleanupRefusesUncertainState(t *testing.T) {
	for _, mode := range []string{"missing_allocated_ledger", "ambiguous_claim", "unallocated_job", "unallocated_pods", "unproven_pods", "sealed_terminal_pods", "recoverable_delete_failure", "terminal_discard_failure", "ledger_read_failure", "foreign_ledger"} {
		t.Run(mode, func(t *testing.T) {
			f, _, p := reviewWorkspace(t)
			ctx := context.Background()
			kube := &cleanupKube{job: true}
			f.api.hub.SetJobManager(kube.manager(t, p.SessionID))
			switch mode {
			case "missing_allocated_ledger", "ambiguous_claim", "unallocated_job", "unallocated_pods":
				if _, err := f.pool.Exec(ctx, `DELETE FROM cluster_workspace_storage WHERE session_id=$1`, p.SessionID); err != nil {
					t.Fatal(err)
				}
				if mode != "missing_allocated_ledger" {
					if _, err := f.pool.Exec(ctx, `UPDATE session_workspaces SET runtime_generation=0,runtime_binding=NULL WHERE session_id=$1`, p.SessionID); err != nil {
						t.Fatal(err)
					}
					kube.job = mode == "unallocated_job"
					kube.claim = mode == "ambiguous_claim"
					kube.denyPods = mode == "unallocated_pods"
				}
			case "ledger_read_failure":
				if _, err := f.pool.Exec(ctx, `ALTER TABLE cluster_workspace_storage RENAME TO unavailable_storage`); err != nil {
					t.Fatal(err)
				}
			case "foreign_ledger":
				if _, err := f.pool.Exec(ctx, `UPDATE cluster_workspace_storage SET owner_id='foreign-owner' WHERE session_id=$1`, p.SessionID); err != nil {
					t.Fatal(err)
				}
			default:
				if mode == "sealed_terminal_pods" {
					if err := persistClusterWorkspaceReport(ctx, f.pool, p.SessionID, p.Runtime, 2, p.PreviousResults, true); err != nil {
						t.Fatal(err)
					}
				}
				kube.denyPods = true
				if mode == "recoverable_delete_failure" {
					at := time.Now()
					if err := db.EndSessionStatus(ctx, f.pool, p.SessionID, "error", &at, systemEnd(db.EndReasonDaemonLost)); err != nil {
						t.Fatal(err)
					}
				} else {
					HandleSessionEnded(ctx, f.api.hub, f.pool, protocol.SessionEnded{SessionID: p.SessionID})
				}
			}
			if strings.Contains(mode, "delete") || strings.Contains(mode, "discard") {
				dc := &DaemonConn{ID: newUUID(), Name: "runner-" + p.SessionID, Mode: "runner", BoundSessionID: p.SessionID, send: make(chan []byte, 4)}
				f.api.hub.Register(dc)
				f.api.hub.SetSessionOwner(p.SessionID, dc.ID)
				endpoint := "delete"
				if mode == "terminal_discard_failure" {
					endpoint = "discard"
				}
				if code := cleanupBrowserRequest(t, f.api, p.SessionID, p.Manifest.OwnerID, endpoint); code != 503 {
					t.Fatal("cleanup failure falsely confirmed", code)
				}
			} else if _, err := f.api.Stop(ctx, RunnerPrincipal{Kind: runnerKeyPrincipalKind}, p.SessionID); err == nil {
				t.Fatal("uncertain state confirmed cleanup")
			}
			row, err := db.GetSession(ctx, f.pool, p.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if row.ArchivedAt != nil {
				t.Fatal("unproven cleanup archived session")
			}
			if mode == "recoverable_delete_failure" && db.TerminallyEnded(row) {
				t.Fatal("failed cleanup confirmed recoverable stop")
			}
			kube.mu.Lock()
			defer kube.mu.Unlock()
			if kube.secrets != 0 {
				t.Fatal("released Secret before writer proof")
			}
		})
	}
}
