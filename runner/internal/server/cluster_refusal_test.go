package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

func TestClusterAuthorizationRefusal(t *testing.T) {
	for _, err := range []error{errCorePrincipalRefused, ErrNoPersonalCredential} {
		code := "owner_authorization_refused"
		if clusterStartFailureStatus(err) != http.StatusUnprocessableEntity {
			t.Fatal("refusal must be terminal", code)
		}
		if !errors.Is(startFailureCause(err), err) {
			t.Fatal("typed cause lost")
		}
		w := httptest.NewRecorder()
		writeAPIError(w, apiErrorCause(422, err, "%s", err.Error()))
		if w.Header().Get("X-Blerg-Launch-Refusal") != code {
			t.Fatal("missing typed refusal", w)
		}
	}
}

func TestRepositoryRefusalCauses(t *testing.T) {
	for _, tc := range []struct {
		status      int
		token, code string
	}{
		{401, "", "owner_token_missing"}, {401, "fixture-canary-token", "owner_token_rejected"},
		{403, "fixture-canary-token", "repository_scope_insufficient"}, {404, "fixture-canary-token", "repository_not_visible"},
	} {
		srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte("fixture-canary-secret"))
		}))
		err := checkRepositoryRead(t.Context(), srv.Client(), srv.URL+"/org/repo.git", tc.token)
		srv.Close()
		refusal := clusterRefusal(err)
		if refusal == nil || refusal.code != tc.code {
			t.Fatalf("status %d: %v", tc.status, err)
		}
		if strings.Contains(err.Error(), "fixture-canary") {
			t.Fatal("secret leaked")
		}
		if got := launchDiagnosticReason(stage("credentials", "", protocol.StageStateFailed, err.Error())); got != tc.code {
			t.Fatal("lost log cause", got)
		}
	}
}

func TestClusterRefusalIdempotency(t *testing.T) {
	f, _ := nativeFixture(t)
	ctx := t.Context()
	id := newUUID()
	_, claimed, err := db.ClaimIdempotencyKey(ctx, f.pool, "fixture-scope", "launch-key", "same-body", id, time.Hour)
	if err != nil || !claimed {
		t.Fatal("claim", err)
	}
	if err := db.RefuseIdempotencyKey(ctx, f.pool, "fixture-scope", "launch-key", id, "owner_token_rejected", ""); err != nil {
		t.Fatal(err)
	}
	replay, claimed, err := db.ClaimIdempotencyKey(ctx, f.pool, "fixture-scope", "launch-key", "same-body", newUUID(), time.Hour)
	if err != nil || claimed || replay.SessionID != id || replay.RefusalCode != "owner_token_rejected" {
		t.Fatalf("replay: %+v %v %v", replay, claimed, err)
	}
	stored, err := db.GetIdempotencyKey(ctx, f.pool, "fixture-scope", "launch-key")
	if err != nil || stored.RefusalCode != replay.RefusalCode {
		t.Fatal("delegate replay lost refusal", err)
	}
}

func TestClusterChildRefusalDoesNotLeaveWorkers(t *testing.T) {
	cx := newClusterDelegFx(t)
	cx.clusterRule("claude")
	calls := 0
	cx.hub.JobManager().repositoryRead = func(context.Context, string, string) error {
		calls++
		return &clusterAuthorizationRefusal{code: "owner_token_rejected"}
	}
	body := map[string]any{"repo": "org/app", "prompt": "x"}
	key := newUUID()
	for attempt := 0; attempt < 3; attempt++ {
		status, out, _ := cx.postChild(cx.cparent, cx.cparentTk, body, key)
		if status != 422 || !strings.Contains(asString(out["error"]), "rejected") {
			t.Fatalf("attempt %d: %d %v", attempt, status, out)
		}
	}
	if calls != 1 {
		t.Fatalf("replayed refusal reached provider %d times", calls)
	}
	var count int
	if err := cx.pool.QueryRow(t.Context(), `SELECT count(*) FROM sessions WHERE parent_session_id=$1 AND archived_at IS NULL`, cx.cparent).Scan(&count); err != nil || count != 0 {
		t.Fatalf("left %d workers: %v", count, err)
	}
	body["prompt"] = "different"
	status, _, _ := cx.postChild(cx.cparent, cx.cparentTk, body, key)
	if status != 409 {
		t.Fatalf("changed request replayed refusal: %d", status)
	}
}

func TestCoreCredentialRefusalIsTerminal(t *testing.T) {
	for _, status := range []int{401, 403} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte("fixture-private-core-body"))
		}))
		_, found, err := fetchCoreCredential(t.Context(), srv.Client(), srv.URL, "fixture-component-key", "fixture-owner", "github", coreProof{SessionID: "fixture-proof"})
		srv.Close()
		if found || !errors.Is(err, errPersonalCredentialUnavailable) || clusterRefusal(err) == nil || clusterStartFailureStatus(err) != 422 {
			t.Fatalf("status %d lost terminal cause: %v", status, err)
		}
		if strings.Contains(err.Error(), "fixture-private") {
			t.Fatal("upstream body escaped")
		}
	}
}

// The reported desktop Main -> cluster card path must reach repository checks.
// The personal GitHub credential is deliberately unusable: workers use their
// independent read-only token, and layout authorization precedes either token.
func TestDesktopWorkspaceClusterCardReachesRepositoryCheck(t *testing.T) {
	cx := newClusterDelegFx(t)
	cx.clusterRule("codex")
	if _, err := cx.pool.Exec(t.Context(), `UPDATE sessions SET runtime='daemon' WHERE id=$1`, cx.parent); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSessionLaunchAuthSession(t.Context(), cx.pool, cx.parent, clusterParentSID); err != nil {
		t.Fatal(err)
	}
	m := hostClusterParentManifest("owner-1")
	m.Kind = "repo"
	m.Members = m.Members[:1]
	m.Members[0].Path = "."
	m.Digest = "fixture-host-digest"
	if err := db.InsertSessionWorkspace(t.Context(), cx.pool, cx.parent, m); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 40)
	jm := cx.hub.JobManager()
	jm.historyPool = cx.pool
	jm.baseBranchRemote = func(_ context.Context, _, _, token string) (string, credentialRefusal, error) {
		if token != "fixture-worker-readonly" {
			t.Fatal("base check used a personal token")
		}
		return sha, credentialOK, nil
	}
	cx.api.resolveBaseRef = func(context.Context, string, *DaemonConn, string, string, string, string) (string, error) {
		return sha, nil
	}
	cx.api.baseSHAPushed = func(context.Context, string, string, string, string, string) (bool, int, bool, error) {
		return true, 0, false, nil
	}
	cx.k8s.credentialFetchResponses["owner-1:github"] = []byte("fixture-rejected-personal-token")
	cx.k8s.credentialFetchResponses["owner-1:"+readOnlyGitCredentialKind("github", "example-org")] = []byte("fixture-worker-readonly")
	checks := 0
	jm.repositoryRead = func(_ context.Context, _ string, token string) error {
		checks++
		if token != "fixture-worker-readonly" {
			t.Fatal("worker did not use its independent token")
		}
		return &clusterAuthorizationRefusal{code: "repository_not_visible"}
	}
	status, out, _ := cx.postChild(cx.parent, cx.parentTok, map[string]any{"engine": "codex", "kind": "card", "runtime": "cluster", "base": "alpha/0.6.0", "repo": "app", "prompt": "fixture card"}, newUUID())
	if status != 422 || checks != 1 || !strings.Contains(asString(out["error"]), "not visible") {
		t.Fatalf("desktop cluster card: status=%d checks=%d reply=%v", status, checks, out)
	}
}

func TestHostClusterLayoutProjectionGuards(t *testing.T) {
	parent := hostClusterParentManifest("fixture-owner")
	parent.Digest = "fixture-parent-digest"
	child := parent
	child.Runtime = "cluster"
	child.LaunchGeneration = "fixture-child"
	child.Members = append([]protocol.WorkspaceMember(nil), parent.Members[:1]...)
	child.Members[0].Destination = child.Members[0].Project
	// Unselected local-only members stay outside the remote projection.
	child.Kind = "repo"
	if err := validateHostClusterLayoutChild(parent, child); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*protocol.WorkspaceManifest){
		func(m *protocol.WorkspaceManifest) { m.OwnerID = "another-owner" },
		func(m *protocol.WorkspaceManifest) { m.Version = 0 },
		func(m *protocol.WorkspaceManifest) { m.Digest = "" },
		func(m *protocol.WorkspaceManifest) { m.Runtime = "host" },
		func(m *protocol.WorkspaceManifest) { m.Layout = protocol.WorkspaceLayoutStructure },
		func(m *protocol.WorkspaceManifest) { m.Members[0].ID = "unselected-member" },
		func(m *protocol.WorkspaceManifest) { m.Members[0].CommonDirectoryIdentity = "another-common-directory" },
		func(m *protocol.WorkspaceManifest) { m.LaunchGeneration = parent.LaunchGeneration },
		func(m *protocol.WorkspaceManifest) { m.Members[0].SourceIdentity = "changed" },
		func(m *protocol.WorkspaceManifest) { m.Members[0].Destination = "another/repo" },
		func(m *protocol.WorkspaceManifest) { m.Members[0].Project = "another/repo" },
		func(m *protocol.WorkspaceManifest) { m.Members[0].Provider = "gitlab" },
		func(m *protocol.WorkspaceManifest) { m.Members[0].Path = "elsewhere" },
	} {
		bad := child
		bad.Members = append([]protocol.WorkspaceMember(nil), child.Members...)
		mutate(&bad)
		err := validateHostClusterLayoutChild(parent, bad)
		if clusterRefusal(err) == nil || clusterRefusal(err).code != "workspace_layout_parent_binding_refused" {
			t.Fatalf("changed selection accepted: %v", err)
		}
	}
}

func TestClusterWorkspaceEarlyRefusalsNameTheCheck(t *testing.T) {
	f, base := nativeFixture(t)
	plan := clusterPlanFixture()
	plan.Manifest.OwnerID = base.SpawningAccountID
	base.Workspace = &plan.Manifest
	base.AuthSessionID = "fixture-proof"
	for _, tc := range []struct {
		code   string
		mutate func(*JobManager, *SessionJobSpec)
	}{
		{"invalid_session_id", func(_ *JobManager, s *SessionJobSpec) { s.SessionID = "invalid" }},
		{"workspace_database_unavailable", func(j *JobManager, _ *SessionJobSpec) { j.historyPool = nil }},
		{"workspace_manifest_missing", func(_ *JobManager, s *SessionJobSpec) { s.Workspace = nil }},
		{"workspace_repo_mode_conflict", func(_ *JobManager, s *SessionJobSpec) { s.NoRepo = true }},
		{"workspace_legacy_repo_conflict", func(_ *JobManager, s *SessionJobSpec) { s.Repo = "org/app" }},
		{"workspace_legacy_url_conflict", func(_ *JobManager, s *SessionJobSpec) { s.GitURL = "https://github.com/org/app" }},
		{"workspace_git_token_override", func(_ *JobManager, s *SessionJobSpec) {
			s.ExtraEnv = map[string]string{gitTokenKey: "fixture-private-token"}
		}},
		{"workspace_plan_override", func(_ *JobManager, s *SessionJobSpec) {
			s.ExtraEnv = map[string]string{clusterworkspace.Env: "fixture-private-plan"}
		}},
		{"workspace_manifest_not_saved", func(_ *JobManager, _ *SessionJobSpec) {}},
	} {
		t.Run(tc.code, func(t *testing.T) {
			jm := newTestJobManager(t, f.fake)
			jm.historyPool = f.pool
			s := base
			tc.mutate(jm, &s)
			err := jm.withClusterWorkspace(s)
			refusal := clusterRefusal(err)
			if refusal == nil || refusal.code != tc.code {
				t.Fatalf("want %s: %v", tc.code, err)
			}
			if !errors.Is(err, errClusterWorkspace) {
				t.Fatal("legacy error identity lost")
			}
			if got := launchDiagnosticReason(stage("authorization", "", protocol.StageStateFailed, err.Error())); got != tc.code {
				t.Fatalf("log cause=%s", got)
			}
			w := httptest.NewRecorder()
			writeAPIError(w, apiErrorCause(422, err, "%s", err.Error()))
			if !strings.Contains(w.Body.String(), tc.code) || strings.Contains(w.Body.String(), "fixture-private") {
				t.Fatal("unsafe or missing HTTP cause")
			}
		})
	}
}

func TestHostClusterProjectionPreservesLayoutAndNonRootPaths(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*protocol.WorkspaceManifest)
	}{
		{"structure layout", func(m *protocol.WorkspaceManifest) { m.Layout = protocol.WorkspaceLayoutStructure }},
		{"legacy layout", func(m *protocol.WorkspaceManifest) { m.Layout = "" }},
		{"non-root path", func(m *protocol.WorkspaceManifest) { m.Members[0].Path = "nested/app" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := hostClusterParentManifest("fixture-owner")
			parent.Digest = "fixture-parent"
			child := parent
			child.Runtime = "cluster"
			child.LaunchGeneration = "fixture-child"
			child.Members = append([]protocol.WorkspaceMember(nil), parent.Members[:1]...)
			child.Kind = "repo"
			child.Members[0].Destination = child.Members[0].Project
			tc.mutate(&parent)
			if err := validateHostClusterLayoutChild(parent, child); err == nil {
				t.Fatal("projection relaxed the original layout or non-root path guard")
			}
		})
	}
}

func TestHostClusterChildWorkspaceRefusesUnsafeProjectedPath(t *testing.T) {
	f, spec := nativeFixture(t)
	jm := f.api.hub.JobManager()
	checks := 0
	jm.baseBranchRemote = func(context.Context, string, string, string) (string, credentialRefusal, error) {
		checks++
		return "", credentialMissing, nil
	}
	f.api.resolveBaseRef = func(context.Context, string, *DaemonConn, string, string, string, string) (string, error) {
		return strings.Repeat("a", 40), nil
	}
	f.api.baseSHAPushed = func(context.Context, string, string, string, string, string) (bool, int, bool, error) {
		return true, 0, false, nil
	}
	parent, err := db.GetSession(t.Context(), f.pool, spec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	manifest := hostClusterParentManifest(spec.SpawningAccountID)
	manifest.Members[0].ID = "unsafe/member"
	manifest.Members[0].Name = "lost+found"
	req := hostClusterReq(spec.SpawningAccountID, "alpha/0.6.0", "unsafe/member")
	_, _, apiErr := f.api.hostClusterChildWorkspace(t.Context(), parent, req, "fixture-child", manifest, coreProof{SessionID: "fixture-proof"})
	refusal := clusterRefusal(apiErr)
	if refusal == nil || refusal.code != "workspace_member_paths_invalid" || checks != 0 {
		t.Fatalf("missing early named path refusal: checks=%d error=%v", checks, apiErr)
	}
}

func TestHostClusterMemberPathProjectionRefusesMalformedResults(t *testing.T) {
	parent := hostClusterParentManifest("fixture-owner")
	for _, tc := range []struct {
		name     string
		paths    []string
		fallback bool
	}{
		{name: "nil"}, {name: "short", paths: []string{"app"}},
		{name: "long", paths: []string{"app", "local-only", "extra"}},
		{name: "empty", paths: []string{"app", ""}},
		{name: "root", paths: []string{"app", "."}},
		{name: "nested", paths: []string{"app", "nested/repo"}},
		{name: "duplicate", paths: []string{"app", "APP"}},
		{name: "reserved", paths: []string{"app", "lost+found"}},
		{name: "unexpected fallback", paths: []string{"app", "local-only"}, fallback: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths, err := projectHostClusterMemberPaths(parent, func(protocol.DiscoveryResolved, ...workspaceselection.MemberPathOptions) ([]string, bool) {
				return tc.paths, tc.fallback
			})
			refusal := clusterRefusal(err)
			if paths != nil || refusal == nil || refusal.code != "workspace_member_paths_invalid" {
				t.Fatalf("unsafe projection accepted: paths=%v error=%v", paths, err)
			}
		})
	}
}

func TestHostClusterProjectionAllowsOnlyRootPathConversion(t *testing.T) {
	for _, layout := range []protocol.WorkspaceLayout{protocol.WorkspaceLayoutNames, ""} {
		parent := hostClusterParentManifest("fixture-owner")
		parent.Digest = "fixture-parent"
		parent.Layout = layout
		parent.Members = parent.Members[:1]
		parent.Kind = "repo"
		parent.Members[0].Path = "."
		child := parent
		child.Runtime = "cluster"
		child.LaunchGeneration = "fixture-child"
		child.Members = append([]protocol.WorkspaceMember(nil), parent.Members...)
		child.Members[0].Destination = child.Members[0].Project
		child.Members[0].Path = child.Members[0].Name
		if layout == "" {
			child.Members[0].Path = child.Members[0].ID
		}
		if err := validateHostClusterLayoutChild(parent, child); err != nil {
			t.Fatalf("layout %q root projection refused: %v", layout, err)
		}
	}
}
