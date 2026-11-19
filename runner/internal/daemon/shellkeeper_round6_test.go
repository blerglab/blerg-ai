package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

func TestKeeperOfflineHostWorkspaceSyncRebindAndStop(t *testing.T) {
	svc, w, _, l, head := round7HostWorkspace(t)
	registry := svc.Supervisor.(*workspaceSupervisors)
	runOfflineHostLifecycle(t, svc, registry, w, l, head)
}

func round7HostWorkspace(t *testing.T) (*LocalWorkspaceService, *LocalWorkspace, *hostWorkspace, protocol.SupervisorLaunch, string) {
	t.Helper()
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	authority := &hostExplorerAuthority{}
	svc := hostLifecycleTestService(t, authority)
	registry := svc.Supervisor.(*workspaceSupervisors)
	registry.service.SetHostBridge(shellruntime.RefusedHostBridge())
	m := legacyExplorerStart("keeper-offline").Manifest
	m.Runtime = "host"
	l, err := authority.Launch(t.Context(), "keeper-offline", m, "token")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	initChildRepo(t, root)
	childRepoGit(t, root, "commit", "--allow-empty", "-qm", "offline agent turn")
	head := childRepoGit(t, root, "rev-parse", "HEAD")
	stat, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: l.LaunchGeneration, Generation: l.RuntimeGeneration}, ManifestDigest: m.Digest, Root: workspaceRootIdentity(stat)}
	if err = authority.Bind(t.Context(), l, b); err != nil {
		t.Fatal(err)
	}
	w := &LocalWorkspace{mu: new(sync.Mutex), record: filepath.Join(t.TempDir(), "workspace.json"), state: workspacePrivateManifest{Version: privateWorkspaceVersion, Host: true, HostDriverStopped: true, HostWorkDir: root, Session: l.SessionID, Launch: m, Binding: &b, SupervisorLaunch: l, RuntimeToken: "token"}}
	common, err := os.Stat(filepath.Join(root, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	w.state.Sources = []WorkspaceSource{{Member: m.Members[0], Path: root, CommonPath: filepath.Join(root, ".git"), RootIdentity: b.Root, CommonIdentity: workspaceRootIdentity(common)}}
	w.state.Members = []workspacePrivateMember{{Member: m.Members[0], Result: protocol.WorkspaceMemberResult{WorkspaceMemberRef: protocol.WorkspaceMemberRef{MemberID: m.Members[0].ID, LaunchGeneration: m.LaunchGeneration}}}}
	w.authorize = func(context.Context) error { return nil }
	h := &hostWorkspace{w: w, service: svc, writers: &countingWriters{}}
	w.host = h
	if err = w.save(); err != nil {
		t.Fatal(err)
	}
	if err = registry.registerHost(t.Context(), h); err != nil {
		t.Fatal(err)
	}
	return svc, w, h, l, head
}

func runOfflineHostLifecycle(t *testing.T, svc *LocalWorkspaceService, registry *workspaceSupervisors, w *LocalWorkspace, l protocol.SupervisorLaunch, head string) {
	t.Helper()
	cancelled, resumed := 0, 0
	svc.CancelStatus = func(context.Context, string) error { cancelled++; return nil }
	svc.ResumeStatus = func(context.Context, string) error { resumed++; return nil }
	results, err := w.Sync(t.Context())
	if err != nil {
		t.Fatal("keeper outage blocked turn-end sync", err)
	}
	if len(results) != 1 || results[0].Head != head || results[0].Pending || results[0].SyncError != "" {
		t.Fatal("offline turn commits were not collected", results)
	}
	if cancelled != 1 || resumed != 1 {
		t.Fatal("status remained cancelled", cancelled, resumed)
	}
	c, err := registry.peer(l.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	next := l
	next.ConnectionID = "new-server-connection"
	if err = c.rebind(t.Context(), next); err != nil {
		t.Fatal("keeper outage blocked server reconnect", err)
	}
	w.state.SupervisorLaunch = next
	if err = w.save(); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Stop(t.Context()); err != nil {
		t.Fatal("keeper outage retained unresolved agent work", err)
	}
	if !w.state.Stopped || !c.retired {
		t.Fatal("host stop did not finish independently")
	}
	if err = registry.service.HostCustodyReady(t.Context(), l.SessionID); err == nil {
		t.Fatal("offline agent lifecycle admitted shells")
	}
}
