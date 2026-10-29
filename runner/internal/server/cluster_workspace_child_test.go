package server

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestClusterWorkspaceChildSelectionBeforeApproval(t *testing.T) {
	p := clusterPlanFixture()
	for _, tc := range []struct {
		body string
		want string
	}{
		{`{"repo":"two","prompt":"p"}`, "two"},
		{`{"repo":"same-name-2","prompt":"p"}`, "two"},
		{`{"repo":"same-name","prompt":"p"}`, "one"},
		{`{"repos":["two","one"],"prompt":"p"}`, "one, two"},
		{`{"all_repos":true,"prompt":"p"}`, "one, two"},
	} {
		cs, _, err := parseChildStart([]byte(tc.body), childHost{cluster: true, workspace: &p.Manifest})
		if err != nil || childSelectionLabel(cs) != tc.want || cs.WorkspaceLaunch != p.Manifest.LaunchGeneration || cs.Repo != "" {
			t.Fatalf("selection: %+v %v", cs, err)
		}
		req := childRunnerRequest(&db.SessionRow{ID: "parent"}, cs, "request:approved")
		if strings.Join(req.WorkspaceRepos, ", ") != tc.want || req.WorkspaceLaunch != cs.WorkspaceLaunch {
			t.Fatal("lost approved selection")
		}
	}
	for _, body := range []string{
		`{"repos":["one","one"],"prompt":"p"}`,
		`{"repos":["one","foreign"],"prompt":"p"}`,
		`{"all_repos":true,"repo":"one","prompt":"p"}`,
		`{"no_repo":true,"prompt":"p"}`,
		`{"all_repos":true,"runtime":"daemon","prompt":"p"}`,
		`{"all_repos":true,"workspace_launch":"forged","prompt":"p"}`,
	} {
		if _, _, err := parseChildStart([]byte(body), childHost{cluster: true, workspace: &p.Manifest}); err == nil {
			t.Fatal("accepted", body)
		}
	}
}

func TestClusterWorkspaceChildDispatchRechecksParent(t *testing.T) {
	f, spec := nativeFixture(t)
	ctx := context.Background()
	p := clusterPlanFixture()
	p.Manifest.OwnerID = spec.SpawningAccountID
	if err := db.InsertSessionWorkspace(ctx, f.pool, spec.SessionID, p.Manifest); err != nil {
		t.Fatal(err)
	}
	contexts := slices.Clone(p.Members)
	for i := range contexts {
		contexts[i].Token = ""
	}
	raw, _ := json.Marshal(contexts)
	if _, err := f.pool.Exec(ctx, `INSERT INTO cluster_workspace_storage(session_id,launch_generation,claim_name,claim_uid,contexts,owner_id) VALUES($1,$2,'claim','uid',$3,$4)`, spec.SessionID, p.Manifest.LaunchGeneration, raw, spec.SpawningAccountID); err != nil {
		t.Fatal(err)
	}
	parent, err := db.GetSession(ctx, f.pool, spec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	req := runnerStartRequest{WorkspaceRepos: []string{"two"}, WorkspaceLaunch: p.Manifest.LaunchGeneration, Runtime: "cluster"}
	f.api.SetWorkspaceClusterConsumer(&workspaceClusterFake{})
	child, members, apiErr := f.api.clusterChildWorkspace(ctx, parent, req, "child")
	if apiErr != nil || child == nil || len(child.Members) != 1 || child.Members[0].ID != "two" || child.Kind != "repo" || child.LaunchGeneration == p.Manifest.LaunchGeneration || child.Root.Inode != 0 || len(members) != 1 || members[0].Token != "" {
		t.Fatalf("child binding: %+v %+v %v", child, members, apiErr)
	}
	req.WorkspaceLaunch = "stale"
	if _, _, e := f.api.clusterChildWorkspace(ctx, parent, req, "child"); e == nil {
		t.Fatal("stale approval dispatched")
	}
	req.WorkspaceLaunch = p.Manifest.LaunchGeneration
	req.WorkspaceRepos = []string{"foreign"}
	if _, _, e := f.api.clusterChildWorkspace(ctx, parent, req, "child"); e == nil {
		t.Fatal("foreign member dispatched")
	}
}

func TestReviewedClusterChildLayoutContextFromImmutableAncestor(t *testing.T) {
	f, spec := nativeFixture(t)
	p := clusterPlanFixture()
	p.Manifest.OwnerID = spec.SpawningAccountID
	p.Manifest.Layout = protocol.WorkspaceLayoutNames
	if err := db.InsertSessionWorkspace(t.Context(), f.pool, spec.SessionID, p.Manifest); err != nil {
		t.Fatal(err)
	}

	parent, err := db.GetSession(t.Context(), f.pool, spec.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	childID := newUUID()
	if err := db.InsertSessionAs(t.Context(), f.pool, childID, parent.DaemonID, "starting", "", "", "", "", db.SessionOrigin{SpawningAccount: spec.SpawningAccountID, ParentSessionID: spec.SessionID}); err != nil {
		t.Fatal(err)
	}

	child := p.Manifest
	child.Kind = "repo"
	child.LaunchGeneration = "child-layout"
	child.Members = append([]protocol.WorkspaceMember(nil), p.Manifest.Members[1:]...)
	sources, err := clusterLayoutSources(t.Context(), f.pool, childID, child)
	if err != nil || len(sources) != 2 {
		t.Fatalf("lost original selection: %+v %v", sources, err)
	}
	plan := clusterworkspace.Plan{SessionID: childID, Manifest: child, Runtime: protocol.WorkspaceRuntimeRef{LaunchGeneration: child.LaunchGeneration, Generation: 1}, Members: append([]clusterworkspace.Member(nil), p.Members[1:]...), LayoutSources: sources}
	if err := plan.Validate(); err != nil {
		t.Fatal("cluster plan rejected approved child", err)
	}
	child.Members[0].Path = "."
	if _, err := clusterLayoutSources(t.Context(), f.pool, childID, child); err == nil {
		t.Fatal("child changed immutable path")
	}
}
