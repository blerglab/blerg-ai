//go:build linux

package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/discovery"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

func hostChildAllocationFixture(t *testing.T, parent workspacePrivateManifest) *LocalWorkspace {
	t.Helper()
	record, _ := workspaceRecord("child")
	w := &LocalWorkspace{record: record, state: workspacePrivateManifest{Host: true, Session: "child", Launch: parent.Launch}}
	pins, err := w.allocateHostChild(t.Context(), parent)
	if err != nil {
		t.Fatal(err)
	}
	pins.Close()
	for i, a := range w.state.HostChildren {
		w.state.Members = append(w.state.Members, workspacePrivateMember{Member: w.state.Launch.Members[i], Clone: a.Path, CloneIdentity: a.Root, Result: protocol.WorkspaceMemberResult{WorkspaceMemberRef: protocol.WorkspaceMemberRef{LaunchGeneration: w.state.Launch.LaunchGeneration, MemberID: a.MemberID}, Pending: true}})
	}
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestHostChildWorktreeLifecycle(t *testing.T) {
	for _, scenario := range []string{"clean", "untracked", "staged", "tracked", "ignored", "ignored-and-untracked", "detached", "switched", "unproven", "replaced"} {
		t.Run(scenario, func(t *testing.T) {
			_, parent, repo := hostChildSelectionFixture(t)
			w := hostChildAllocationFixture(t, parent)
			a := &w.state.HostChildren[0]
			base := childRepoGit(t, repo, "rev-parse", "HEAD")
			if a.Base != base {
				t.Fatal("wrong base", a.Base)
			}
			childCommit(t, a.Path, "child.txt")
			switch scenario {
			case "untracked":
				workspaceFixture(t, a.Path, "left.txt", "keep me")
			case "staged":
				workspaceFixture(t, a.Path, "left.txt", "keep staged")
				childRepoGit(t, a.Path, "add", "left.txt")
			case "tracked":
				workspaceFixture(t, a.Path, "child.txt", "keep unstaged")
			case "ignored", "ignored-and-untracked":
				workspaceFixture(t, a.Path, ".gitignore", "ignored.txt\n")
				childRepoGit(t, a.Path, "add", ".gitignore")
				childRepoGit(t, a.Path, "commit", "-qm", "ignore")
				workspaceFixture(t, a.Path, "ignored.txt", "build output")
				if scenario == "ignored-and-untracked" {
					workspaceFixture(t, a.Path, "left.txt", "keep me")
				}
			case "detached":
				childRepoGit(t, a.Path, "checkout", "--detach", "-q")
			case "switched":
				childRepoGit(t, a.Path, "checkout", "-qb", "other")
			case "unproven":
				a.Root = protocol.WorkspaceRoot{}
			case "replaced":
				if err := os.Rename(a.Path, a.Path+"-saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(a.Path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			commit := childRepoGit(t, repo, "rev-parse", "wip/child")
			if err := w.save(); err != nil {
				t.Fatal(err)
			}
			state, err := readPrivateWorkspace(w.record)
			if err != nil {
				t.Fatal(err)
			}
			recovered := &LocalWorkspace{record: w.record, state: state}
			recovered.collectHostChildren(t.Context())
			if err := recovered.releaseHostChildren(t.Context()); err != nil {
				t.Fatal(err)
			}
			r := recovered.state.Members[0].Result
			if scenario == "clean" || scenario == "ignored" {
				if _, err := os.Stat(a.Path); !os.IsNotExist(err) {
					t.Fatal("clean worktree retained", err)
				}
				if !recovered.state.HostChildren[0].Released || r.RecoveryLocation != "" || r.Branch != "wip/child" || r.Base != base || r.Head != commit {
					t.Fatalf("clean result: %+v", r)
				}
			} else if _, err := os.Stat(a.Path); err != nil || r.RecoveryLocation != a.Path || r.SyncError == "" {
				t.Fatalf("unsafe worktree not retained/reported: %+v %v", r, err)
			}
			if got := childRepoGit(t, repo, "rev-parse", "wip/child"); got != commit {
				t.Fatal("branch lost")
			}
			if got := childRepoGit(t, repo, "rev-parse", "HEAD"); got != base {
				t.Fatal("parent HEAD changed")
			}
		})
	}
}

func TestHostChildWorktreeRefusesNonGitMember(t *testing.T) {
	_, parent, _ := hostChildSelectionFixture(t)
	nongit := t.TempDir()
	st, _ := os.Stat(nongit)
	parent.Sources[0].Path, parent.Sources[0].RootIdentity = nongit, workspaceRootIdentity(st)
	record, _ := workspaceRecord("nongit-child")
	w := &LocalWorkspace{record: record, state: workspacePrivateManifest{Host: true, Session: "nongit-child", Launch: parent.Launch}}
	if pins, err := w.allocateHostChild(t.Context(), parent); err == nil {
		pins.Close()
		t.Fatal("non-Git member accepted")
	}
}

// The fake authority models the server's independent session bindings, so both
// admissions use one actual manager, lifecycle service and supervisor registry.
type concurrentHostChildAuthority struct {
	hostExplorerAuthority
	mu       sync.Mutex
	bindings map[string]protocol.WorkspaceRuntimeBinding
}

func (a *concurrentHostChildAuthority) Bind(_ context.Context, l protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.bindings == nil {
		a.bindings = map[string]protocol.WorkspaceRuntimeBinding{}
	}
	a.bindings[l.SessionID] = b
	return nil
}
func (a *concurrentHostChildAuthority) Current(_ context.Context, l protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	current, ok := a.bindings[l.SessionID]
	if !ok || current != b {
		return fmt.Errorf("stale child runtime binding")
	}
	return nil
}

// Real Git writers exercise workspace admission and turn/discard delivery.
// Writer containment has its own processcontainment suite.
func TestHostChildrenConcurrentCommits(t *testing.T) { hostChildrenConcurrentCommits(t, nil) }

func hostChildrenConcurrentCommits(t *testing.T, stress func(*testing.T, *LocalWorkspace, string, func())) {
	selection, parent, repo := hostChildSelectionFixture(t)
	workspaceFixture(t, repo, "staged.txt", "parent staged")
	childRepoGit(t, repo, "add", "staged.txt")
	workspaceFixture(t, repo, "initial.txt", "parent unstaged")
	workspaceFixture(t, repo, "untracked.txt", "parent untracked")
	status := childRepoGit(t, repo, "status", "--porcelain=v1", "--untracked-files=all")
	head, branch := childRepoGit(t, repo, "rev-parse", "HEAD"), childRepoGit(t, repo, "symbolic-ref", "HEAD")
	index, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	type child struct {
		reader    WorkspaceExplorer
		spawn     protocol.SpawnSession
		work      *LocalWorkspace
		cwd       string
		err       error
		delivered []protocol.WorkspaceMemberResult
	}
	service := hostLifecycleTestService(t, &concurrentHostChildAuthority{})
	service.Resolve = func(context.Context, protocol.SpawnSession) ([]WorkspaceSource, string, error) {
		return nil, "parent", nil
	}
	service.AuthorizeChild = func(context.Context, string, string) error { return nil }
	service.TrustedLaunch = func(context.Context, string) (protocol.WorkspaceManifest, error) { return parent.Launch, nil }
	manager := newManagerWithSender(newRecordingSender(8), ManagerConfig{ReposRoot: filepath.Dir(repo), Workspaces: service})
	manager.discovery = selection

	children := make([]*child, 2)
	for i := range children {
		id := fmt.Sprintf("parallel-%d", i)
		manifest := parent.Launch
		manifest.LaunchGeneration = id + "-launch"
		manifest.Digest = workspaceselection.Digest(manifest)
		if r := selection.Handle(t.Context(), protocol.DiscoverWorkspace{RequestID: id, Operation: "bind_child", Runtime: "host", ParentLaunchGeneration: parent.Launch.LaunchGeneration, Manifest: &manifest}); !r.OK {
			t.Fatal(r)
		}
		c := &child{spawn: protocol.SpawnSession{SessionID: id, Kind: "agent", Workspace: &manifest, WorkspaceCapabilities: protocol.WorkspaceCapabilitiesFor("host", "linux")}}
		children[i] = c
	}
	service.Results = func(_ context.Context, id string, _ protocol.WorkspaceRuntimeBinding, r []protocol.WorkspaceMemberResult) error {
		for _, c := range children {
			if c.spawn.SessionID == id {
				c.delivered = r
				return nil
			}
		}
		return fmt.Errorf("unexpected child result session %s", id)
	}

	var wg sync.WaitGroup
	for _, c := range children {
		wg.Go(func() {
			c.cwd, c.reader, c.err = manager.prepareHostWorkspace(t.Context(), c.spawn)
		})
	}
	wg.Wait()
	for _, c := range children {
		service.mu.Lock()
		c.work = service.sessions[c.spawn.SessionID]
		service.mu.Unlock()
		if c.err != nil {
			t.Fatal(c.err)
		}
		if c.cwd == repo || c.work == nil {
			t.Fatal("parent checkout exposed")
		}
		t.Cleanup(func() {
			_, _ = c.work.Stop(context.Background())
			if closer, ok := c.reader.(io.Closer); ok {
				_ = closer.Close()
			}
		})
	}
	for _, c := range children {
		req := protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", RequestID: "status", SessionID: c.spawn.SessionID, WorkspaceRuntimeRef: c.work.state.Binding.WorkspaceRuntimeRef, Operation: "status", MemberID: c.spawn.Workspace.Members[0].ID, Path: ""}
		if res := c.reader.Explore(t.Context(), req); !res.OK {
			t.Fatalf("child worktree status: %+v", res)
		}
	}
	if stress != nil {
		for _, c := range children {
			stress(t, c.work, c.cwd, func() {
				if err := service.Report(t.Context(), c.work); err != nil {
					t.Fatal(err)
				}
				if len(c.delivered) != 1 || c.delivered[0].Head != childRepoGit(t, c.cwd, "rev-parse", "HEAD") || c.delivered[0].Pending || c.delivered[0].SyncError != "" {
					t.Fatalf("stress turn delivery: %+v", c.delivered)
				}
			})
		}
	}
	commands := make([]*exec.Cmd, 2)
	gates := make([]io.WriteCloser, 2)
	for i, c := range children {
		cmd := exec.CommandContext(t.Context(), "sh", "-c", `read -r ready && printf '%s' "$1" > child.txt && git add child.txt && git commit -qm child`, "sh", c.spawn.SessionID)
		cmd.Dir = c.cwd
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Example Developer", "GIT_AUTHOR_EMAIL=developer@example.com", "GIT_COMMITTER_NAME=Example Developer", "GIT_COMMITTER_EMAIL=developer@example.com")
		gate, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		gates[i] = gate
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands[i] = cmd
	}
	for _, gate := range gates {
		if _, err := io.WriteString(gate, "go\n"); err != nil {
			t.Fatal(err)
		}
		if err := gate.Close(); err != nil {
			t.Fatal(err)
		}
	}
	for i, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
		c := children[i]
		if _, err := c.work.Sync(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err := service.Report(t.Context(), c.work); err != nil {
			t.Fatal(err)
		}
		if len(c.delivered) != 1 || c.delivered[0].Branch != "wip/"+c.spawn.SessionID || c.delivered[0].Base != head || c.delivered[0].Pending || c.delivered[0].SyncError != "" {
			t.Fatalf("turn delivery: %+v", c.delivered)
		}
		if got := childRepoGit(t, repo, "show", "wip/"+c.spawn.SessionID+":child.txt"); got != c.spawn.SessionID {
			t.Fatal("wrong child commit", got)
		}
		if _, err := c.work.Stop(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if got := childRepoGit(t, repo, "rev-parse", "HEAD"); got != head {
		t.Fatal("parent HEAD changed")
	}
	if got := childRepoGit(t, repo, "symbolic-ref", "HEAD"); got != branch {
		t.Fatal("parent branch changed")
	}
	after, err := os.ReadFile(filepath.Join(repo, ".git", "index"))
	if err != nil || !bytes.Equal(index, after) {
		t.Fatal("parent index changed", err)
	}
	if got := childRepoGit(t, repo, "status", "--porcelain=v1", "--untracked-files=all"); got != status {
		t.Fatal("parent working tree changed", got)
	}
	for name, want := range map[string]string{"initial.txt": "parent unstaged", "staged.txt": "parent staged", "untracked.txt": "parent untracked"} {
		raw, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil || string(raw) != want {
			t.Fatal("parent bytes changed", name, err)
		}
	}
}

func TestHostChildWorktreeMultipleMembersAtCurrentHead(t *testing.T) {
	_, parent, repo := hostChildSelectionFixture(t)
	childCommit(t, repo, "parent-later.txt")
	second := filepath.Join(t.TempDir(), "second")
	initChildRepo(t, second)
	st, _ := os.Stat(second)
	common, _ := os.Stat(filepath.Join(second, ".git"))
	first := parent.Launch.Members[0]
	first.Path = "one"
	two := first
	two.ID, two.Name, two.Path = "two", "two", "two"
	parent.Launch.Kind = "folder"
	parent.Launch.Members = []protocol.WorkspaceMember{first, two}
	parent.Sources[0].Member = first
	parent.Sources = append(parent.Sources, WorkspaceSource{Member: two, Path: second, CommonPath: filepath.Join(second, ".git"), RootIdentity: workspaceRootIdentity(st), CommonIdentity: workspaceRootIdentity(common)})
	w := hostChildAllocationFixture(t, parent)
	if len(w.state.HostChildren) != 2 {
		t.Fatal("member allocation missing")
	}
	for i, source := range parent.Sources {
		a := w.state.HostChildren[i]
		if a.Base != childRepoGit(t, source.Path, "rev-parse", "HEAD") || a.Path == source.Path {
			t.Fatal("member not isolated at current HEAD")
		}
		childCommit(t, a.Path, "child.txt")
		if got := childRepoGit(t, source.Path, "rev-parse", "wip/child"); got != childRepoGit(t, a.Path, "rev-parse", "HEAD") {
			t.Fatal("member commit not delivered")
		}
	}
	w.collectHostChildren(t.Context())
	if err := w.releaseHostChildren(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, a := range w.state.HostChildren {
		if !a.Released {
			t.Fatal("member not released")
		}
	}
}

func TestHostChildWorktreeRefusesUnsupportedCheckout(t *testing.T) {
	for _, scenario := range []string{"unborn", "branch-exists", "filters"} {
		t.Run(scenario, func(t *testing.T) {
			_, parent, repo := hostChildSelectionFixture(t)
			switch scenario {
			case "unborn":
				childRepoGit(t, repo, "checkout", "--orphan", "empty")
				childRepoGit(t, repo, "rm", "-rf", ".")
			case "branch-exists":
				childRepoGit(t, repo, "branch", "wip/child")
			case "filters":
				childRepoGit(t, repo, "config", "filter.example.smudge", "false")
			}
			head, _ := childRepoGitErr(repo, "rev-parse", "HEAD")
			status := childRepoGit(t, repo, "status", "--porcelain=v1")
			record, _ := workspaceRecord("child")
			w := &LocalWorkspace{record: record, state: workspacePrivateManifest{Host: true, Session: "child", Launch: parent.Launch}}
			pins, err := w.allocateHostChild(t.Context(), parent)
			if err == nil {
				pins.Close()
				t.Fatal("unsupported checkout accepted")
			}
			after, _ := childRepoGitErr(repo, "rev-parse", "HEAD")
			if head != after || status != childRepoGit(t, repo, "status", "--porcelain=v1") {
				t.Fatal("refused child changed parent")
			}
		})
	}
}

func TestHostChildRestartRecoveryUsesAllocationJournal(t *testing.T) {
	for _, dirty := range []bool{false, true} {
		t.Run(fmt.Sprint(dirty), func(t *testing.T) {
			_, parent, repo := hostChildSelectionFixture(t)
			w := hostChildAllocationFixture(t, parent)
			path := w.state.HostChildren[0].Path
			childCommit(t, path, "child.txt")
			if dirty {
				workspaceFixture(t, path, "left.txt", "keep me")
			}
			authority := &hostExplorerAuthority{}
			launch, err := authority.Launch(t.Context(), w.state.Session, w.state.Launch, "recovery-token")
			if err != nil {
				t.Fatal(err)
			}
			binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: launch.LaunchGeneration, Generation: launch.RuntimeGeneration}, ManifestDigest: w.state.Launch.Digest, Root: w.state.RootIdentity}
			if err := authority.Bind(t.Context(), launch, binding); err != nil {
				t.Fatal(err)
			}
			w.state.SupervisorLaunch, w.state.Binding, w.state.RuntimeToken, w.state.HostDriverStopped = launch, &binding, "recovery-token", true
			if err := w.save(); err != nil {
				t.Fatal(err)
			}
			saved, err := readPrivateWorkspace(w.record)
			if err != nil {
				t.Fatal(err)
			}
			service := hostLifecycleTestService(t, authority)
			service.TrustedLaunch = func(context.Context, string) (protocol.WorkspaceManifest, error) { return saved.Launch, nil }
			var results []protocol.WorkspaceMemberResult
			service.Results = func(_ context.Context, _ string, _ protocol.WorkspaceRuntimeBinding, r []protocol.WorkspaceMemberResult) error {
				results = r
				return nil
			}
			if err := service.recoverHost(t.Context(), saved, w.record, func(protocol.SessionEnded) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].Branch != "wip/child" || results[0].Pending {
				t.Fatalf("recovery results: %+v", results)
			}
			_, err = os.Stat(path)
			if dirty && (err != nil || results[0].RecoveryLocation != path) {
				t.Fatal("dirty recovery lost allocation", err)
			}
			if !dirty && !os.IsNotExist(err) {
				t.Fatal("clean recovery kept allocation", err)
			}
			if childRepoGit(t, repo, "branch", "--show-current") != "main" {
				t.Fatal("recovery touched parent branch")
			}
		})
	}
}

func TestHostChildInterruptedPreparationRetainsUnprovenWorktree(t *testing.T) {
	_, parent, repo := hostChildSelectionFixture(t)
	w := hostChildAllocationFixture(t, parent)
	path := w.state.HostChildren[0].Path
	workspaceFixture(t, path, "left.txt", "unproven work")
	w.state.HostChildren[0].Root = protocol.WorkspaceRoot{}
	w.state.HostChildren[0].GitIdentity = protocol.WorkspaceRoot{}
	w.state.Sources, w.state.Members = nil, nil
	w.state.RuntimePreparing, w.state.HostDriverStopped, w.state.RuntimeToken = true, true, "interrupted-token"
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	saved, err := readPrivateWorkspace(w.record)
	if err != nil {
		t.Fatal(err)
	}
	authority := &hostPreparationFaultAuthority{hostExplorerAuthority: &hostExplorerAuthority{}}
	service := hostLifecycleTestService(t, authority)
	service.TrustedLaunch = func(context.Context, string) (protocol.WorkspaceManifest, error) { return saved.Launch, nil }
	var results []protocol.WorkspaceMemberResult
	service.Results = func(_ context.Context, _ string, _ protocol.WorkspaceRuntimeBinding, r []protocol.WorkspaceMemberResult) error {
		results = r
		return nil
	}
	if err := service.recoverHost(t.Context(), saved, w.record, func(protocol.SessionEnded) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Pending || results[0].RecoveryLocation != path || results[0].SyncError == "" {
		t.Fatalf("unproven allocation not reported: %+v", results)
	}
	raw, err := os.ReadFile(filepath.Join(path, "left.txt"))
	if err != nil || string(raw) != "unproven work" {
		t.Fatal("unproven bytes lost", err)
	}
	if childRepoGit(t, repo, "branch", "--show-current") != "main" {
		t.Fatal("recovery used parent checkout")
	}
}

// A restarted daemon resumes a host child over its own journalled worktrees:
// no P1 selection or parent lookup is needed, the parent's checkout is never
// used, and a journal that points a child at the parent's checkout is refused.
func TestHostChildResumesOverItsOwnWorktrees(t *testing.T) {
	for _, scenario := range []string{"resume", "parent_checkout"} {
		t.Run(scenario, func(t *testing.T) {
			selection, parent, repo := hostChildSelectionFixture(t)
			head := childRepoGit(t, repo, "rev-parse", "HEAD")
			manifest := parent.Launch
			manifest.LaunchGeneration = "child-launch"
			manifest.Digest = workspaceselection.Digest(manifest)
			if r := selection.Handle(t.Context(), protocol.DiscoverWorkspace{RequestID: "child", Operation: "bind_child", Runtime: "host", ParentLaunchGeneration: parent.Launch.LaunchGeneration, Manifest: &manifest}); !r.OK {
				t.Fatal(r)
			}
			spawn := protocol.SpawnSession{SessionID: "child", Kind: "agent", Workspace: &manifest, WorkspaceCapabilities: protocol.WorkspaceCapabilitiesFor("host", "linux")}
			authority := &concurrentHostChildAuthority{}
			first := hostLifecycleTestService(t, authority)
			first.Resolve = func(context.Context, protocol.SpawnSession) ([]WorkspaceSource, string, error) {
				return nil, "parent", nil
			}
			first.AuthorizeChild = func(context.Context, string, string) error { return nil }
			first.TrustedLaunch = func(_ context.Context, id string) (protocol.WorkspaceManifest, error) {
				if id == "parent" {
					return parent.Launch, nil
				}
				return manifest, nil
			}
			before := newManagerWithSender(newRecordingSender(8), ManagerConfig{ReposRoot: filepath.Dir(repo), Workspaces: first})
			before.discovery = selection
			cwd, reader, err := before.prepareHostWorkspace(t.Context(), spawn)
			if err != nil {
				t.Fatal(err)
			}
			if cwd == repo {
				t.Fatal("fresh child got the parent checkout")
			}
			childCommit(t, cwd, "before.txt")
			if closer, ok := reader.(io.Closer); ok {
				_ = closer.Close()
			}

			// The daemon dies: its controller is gone, and nothing is stopped.
			record, _ := workspaceRecord("child")
			state, err := readPrivateWorkspace(record)
			if err != nil || !state.HostChild || len(state.HostChildren) != 1 {
				t.Fatal("child journal", err)
			}
			state.HostController = hostHelperIdentity{PID: os.Getpid(), Start: "previous-incarnation"}
			if scenario == "parent_checkout" {
				state.Sources = parent.Sources
				state.Root, state.RootIdentity = parent.Sources[0].Path, parent.Sources[0].RootIdentity
				state.Binding.Root = parent.Sources[0].RootIdentity
			}
			saved := &LocalWorkspace{mu: new(sync.Mutex), record: record, state: state}
			if err = saved.save(); err != nil {
				t.Fatal(err)
			}

			// The new daemon: a fresh service and manager, no P1 selection, and a
			// selection resolver that must not be needed.
			second := hostLifecycleTestService(t, authority)
			second.Resolve = func(context.Context, protocol.SpawnSession) ([]WorkspaceSource, string, error) {
				return nil, "", errors.New("trusted workspace selection unavailable")
			}
			second.TrustedLaunch = first.TrustedLaunch
			after := newManagerWithSender(newRecordingSender(8), ManagerConfig{ReposRoot: filepath.Dir(repo), Workspaces: second})
			after.discovery = discovery.New(func() string { return filepath.Dir(repo) })
			resumed, reader, err := after.prepareHostWorkspace(t.Context(), spawn)
			if scenario == "parent_checkout" {
				if err == nil || err.Error() != "host child source is not its own worktree" {
					t.Fatal("a child resumed over the parent's checkout", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if closer, ok := reader.(io.Closer); ok {
					t.Cleanup(func() { _ = closer.Close() })
				}
				if resumed != cwd {
					t.Fatalf("resumed in %s, want its own worktree %s", resumed, cwd)
				}
				second.mu.Lock()
				w := second.sessions["child"]
				second.mu.Unlock()
				if w == nil || !w.state.HostChild || len(w.state.Sources) != 1 || w.state.Sources[0].Path != cwd {
					t.Fatal("resumed child lost its worktree journal")
				}
				childCommit(t, resumed, "after.txt")
				if got := childRepoGit(t, repo, "show", "wip/child:after.txt"); got == "" {
					t.Fatal("resumed child commit missing")
				}
			}
			if got := childRepoGit(t, repo, "rev-parse", "HEAD"); got != head {
				t.Fatal("parent HEAD changed")
			}
			if got := childRepoGit(t, repo, "status", "--porcelain=v1", "--untracked-files=all"); got != "" {
				t.Fatal("parent checkout changed", got)
			}
		})
	}
}

// After a restart, a resumed host workspace parent can start host children
// again: its selection binding is restored from its own signed journal when it
// resumes. Before, the fresh daemon's discovery had no binding for the parent
// and bind_child failed (the server's "workspace runtime unavailable").
func TestResumedHostParentCanBindChildrenAfterRestart(t *testing.T) {
	selection, parent, repo := hostChildSelectionFixture(t)
	manifest := parent.Launch
	record, _ := workspaceRecord("parent")
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	spawn := protocol.SpawnSession{SessionID: "parent", Kind: "agent", Workspace: &manifest, WorkspaceCapabilities: protocol.WorkspaceCapabilitiesFor("host", "linux")}
	authority := &concurrentHostChildAuthority{}
	first := hostLifecycleTestService(t, authority)
	first.TrustedLaunch = func(context.Context, string) (protocol.WorkspaceManifest, error) { return manifest, nil }
	before := newManagerWithSender(newRecordingSender(8), ManagerConfig{ReposRoot: filepath.Dir(repo), Workspaces: first})
	before.discovery = selection
	cwd, reader, err := before.prepareHostWorkspace(t.Context(), spawn)
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := reader.(io.Closer); ok {
		_ = closer.Close()
	}

	// The daemon dies: its controller is gone, nothing is stopped.
	state, err := readPrivateWorkspace(record)
	if err != nil {
		t.Fatal(err)
	}
	state.HostController = hostHelperIdentity{PID: os.Getpid(), Start: "previous-incarnation"}
	if err = (&LocalWorkspace{mu: new(sync.Mutex), record: record, state: state}).save(); err != nil {
		t.Fatal(err)
	}

	child := manifest
	child.LaunchGeneration = "child-launch"
	child.Digest = workspaceselection.Digest(child)
	bindChild := protocol.DiscoverWorkspace{RequestID: "child", Operation: "bind_child", Runtime: "host", ParentLaunchGeneration: manifest.LaunchGeneration, Manifest: &child}

	second := hostLifecycleTestService(t, authority)
	second.TrustedLaunch = first.TrustedLaunch
	after := newManagerWithSender(newRecordingSender(8), ManagerConfig{ReposRoot: filepath.Dir(repo), Workspaces: second})
	after.discovery = discovery.New(func() string { return filepath.Dir(repo) })
	if r := after.discovery.Handle(t.Context(), bindChild); r.OK {
		t.Fatal("a restarted daemon bound a child before its parent resumed")
	}
	resumed, reader, err := after.prepareHostWorkspace(t.Context(), spawn)
	if err != nil {
		t.Fatal(err)
	}
	if resumed != cwd {
		t.Fatalf("resumed in %s, want %s", resumed, cwd)
	}
	if r := after.discovery.Handle(t.Context(), bindChild); !r.OK {
		t.Fatal("resumed parent cannot bind a host child:", r.ErrorCode)
	}
	pins, err := after.PinWorkspaceSelection(t.Context(), child)
	if err != nil {
		t.Fatal(err)
	}
	if len(pins.Sources) != 1 || pins.Sources[0].CanonicalPath != repo {
		t.Fatal("child selection is not the parent's repository")
	}
	pins.Close()
	after.ReleaseWorkspaceSelection(child)

	// The parent's binding lives as long as its session: closing it releases it.
	if closer, ok := reader.(io.Closer); ok {
		_ = closer.Close()
	}
	if r := after.discovery.Handle(t.Context(), bindChild); r.OK {
		t.Fatal("a closed parent still binds children")
	}
}
