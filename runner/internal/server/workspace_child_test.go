package server

import (
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

func hostWorkspaceFixture() protocol.WorkspaceManifest {
	m := protocol.WorkspaceManifest{
		Version: protocol.WorkspaceVersion, Kind: "folder", OwnerID: "acct-1", Runtime: "host", LaunchGeneration: "launch-1",
		Members: []protocol.WorkspaceMember{
			{ID: "m1", Name: "app", Path: "app", SourceIdentity: "src-1", CommonDirectoryIdentity: "cd-1", Destination: "dest-1", Provider: "github", Project: "example-org/app"},
			{ID: "m2", Name: "lib", Path: "lib", SourceIdentity: "src-2", CommonDirectoryIdentity: "cd-2", Destination: "dest-2"},
		},
	}
	m.Digest = workspaceselection.Digest(m)
	return m
}

// BLG-460's original bug: a host parent's explicit --runtime cluster child must not be
// refused with "keep the parent's runtime" once this increment ships. This is the core
// relaxation the rest of the matrix builds on.
func TestSelectHostClusterChildSelectorsAllowsExplicitCluster(t *testing.T) {
	parent := hostWorkspaceFixture()
	cs := &childStart{Runtime: "cluster", Repo: "m1", Base: "alpha/0.6.0"}
	if err := selectHostClusterChildSelectors(cs, parent); err != nil {
		t.Fatalf("refused: %v", err)
	}
	if len(cs.Repos) != 1 || cs.Repos[0] != "m1" || cs.Repo != "" || cs.AllRepos || cs.WorkspaceLaunch != parent.LaunchGeneration {
		t.Fatalf("selection: %+v", cs)
	}
	if cs.Workspace != nil {
		t.Fatal("a host cluster child must not carry a Workspace manifest of its own")
	}
}

func TestSelectHostClusterChildSelectorsRequiresBase(t *testing.T) {
	parent := hostWorkspaceFixture()
	cs := &childStart{Runtime: "cluster", Repo: "m1"}
	if err := selectHostClusterChildSelectors(cs, parent); err == nil {
		t.Fatal("expected a refusal for a missing --base")
	}
}

// Round-4 Codex review (LOW): a leading '/' or an empty path segment is refused before
// --base ever reaches a path.Join, both here and in the daemon's own resolve_base
// (internal/discovery/baseref.go's validRefBranch, kept in sync with this one).
func TestSelectHostClusterChildSelectorsRefusesLeadingSlashAndEmptySegment(t *testing.T) {
	parent := hostWorkspaceFixture()
	for _, hostile := range []string{"/etc/passwd", "/alpha/0.6.0", "alpha//0.6.0", "alpha/0.6.0/", "/"} {
		cs := &childStart{Runtime: "cluster", Repo: "m1", Base: hostile}
		if err := selectHostClusterChildSelectors(cs, parent); err == nil {
			t.Fatalf("hostile base %q was accepted", hostile)
		}
	}
}

func TestSelectHostClusterChildSelectorsCanonicalOrder(t *testing.T) {
	parent := hostWorkspaceFixture()
	cs := &childStart{Runtime: "cluster", Repos: []string{"m2", "m1"}, Base: "alpha/0.6.0"}
	if err := selectHostClusterChildSelectors(cs, parent); err != nil {
		t.Fatalf("refused: %v", err)
	}
	if len(cs.Repos) != 2 || cs.Repos[0] != "m1" || cs.Repos[1] != "m2" {
		t.Fatalf("expected parent order, got %v", cs.Repos)
	}
}

func TestSelectHostClusterChildSelectorsAllRepos(t *testing.T) {
	parent := hostWorkspaceFixture()
	cs := &childStart{Runtime: "cluster", AllRepos: true, Base: "alpha/0.6.0"}
	if err := selectHostClusterChildSelectors(cs, parent); err != nil {
		t.Fatalf("refused: %v", err)
	}
	if len(cs.Repos) != 2 {
		t.Fatalf("expected both members, got %v", cs.Repos)
	}
}

func TestSelectHostClusterChildSelectorsRefusesNonHostParent(t *testing.T) {
	parent := hostWorkspaceFixture()
	parent.Runtime = "sandbox"
	cs := &childStart{Runtime: "cluster", Repo: "m1", Base: "alpha/0.6.0"}
	if err := selectHostClusterChildSelectors(cs, parent); err == nil {
		t.Fatal("expected a refusal for a non-host parent")
	}
}

func TestSelectHostClusterChildSelectorsRefusesUnmatchedSelector(t *testing.T) {
	parent := hostWorkspaceFixture()
	cs := &childStart{Runtime: "cluster", Repo: "does-not-exist", Base: "alpha/0.6.0"}
	if err := selectHostClusterChildSelectors(cs, parent); err == nil {
		t.Fatal("expected a refusal for an unmatched selector")
	}
}

func TestSelectHostClusterChildSelectorsRefusesEmptySelection(t *testing.T) {
	parent := hostWorkspaceFixture()
	cs := &childStart{Runtime: "cluster", Base: "alpha/0.6.0"}
	if err := selectHostClusterChildSelectors(cs, parent); err == nil {
		t.Fatal("expected a refusal for no --repo/--all-repos")
	}
}

// Row 23 regression (design §5/§7): this relaxation must only ever fire for parent.Runtime
// == "host" — it must never let a cluster-runtime parent through this path.
func TestSelectHostClusterChildSelectorsNeverFiresForClusterParent(t *testing.T) {
	parent := hostWorkspaceFixture()
	parent.Runtime = "cluster"
	cs := &childStart{Runtime: "cluster", Repo: "m1", Base: "alpha/0.6.0"}
	if err := selectHostClusterChildSelectors(cs, parent); err == nil {
		t.Fatal("expected a refusal: this function is for a host parent only")
	}
}
