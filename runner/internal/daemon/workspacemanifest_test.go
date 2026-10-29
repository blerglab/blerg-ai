package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Only a fake quota authority: production must supply an enforced backend.
type testWorkspaceStorage struct {
	root     string
	released bool
	invalid  bool
}

type testDirectWorkspaceWorker struct{}

func (testDirectWorkspaceWorker) Cleanup(context.Context, string, string) error { return nil }

func (testDirectWorkspaceWorker) Run(ctx context.Context, request workspaceWorkerRequest) (workspaceWorkerResponse, error) {
	response := executeWorkspaceWorker(ctx, request)
	if response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}
func (s *testWorkspaceStorage) workspaceWorker() workspaceWorker { return testDirectWorkspaceWorker{} }
func (s *testWorkspaceStorage) Allocate(context.Context, string, protocol.WorkspaceBudget) (string, error) {
	return s.root, nil
}
func (s *testWorkspaceStorage) Validate(context.Context, string, string, protocol.WorkspaceBudget) error {
	if s.invalid {
		return errors.New("quota authority unavailable")
	}
	return nil
}
func (s *testWorkspaceStorage) Release(context.Context, string, string) error {
	s.released = true
	return os.RemoveAll(s.root)
}

type testWorkspaceRuntime struct {
	gone                             bool
	root                             protocol.WorkspaceRoot
	w                                *LocalWorkspace
	freeze, thaw, heads              int
	frozen                           bool
	failFreeze, failThaw, failRemove bool
	failSettle                       bool
	clean                            bool
	signal                           []string
	onFreeze                         func()
}

func (r *testWorkspaceRuntime) Absent(context.Context) (bool, error) { return r.gone, nil }
func (r *testWorkspaceRuntime) RootIdentity(context.Context) (protocol.WorkspaceRoot, error) {
	if r.root.Inode != 0 {
		return r.root, nil
	}
	return r.w.state.RootIdentity, nil
}
func (r *testWorkspaceRuntime) Reconcile(ctx context.Context) error {
	if r.frozen {
		return r.Thaw(ctx)
	}
	return nil
}
func (r *testWorkspaceRuntime) RuntimeID() string                              { return "fake-container-id" }
func (r *testWorkspaceRuntime) Identity(context.Context, string, string) error { return nil }
func (r *testWorkspaceRuntime) StopWriters(context.Context) error              { return nil }
func (r *testWorkspaceRuntime) QuiesceStatus(context.Context) error            { return nil }
func (r *testWorkspaceRuntime) Head(_ context.Context, path string) (string, error) {
	if r.frozen {
		return "", errors.New("exec while frozen")
	}
	r.heads++
	for _, m := range r.w.state.Members {
		if m.Member.Path == path {
			h, ok := readCloneHead(m.Clone)
			if !ok {
				return "", errors.New("bad head")
			}
			return h, nil
		}
	}
	return "", errors.New("missing member")
}
func (r *testWorkspaceRuntime) Freeze(context.Context) error {
	r.freeze++
	r.frozen = true
	if r.onFreeze != nil {
		r.onFreeze()
	}
	if r.failFreeze {
		return errors.New("pause failed")
	}
	return nil
}
func (r *testWorkspaceRuntime) Thaw(context.Context) error {
	r.thaw++
	if r.failThaw {
		return errors.New("unpause failed")
	}
	r.frozen = false
	return nil
}
func (r *testWorkspaceRuntime) Settle(context.Context, string, string) (bool, error) {
	if r.frozen {
		return false, errors.New("exec while frozen")
	}
	if r.failSettle {
		return true, errors.New("settlement outcome unknown")
	}
	return r.clean, nil
}
func (r *testWorkspaceRuntime) Remove(context.Context) error {
	if r.failRemove {
		return errors.New("removal failed")
	}
	r.gone = true
	return nil
}
func (r *testWorkspaceRuntime) SyncSignal(s string) { r.signal = append(r.signal, s) }

func localWorkspaceFixture(t *testing.T) (*LocalWorkspace, *testWorkspaceRuntime, *testWorkspaceStorage, []WorkspaceSource) {
	t.Helper()
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	launch := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner", Runtime: "sandbox", LaunchGeneration: "generation", Root: protocol.WorkspaceRoot{Inode: 1}, Digest: "trusted-digest"}
	var sources []WorkspaceSource
	for _, id := range []string{"one", "two"} {
		repo := filepath.Join(t.TempDir(), id)
		initChildRepo(t, repo)
		childCommit(t, repo, "source-"+id+".txt")
		m := protocol.WorkspaceMember{ID: id, Name: id, Path: id, SourceIdentity: "source-" + id, CommonDirectoryIdentity: "common-" + id, Destination: "destination-" + id}
		launch.Members = append(launch.Members, m)
		rootIdentity, err := workspaceDirectoryIdentity(repo)
		if err != nil {
			t.Fatal(err)
		}
		commonIdentity, err := workspaceDirectoryIdentity(filepath.Join(repo, ".git"))
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, WorkspaceSource{Member: m, Path: repo, RootIdentity: rootIdentity, CommonIdentity: commonIdentity})
	}
	storage := &testWorkspaceStorage{root: filepath.Join(t.TempDir(), "checkout")}
	if err := os.Mkdir(storage.root, 0o700); err != nil {
		t.Fatal(err)
	}
	w, err := PrepareLocalWorkspace(t.Context(), "p2-session", launch, sources, storage)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &testWorkspaceRuntime{w: w}
	if _, err := w.BindRuntime(t.Context(), runtime); err != nil {
		t.Fatal(err)
	}
	return w, runtime, storage, sources
}

func TestWorkspacePrepareAllAndKeepRootGate(t *testing.T) {
	w, _, _, sources := localWorkspaceFixture(t)
	for i, m := range w.state.Members {
		if isSandboxClone(m.Clone, w.state.Session) {
			t.Fatal("legacy session-root gate accepts a member")
		}
		if m.Source.Repo != sources[i].Path || m.Source.Base == "" || m.LastSynced != m.Source.Base {
			t.Fatal("private source/base not pinned")
		}
		if got := childRepoGit(t, m.Clone, "branch", "--show-current"); got != "wip/p2-session" {
			t.Fatal(got)
		}
		if err := w.validateMember(m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(w.state.Root, "notes.txt")); !os.IsNotExist(err) {
		t.Fatal("loose host data copied")
	}
	if err := w.state.Launch.ValidateResults(w.Results()); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareLocalWorkspace(t.Context(), "other", w.state.Launch, sources, nil); err == nil {
		t.Fatal("unbounded storage accepted")
	}
}

func TestWorkspaceUnchangedSkipsFreeze(t *testing.T) {
	w, r, _, _ := localWorkspaceFixture(t)
	if _, err := w.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r.freeze != 0 || r.thaw != 0 || r.heads != 2 || len(r.signal) != 0 {
		t.Fatalf("unchanged sync touched freeze: %+v", r)
	}
	childCommit(t, w.state.Members[0].Clone, "change.txt")
	results, err := w.Sync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if r.freeze != 1 || r.thaw != 1 || r.frozen || len(r.signal) != 2 || r.signal[0] != workspaceSyncNotice || r.signal[1] != "" {
		t.Fatalf("bad freeze or header: %+v", r)
	}
	if results[0].Pending || results[0].Head == results[0].Base {
		t.Fatalf("missing sync result: %+v", results[0])
	}
	if _, err := w.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r.freeze != 1 {
		t.Fatal("synced heads froze again")
	}
}

func TestWorkspaceMemberRefusalDoesNotBlockOthers(t *testing.T) {
	for _, attack := range []string{"include", "index-link", "git-swap", "alternates-link", "detached"} {
		t.Run(attack, func(t *testing.T) {
			w, r, _, _ := localWorkspaceFixture(t)
			childCommit(t, w.state.Members[0].Clone, "bad.txt")
			childCommit(t, w.state.Members[1].Clone, "good.txt")
			bad := w.state.Members[0]
			switch attack {
			case "detached":
				childRepoGit(t, bad.Clone, "checkout", "--detach", "-q", "HEAD")
			case "include":
				f, err := os.OpenFile(filepath.Join(bad.Clone, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.WriteString("\n[include]\npath=/private/forbidden\n")
				if err != nil {
					t.Fatal(err)
				}
				_ = f.Close()
			case "index-link":
				p := filepath.Join(bad.Clone, ".git", "index")
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/private/forbidden", p); err != nil {
					t.Fatal(err)
				}
			case "alternates-link":
				p := filepath.Join(bad.Clone, ".git", "objects", "info", "alternates")
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("/private/forbidden", p); err != nil {
					t.Fatal(err)
				}
			case "git-swap":
				p := filepath.Join(bad.Clone, ".git")
				if err := os.Rename(p, p+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(p, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			results, err := w.Sync(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if results[0].SyncError == "" || !results[0].Pending || results[1].SyncError != "" || results[1].Pending {
				t.Fatalf("partial results: %+v", results)
			}
			if r.freeze != 1 || r.thaw != 1 {
				t.Fatal("not one session freeze")
			}
			if got := childRepoGit(t, w.state.Members[1].Source.Repo, "rev-parse", "refs/heads/wip/p2-session"); got != results[1].Head {
				t.Fatal("good member not fetched")
			}
		})
	}
}

func TestWorkspaceFailedFreezeBlocksAllAndThawFences(t *testing.T) {
	w, r, s, _ := localWorkspaceFixture(t)
	childCommit(t, w.state.Members[0].Clone, "change.txt")
	r.failFreeze = true
	if _, err := w.Sync(t.Context()); err == nil {
		t.Fatal("failed pause accepted")
	}
	if r.thaw != 1 {
		t.Fatal("failed freeze did not thaw")
	}
	if _, err := childRepoGitErr(w.state.Members[0].Source.Repo, "rev-parse", "--verify", "refs/heads/wip/p2-session"); err == nil {
		t.Fatal("fetched after failed pause")
	}
	r.failFreeze = false
	recovered, recoverErr := RecoverLocalWorkspace(t.Context(), w.state.Session, w.state.Launch, s, r)
	if recoverErr != nil {
		t.Fatal(recoverErr)
	}
	w = recovered
	r.failThaw = true
	if _, err := w.Sync(t.Context()); err == nil || !w.state.Unavailable {
		t.Fatal("failed unpause did not fence runtime")
	}
	if _, err := w.Sync(t.Context()); err == nil {
		t.Fatal("unavailable runtime synced")
	}
	if s.released {
		t.Fatal("uncertain state deleted")
	}
}

func TestWorkspaceRecoveryRequiresTrustedLaunchAndPrivateRecord(t *testing.T) {
	w, r, s, _ := localWorkspaceFixture(t)
	recovered, err := RecoverLocalWorkspace(t.Context(), w.state.Session, w.state.Launch, s, r)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.state.RootIdentity != w.state.RootIdentity {
		t.Fatal("root identity changed")
	}
	bad := w.state.Launch
	bad.LaunchGeneration = "stale"
	if _, err := RecoverLocalWorkspace(t.Context(), w.state.Session, bad, s, r); err == nil {
		t.Fatal("stale launch recovered")
	}
	if err := os.WriteFile(w.record, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Sync(t.Context()); err == nil {
		t.Fatal("corrupt shared manifest accepted")
	}
	if r.freeze != 0 {
		t.Fatal("corrupt manifest froze runtime")
	}
}

func TestWorkspaceStopRetainsUncertainAndUnsyncedWork(t *testing.T) {
	for _, failure := range []string{"remove", "settle", "dirty", "member", "clean"} {
		t.Run(failure, func(t *testing.T) {
			w, r, s, _ := localWorkspaceFixture(t)
			childCommit(t, w.state.Members[0].Clone, "change.txt")
			r.clean = true
			switch failure {
			case "settle":
				r.failSettle = true
				f, err := os.OpenFile(filepath.Join(w.state.Members[0].Clone, ".git", "config"), os.O_WRONLY|os.O_APPEND, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = f.WriteString("\n[include]\npath=/forbidden\n")
				_ = f.Close()
			case "remove":
				r.failRemove = true
			case "dirty":
				r.clean = false
				childRepoGit(t, w.state.Members[0].Clone, "branch", "other-work")
			case "member":
				if err := os.Rename(w.state.Members[0].Source.Objects, w.state.Members[0].Source.Objects+"-gone"); err != nil {
					t.Fatal(err)
				}
			}
			results, err := w.Stop(t.Context())
			if failure == "remove" {
				if err == nil || !w.state.Unavailable {
					t.Fatal("uncertain removal accepted")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if s.released != (failure == "clean") {
				t.Fatalf("release=%v for %s", s.released, failure)
			}
			if failure == "member" && (results[0].SyncError == "" || results[1].SyncError != "") {
				t.Fatalf("member refusal lost: %+v", results)
			}
		})
	}
}

func TestWorkspaceSyncStopUseOneMutex(t *testing.T) {
	w, r, _, _ := localWorkspaceFixture(t)
	childCommit(t, w.state.Members[0].Clone, "change.txt")
	entered := make(chan struct{})
	release := make(chan struct{})
	r.onFreeze = func() { close(entered); <-release }
	var wait sync.WaitGroup
	wait.Add(2)
	go func() { defer wait.Done(); _, _ = w.Sync(context.Background()) }()
	<-entered
	go func() { defer wait.Done(); _, _ = w.Stop(context.Background()) }()
	close(release)
	wait.Wait()
	if r.freeze != 1 || r.thaw != 1 || !w.state.Stopped {
		t.Fatal("lifecycle did not serialize")
	}
}

func TestWorkspaceChildSubsetAndIndependentBranch(t *testing.T) {
	w, _, _, sources := localWorkspaceFixture(t)
	child := w.state.Launch
	child.LaunchGeneration = "child-generation"
	child.Members = append([]protocol.WorkspaceMember(nil), child.Members[1])
	storage := &testWorkspaceStorage{root: filepath.Join(t.TempDir(), "child")}
	if err := os.Mkdir(storage.root, 0o700); err != nil {
		t.Fatal(err)
	}
	c, err := PrepareLocalWorkspaceChild(t.Context(), "p2-child", w.state.Launch, child, sources[1:], storage)
	if err != nil {
		t.Fatal(err)
	}
	if c.state.Members[0].Result.Branch != "wip/p2-child" {
		t.Fatal("child inherited parent branch")
	}
	child.Members[0].Destination = "forged"
	forgedSources := []WorkspaceSource{sources[1]}
	forgedSources[0].Member = child.Members[0]
	forgedStorage := &testWorkspaceStorage{root: filepath.Join(t.TempDir(), "forged")}
	if err := os.Mkdir(forgedStorage.root, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareLocalWorkspaceChild(t.Context(), "p2-forged", w.state.Launch, child, forgedSources, forgedStorage); err == nil {
		t.Fatal("child destination forgery accepted")
	}
}

func TestWorkspacePrivateRecordNeverEntersPublicResults(t *testing.T) {
	w, _, _, _ := localWorkspaceFixture(t)
	for _, result := range w.Results() {
		if strings.Contains(result.Branch, w.state.Root) {
			t.Fatal("private path in branch")
		}
	}
	st, err := os.Stat(w.record)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatal("private manifest permissions")
	}
}

func TestWorkspaceAggregateObjectBudgetBeforeAnyFetch(t *testing.T) {
	w, r, _, _ := localWorkspaceFixture(t)
	for _, m := range w.state.Members {
		childCommit(t, m.Clone, "change.txt")
		f, err := os.Create(filepath.Join(m.Clone, ".git", "objects", "budget-test"))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(600 << 20); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	if _, err := w.Sync(t.Context()); err == nil || !strings.Contains(err.Error(), "aggregate") {
		t.Fatalf("aggregate quota accepted: %v", err)
	}
	if r.freeze != 1 || r.thaw != 1 {
		t.Fatal("budget refusal did not thaw")
	}
	for _, m := range w.state.Members {
		if _, err := childRepoGitErr(m.Source.Repo, "rev-parse", "--verify", "refs/heads/wip/p2-session"); err == nil {
			t.Fatal("fetched before aggregate budget check")
		}
	}
}

func TestWorkspaceMemberIdentityCannotBeReplaced(t *testing.T) {
	w, _, _, _ := localWorkspaceFixture(t)
	m := w.state.Members[0]
	git := filepath.Join(m.Clone, ".git")
	if err := os.Rename(git, git+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(git, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := w.validateMember(m); err == nil {
		t.Fatal("member .git identity replacement accepted")
	}
}

func TestWorkspaceRecoveryAuthenticatesPrivateState(t *testing.T) {
	w, r, s, _ := localWorkspaceFixture(t)
	raw, err := os.ReadFile(w.record)
	if err != nil {
		t.Fatal(err)
	}
	// Keep valid JSON and unchanged public launch, but change a private mount.
	raw = []byte(strings.Replace(string(raw), w.state.Members[0].Source.Objects, "/private/forged-objects", 1))
	if err := os.WriteFile(w.record, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RecoverLocalWorkspace(t.Context(), w.state.Session, w.state.Launch, s, r); err == nil {
		t.Fatal("modified private mount recovered")
	}
}

func TestWorkspaceSingleRepoDotPath(t *testing.T) {
	w, _, _, sources := localWorkspaceFixture(t)
	launch := w.state.Launch
	launch.Kind = "repo"
	launch.LaunchGeneration = "single-generation"
	launch.Members = append([]protocol.WorkspaceMember(nil), launch.Members[0])
	launch.Members[0].Path = "."
	source := sources[0]
	source.Member = launch.Members[0]
	storage := &testWorkspaceStorage{root: filepath.Join(t.TempDir(), "single")}
	if err := os.Mkdir(storage.root, 0o700); err != nil {
		t.Fatal(err)
	}
	single, err := PrepareLocalWorkspace(t.Context(), "p2-single", launch, []WorkspaceSource{source}, storage)
	if err != nil {
		t.Fatal(err)
	}
	if single.state.Members[0].Clone != storage.root {
		t.Fatal("single repository public root moved")
	}
	runtime := &testWorkspaceRuntime{w: single, root: protocol.WorkspaceRoot{Device: 3, Inode: 4}}
	root, err := single.BindRuntime(t.Context(), runtime)
	if err != nil || root != runtime.root || root == single.state.RootIdentity {
		t.Fatal("runtime root not reported after binding")
	}
}

func TestWorkspaceUnintegratedDispatchRefusesScalarFallback(t *testing.T) {
	w, _, _, _ := localWorkspaceFixture(t)
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: t.TempDir()})
	host.Spawn(protocol.SpawnSession{SessionID: "v2-unsupported", Repo: "scalar-fallback", Sandbox: true, Workspace: &w.state.Launch})
	reason := sender.waitForSessionError(t)
	if !strings.Contains(reason, "workspace-v2") {
		t.Fatalf("workspace dispatch fell through: %s", reason)
	}
	manager := newManagerWithSender(newRecordingSender(8), ManagerConfig{})
	if _, _, err := manager.prepareWorkspaceTracked(protocol.SpawnSession{SessionID: "v2-unsupported", Repo: "scalar-fallback", Sandbox: true, Workspace: &w.state.Launch}); err == nil || !strings.Contains(err.Error(), "workspace-v2") {
		t.Fatalf("manager accepted scalar fallback: %v", err)
	}
}

func TestWorkspaceRuntimeMemberGateFencesGeneration(t *testing.T) {
	w, r, _, _ := localWorkspaceFixture(t)
	calls := 0
	operation := func(_ context.Context, runtime WorkspaceContainer, path string) error {
		calls++
		if runtime != r || path != w.state.Members[0].Member.Path {
			t.Fatal("runtime member binding changed")
		}
		return nil
	}
	ref := protocol.WorkspaceMemberRef{MemberID: w.state.Members[0].Member.ID, LaunchGeneration: w.state.Launch.LaunchGeneration}
	if err := w.WithRuntimeMember(t.Context(), ref, operation); err != nil {
		t.Fatal(err)
	}
	ref.LaunchGeneration = "stale"
	if err := w.WithRuntimeMember(t.Context(), ref, operation); err == nil {
		t.Fatal("stale member operation dispatched")
	}
	ref.LaunchGeneration = w.state.Launch.LaunchGeneration
	ref.MemberID = "new-discovery"
	if err := w.WithRuntimeMember(t.Context(), ref, operation); err == nil {
		t.Fatal("nonmember operation dispatched")
	}
	if calls != 1 {
		t.Fatal("unauthorized runtime operation executed")
	}
}

func TestWorkspaceSharedRefusalMarksPostProbeCommitPending(t *testing.T) {
	w, r, _, _ := localWorkspaceFixture(t)
	childCommit(t, w.state.Members[0].Clone, "trigger.txt")
	r.onFreeze = func() {
		childCommit(t, w.state.Members[1].Clone, "late.txt")
		for _, m := range w.state.Members {
			f, err := os.Create(filepath.Join(m.Clone, ".git", "objects", "budget-test"))
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Truncate(600 << 20); err != nil {
				t.Fatal(err)
			}
			_ = f.Close()
		}
	}
	results, err := w.Sync(t.Context())
	if err == nil {
		t.Fatal("expected shared budget refusal")
	}
	if !results[1].Pending || results[1].SyncError == "" {
		t.Fatalf("post-probe commit falsely saved: %+v", results[1])
	}
}
