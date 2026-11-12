package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/discovery"
	"github.com/blerglab/blerg-ai/runner/internal/gitprovider"
	"github.com/blerglab/blerg-ai/runner/internal/gitremote"
	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

func TestConnectedCloneDoesNotFallbackAndCleansFailure(t *testing.T) {
	root := t.TempDir()
	oldConnected, oldLegacy := cloneConnectedGit, cloneTargetGit
	t.Cleanup(func() { cloneConnectedGit, cloneTargetGit = oldConnected, oldLegacy })
	calls := 0
	cloneConnectedGit = func(_ context.Context, _ time.Duration, remote, dest string, _ []string) error {
		calls++
		if remote != "https://github.com/example-org/example-repo.git" {
			t.Fatal(remote)
		}
		if err := os.MkdirAll(dest, 0700); err != nil {
			t.Fatal(err)
		}
		return errors.New("fixture secret must not escape")
	}
	cloneTargetGit = func(context.Context, time.Duration, string, string, []string) error {
		t.Fatal("unapproved transport fallback")
		return nil
	}
	err := EnsureClonedTarget(t.Context(), root, CloneTarget{ConnectedHost: true, Provider: "github", FullName: "example-org/example-repo", Folder: "github-example-repo"})
	if err == nil || strings.Contains(err.Error(), "fixture secret") || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("partial clone left: %v %v", entries, err)
	}
}

func TestConnectedCloneInstallsOnlyCompleteDestination(t *testing.T) {
	root := t.TempDir()
	old := cloneConnectedGit
	t.Cleanup(func() { cloneConnectedGit = old })
	cloneConnectedGit = func(_ context.Context, _ time.Duration, _ string, dest string, _ []string) error {
		if _, err := os.Lstat(filepath.Join(root, "github-example-repo")); !os.IsNotExist(err) {
			t.Fatal("destination published before clone completed")
		}
		return os.MkdirAll(filepath.Join(dest, ".git"), 0700)
	}
	if err := EnsureClonedTarget(t.Context(), root, CloneTarget{ConnectedHost: true, Provider: "github", FullName: "example-org/example-repo", Folder: "github-example-repo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "github-example-repo", ".git")); err != nil {
		t.Fatal(err)
	}
}

func TestConnectedPrivateCloneUsesProtectedSandboxTransport(t *testing.T) {
	root := t.TempDir()
	oldConnected, oldLegacy, oldHost := cloneConnectedInSandbox, cloneInSandbox, cloneConnectedGit
	t.Cleanup(func() { cloneConnectedInSandbox, cloneInSandbox, cloneConnectedGit = oldConnected, oldLegacy, oldHost })
	called := false
	cloneConnectedInSandbox = func(_ context.Context, _ time.Duration, remote, tmp string, p gitprovider.Provider, token string) error {
		called = true
		if remote != "https://github.com/example-org/example-repo.git" || p.ID() != "github" || token != "fixture-token" {
			t.Fatal("wrong sandbox clone source or credential")
		}
		return os.MkdirAll(filepath.Join(tmp, "repo", ".git"), 0700)
	}
	cloneInSandbox = func(context.Context, time.Duration, string, string, gitprovider.Provider, string) error {
		t.Fatal("clone permits redirects")
		return nil
	}
	cloneConnectedGit = func(context.Context, time.Duration, string, string, []string) error {
		t.Fatal("sandbox token sent to host Git")
		return nil
	}
	if err := EnsureClonedTarget(t.Context(), root, CloneTarget{ConnectedHost: true, Sandbox: true, Token: "fixture-token", Provider: "github", FullName: "example-org/example-repo", Folder: "github-example-repo"}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("sandbox clone was not used")
	}
}

func TestConnectedCloneEnvironmentIsAllowlisted(t *testing.T) {
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "NO_PROXY", "SSL_CERT_FILE", "SSL_CERT_DIR", "GIT_SSL_CAINFO", "GIT_SSL_CAPATH", "GIT_CONFIG_COUNT", "GIT_SSH_COMMAND", "AWS_SECRET_ACCESS_KEY"} {
		t.Setenv(key, "canary-"+key)
	}
	env := strings.Join(hostgit.Environ(connectedCloneOptions(nil), "/usr/bin/git"), "\n")
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "NO_PROXY", "SSL_CERT_FILE", "SSL_CERT_DIR", "GIT_SSL_CAINFO", "GIT_SSL_CAPATH"} {
		if !strings.Contains(env, key+"=canary-"+key) {
			t.Errorf("missing %s", key)
		}
	}
	for _, key := range []string{"GIT_CONFIG_COUNT", "GIT_SSH_COMMAND", "AWS_SECRET_ACCESS_KEY"} {
		if strings.Contains(env, "canary-"+key) {
			t.Errorf("inherited %s", key)
		}
	}
}

func TestConnectedCloneCollisionIsTruthful(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "occupied"), 0700); err != nil {
		t.Fatal(err)
	}
	err := EnsureClonedTarget(t.Context(), root, CloneTarget{ConnectedHost: true, Provider: "github", FullName: "example-org/example-repo", Folder: "occupied"})
	if !errors.Is(err, errConnectedCloneDestinationExists) {
		t.Fatal(err)
	}
}

func TestMembershipCloneCapacityDoesNotConsumeDiscovery(t *testing.T) {
	if workspaceOperationSlots("clone_addition") == workspaceOperationSlots("list") || cap(workspaceOperationSlots("clone_addition")) != 2 {
		t.Fatal("clone capacity shares discovery")
	}
	membershipCloneSlots <- struct{}{}
	membershipCloneSlots <- struct{}{}
	defer func() { <-membershipCloneSlots; <-membershipCloneSlots }()
	select {
	case discoverySlots <- struct{}{}:
		<-discoverySlots
	default:
		t.Fatal("clones blocked discovery")
	}
}

// TestResolveBaseCapacityDoesNotShareDiscovery is the structural half of the BLG-501 live
// e2e fix: resolve_base used to share discoverySlots with ordinary list/resolve UI traffic,
// so a busy Explorer/file panel could silently starve a launch's own resolve_base call —
// dropped, never answered, never logged. It now has its own capacity.
func TestResolveBaseCapacityDoesNotShareDiscovery(t *testing.T) {
	if workspaceOperationSlots("resolve_base") == workspaceOperationSlots("list") || cap(workspaceOperationSlots("resolve_base")) != 2 {
		t.Fatal("resolve_base capacity shares discovery")
	}
	discoverySlots <- struct{}{}
	discoverySlots <- struct{}{}
	defer func() { <-discoverySlots; <-discoverySlots }()
	select {
	case resolveBaseSlots <- struct{}{}:
		<-resolveBaseSlots
	default:
		t.Fatal("busy ordinary discovery traffic blocked resolve_base")
	}
}

func TestConnectedCloneNestedDestinationKeepsConfiguredRootFence(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	old := cloneConnectedGit
	t.Cleanup(func() { cloneConnectedGit = old })
	calls := 0
	cloneConnectedGit = func(_ context.Context, _ time.Duration, _ string, dest string, _ []string) error {
		calls++
		return os.MkdirAll(dest, 0700)
	}
	target := CloneTarget{ConnectedHost: true, Provider: "github", FullName: "example-org/example-repo", Folder: "group/new"}
	if err := os.Mkdir(filepath.Join(root, "group"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := EnsureClonedTarget(t.Context(), root, target); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	if _, err := os.Stat(filepath.Join(root, "group", "new")); err != nil {
		t.Fatal(err)
	}
	target.Folder = "escape/new"
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := EnsureClonedTarget(t.Context(), root, target); err == nil {
		t.Fatal("clone followed an external workspace anchor")
	}
	for _, folder := range []string{"../new", "group/../../new", "/tmp/new", "group\\new", "group/../new"} {
		target.Folder = folder
		if err := EnsureClonedTarget(t.Context(), root, target); err == nil {
			t.Fatalf("accepted %q", folder)
		}
	}
	if calls != 1 {
		t.Fatal("invalid destination started a clone")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("clone escaped root", err)
	}
}

func TestMembershipCloneOperationUsesFolderDestinationBeforeRemoteWork(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "work:2026"), 0700); err != nil {
		t.Fatal(err)
	}
	service := discovery.NewOwned(func() string { return root }, "desktop")
	listed := service.Handle(t.Context(), protocol.DiscoverWorkspace{RequestID: "list", Operation: "list", Path: ".", Runtime: "host"})
	if !listed.OK || listed.Page == nil || len(listed.Page.Entries) != 1 {
		t.Fatalf("list: %+v", listed)
	}
	entry := listed.Page.Entries[0]
	selected := service.Handle(t.Context(), protocol.DiscoverWorkspace{RequestID: "expand", Operation: "expand", Runtime: "host", Folder: &protocol.SelectionRef{Path: entry.Path, SourceIdentity: entry.SourceIdentity}})
	if !selected.OK {
		t.Fatal(selected.ErrorCode)
	}
	manifest := protocol.WorkspaceManifest{Version: 2, Layout: selected.Resolved.Layout, Kind: "folder", OwnerID: "owner", Runtime: "host", LaunchGeneration: "parent", Root: selected.Resolved.Root, Members: []protocol.WorkspaceMember{}}
	manifest.Digest = workspaceselection.Digest(manifest)
	bound := service.Handle(t.Context(), protocol.DiscoverWorkspace{RequestID: "bind", Operation: "bind", Runtime: "host", SelectionIdentity: selected.Resolved.SelectionIdentity, Manifest: &manifest})
	if !bound.OK {
		t.Fatal(bound.ErrorCode)
	}
	m := &Manager{config: ManagerConfig{ReposRoot: root}.withReposRootSetting(), discovery: service}
	ref, _ := gitremote.ParseAddition("https://github.com/example-org/example-repo.git")
	req := protocol.DiscoverWorkspace{Type: "clone_repository", RequestID: "clone", Operation: "clone_addition", GitURL: "https://github.com/example-org/example-repo.git", Path: gitremote.AdditionFolder(ref), ParentLaunchGeneration: "parent", Runtime: "host"}
	old := cloneConnectedGit
	t.Cleanup(func() { cloneConnectedGit = old })
	calls := 0
	cloneConnectedGit = func(ctx context.Context, _ time.Duration, remote, dest string, _ []string) error {
		calls++
		if filepath.Dir(filepath.Dir(dest)) != root {
			t.Errorf("clone staged outside the swept repositories root: %s", dest)
		}
		if err := os.MkdirAll(dest, 0700); err != nil {
			return err
		}
		if err := hostgit.Run(ctx, hostgit.Options{Dir: dest}, "init", "--initial-branch=main"); err != nil {
			return err
		}
		return hostgit.Run(ctx, hostgit.Options{Dir: dest}, "remote", "add", "origin", remote)
	}
	// Restart loses ephemeral bindings. No remote work starts until resume
	// restores this launch's selection, and the same request can then succeed.
	m.discovery = discovery.NewOwned(func() string { return root }, "desktop")
	result := m.cloneMembershipAddition(t.Context(), req)
	if result.OK || result.ErrorCode != "binding_pending" || calls != 0 {
		t.Fatalf("restart: %+v calls=%d", result, calls)
	}
	if err := m.discovery.RebindResumed(t.Context(), manifest, filepath.Join(root, "work:2026"), nil); err != nil {
		t.Fatal(err)
	}
	result = m.cloneMembershipAddition(t.Context(), req)
	if !result.OK || result.Addition == nil || result.Addition.Source.Path != "work:2026/"+req.Path || result.Addition.PhysicalPath != req.Path {
		t.Fatalf("clone addition: %+v", result)
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	// A repeated approved operation reuses the complete checkout, rather than
	// failing because a previous attempt already installed its destination.
	if result = m.cloneMembershipAddition(t.Context(), req); !result.OK || calls != 1 {
		t.Fatalf("retry: %+v calls=%d", result, calls)
	}
	if err := os.Rename(filepath.Join(root, "work:2026"), filepath.Join(root, "moved")); err != nil {
		t.Fatal(err)
	}
	if result = m.cloneMembershipAddition(t.Context(), req); result.OK || result.ErrorCode != "destination_unavailable" || calls != 1 {
		t.Fatalf("stale destination started remote work: %+v calls=%d", result, calls)
	}
}
