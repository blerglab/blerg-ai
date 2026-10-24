package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func workspaceServiceFixture(t *testing.T) (*LocalWorkspaceService, protocol.SpawnSession, []WorkspaceSource) {
	t.Helper()
	useWorkspaceTestContext(t)
	parent, _, _, sources := localWorkspaceFixture(t)
	launch := parent.state.Launch
	launch.LaunchGeneration = "service-generation"
	launch.Root = protocol.WorkspaceRoot{}
	storage := &testWorkspaceStorage{root: filepath.Join(t.TempDir(), "service")}
	if err := os.Mkdir(storage.root, 0o700); err != nil {
		t.Fatal(err)
	}
	service := &LocalWorkspaceService{CommitFinal: acknowledgeWorkspaceFinal, Results: func(context.Context, string, protocol.WorkspaceRuntimeBinding, []protocol.WorkspaceMemberResult) error {
		return nil
	}, Authority: &testWorkspaceAuthority{}, Supervisor: &testWorkspaceSupervisor{}, Storage: storage, Resolve: func(context.Context, protocol.SpawnSession) ([]WorkspaceSource, string, error) {
		return sources, "", nil
	}}
	msg := protocol.SpawnSession{SessionID: "service-session", Kind: "agent", Sandbox: true, Workspace: &launch, WorkspaceCapabilities: protocol.WorkspaceCapabilities{ManifestVersion: 2}}
	return service, msg, sources
}
func TestWorkspaceManagerDispatchUsesMemberPreparation(t *testing.T) {
	service, msg, _ := workspaceServiceFixture(t)
	manager := newManagerWithSender(newRecordingSender(8), ManagerConfig{Workspaces: service})
	root, _, err := manager.prepareWorkspaceTracked(msg)
	if err != nil {
		t.Fatal(err)
	}
	w, err := service.Prepare(t.Context(), msg)
	if err != nil {
		t.Fatal(err)
	}
	if root != w.state.Root || len(w.state.Members) != 2 {
		t.Fatal("manager fell through scalar preparation")
	}
	for _, m := range w.state.Members {
		if got := childRepoGit(t, m.Clone, "branch", "--show-current"); got != "wip/service-session" {
			t.Fatal("wrong member branch")
		}
	}
}
func TestWorkspaceServiceChildInheritsOnlyPrivateParentSources(t *testing.T) {
	useWorkspaceTestContext(t)
	parent, _, _, _ := localWorkspaceFixture(t)
	launch := parent.state.Launch
	launch.LaunchGeneration = "child-service"
	launch.Members = append([]protocol.WorkspaceMember(nil), launch.Members[1])
	storage := &testWorkspaceStorage{root: filepath.Join(t.TempDir(), "child")}
	if err := os.Mkdir(storage.root, 0o700); err != nil {
		t.Fatal(err)
	}
	authorized := false
	service := &LocalWorkspaceService{CommitFinal: acknowledgeWorkspaceFinal, Results: func(context.Context, string, protocol.WorkspaceRuntimeBinding, []protocol.WorkspaceMemberResult) error {
		return nil
	}, Authority: &testWorkspaceAuthority{}, Supervisor: &testWorkspaceSupervisor{}, Storage: storage, Resolve: func(context.Context, protocol.SpawnSession) ([]WorkspaceSource, string, error) {
		return []WorkspaceSource{{Path: "/forged"}}, parent.state.Session, nil
	}, AuthorizeChild: func(_ context.Context, parentID, childID string) error {
		authorized = parentID == parent.state.Session && childID == "service-child"
		return nil
	}}
	child, err := service.Prepare(t.Context(), protocol.SpawnSession{SessionID: "service-child", Kind: "agent", Sandbox: true, Workspace: &launch, WorkspaceCapabilities: protocol.WorkspaceCapabilities{ManifestVersion: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if !authorized || len(child.state.Members) != 1 || child.state.Members[0].Source.Repo != parent.state.Sources[1].Path {
		t.Fatal("child trusted forged selection instead of parent sources")
	}
	if child.state.Members[0].Result.Branch != "wip/service-child" {
		t.Fatal("child branch inherited parent")
	}
}
func TestWorkspaceServiceRefusesMissingAuthorityBeforePreparation(t *testing.T) {
	service, msg, _ := workspaceServiceFixture(t)
	service.Authority = nil
	if _, err := service.Prepare(t.Context(), msg); err == nil || !strings.Contains(err.Error(), "P0b") {
		t.Fatalf("missing authority accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sandboxCloneRoot(), msg.SessionID+".workspace.json")); !os.IsNotExist(err) {
		t.Fatal("unbound launch performed preparation")
	}
}
func TestWorkspaceGuideNamesMembersAndCommitPolicy(t *testing.T) {
	_, msg, _ := workspaceServiceFixture(t)
	guide := workspaceGuide(msg.Workspace)
	if !strings.Contains(guide, "/workspace/members/one") || !strings.Contains(guide, "Commit in each affected member") || strings.Contains(guide, "/private/") {
		t.Fatal("invalid runtime workspace guide")
	}
}

func TestP2DockerAgentDispatchSyncStopAndResults(t *testing.T) {
	if os.Getenv("BLERG_P2_DOCKER_TEST") != "1" {
		t.Skip("set BLERG_P2_DOCKER_TEST=1")
	}
	t.Setenv("BLERG_SANDBOX_CHILD_TEST", "1")
	_, msg, sources := workspaceServiceFixture(t)
	engine := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
set -e
cd /workspace/members/one
printf 'committed work\n' > committed.txt
git add committed.txt
git -c user.name=Example -c user.email=developer@example.com commit -qm work
printf 'leftover work\n' > /workspace/members/two/leftover.txt
cat <<'JSON'
{"type":"system","subtype":"init","session_id":"workspace-engine"}
{"type":"assistant","message":{"content":[{"type":"text","text":"Finished."}]}}
{"type":"result","subtype":"success","is_error":false,"session_id":"workspace-engine","result":"Finished."}
JSON
`
	if err := os.WriteFile(engine, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	host, sender := realSandboxHost(t, map[string]string{"claude": engine})
	root := filepath.Join(t.TempDir(), "dispatch")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	authority := &testWorkspaceAuthority{}
	var deliveredResults []protocol.WorkspaceMemberResult
	host.cfg.Workspaces = &LocalWorkspaceService{CommitFinal: acknowledgeWorkspaceFinal, Results: func(_ context.Context, _ string, b protocol.WorkspaceRuntimeBinding, results []protocol.WorkspaceMemberResult) error {
		if b.Generation != 7 {
			return errors.New("missing result generation")
		}
		deliveredResults = results
		return nil
	}, Authority: authority, Supervisor: &testWorkspaceSupervisor{}, Storage: &realWorkerTestStorage{storage: &testWorkspaceStorage{root: root}}, Resolve: func(context.Context, protocol.SpawnSession) ([]WorkspaceSource, string, error) {
		return sources, "", nil
	}}
	host.cfg.Workspaces.CancelStatus = func(ctx context.Context, id string) error { host.explorer.CancelSession(id); return ctx.Err() }
	msg.InitialPrompt = "work"
	host.Spawn(msg)
	sender.waitForKind(t, "turn_done")
	if authority.bound == nil || authority.bound.Root.Inode == 0 {
		t.Fatal("agent started without runtime authority")
	}
	if got := childRepoGit(t, sources[0].Path, "show", "wip/service-session:committed.txt"); got != "committed work" {
		t.Fatal("turn-end member sync missing")
	}
	local := host.get(msg.SessionID).localWorkspace
	local.mu.Lock()
	if local.state.Unavailable {
		t.Fatal("turn sync did not restore workspace availability")
	}
	local.mu.Unlock() // wait for the completed turn's thaw/save
	request := protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", RequestID: "production-status", SessionID: msg.SessionID, WorkspaceRuntimeRef: authority.bound.WorkspaceRuntimeRef, Operation: "status", Path: "members/one", MemberID: "one"}
	raw, _ := json.Marshal(request)
	host.HandleWorkspaceExplorer(t.Context(), raw)
	if result := waitExplorerResult(t, sender, request.RequestID); !result.OK || result.Status == nil || result.Status.Branch != "wip/service-session" {
		t.Fatalf("production shared-clone status: %+v", result)
	}
	host.ReadWorkspaceFile(protocol.ReadWorkspaceFile{Type: "read_workspace_file", RequestID: "production-file", SessionID: msg.SessionID, WorkspaceRuntimeRef: authority.bound.WorkspaceRuntimeRef, Path: "/workspace/members/one/committed.txt"})
	if result := waitWorkspaceResult(t, sender, "production-file"); !result.OK || result.Path != "members/one/committed.txt" || result.RequestedPath != "/workspace/members/one/committed.txt" {
		t.Fatalf("production shared-clone file: %+v", result)
	}
	host.Kill(msg.SessionID)
	var ended *protocol.SessionEnded
	sender.mu.Lock()
	for _, message := range sender.msgs {
		if value, ok := message.(protocol.SessionEnded); ok && value.SessionID == msg.SessionID {
			snapshot := value
			ended = &snapshot
		}
	}
	sender.mu.Unlock()
	if ended == nil || len(deliveredResults) != 2 || ended.ExitCode != 0 {
		t.Fatalf("member end results missing: %+v", ended)
	}
	if got := childRepoGit(t, sources[1].Path, "show", "wip/service-session:leftover.txt"); got != "leftover work" {
		t.Fatal("stop settlement lost member work")
	}
}

type testWorkspaceAuthority struct {
	bindErr error
	stale   bool
	bound   *protocol.WorkspaceRuntimeBinding
}

func (a *testWorkspaceAuthority) Launch(_ context.Context, session string, m protocol.WorkspaceManifest, _ string) (protocol.SupervisorLaunch, error) {
	return protocol.SupervisorLaunch{SessionID: session, LaunchGeneration: m.LaunchGeneration, RuntimeGeneration: 7, Runtime: "sandbox", ConnectionID: "authenticated-connection"}, nil
}
func (a *testWorkspaceAuthority) Bind(_ context.Context, _ protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding) error {
	if a.bindErr != nil {
		return a.bindErr
	}
	a.bound = &b
	return nil
}
func (a *testWorkspaceAuthority) Current(_ context.Context, _ protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding) error {
	if a.stale || a.bound == nil || *a.bound != b {
		return errors.New("stale runtime authority")
	}
	return nil
}

type testWorkspaceSupervisor struct {
	bad    bool
	phases []protocol.SupervisorPhase
}

func (s *testWorkspaceSupervisor) AdmitShell(context.Context, protocol.SupervisorShell) error {
	return nil
}
func (s *testWorkspaceSupervisor) Notify(_ context.Context, _ protocol.SupervisorLaunch, p protocol.SupervisorPhase) error {
	s.phases = append(s.phases, p)
	return nil
}
func (s *testWorkspaceSupervisor) CloseShell(_ context.Context, b protocol.SupervisorShell, _ string) (protocol.SupervisorCleanup, error) {
	return protocol.SupervisorCleanup{Launch: b.Launch, ShellGeneration: b.ShellGeneration, ContainmentEmpty: !s.bad, PTYsClosed: true, Reaped: true}, nil
}
func (s *testWorkspaceSupervisor) CloseLaunch(_ context.Context, b protocol.SupervisorLaunch, _ string) (protocol.SupervisorCleanup, error) {
	return protocol.SupervisorCleanup{Launch: b, ContainmentEmpty: !s.bad, PTYsClosed: true, Reaped: true}, nil
}
func (s *testWorkspaceSupervisor) RecoverLaunch(ctx context.Context, b protocol.SupervisorLaunch) (protocol.SupervisorCleanup, error) {
	return s.CloseLaunch(ctx, b, "recovery")
}

func TestWorkspaceBoundOperationsRejectStaleAuthority(t *testing.T) {
	w, r, _, _ := localWorkspaceFixture(t)
	b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: w.state.Launch.LaunchGeneration, Generation: 7}, ManifestDigest: w.state.Launch.Digest, Root: protocol.WorkspaceRoot{Inode: 1}}
	a := &testWorkspaceAuthority{bound: &b}
	w.state.Binding = &b
	w.authorize = func(ctx context.Context) error { return a.Current(ctx, protocol.SupervisorLaunch{}, b) }
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	ref := w.state.Members[0].Result.WorkspaceMemberRef
	called := false
	operation := func(context.Context, WorkspaceContainer, string) error { called = true; return nil }
	if err := w.WithBoundRuntimeMember(t.Context(), b.WorkspaceRuntimeRef, ref, operation); err != nil || !called {
		t.Fatal(err)
	}
	called = false
	a.stale = true
	if err := w.WithBoundRuntimeMember(t.Context(), b.WorkspaceRuntimeRef, ref, operation); err == nil || called {
		t.Fatal("stale connection admitted")
	}
	a.stale = false
	old := b.WorkspaceRuntimeRef
	old.Generation--
	if err := w.WithBoundRuntimeMember(t.Context(), old, ref, operation); err == nil {
		t.Fatal("stale generation admitted")
	}
	operation = func(context.Context, WorkspaceContainer, string) error { a.stale = true; return nil }
	if err := w.WithBoundRuntimeMember(t.Context(), b.WorkspaceRuntimeRef, ref, operation); err == nil {
		t.Fatal("stale delivery admitted")
	}
	_ = r
}
func TestWorkspaceSupervisorRejectsIncompleteLaunchCleanup(t *testing.T) {
	d := &DockerWorkspaceContainer{Supervisor: &testWorkspaceSupervisor{bad: true}, SupervisorLaunch: protocol.SupervisorLaunch{SessionID: "session", LaunchGeneration: "launch", RuntimeGeneration: 1, Runtime: "sandbox", ConnectionID: "connection"}}
	if err := d.StopWriters(t.Context()); err == nil || !strings.Contains(err.Error(), "cleanup unproven") {
		t.Fatalf("unsafe cleanup accepted: %v", err)
	}
}

func TestWorkspaceRuntimeBindingAwaitsCASAndFencesResults(t *testing.T) {
	w, _, _, _ := localWorkspaceFixture(t)
	a := &testWorkspaceAuthority{bindErr: errors.New("CAS refused")}
	delivered := 0
	service := &LocalWorkspaceService{Authority: a, Supervisor: &testWorkspaceSupervisor{}, CommitFinal: acknowledgeWorkspaceFinal, Results: func(_ context.Context, _ string, b protocol.WorkspaceRuntimeBinding, _ []protocol.WorkspaceMemberResult) error {
		if b.Generation != 7 {
			t.Fatal("result generation lost")
		}
		delivered++
		return nil
	}}
	d := &DockerWorkspaceContainer{}
	w.state.SupervisorLaunch, _ = a.Launch(t.Context(), w.state.Session, w.state.Launch, "test-token")
	root := protocol.WorkspaceRoot{Device: 2, Inode: 3}
	if err := service.bindAuthority(t.Context(), w, d, root); err == nil || w.state.Binding != nil {
		t.Fatal("CAS refusal accepted")
	}
	a.bindErr = nil
	if err := service.bindAuthority(t.Context(), w, d, root); err != nil {
		t.Fatal(err)
	}
	if w.state.Binding.Root != root || w.state.SupervisorLaunch.ConnectionID != "authenticated-connection" {
		t.Fatal("runtime authority lost")
	}
	if err := service.Report(t.Context(), w); err != nil || delivered != 1 {
		t.Fatal("bound result missing", err)
	}
	a.stale = true
	if err := service.Report(t.Context(), w); err == nil || delivered != 1 {
		t.Fatal("stale result delivered")
	}
}

func TestWorkspaceRecoveryChecksAuthorityBeforeEffects(t *testing.T) {
	w, r, storage, _ := localWorkspaceFixture(t)
	before := false
	_, err := recoverLocalWorkspace(t.Context(), w.state.Session, w.state.Launch, storage, r, func(context.Context) error { return errors.New("stale incarnation") }, func(context.Context) error { before = true; return nil })
	if err == nil || before || r.gone {
		t.Fatal("stale recovery performed lifecycle side effects")
	}
}

func TestP2DockerFolderChildUsesIndependentSandboxClones(t *testing.T) {
	parent, _ := p2DockerWorkspace(t)
	launch := parent.state.Launch
	launch.LaunchGeneration = "folder-child-generation"
	launch.Root = protocol.WorkspaceRoot{}
	launch.Members = append([]protocol.WorkspaceMember(nil), launch.Members[1])
	root := filepath.Join(t.TempDir(), "child-checkout")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	service := &LocalWorkspaceService{Authority: &testWorkspaceAuthority{}, Supervisor: &testWorkspaceSupervisor{}, Storage: &realWorkerTestStorage{storage: &testWorkspaceStorage{root: root}}, CommitFinal: acknowledgeWorkspaceFinal, Results: func(context.Context, string, protocol.WorkspaceRuntimeBinding, []protocol.WorkspaceMemberResult) error {
		return nil
	}, Resolve: func(context.Context, protocol.SpawnSession) ([]WorkspaceSource, string, error) {
		return nil, parent.state.Session, nil
	}, AuthorizeChild: func(_ context.Context, p, c string) error {
		if p != parent.state.Session || c != "folder-child" {
			return errors.New("child authorization refused")
		}
		return nil
	}}
	child, err := service.Prepare(t.Context(), protocol.SpawnSession{SessionID: "folder-child", Kind: "agent", Sandbox: true, Workspace: &launch, WorkspaceCapabilities: protocol.WorkspaceCapabilities{ManifestVersion: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.BeginRuntime(t.Context(), child); err != nil {
		t.Fatal(err)
	}
	plan, err := child.DockerMountPlan(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	label := "p2-folder-child-" + ccUUID()
	t.Cleanup(func() { cleanupP2Containers(t, label) })
	entryPolicy, err := workspaceEntrySecurityOption()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "--security-opt", entryPolicy, "-d", "--pull=never", "--network=none", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--label", "blerg-test-run=" + label}
	for _, label := range plan.Labels {
		args = append(args, "--label", label)
	}
	for _, mount := range plan.Mounts {
		args = append(args, "--mount", mount)
	}
	args = append(args, "--entrypoint", "sleep", sandboxImage(), "3600")
	id := strings.TrimSpace(p2Docker(t, args...))
	runtime, err := service.Runtime(t.Context(), child, id)
	if err != nil {
		t.Fatal(err)
	}
	m := child.state.Members[0]
	if m.Clone == parent.state.Members[1].Clone || m.Result.Branch != "wip/folder-child" {
		t.Fatal("child borrowed parent clone or branch")
	}
	p2Docker(t, "exec", "-w", "/workspace/"+m.Member.Path, runtime.ID, "sh", "-c", "printf child > child.txt; git add child.txt && git -c user.name=Test -c user.email=test@example.com commit -m child")
	if _, err := child.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := childRepoGit(t, m.Source.Repo, "show", "wip/folder-child:child.txt"); got != "child" {
		t.Fatal("child commit did not sync")
	}
	if _, err := os.Stat(filepath.Join(parent.state.Members[1].Clone, "child.txt")); !os.IsNotExist(err) {
		t.Fatal("child modified parent's checkout")
	}
	if _, err := child.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func (a *testWorkspaceAuthority) Abort(context.Context, string, protocol.WorkspaceManifest, string, protocol.SupervisorLaunch) error {
	a.bound = nil
	return nil
}
func (a *testWorkspaceAuthority) Recover(_ context.Context, l protocol.SupervisorLaunch, _ protocol.WorkspaceRuntimeBinding) (protocol.SupervisorLaunch, error) {
	return l, nil
}

func useWorkspaceTestContext(t *testing.T) {
	t.Helper()
	old := currentDockerContext
	currentDockerContext = func() string { return "test-peer" }
	t.Cleanup(func() { currentDockerContext = old })
}
