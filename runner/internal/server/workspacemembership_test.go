package server

import (
	"context"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestBoundedLocalFilesKeepsARowReadable(t *testing.T) {
	names := make([]string, protocol.MaxWorkspaceLocalNames+5)
	names[0] = "bad\x1b[31m\nname\xff"
	names[1] = strings.Repeat("x", 500)
	got := boundedLocalFiles(protocol.WorkspaceLocalFiles{Names: names, Count: 900, Digest: "d"})
	if len(got.Names) != protocol.MaxWorkspaceLocalNames || got.Count != 900 || got.Digest != "d" {
		t.Fatalf("%+v", got)
	}
	if got.Names[0] != "bad[31mname" || len([]rune(got.Names[1])) != 200 {
		t.Fatalf("%q", got.Names[:2])
	}
}

func TestMembershipURLIsAConnectedHostApprovalWithoutDiscovery(t *testing.T) {
	f := newLiveFx(t)
	member := protocol.WorkspaceMember{ID: "api", Name: "API", Path: "api", SourceIdentity: "api-source", CommonDirectoryIdentity: "api-common", Destination: "api-source"}
	m := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: topicOwner, Runtime: "host", LaunchGeneration: "launch", Root: protocol.WorkspaceRoot{Device: 1, Inode: 2}, Members: []protocol.WorkspaceMember{member}}
	m.Digest = workspaceselection.Digest(m)
	row, err := db.GetSession(t.Context(), f.pool, f.sess)
	if err != nil {
		t.Fatal(err)
	}
	c := &membershipContext{caller: delegationCaller{SessionID: f.sess}, row: row, m: m}
	body := membershipChangeBody{OperationID: newUUID(), Kind: "add", Repository: "https://github.com/example-org/example-repo.git"}
	f.api.userRepos = NewUserRepoLister(gitprovider.Default, func(context.Context, string) ([]string, bool) { return []string{"github"}, true }, nil)
	op, problem := f.api.buildMembership(t.Context(), c, body)
	if problem != nil {
		t.Fatal(problem)
	}
	if op.Phase != db.MembershipRequested || op.MemberName != "github.com/example-org/example-repo" || op.Source.GitURL != body.Repository {
		t.Fatalf("%+v", op)
	}
	// No daemon is connected: this success also proves that requesting a URL
	// neither performs discovery nor starts a clone.
	for _, raw := range []string{"https://secret@github.com/example-org/example-repo", "ssh://git:secret@github.com/example-org/example-repo", "ext::command", "-oProxyCommand=evil@github.com:example-org/example-repo"} {
		body.Repository = raw
		if _, problem := f.api.buildMembership(t.Context(), c, body); problem == nil || strings.Contains(problem.Message, "secret") {
			t.Fatalf("unsafe input %q: %v", raw, problem)
		}
	}
	body.Repository = "https://gitlab.com/example-org/example-repo.git"
	if _, problem := f.api.buildMembership(t.Context(), c, body); problem == nil || !strings.Contains(problem.Message, "Connect this Git host") {
		t.Fatal(problem)
	}
	for _, local := range []string{"local@repo", "git@local", "work:2026/repo"} {
		body.Repository = local
		if _, problem := f.api.buildMembership(t.Context(), c, body); problem == nil || problem.Message != msgMembershipOffline {
			t.Fatalf("local path %q was treated as a Git URL: %v", local, problem)
		}
	}
}

func TestRepositoryRequestAttentionSurvivesReloadAndDecision(t *testing.T) {
	for _, allow := range []bool{true, false} {
		t.Run(strconv.FormatBool(allow), func(t *testing.T) { testRepositoryRequestAttention(t, allow) })
	}
}

func TestRepositoryCloneRefusalProjection(t *testing.T) {
	op := &db.MembershipOperation{ID: "op", RequestedBy: "main", Phase: db.MembershipRefused, Source: protocol.SelectionRef{GitURL: "https://github.com/example-org/example-repo.git"}, Refusal: "Could not clone this repository."}
	if got := repositoryRequest(op); got != nil {
		t.Fatal(got)
	}
	op.Source.GitURL = ""
	if got := repositoryRequest(op); got != nil {
		t.Fatal("unrelated refusal projected as clone approval", got)
	}
	op.Source.GitURL = "https://github.com/example-org/example-repo.git"
	op.RequestedBy = "person"
	if got := repositoryRequest(op); got != nil {
		t.Fatal("person operation projected as Main approval", got)
	}
}

func TestMembershipClonePersonalTokenBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, runtime                                   string
		private, known, hostAllowed, wantToken, refused bool
	}{
		{"public host", "host", false, true, false, false, false},
		{"public sandbox", "sandbox", false, true, false, false, false},
		{"private host", "host", true, true, false, false, true},
		{"private opted-in host", "host", true, true, true, true, false},
		{"private sandbox", "sandbox", true, true, false, true, false},
		{"unknown host", "host", false, false, false, false, false},
		{"unknown sandbox", "sandbox", false, false, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, dc := &API{}, &DaemonConn{}
			dc.SetAllowHostCredentialClone(tc.hostAllowed)
			a.repoVisibility = func(context.Context, gitprovider.Provider, string, gitprovider.Ref) (bool, bool) {
				return tc.private, tc.known
			}
			token, reason := a.membershipCloneToken(t.Context(), dc, tc.runtime, gitprovider.Ref{Provider: "github", Owner: "example-org", Name: "example-repo"}, []byte("fixture-token\n"))
			if (token != "") != tc.wantToken || (reason != "") != tc.refused {
				t.Fatalf("token present=%v reason=%q", token != "", reason)
			}
		})
	}
}

func TestMembershipCloneCannotAdoptAReplacedSource(t *testing.T) {
	ref := gitprovider.Ref{Provider: "github", Owner: "example-org", Name: "example-repo"}
	for _, change := range []string{"", "origin", "provider", "missing origin", "source", "member", "common", "project"} {
		t.Run(change, func(t *testing.T) {
			source := protocol.DiscoverySource{ID: "member", SourceIdentity: "source", CommonDirectoryIdentity: "common", Origin: &protocol.RepoOrigin{Provider: ref.Provider, FullName: ref.FullName()}}
			op := &db.MembershipOperation{MemberID: "member", Source: protocol.SelectionRef{SourceIdentity: "source"}, Proposed: protocol.WorkspaceManifest{Members: []protocol.WorkspaceMember{{ID: "member", CommonDirectoryIdentity: "common", Provider: ref.Provider, Project: ref.FullName()}}}}
			switch change {
			case "origin":
				source.Origin.FullName = "other-org/other-repo"
			case "provider":
				source.Origin.Provider = "gitlab"
			case "missing origin":
				source.Origin = nil
			case "source":
				op.Source.SourceIdentity = "replacement"
			case "member":
				op.MemberID = "replacement"
			case "common":
				op.Proposed.Members[0].CommonDirectoryIdentity = "replacement"
			case "project":
				op.Proposed.Members[0].Project = "other-org/other-repo"
			}
			if got := membershipCloneMatches(ref, source, op); got != (change == "") {
				t.Fatalf("accepted=%v", got)
			}
		})
	}
}

func testRepositoryRequestAttention(t *testing.T, allow bool) {
	fx := newLiveFx(t)
	ctx := t.Context()
	if _, err := fx.pool.Exec(ctx, `UPDATE sessions SET runtime='daemon' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	member := protocol.WorkspaceMember{ID: "api", Name: "API", Path: "api", SourceIdentity: "api-source", CommonDirectoryIdentity: "api-common", Destination: "api-source"}
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: topicOwner, Runtime: "host", LaunchGeneration: "launch", Root: protocol.WorkspaceRoot{Device: 1, Inode: 2}, Members: []protocol.WorkspaceMember{member}}
	manifest.Digest = workspaceselection.Digest(manifest)
	if err := db.InsertSessionWorkspace(ctx, fx.pool, fx.sess, manifest); err != nil {
		t.Fatal(err)
	}
	next := manifest
	next.Revision++
	next.Members = append(append([]protocol.WorkspaceMember{}, manifest.Members...), protocol.WorkspaceMember{ID: "web", Name: "Web", Path: "web", SourceIdentity: "web-source", CommonDirectoryIdentity: "web-common", Destination: "web-source"})
	next.Digest = workspaceselection.Digest(next)
	op := db.MembershipOperation{ID: newUUID(), SessionID: fx.sess, Kind: "add", Phase: db.MembershipRequested, RequestedBy: "main", Principal: fx.sess, ExpectedRevision: manifest.Revision, OldDigest: manifest.Digest, Proposed: next, MemberID: "web", MemberName: "Web"}
	if _, fresh, err := db.CreateMembershipOperation(ctx, fx.pool, op); err != nil || !fresh {
		t.Fatal(fresh, err)
	}
	if _, fresh, err := db.CreateMembershipOperation(ctx, fx.pool, op); err != nil || fresh {
		t.Fatal("retry must not send another attention push", fresh, err)
	}
	owner := fx.token(topicOwner, "human")
	first := fx.getLive(fx.sess, owner, "")
	if first.Code != 200 || decodeLive(t, first).Counts.NeedsYou != 1 {
		t.Fatalf("pending: %d %s", first.Code, first.Body)
	}
	if got := decodeLive(t, fx.getLive(fx.sess, fx.token(topicOther, "human"), "")).Counts.NeedsYou; got != 0 {
		t.Fatalf("foreign count %d", got)
	}
	stored, err := db.OpenMembershipOperation(ctx, fx.pool, fx.sess)
	if err != nil || repositoryRequest(stored) == nil {
		t.Fatal(stored, err)
	}
	restarted := NewAPI(NewHub(), fx.pool, topicDaemonToken, nil, "")
	restarted.coreAuth = fx.api.coreAuth
	mux := http.NewServeMux()
	restarted.RegisterLiveRoutes(mux)
	restarted.RegisterWorkspaceMembershipRoutes(mux)
	fx.api, fx.mux = restarted, mux
	if got := decodeLive(t, fx.getLive(fx.sess, owner, "")).Counts.NeedsYou; got != 1 {
		t.Fatalf("restart count %d", got)
	}
	req := httptest.NewRequest("POST", "/api/sessions/"+fx.sess+"/workspace/repositories/"+op.ID+"/decide", strings.NewReader(`{"allow":`+strconv.FormatBool(allow)+`,"proposed_digest":"`+next.Digest+`"}`))
	req.Header.Set("Authorization", "Bearer "+owner)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("decide: %d %s", rec.Code, rec.Body)
	}
	resolved := fx.getLive(fx.sess, owner, first.Header().Get("ETag"))
	if resolved.Code != 200 || decodeLive(t, resolved).Counts.NeedsYou != 0 {
		t.Fatalf("resolved: %d %s", resolved.Code, resolved.Body)
	}
}
