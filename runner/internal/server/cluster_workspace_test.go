package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func clusterPlanFixture() clusterworkspace.Plan {
	p := clusterworkspace.Plan{SessionID: "session", Manifest: protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner", Runtime: "cluster", LaunchGeneration: "selection", Digest: "digest"}, Runtime: protocol.WorkspaceRuntimeRef{LaunchGeneration: "selection", Generation: 1}}
	for _, id := range []string{"one", "two"} {
		p.Manifest.Members = append(p.Manifest.Members, protocol.WorkspaceMember{ID: id, Name: "same-name", Path: id, SourceIdentity: "source-" + id, CommonDirectoryIdentity: "common-" + id, Provider: "github", Destination: "org/" + id})
		p.Members = append(p.Members, clusterworkspace.Member{ID: id, Provider: "github", CloneURL: "https://github.com/org/" + id + ".git", PushURL: "https://github.com/org/" + id + ".git", DestinationIdentity: "org/" + id, CredentialHost: "github.com", Token: "private-token-" + id, PushAuthorized: true})
	}
	return p
}
func TestClusterWorkspaceJobKeepsHomeAndCredentialsOutsidePublicRoot(t *testing.T) {
	f := &fakeK8s{}
	j := newTestJobManager(t, f)
	p := clusterPlanFixture()
	spec := SessionJobSpec{SessionID: p.SessionID, workspacePlan: &p, WorkspaceMembers: p.Members, workspaceClaim: "fixture-workspace", Engine: "claude"}
	rawSpec, _ := json.Marshal(spec)
	if strings.Contains(string(rawSpec), "private-token-") {
		t.Fatal("private contexts escaped Job spec serialization")
	}
	if err := j.createSessionJob(spec); err != nil {
		t.Fatal(err)
	}
	if len(f.created) != 1 || len(f.createdSecrets) != 1 {
		t.Fatal("missing job or private secret")
	}
	raw, _ := json.Marshal(f.created[0])
	job := string(raw)
	for _, member := range p.Members {
		if strings.Contains(job, member.Token) {
			t.Fatal("credential in Job literal")
		}
	}
	if strings.Contains(job, `"mountPath":"/workspace","name":"workspace-projects"`) || !strings.Contains(job, `"mountPath":"/workspace/projects","name":"workspace-projects"`) {
		t.Fatal("public root includes HOME")
	}
	if strings.Contains(job, `"name":"BLERG_RUNNER_GIT_TOKEN"`) {
		t.Fatal("operator or singular Git fallback injected")
	}
	if !strings.Contains(job, `"memory":"536870912"`) {
		t.Fatal("aggregate Git descendants lack an enforced memory cgroup ceiling")
	}
	if !strings.Contains(job, `"automountServiceAccountToken":false`) {
		t.Fatal("pod received Kubernetes authority")
	}
	data := f.createdSecrets[0]["stringData"].(map[string]any)
	encoded, ok := data[clusterworkspace.Env].(string)
	if !ok {
		t.Fatal("missing secret launch context")
	}
	decoded := []byte(encoded)
	var got clusterworkspace.Plan
	if json.Unmarshal(decoded, &got) != nil || got.Validate() != nil || got.Members[0].Token == got.Members[1].Token {
		t.Fatal("lost per-member credential scopes")
	}
	rbac, e := os.ReadFile("../../../install/k8s/cluster-runtime.yaml")
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(rbac), "pods/exec") {
		t.Fatal("new exec RBAC")
	}
}
func TestClusterWorkspaceChildSubset(t *testing.T) {
	p := clusterPlanFixture()
	child := p.Manifest
	child.LaunchGeneration = "child-selection"
	child.Members = append([]protocol.WorkspaceMember(nil), p.Manifest.Members[1:]...)
	selected, e := ClusterWorkspaceChild(p.Manifest, child, p.Members)
	if e != nil || len(selected) != 1 || selected[0].ID != "two" || selected[0].Token != "" {
		t.Fatal("child inherited credentials or changed selection", e)
	}
	child.Members[0].Destination = "other/repo"
	if _, e = ClusterWorkspaceChild(p.Manifest, child, p.Members); e == nil {
		t.Fatal("child escalated destination")
	}
}
func TestClusterWorkspaceStorageMissingFailsClosed(t *testing.T) {
	p := clusterPlanFixture()
	j := &JobManager{}
	if e := j.CreateSessionJob(SessionJobSpec{Workspace: &p.Manifest, WorkspaceMembers: p.Members}); e == nil {
		t.Fatal("workspace fell back to ephemeral single-repo job")
	}
}
func TestClusterWorkspacePreparedAndOutboundFencing(t *testing.T) {
	f, spec := nativeFixture(t)
	ctx := context.Background()
	p := clusterPlanFixture()
	p.SessionID = spec.SessionID
	p.Manifest.OwnerID = spec.SpawningAccountID
	if err := db.InsertSessionWorkspace(ctx, f.pool, spec.SessionID, p.Manifest); err != nil {
		t.Fatal(err)
	}
	storedContexts := append([]clusterworkspace.Member(nil), p.Members...)
	for i := range storedContexts {
		storedContexts[i].Token = ""
	}
	contexts, _ := json.Marshal(storedContexts)
	if _, e := f.pool.Exec(ctx, `INSERT INTO cluster_workspace_storage(session_id,launch_generation,claim_name,claim_uid,contexts,owner_id) VALUES($1,$2,'fixture-claim','fixture-uid',$3,$4)`, spec.SessionID, p.Manifest.LaunchGeneration, contexts, spec.SpawningAccountID); e != nil {
		t.Fatal(e)
	}
	n, e := db.BeginWorkspaceRuntime(ctx, f.pool, spec.SessionID, p.Manifest.LaunchGeneration, 0)
	if e != nil {
		t.Fatal(e)
	}
	rawToken, _, e := db.MintRuntimeLaunch(ctx, f.pool, spec.SessionID, "", time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	token, e := db.ValidateRuntimeToken(ctx, f.pool, rawToken)
	if e != nil {
		t.Fatal(e)
	}
	dc := &DaemonConn{ID: "fixture-daemon", Mode: "runner", Name: "runner-" + spec.SessionID, RuntimeTokenID: token.ID, BoundSessionID: spec.SessionID, WorkspaceRuntime: protocol.WorkspaceRuntimeRef{LaunchGeneration: p.Manifest.LaunchGeneration, Generation: n}}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: p.Manifest.LaunchGeneration, Generation: n}, ManifestDigest: p.Manifest.Digest, Root: protocol.WorkspaceRoot{Device: 1, Inode: 2}}
	identities := []clusterworkspace.MemberIdentity{}
	for i, m := range p.Members {
		identities = append(identities, clusterworkspace.MemberIdentity{ID: m.ID, Directory: protocol.WorkspaceRoot{Device: 1, Inode: uint64(10 + i)}, GitDirectory: protocol.WorkspaceRoot{Device: 1, Inode: uint64(20 + i)}, Objects: protocol.WorkspaceRoot{Device: 1, Inode: uint64(30 + i)}})
	}
	prepared := struct {
		protocol.WorkspaceRuntimePrepared
		Identities []clusterworkspace.MemberIdentity `json:"identities"`
	}{protocol.WorkspaceRuntimePrepared{Type: "workspace_runtime_prepared", SessionID: spec.SessionID, Binding: binding}, identities}
	raw, _ := json.Marshal(prepared)
	if !handleDaemonFrame(ctx, f.api.hub, dc, f.pool, raw, envelope{Type: "workspace_runtime_prepared", SessionID: spec.SessionID}) {
		t.Fatal("current outbound preparation refused")
	}
	_, stored, e := db.GetWorkspaceRuntime(ctx, f.pool, spec.SessionID)
	if e != nil || stored == nil || *stored != binding {
		t.Fatal("root not persisted", e)
	}
	delivered := 0
	dc.SetClusterWorkspaceReplyHandler(func(context.Context, []byte) { delivered++ })
	reply := map[string]any{"type": "owner_shell_result", "session_id": spec.SessionID, "request_id": "private-request", "launch_generation": binding.LaunchGeneration, "generation": binding.Generation, "data": "private-shell-bytes"}
	replyRaw, _ := json.Marshal(reply)
	if !handleDaemonFrame(ctx, f.api.hub, dc, f.pool, replyRaw, envelope{Type: "owner_shell_result", SessionID: spec.SessionID}) || delivered != 1 {
		t.Fatal("private pod reply not delivered")
	}
	reply["generation"] = binding.Generation + 1
	staleReply, _ := json.Marshal(reply)
	if handleDaemonFrame(ctx, f.api.hub, dc, f.pool, staleReply, envelope{Type: "owner_shell_result", SessionID: spec.SessionID}) || delivered != 1 {
		t.Fatal("stale private reply delivered")
	}
	bad := prepared
	bad.Identities = append([]clusterworkspace.MemberIdentity(nil), identities...)
	bad.Identities[0].GitDirectory.Inode++
	altered, _ := json.Marshal(bad)
	if persistClusterWorkspacePrepared(ctx, f.pool, spec.SessionID, binding.WorkspaceRuntimeRef, altered) == nil {
		t.Fatal("recovery adopted switched metadata")
	}
	newer := []protocol.WorkspaceMemberResult{}
	for _, m := range p.Members {
		newer = append(newer, protocol.WorkspaceMemberResult{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: m.ID, LaunchGeneration: p.Manifest.LaunchGeneration}, Head: "successful-" + m.ID})
	}
	if e = persistClusterWorkspaceResults(ctx, f.pool, spec.SessionID, binding.WorkspaceRuntimeRef, 5, newer); e != nil {
		t.Fatal(e)
	}
	older := append([]protocol.WorkspaceMemberResult(nil), newer...)
	older[0].Head = "older"
	older[0].Pending = true
	older[0].PushError = "older refusal"
	if e = persistClusterWorkspaceResults(ctx, f.pool, spec.SessionID, binding.WorkspaceRuntimeRef, 4, older); e != nil {
		t.Fatal(e)
	}
	latest, e := db.GetSessionWorkspace(ctx, f.pool, spec.SessionID)
	if e != nil || latest.Results[0].Head != "successful-one" {
		t.Fatal("reordered report overwrote success", e)
	}
	preserved := []protocol.WorkspaceMemberResult{{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: "one", LaunchGeneration: p.Manifest.LaunchGeneration}, Head: "preserved"}}
	if e = db.SetWorkspaceRuntimeResults(ctx, f.pool, spec.SessionID, binding.WorkspaceRuntimeRef, preserved); e != nil {
		t.Fatal(e)
	}
	// Simulate an authority boundary independently: even if a caller has not
	// revoked the token yet, its connection's old generation must remain old.
	next, e := db.BeginWorkspaceRuntime(ctx, f.pool, spec.SessionID, p.Manifest.LaunchGeneration, n)
	if e != nil {
		t.Fatal(e)
	}
	nextBinding := binding
	nextBinding.Generation = next
	if e = db.BindWorkspaceRuntime(ctx, f.pool, spec.SessionID, nextBinding.WorkspaceRuntimeRef, nextBinding); e != nil {
		t.Fatal(e)
	}
	staleReport, _ := json.Marshal(struct {
		Type      string                           `json:"type"`
		SessionID string                           `json:"session_id"`
		Revision  int64                            `json:"revision"`
		Results   []protocol.WorkspaceMemberResult `json:"results"`
	}{"cluster_workspace_results", spec.SessionID, 6, older})
	if handleDaemonFrame(ctx, f.api.hub, dc, f.pool, staleReport, envelope{Type: "cluster_workspace_results", SessionID: spec.SessionID}) {
		t.Fatal("old socket inherited new generation from database")
	}
	retained, e := db.GetSessionWorkspace(ctx, f.pool, spec.SessionID)
	if e != nil || len(retained.Results) != 1 || retained.Results[0].Head != "preserved" {
		t.Fatal("stale report erased successful result", e)
	}
	if _, _, e = db.MintRuntimeLaunch(ctx, f.pool, spec.SessionID, "", time.Hour); e != nil {
		t.Fatal(e)
	}
	if handleDaemonFrame(ctx, f.api.hub, dc, f.pool, raw, envelope{Type: "workspace_runtime_prepared", SessionID: spec.SessionID}) {
		t.Fatal("replaced writer prepared a root")
	}
	report := []byte(`{"type":"cluster_workspace_results","session_id":"` + spec.SessionID + `","revision":2,"results":[]}`)
	if handleDaemonFrame(ctx, f.api.hub, dc, f.pool, report, envelope{Type: "cluster_workspace_results", SessionID: spec.SessionID}) {
		t.Fatal("replaced writer reported results")
	}
}

func TestClusterWorkspacePrechecksEachCredentialBeforeStorage(t *testing.T) {
	fixture, spec := nativeFixture(t)
	p := clusterPlanFixture()
	p.SessionID = spec.SessionID
	p.Manifest.OwnerID = spec.SpawningAccountID
	p.Manifest.Members[1].Provider = "gitlab"
	p.Members[1].Provider = "gitlab"
	p.Members[1].CloneURL = "https://gitlab.com/org/two.git"
	p.Members[1].PushURL = p.Members[1].CloneURL
	p.Members[1].CredentialHost = "gitlab.com"
	for i := range p.Members {
		p.Members[i].Token = ""
	}
	if e := db.InsertSessionWorkspace(context.Background(), fixture.pool, spec.SessionID, p.Manifest); e != nil {
		t.Fatal(e)
	}
	fixture.fake.credentialKinds = []string{"github", "gitlab", "codex"}
	fixture.fake.credentialFetchResponses = map[string][]byte{spec.SpawningAccountID + ":github": []byte("fixture-github"), spec.SpawningAccountID + ":gitlab": []byte("fixture-gitlab")}
	j := newTestJobManager(t, fixture.fake)
	j.historyPool = fixture.pool
	j.WorkspaceStorageClass = "fixture-enforced"
	var checks []string
	j.repositoryRead = func(_ context.Context, url, token string) error {
		checks = append(checks, url)
		if strings.Contains(url, "github.com") {
			if token != "fixture-github" {
				t.Error("wrong credential for GitHub")
			}
			return nil
		}
		if token != "fixture-gitlab" {
			t.Error("wrong credential for GitLab")
		}
		return errRepositoryUnreadable
	}
	spec.Repo = ""
	spec.GitURL = ""
	spec.AuthSessionID = "fixture-login"
	spec.Workspace = &p.Manifest
	spec.WorkspaceMembers = p.Members
	if e := j.CreateSessionJob(spec); e == nil {
		t.Fatal("partial repository access accepted")
	}
	if len(checks) != 2 {
		t.Fatal("did not precheck every selected member", len(checks))
	}
	if len(fixture.fake.created) != 0 || len(fixture.fake.createdSecrets) != 0 {
		t.Fatal("Job or credential Secret created after failed member precheck")
	}
	var count int
	if e := fixture.pool.QueryRow(context.Background(), `SELECT count(*) FROM cluster_workspace_storage WHERE session_id=$1`, spec.SessionID).Scan(&count); e != nil || count != 0 {
		t.Fatal("storage binding created before prechecks", e)
	}
}

func TestClusterWorkspaceLaunchPersistsOnlyTokenFreeStorageBinding(t *testing.T) {
	fixture, spec := nativeFixture(t)
	p := clusterPlanFixture()
	p.SessionID = spec.SessionID
	p.Manifest.OwnerID = spec.SpawningAccountID
	if e := db.InsertSessionWorkspace(context.Background(), fixture.pool, spec.SessionID, p.Manifest); e != nil {
		t.Fatal(e)
	}
	fixture.fake.credentialKinds = []string{"github"}
	fixture.fake.credentialFetchResponses = map[string][]byte{spec.SpawningAccountID + ":github": []byte("fixture-personal-git")}
	fixture.fake.pods = map[string]map[string]any{}
	fixture.fake.jobUID = "fixture-job-uid"
	j := newTestJobManager(t, fixture.fake)
	j.historyPool = fixture.pool
	j.WorkspaceStorageClass = "fixture-enforced"
	fixture.fake.setJobMissing(j.jobName(spec.SessionID))
	var mu sync.Mutex
	var claim map[string]any
	fallback := fixture.fake.handler()
	kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pods") {
			_ = json.NewEncoder(w).Encode(map[string]any{"items": []any{}})
			return
		}
		if strings.Contains(r.URL.Path, "/persistentvolumeclaims") {
			mu.Lock()
			defer mu.Unlock()
			if r.Method == http.MethodPost {
				if json.NewDecoder(r.Body).Decode(&claim) != nil {
					t.Error("invalid claim request")
					w.WriteHeader(500)
					return
				}
				claim["metadata"].(map[string]any)["uid"] = "fixture-workspace-uid"
				w.WriteHeader(http.StatusCreated)
				return
			}
			if claim == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(claim)
			return
		}
		fallback(w, r)
	}))
	defer kube.Close()
	j.BaseURL = kube.URL
	j.Client = kube.Client()
	spec.AuthSessionID = "fixture-login"
	spec.Workspace = &p.Manifest
	spec.WorkspaceMembers = p.Members
	spec.Repo = ""
	spec.GitURL = ""
	if e := j.CreateSessionJob(spec); e != nil {
		t.Fatal(e)
	}
	var stored []byte
	var owner, uid string
	if e := fixture.pool.QueryRow(context.Background(), `SELECT contexts,owner_id,claim_uid FROM cluster_workspace_storage WHERE session_id=$1`, spec.SessionID).Scan(&stored, &owner, &uid); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(stored), "token") || owner != spec.SpawningAccountID || uid != "fixture-workspace-uid" {
		t.Fatal("private storage binding contains credentials or wrong identity")
	}
	generation, binding, e := db.GetWorkspaceRuntime(context.Background(), fixture.pool, spec.SessionID)
	if e != nil || generation != 1 || binding != nil {
		t.Fatal("launch did not enter fenced pending preparation", e)
	}
	if len(fixture.fake.created) != 1 {
		t.Fatal("workspace Job missing")
	}
	// A pending allocation without prepared member identities is uncertain and
	// must never be silently adopted or replaced by a fresh clone on resume.
	spec.Resume = true
	spec.WorkspaceMembers = nil
	if j.CreateSessionJob(spec) == nil {
		t.Fatal("uncertain pending allocation was adopted")
	}
}

func TestClusterWorkspaceMemoryBudgetPreservesTighterLimits(t *testing.T) {
	for _, limit := range []string{"256Mi", "512Mi", "4Gi"} {
		j := newTestJobManager(t, &fakeK8s{})
		j.MemLimit = limit
		p := clusterPlanFixture()
		spec := SessionJobSpec{SessionID: p.SessionID, workspacePlan: &p, workspaceClaim: "fixture-claim"}
		pod := map[string]any{"containers": []map[string]any{{"resources": map[string]any{"requests": map[string]any{"memory": "1Gi"}, "limits": map[string]any{"memory": limit}}}}}
		if e := clusterWorkspacePod(spec, pod); e != nil {
			t.Fatal(e)
		}
		resources := pod["containers"].([]map[string]any)[0]["resources"].(map[string]any)
		actual, e := clusterMemoryBytes(resources["limits"].(map[string]any)["memory"])
		if e != nil {
			t.Fatal(e)
		}
		original, _ := clusterMemoryBytes(limit)
		if actual > original || actual > protocol.DefaultWorkspaceBudget().GitMemoryBytes {
			t.Fatal("memory ceiling was relaxed")
		}
	}
	if _, e := clusterMemoryBytes("unknown"); e == nil {
		t.Fatal("unknown resource quantity admitted")
	}
}

type clusterSecretLog struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (l *clusterSecretLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buffer.Write(p)
}
func (l *clusterSecretLog) text() string { l.mu.Lock(); defer l.mu.Unlock(); return l.buffer.String() }
func TestClusterWorkspaceSecretRejectionOmitsUpstreamPayload(t *testing.T) {
	const token = "fixture-private-log-value"
	fake := &fakeK8s{secretCreateStatus: 500, secretCreateBody: token}
	j := newTestJobManager(t, fake)
	capture := &clusterSecretLog{}
	previous := log.Writer()
	log.SetOutput(capture)
	defer log.SetOutput(previous)
	if _, e := j.createSessionSecret(t.Context(), "fixture", map[string][]byte{clusterworkspace.Env: []byte(token)}); e == nil {
		t.Fatal("secret rejection accepted")
	}
	if strings.Contains(capture.text(), token) {
		t.Fatal("upstream credential payload escaped into logs")
	}
}

func TestClusterWorkspaceValidatesPodBeforeCredentialAllocation(t *testing.T) {
	f := &fakeK8s{}
	j := newTestJobManager(t, f)
	j.MemLimit = "0.5Gi"
	p := clusterPlanFixture()
	spec := SessionJobSpec{SessionID: p.SessionID, workspacePlan: &p, workspaceClaim: "fixture-claim", Engine: "claude"}
	if err := j.createSessionJob(spec); err == nil {
		t.Fatal("invalid pod resource accepted")
	}
	if len(f.created) != 0 || len(f.createdSecrets) != 0 || len(f.deletedSecrets) != 0 {
		t.Fatal("credential allocation survived unused pod", len(f.created), len(f.createdSecrets), f.deletedSecrets)
	}
	// The fake's default Job is live. A pre-existing credential allocation
	// belongs to it and must remain untouched by a rejected replacement pod.
	f.mu.Lock()
	f.existingSecrets = map[string]bool{sessionSecretName(spec.SessionID): true}
	f.mu.Unlock()
	if err := j.createSessionJob(spec); err == nil {
		t.Fatal("invalid replacement accepted")
	}
	if len(f.deletedSecrets) != 0 || !f.existingSecrets[sessionSecretName(spec.SessionID)] {
		t.Fatal("deleted a live Job's credential Secret")
	}

}
