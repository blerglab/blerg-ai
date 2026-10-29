package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func reviewWorkspace(t *testing.T, configure ...func(*clusterworkspace.Plan)) (*reconcileFixture, SessionJobSpec, clusterworkspace.Plan) {
	t.Helper()
	f, spec := nativeFixture(t)
	ctx := context.Background()
	p := clusterPlanFixture()
	p.SessionID = spec.SessionID
	p.Manifest.OwnerID = spec.SpawningAccountID
	for _, apply := range configure {
		apply(&p)
	}
	if err := db.InsertSessionWorkspace(ctx, f.pool, p.SessionID, p.Manifest); err != nil {
		t.Fatal(err)
	}
	gen, err := db.BeginWorkspaceRuntime(ctx, f.pool, p.SessionID, p.Manifest.LaunchGeneration, 0)
	if err != nil {
		t.Fatal(err)
	}
	p.Runtime.Generation = gen
	if err := db.BindWorkspaceRuntime(ctx, f.pool, p.SessionID, p.Runtime, protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: p.Runtime, ManifestDigest: p.Manifest.Digest, Root: protocol.WorkspaceRoot{Device: 1, Inode: 2}}); err != nil {
		t.Fatal(err)
	}
	stored := append([]clusterworkspace.Member(nil), p.Members...)
	for i, m := range p.Members {
		stored[i].Token = ""
		identity := protocol.WorkspaceRoot{Device: 1, Inode: uint64(i + 10)}
		p.Identities = append(p.Identities, clusterworkspace.MemberIdentity{ID: m.ID, Directory: identity, GitDirectory: identity, Objects: identity})
		p.PreviousResults = append(p.PreviousResults, protocol.WorkspaceMemberResult{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: m.ID, LaunchGeneration: p.Manifest.LaunchGeneration}, Head: "head-" + m.ID, Branch: "wip/" + p.SessionID, Pending: true, RecoveryLocation: "projects/" + p.Manifest.Members[i].Path})
	}
	raw, _ := json.Marshal(stored)
	ids, _ := json.Marshal(p.Identities)
	if _, err := f.pool.Exec(ctx, `INSERT INTO cluster_workspace_storage(session_id,launch_generation,claim_name,claim_uid,owner_id,contexts,member_identities) VALUES($1,$2,$3,'fixture-uid',$4,$5,$6)`, p.SessionID, p.Manifest.LaunchGeneration, "blerg-workspace-"+p.SessionID, p.Manifest.OwnerID, raw, ids); err != nil {
		t.Fatal(err)
	}
	if err := persistClusterWorkspaceResults(ctx, f.pool, p.SessionID, p.Runtime, 1, p.PreviousResults); err != nil {
		t.Fatal(err)
	}
	return f, spec, p
}

type reviewResumeGit struct{ onePushes int }

func (g *reviewResumeGit) Prepare(_ context.Context, m clusterworkspace.Member, _ string, _ bool) (string, string, error) {
	return "base-" + m.ID, "head-" + m.ID, nil
}
func (g *reviewResumeGit) Head(_ context.Context, m clusterworkspace.Member) (string, error) {
	return "head-" + m.ID, nil
}
func (g *reviewResumeGit) Push(_ context.Context, m clusterworkspace.Member, _, _ string) error {
	if m.ID == "one" {
		g.onePushes++
		return errors.New("redundant remote push rejected")
	}
	return nil
}
func TestClusterWorkspaceReplacementUsesPostFenceResults(t *testing.T) {
	f, spec, p := reviewWorkspace(t)
	ctx := context.Background()
	f.fake.credentialKinds = []string{"github"}
	f.fake.credentialFetchResponses = map[string][]byte{spec.SpawningAccountID + ":github": []byte("fixture-personal-git")}
	f.fake.pods = map[string]map[string]any{}
	f.fake.jobUID = "new-job"
	j := newTestJobManager(t, f.fake)
	j.historyPool = f.pool
	j.WorkspaceStorageClass = "fixture-enforced"
	f.fake.setJobMissing(j.jobName(p.SessionID))
	fallback := f.fake.handler()
	var injected atomic.Bool
	kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pods") {
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
			return
		}
		if strings.Contains(r.URL.Path, "/persistentvolumeclaims") {
			// This request is after the initial manifest/results read and prechecks,
			// before physical fencing. The old generation may still report success.
			if injected.CompareAndSwap(false, true) {
				results := append([]protocol.WorkspaceMemberResult(nil), p.PreviousResults...)
				results[0].Pending = false
				if err := persistClusterWorkspaceResults(ctx, f.pool, p.SessionID, p.Runtime, 2, results); err != nil {
					t.Error(err)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"name": "blerg-workspace-" + p.SessionID, "uid": "fixture-uid", "labels": map[string]string{"app": "blerg-workspace", "session": p.SessionID, "account": spec.SpawningAccountID}}})
			return
		}
		fallback(w, r)
	}))
	defer kube.Close()
	j.BaseURL = kube.URL
	j.Client = kube.Client()
	spec.Workspace = &p.Manifest
	spec.Repo = ""
	spec.GitURL = ""
	spec.Resume = true
	spec.AuthSessionID = "fixture-login"
	if err := j.CreateSessionJob(spec); err != nil {
		t.Fatal(err)
	}
	if !injected.Load() || len(f.fake.createdSecrets) != 1 {
		t.Fatal("replacement not dispatched")
	}
	data := f.fake.createdSecrets[0]["stringData"].(map[string]any)
	var next clusterworkspace.Plan
	if err := json.Unmarshal([]byte(data[clusterworkspace.Env].(string)), &next); err != nil {
		t.Fatal(err)
	}
	g := &reviewResumeGit{}
	runtime, err := clusterworkspace.New(next, g)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Prepare(ctx, true); err != nil {
		t.Fatal(err)
	}
	out := runtime.PushRound(ctx)
	if out[0].Pending || g.onePushes != 0 {
		t.Fatal("stale pre-fence results lost pushed success", out, g.onePushes)
	}
}

func TestClusterWorkspaceStopWaitsForDurableFinalResults(t *testing.T) {
	for _, mode := range []string{"ack", "autostop", "stale_ack", "timeout", "disconnected", "ack_delete_retry", "ack_terminal_retry"} {
		t.Run(mode, func(t *testing.T) {
			f, _, p := reviewWorkspace(t)
			ctx := context.Background()
			api := f.api
			if mode == "ack" {
				api.hub.SetJobManager((&cleanupKube{job: true}).manager(t, p.SessionID))
			}
			retry := strings.HasPrefix(mode, "ack_")
			var failCleanup atomic.Bool
			failCleanup.Store(true)
			if mode == "ack_delete_retry" {
				kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if failCleanup.Load() {
						w.WriteHeader(http.StatusServiceUnavailable)
					} else {
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer kube.Close()
				api.hub.SetJobManager(&JobManager{BaseURL: kube.URL, Namespace: "fixture", Client: kube.Client()})
			}
			if mode == "ack_terminal_retry" {
				_, err := f.pool.Exec(ctx, `CREATE FUNCTION refuse_terminal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.ended_at IS NOT NULL THEN RAISE EXCEPTION 'fixture terminal failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER refuse_terminal BEFORE UPDATE ON sessions FOR EACH ROW EXECUTE FUNCTION refuse_terminal()`)
				if err != nil {
					t.Fatal(err)
				}
			}
			api.clusterStopTimeout = 200 * time.Millisecond
			if mode == "ack" || mode == "autostop" || retry {
				api.clusterStopTimeout = 5 * time.Second
			}
			raw, _, err := db.MintRuntimeLaunch(ctx, f.pool, p.SessionID, "", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			token, err := db.ValidateRuntimeToken(ctx, f.pool, raw)
			if err != nil {
				t.Fatal(err)
			}
			dc := &DaemonConn{ID: newUUID(), Name: "runner-" + p.SessionID, Mode: "runner", BoundSessionID: p.SessionID, RuntimeTokenID: token.ID, WorkspaceRuntime: p.Runtime, send: make(chan []byte, 4)}
			if mode != "disconnected" {
				api.hub.Register(dc)
				api.hub.SetSessionOwner(p.SessionID, dc.ID)
			}
			done := make(chan *APIError, 1)
			if mode == "autostop" {
				if err := db.SetSessionAutoStop(ctx, f.pool, p.SessionID, true); err != nil {
					t.Fatal(err)
				}
				go func() { api.autoStopTurnDone(p.SessionID, true); done <- nil }()
			} else {
				go func() {
					_, err := api.Stop(ctx, RunnerPrincipal{Kind: runnerKeyPrincipalKind}, p.SessionID)
					done <- err
				}()
			}
			if mode != "disconnected" {
				var command struct {
					Type      string `json:"type"`
					RequestID string `json:"request_id"`
				}
				select {
				case wire := <-dc.send:
					if err := json.Unmarshal(wire, &command); err != nil {
						t.Fatal(err)
					}
				case <-time.After(time.Second):
					t.Fatal("no bounded settlement command")
				}
				if command.Type != "cluster_workspace_stop" {
					t.Fatal("used unacknowledged kill", command.Type)
				}
				row, err := db.GetSession(ctx, f.pool, p.SessionID)
				if err != nil || row.EndedAt != nil {
					t.Fatal("ended before settlement", err)
				}
				if err := db.ValidateRuntimeBinding(ctx, f.pool, p.SessionID, token.ID, false); err != nil {
					t.Fatal("revoked before final report", err)
				}
				if revoked, err := clusterWorkspaceWorkRevoked(ctx, f.pool, p.SessionID); err != nil || !revoked {
					t.Fatal("new work/reconnect was not fenced before final report", err)
				}
				if mode == "stale_ack" {
					newer := append([]protocol.WorkspaceMemberResult(nil), p.PreviousResults...)
					newer[0].Head = "newer-turn-report"
					if err := persistClusterWorkspaceResults(ctx, f.pool, p.SessionID, p.Runtime, 5, newer); err != nil {
						t.Fatal(err)
					}
					wire, _ := json.Marshal(struct {
						protocol.SessionEnded
						Revision int64  `json:"workspace_revision"`
						StopID   string `json:"workspace_stop_id"`
					}{protocol.SessionEnded{Type: "session_ended", SessionID: p.SessionID, WorkspaceResults: p.PreviousResults}, 3, command.RequestID})
					if handleDaemonFrame(ctx, api.hub, dc, f.pool, wire, envelope{Type: "session_ended", SessionID: p.SessionID}) {
						t.Fatal("stale discarded outcomes acknowledged stop")
					}
				}
				if mode == "ack" || mode == "autostop" || retry {
					results := append([]protocol.WorkspaceMemberResult(nil), p.PreviousResults...)
					results[0].Head = "final-head"
					results[0].Pending = false
					results[1].PushError = "final refusal"
					results[1].Pending = true
					wire, _ := json.Marshal(struct {
						protocol.SessionEnded
						Revision int64  `json:"workspace_revision"`
						StopID   string `json:"workspace_stop_id"`
					}{protocol.SessionEnded{Type: "session_ended", SessionID: p.SessionID, WorkspaceResults: results}, 3, command.RequestID})
					if !handleDaemonFrame(ctx, api.hub, dc, f.pool, wire, envelope{Type: "session_ended", SessionID: p.SessionID}) {
						t.Fatal("final report rejected during stop")
					}
				}
			}
			select {
			case err := <-done:
				if retry {
					if err == nil {
						t.Fatal("cleanup failure confirmed stop")
					}
					row, getErr := db.GetSession(ctx, f.pool, p.SessionID)
					if getErr != nil || row.EndedAt != nil {
						t.Fatal("failed cleanup marked terminal", getErr)
					}
					var sealed int64
					if err := f.pool.QueryRow(ctx, `SELECT settled_generation FROM cluster_workspace_storage WHERE session_id=$1`, p.SessionID).Scan(&sealed); err != nil || sealed != p.Runtime.Generation {
						t.Fatal("no durable exact-generation seal", sealed, err)
					}
					if err := persistClusterWorkspaceResults(ctx, f.pool, p.SessionID, p.Runtime, 4, p.PreviousResults); err == nil {
						t.Fatal("late report rewrote sealed outcomes")
					}
					failCleanup.Store(false)
					if mode == "ack_terminal_retry" {
						if _, e := f.pool.Exec(ctx, `DROP TRIGGER refuse_terminal ON sessions`); e != nil {
							t.Fatal(e)
						}
					}
					// Lose the socket and its waiter, as after server/pod disconnect.
					api.hub.UnregisterIfCurrent(dc)
					_, err = api.Stop(ctx, RunnerPrincipal{Kind: runnerKeyPrincipalKind}, p.SessionID)
				}
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("stop did not complete")
			}
			w, err := db.GetSessionWorkspace(ctx, f.pool, p.SessionID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "ack" || mode == "autostop" || retry {
				expected := append([]protocol.WorkspaceMemberResult(nil), p.PreviousResults...)
				expected[0].Head = "final-head"
				expected[0].Pending = false
				expected[1].PushError = "final refusal"
				expected[1].Pending = true
				if w.Results[0] != expected[0] || w.Results[1] != expected[1] {
					t.Fatal("lost final settlement", w.Results)
				}
			} else {
				for _, v := range w.Results {
					if !v.Pending || !strings.Contains(v.SyncError, "unacknowledged") || v.RecoveryLocation == "" {
						t.Fatal("lost uncertainty evidence", w.Results)
					}
				}
			}
			row, err := db.GetSession(ctx, f.pool, p.SessionID)
			if err != nil || row.EndedAt == nil {
				t.Fatal("stop not terminal", err)
			}
			if mode == "autostop" && derefOrEmpty(row.EndReason) != db.EndReasonAutoStopped {
				t.Fatal("natural exit overwrote coordinated auto-stop reason", row.EndReason)
			}
			if err := db.ValidateRuntimeBinding(ctx, f.pool, p.SessionID, token.ID, false); err == nil {
				t.Fatal("terminal runtime authority survived")
			}
			if mode == "ack" {
				if _, err := api.Stop(ctx, RunnerPrincipal{Kind: runnerKeyPrincipalKind}, p.SessionID); err != nil {
					t.Fatal("second stop after acknowledged settlement failed", err)
				}
				again, err := db.GetSession(ctx, f.pool, p.SessionID)
				if err != nil || !again.EndedAt.Equal(*row.EndedAt) || derefOrEmpty(again.EndReason) != derefOrEmpty(row.EndReason) {
					t.Fatal("second stop changed original terminal evidence", err)
				}
				if len(dc.send) != 0 {
					t.Fatal("second stop asked removed agent for another report")
				}
			}
		})
	}
}

func TestClusterWorkspaceStopCannotConfirmWithoutDurableEvidence(t *testing.T) {
	f, _, p := reviewWorkspace(t)
	ctx := context.Background()
	f.api.clusterStopTimeout = 200 * time.Millisecond
	raw, _, err := db.MintRuntimeLaunch(ctx, f.pool, p.SessionID, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	token, err := db.ValidateRuntimeToken(ctx, f.pool, raw)
	if err != nil {
		t.Fatal(err)
	}
	dc := &DaemonConn{ID: newUUID(), Name: "runner-" + p.SessionID, Mode: "runner", BoundSessionID: p.SessionID, RuntimeTokenID: token.ID, WorkspaceRuntime: p.Runtime, send: make(chan []byte, 4)}
	f.api.hub.Register(dc)
	f.api.hub.SetSessionOwner(p.SessionID, dc.ID)
	done := make(chan *APIError, 1)
	go func() {
		_, err := f.api.Stop(ctx, RunnerPrincipal{Kind: runnerKeyPrincipalKind}, p.SessionID)
		done <- err
	}()
	select {
	case <-dc.send:
	case <-time.After(time.Second):
		t.Fatal("no stop request")
	}
	// Break only this fixture's ledger after admission, before timeout evidence.
	// A successful HTTP stop must not be claimed if durable evidence failed.
	if _, err := f.pool.Exec(ctx, `ALTER TABLE session_workspaces RENAME COLUMN results TO unavailable_results`); err != nil {
		t.Fatal(err)
	}
	restored := false
	defer func() {
		if !restored {
			_, _ = f.pool.Exec(ctx, `ALTER TABLE session_workspaces RENAME COLUMN unavailable_results TO results`)
		}
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("confirmed stop without durable settlement/uncertainty")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("failed stop did not return")
	}
	if _, err := f.pool.Exec(ctx, `ALTER TABLE session_workspaces RENAME COLUMN unavailable_results TO results`); err != nil {
		t.Fatal(err)
	}
	restored = true
	row, err := db.GetSession(ctx, f.pool, p.SessionID)
	if err != nil || row.EndedAt != nil {
		t.Fatal("terminal before durable evidence", err)
	}
	if _, err := f.api.Stop(ctx, RunnerPrincipal{Kind: runnerKeyPrincipalKind}, p.SessionID); err != nil {
		t.Fatal("retry could not persist uncertainty", err)
	}
}

func TestClusterWorkspaceStopCannotConfirmFailedJobDeletion(t *testing.T) {
	f, _, p := reviewWorkspace(t)
	ctx := context.Background()
	kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer kube.Close()
	f.api.hub.SetJobManager(&JobManager{BaseURL: kube.URL, Namespace: "fixture", Client: kube.Client()})
	if _, err := f.api.Stop(ctx, RunnerPrincipal{Kind: runnerKeyPrincipalKind}, p.SessionID); err == nil {
		t.Fatal("confirmed stop despite unproven Job deletion")
	}
	row, err := db.GetSession(ctx, f.pool, p.SessionID)
	if err != nil || row.EndedAt != nil {
		t.Fatal("marked terminal despite failed cleanup", err)
	}
	workspace, err := db.GetSessionWorkspace(ctx, f.pool, p.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range workspace.Results {
		if !v.Pending || v.RecoveryLocation == "" || !strings.Contains(v.SyncError, "unacknowledged") {
			t.Fatal("cleanup failure lost retained evidence", v)
		}
	}
}

func TestClusterWorkspaceSettlementSealIsGenerationScoped(t *testing.T) {
	f, _, p := reviewWorkspace(t)
	ctx := context.Background()
	final := append([]protocol.WorkspaceMemberResult(nil), p.PreviousResults...)
	final[0].Pending = false
	if err := persistClusterWorkspaceReport(ctx, f.pool, p.SessionID, p.Runtime, 2, final, true); err != nil {
		t.Fatal(err)
	}
	next, err := db.BeginWorkspaceRuntime(ctx, f.pool, p.SessionID, p.Runtime.LaunchGeneration, p.Runtime.Generation)
	if err != nil {
		t.Fatal(err)
	}
	if next == p.Runtime.Generation {
		t.Fatal("runtime generation did not change")
	}
	// No peer for this generation: a previous generation's seal is no evidence
	// of its final state. The trusted coordinator must record uncertainty anew.
	if err := f.api.settleClusterStop(ctx, p.SessionID); err != nil {
		t.Fatal(err)
	}
	workspace, err := db.GetSessionWorkspace(ctx, f.pool, p.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !workspace.Results[0].Pending || !strings.Contains(workspace.Results[0].SyncError, "unacknowledged") {
		t.Fatal("old seal acknowledged replacement runtime", workspace.Results)
	}
	var sealed int64
	if err := f.pool.QueryRow(ctx, `SELECT settled_generation FROM cluster_workspace_storage WHERE session_id=$1`, p.SessionID).Scan(&sealed); err != nil || sealed != next {
		t.Fatal(sealed, err)
	}
	if err := persistClusterWorkspaceResults(ctx, f.pool, p.SessionID, p.Runtime, 3, final); err == nil {
		t.Fatal("old runtime overwrote new settlement")
	}
}

func TestClusterWorkspaceSettlementSealAndResultsAreAtomic(t *testing.T) {
	f, _, p := reviewWorkspace(t)
	ctx := context.Background()
	if _, err := f.pool.Exec(ctx, `CREATE FUNCTION refuse_seal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.settled_generation>0 THEN RAISE EXCEPTION 'fixture seal failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER refuse_seal BEFORE UPDATE ON cluster_workspace_storage FOR EACH ROW EXECUTE FUNCTION refuse_seal()`); err != nil {
		t.Fatal(err)
	}
	final := append([]protocol.WorkspaceMemberResult(nil), p.PreviousResults...)
	final[0].Head = "uncommitted-final"
	final[0].Pending = false
	if err := persistClusterWorkspaceReport(ctx, f.pool, p.SessionID, p.Runtime, 2, final, true); err == nil {
		t.Fatal("failed seal confirmed final report")
	}
	workspace, err := db.GetSessionWorkspace(ctx, f.pool, p.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Results[0].Head != p.PreviousResults[0].Head || !workspace.Results[0].Pending {
		t.Fatal("results committed without seal", workspace.Results)
	}
}

func TestReviewedStoredLegacyClusterRestartResumeStop(t *testing.T) {
	for _, single := range []bool{true, false} {
		for _, resume := range []bool{false, true} {
			t.Run(fmt.Sprintf("single=%v/resume=%v", single, resume), func(t *testing.T) {
				f, spec, p := reviewWorkspace(t, func(p *clusterworkspace.Plan) {
					p.Manifest.Layout = ""
					for i := range p.Manifest.Members {
						p.Manifest.Members[i].Path = p.Manifest.Members[i].ID
					}
					if single {
						p.Manifest.Kind = "repo"
						p.Manifest.Members = p.Manifest.Members[:1]
						p.Members = p.Members[:1]
					}
				})
				f.fake.credentialKinds = []string{"github"}
				f.fake.credentialFetchResponses = map[string][]byte{spec.SpawningAccountID + ":github": []byte("fixture-personal-git")}
				f.fake.pods = map[string]map[string]any{}
				f.fake.jobUID = "new-job"
				j := newTestJobManager(t, f.fake)
				j.historyPool = f.pool
				j.WorkspaceStorageClass = "fixture-enforced"
				f.fake.setJobMissing(j.jobName(p.SessionID))
				fallback := f.fake.handler()
				kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pods") {
						_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
						return
					}
					if strings.Contains(r.URL.Path, "/persistentvolumeclaims") {
						_ = json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"name": "blerg-workspace-" + p.SessionID, "uid": "fixture-uid", "labels": map[string]string{"app": "blerg-workspace", "session": p.SessionID, "account": spec.SpawningAccountID}}})
						return
					}
					fallback(w, r)
				}))
				defer kube.Close()
				j.BaseURL, j.Client = kube.URL, kube.Client()
				spec.Workspace, spec.Repo, spec.GitURL, spec.Resume, spec.AuthSessionID = &p.Manifest, "", "", resume, "fixture-login"
				if err := j.CreateSessionJob(spec); err != nil {
					t.Fatal("stored legacy session failed to restart/resume", err)
				}
				var next clusterworkspace.Plan
				data := f.fake.createdSecrets[0]["stringData"].(map[string]any)
				if err := json.Unmarshal([]byte(data[clusterworkspace.Env].(string)), &next); err != nil {
					t.Fatal(err)
				}
				rt, err := clusterworkspace.New(next, &reviewResumeGit{})
				if err != nil {
					t.Fatal("legacy pod rejected stored path", err)
				}
				if err := rt.Prepare(t.Context(), true); err != nil {
					t.Fatal(err)
				}
				for i, result := range rt.Stop(t.Context()) {
					if result.RecoveryLocation != "projects/"+p.Manifest.Members[i].ID {
						t.Fatal("legacy path changed", result)
					}
				}
				f.api.clusterStopTimeout = time.Millisecond
				if err := f.api.settleClusterStop(t.Context(), p.SessionID); err != nil {
					t.Fatal("stored legacy session failed to stop", err)
				}
				parent, err := db.GetSession(t.Context(), f.pool, p.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				childID := newUUID()
				if err := db.InsertSessionAs(t.Context(), f.pool, childID, parent.DaemonID, "starting", "", "", "", "", db.SessionOrigin{SpawningAccount: spec.SpawningAccountID, ParentSessionID: p.SessionID}); err != nil {
					t.Fatal(err)
				}
				child := p.Manifest
				child.Kind = "repo"
				child.LaunchGeneration = "legacy-child"
				child.Members = append([]protocol.WorkspaceMember(nil), p.Manifest.Members[len(p.Manifest.Members)-1:]...)
				sources, err := clusterLayoutSources(t.Context(), f.pool, childID, child)
				if err != nil {
					t.Fatal("legacy child lost layout", err)
				}
				next.Manifest, next.LayoutSources, next.Members, next.Identities, next.PreviousResults = child, sources, p.Members[len(p.Members)-1:], nil, nil
				next.Runtime.LaunchGeneration = child.LaunchGeneration
				if err := next.Validate(); err != nil {
					t.Fatal("legacy child plan rejected", err)
				}
			})
		}
	}
}
