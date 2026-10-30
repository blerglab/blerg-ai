//go:build linux

package daemon

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/discovery"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

type childAdmissionAuthority struct {
	hostExplorerAuthority
	inspect func()
}

func (a *childAdmissionAuthority) Launch(context.Context, string, protocol.WorkspaceManifest, string) (protocol.SupervisorLaunch, error) {
	a.inspect()
	return protocol.SupervisorLaunch{}, errors.New("test stops before engine admission")
}

func hostChildSelectionFixture(t *testing.T) (*discovery.Service, workspacePrivateManifest, string) {
	t.Helper()
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	root := t.TempDir()
	repo := filepath.Join(root, "app")
	initChildRepo(t, repo)
	selection := discovery.New(func() string { return root })
	page := selection.Handle(t.Context(), protocol.DiscoverWorkspace{RequestID: "test", Operation: "list", Runtime: "host", Path: "."})
	if !page.OK {
		t.Fatal(page)
	}
	entry := page.Page.Entries[0]
	resolved := selection.Handle(t.Context(), protocol.DiscoverWorkspace{RequestID: "test", Operation: "resolve", Runtime: "host", RootIdentity: page.Page.RootIdentity, Members: []protocol.SelectionRef{{Path: entry.Path, SourceIdentity: entry.SourceIdentity}}})
	if !resolved.OK {
		t.Fatal(resolved)
	}
	chosen := resolved.Resolved
	paths, _ := workspaceselection.MemberPaths(*chosen)
	source := chosen.Members[0]
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "repo", OwnerID: "owner", Runtime: "host", LaunchGeneration: "parent-launch", Root: chosen.Root, Members: []protocol.WorkspaceMember{{ID: source.ID, Name: source.Name, Path: paths[0], SourceIdentity: source.SourceIdentity, CommonDirectoryIdentity: source.CommonDirectoryIdentity, Destination: source.SourceIdentity}}}
	manifest.Digest = workspaceselection.Digest(manifest)
	if r := selection.Handle(t.Context(), protocol.DiscoverWorkspace{RequestID: "test", Operation: "bind", Runtime: "host", SelectionIdentity: chosen.SelectionIdentity, Manifest: &manifest}); !r.OK {
		t.Fatal(r)
	}
	pins, err := selection.PinBound(t.Context(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	defer pins.Close()
	record, _ := workspaceRecord("parent")
	parent := &LocalWorkspace{record: record, state: workspacePrivateManifest{Host: true, Session: "parent", Launch: manifest}}
	for _, p := range pins.Sources {
		st, _ := p.Directory.Stat()
		common, _ := p.CommonDirectory.Stat()
		parent.state.Sources = append(parent.state.Sources, WorkspaceSource{Member: p.Member, Path: p.CanonicalPath, CommonPath: p.CommonDirectoryPath, RootIdentity: workspaceRootIdentity(st), CommonIdentity: workspaceRootIdentity(common)})
	}
	if err := parent.save(); err != nil {
		t.Fatal(err)
	}
	return selection, parent.state, repo
}

func TestHostChildAdmissionIsolatesParent(t *testing.T) {
	selection, parent, repo := hostChildSelectionFixture(t)
	manifest := parent.Launch

	child := manifest
	child.LaunchGeneration = "child-launch"
	child.Digest = workspaceselection.Digest(child)
	if r := selection.Handle(t.Context(), protocol.DiscoverWorkspace{RequestID: "test", Operation: "bind_child", Runtime: "host", ParentLaunchGeneration: manifest.LaunchGeneration, Manifest: &child}); !r.OK {
		t.Fatal(r)
	}
	authority := &childAdmissionAuthority{}
	service := hostLifecycleTestService(t, authority)
	service.Resolve = func(context.Context, protocol.SpawnSession) ([]WorkspaceSource, string, error) {
		return nil, "parent", nil
	}
	service.AuthorizeChild = func(context.Context, string, string) error { return nil }
	service.TrustedLaunch = func(context.Context, string) (protocol.WorkspaceManifest, error) { return manifest, nil }
	authority.inspect = func() {
		record, _ := workspaceRecord("child")
		state, err := readPrivateWorkspace(record)
		if err != nil {
			t.Fatal(err)
		}
		if state.Sources[0].Path == repo {
			t.Fatal("host child was handed the parent's checkout")
		}
		if got := childRepoGit(t, state.Sources[0].Path, "branch", "--show-current"); got != "wip/child" {
			t.Fatal(got)
		}
	}
	manager := newManagerWithSender(newRecordingSender(8), ManagerConfig{ReposRoot: filepath.Dir(repo), Workspaces: service})
	manager.discovery = selection
	_, _, err := manager.prepareHostWorkspace(t.Context(), protocol.SpawnSession{SessionID: "child", Kind: "agent", Workspace: &child, WorkspaceCapabilities: protocol.WorkspaceCapabilitiesFor("host", "linux")})
	if err == nil {
		t.Fatal("expected test authority refusal")
	}
}
