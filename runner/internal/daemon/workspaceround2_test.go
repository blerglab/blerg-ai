package daemon

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestP2DockerSettlementPreservesTrackedIgnoredBytes(t *testing.T) {
	if os.Getenv("BLERG_P2_DOCKER_TEST") != "1" {
		t.Skip("Docker opt-in")
	}
	w, _ := p2DockerWorkspace(t)
	sources := w.state.Sources
	w.storage = &realWorkerTestStorage{storage: w.storage.(*testWorkspaceStorage)}
	m := w.state.Members[0]
	childCommit(t, m.Clone, "valuable.txt")
	want := []byte("new valuable bytes\x00\r\nkeep exactly\n")
	if err := os.WriteFile(filepath.Join(m.Clone, "valuable.txt"), want, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.Clone, ".gitignore"), []byte("valuable.txt\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.Clone, ".git", "index"), []byte("agent index must never be parsed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := exec.Command("git", "-C", sources[0].Path, "show", "wip/p2-session:valuable.txt").Output()
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("saved bytes differ: %q %v", got, err)
	}
	if !w.state.StorageReleased {
		t.Fatal("proved saved checkout not released")
	}
}

func round2Runtime(ctx context.Context, w *LocalWorkspace) (string, error) {
	plan, err := w.DockerMountPlan(ctx)
	if err != nil {
		return "", err
	}
	policy, err := workspaceEntrySecurityOption()
	if err != nil {
		return "", err
	}
	args := []string{"--context", w.state.RuntimeContext, "run", "-d", "--pull=never", "--network=none", "--cap-drop=ALL", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--security-opt", policy, "--label", "blerg.runner.workspace-start=" + w.state.RuntimeToken}
	for _, label := range plan.Labels {
		args = append(args, "--label", label)
	}
	for _, mount := range plan.Mounts {
		args = append(args, "--mount", mount)
	}
	args = append(args, "--entrypoint", "sleep", sandboxImage(), "600")
	raw, err := workspaceDockerOutput(ctx, args...)
	if err != nil {
		return "", err
	}
	workspaceCrashBoundary("after-runtime-create")
	return strings.TrimSpace(string(raw)), nil
}
func TestP2DockerVMStorageHasNoHostCheckoutDependency(t *testing.T) { testP2DockerVMStorage(t) }
func testP2DockerVMStorage(t *testing.T) {
	parent, _ := p2DockerWorkspace(t)
	launch := parent.state.Launch
	launch.LaunchGeneration = "VM-generation"
	launch.Root = protocol.WorkspaceRoot{}
	storage := &DockerWorkspaceStorage{Root: filepath.Join(t.TempDir(), "journals")}
	service := &LocalWorkspaceService{Authority: &testWorkspaceAuthority{}, Supervisor: &testWorkspaceSupervisor{}, Storage: storage, Resolve: func(context.Context, protocol.SpawnSession) ([]WorkspaceSource, string, error) {
		return parent.state.Sources, "", nil
	}, CommitFinal: acknowledgeWorkspaceFinal, Results: func(context.Context, string, protocol.WorkspaceRuntimeBinding, []protocol.WorkspaceMemberResult) error {
		return nil
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	t.Cleanup(func() { cleanupVMStorageTest(t, storage, "vm-session") })
	w, err := service.Prepare(ctx, protocol.SpawnSession{SessionID: "vm-session", Kind: "agent", Sandbox: true, Workspace: &launch, WorkspaceCapabilities: protocol.WorkspaceCapabilities{ManifestVersion: 2, LinuxHost: true, Sandbox: true, Cluster: true}})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Lstat(w.state.Root); !os.IsNotExist(err) {
		t.Fatal("checkout unexpectedly depends on host mount")
	}
	if err := service.BeginRuntime(ctx, w); err != nil {
		t.Fatal(err)
	}
	id, err := round2Runtime(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := service.Runtime(ctx, w, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Remove(context.Background()) })
	member := w.state.Members[0]
	p2Docker(t, "exec", "-w", "/workspace/"+member.Member.Path, id, "sh", "-c", "printf exact-vm-bytes > vm.txt; git add vm.txt; git -c user.name=Test -c user.email=test@example.com commit -m vm")
	if _, err := w.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if got := childRepoGit(t, member.Source.Repo, "show", "wip/vm-session:vm.txt"); got != "exact-vm-bytes" {
		t.Fatal("VM snapshot not fetched")
	}
	if _, err := w.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if !w.state.StorageReleased {
		t.Fatal("VM quota allocation not released")
	}
}

func cleanupVMStorageTest(t *testing.T, s *DockerWorkspaceStorage, session string) {
	t.Helper()
	r, err := s.read(session)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Error(err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ids, err := workspaceDockerOutput(ctx, "--context", r.Context, "ps", "-aq", "--filter", "label=blerg.workspace.storage="+r.Token)
	if err != nil {
		t.Error(err)
		return
	}
	for _, id := range strings.Fields(string(ids)) {
		label, err := workspaceDockerOutput(ctx, "--context", r.Context, "inspect", "--format", `{{index .Config.Labels "blerg.workspace.storage"}}`, id)
		if err == nil && strings.TrimSpace(string(label)) == r.Token {
			_, _ = workspaceDockerOutput(ctx, "--context", r.Context, "rm", "-f", id)
		}
	}
	_ = s.removeVolume(ctx, r, r.dataVolume())
	label, err := workspaceDockerOutput(ctx, "--context", r.Context, "volume", "inspect", "--format", `{{index .Labels "blerg.workspace.storage"}}`, r.volume())
	if err == nil && strings.TrimSpace(string(label)) == r.Token {
		_, _ = workspaceDockerOutput(ctx, "--context", r.Context, "volume", "rm", r.volume())
	}
}

type reconnectWorkspaceAuthority struct {
	*testWorkspaceAuthority
	connection string
}

func (a *reconnectWorkspaceAuthority) Current(ctx context.Context, l protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding) error {
	if l.ConnectionID != a.connection {
		return fmt.Errorf("old connection")
	}
	return a.testWorkspaceAuthority.Current(ctx, l, b)
}
func (a *reconnectWorkspaceAuthority) Recover(_ context.Context, l protocol.SupervisorLaunch, _ protocol.WorkspaceRuntimeBinding) (protocol.SupervisorLaunch, error) {
	l.ConnectionID = a.connection
	return l, nil
}

func TestP2DockerFinalResultFailureSurvivesReconnect(t *testing.T) {
	for _, lostAck := range []bool{false, true} {
		t.Run(fmt.Sprint("lost-ack-", lostAck), func(t *testing.T) { testFinalResultReconnect(t, lostAck) })
	}
}
func testFinalResultReconnect(t *testing.T, lostAck bool) {
	w, d := p2DockerWorkspace(t)
	w.storage = &realWorkerTestStorage{storage: w.storage.(*testWorkspaceStorage)}
	a := &reconnectWorkspaceAuthority{testWorkspaceAuthority: &testWorkspaceAuthority{}, connection: "authenticated-connection"}
	s := &LocalWorkspaceService{Authority: a, Supervisor: &testWorkspaceSupervisor{}, Storage: w.storage, TrustedLaunch: func(context.Context, string) (protocol.WorkspaceManifest, error) { return w.state.Launch, nil }}
	w.state.SupervisorLaunch, _ = a.Launch(t.Context(), w.state.Session, w.state.Launch, "token")
	if err := s.bindAuthority(t.Context(), w, d, w.state.RuntimeRoot); err != nil {
		t.Fatal(err)
	}
	childCommit(t, w.state.Members[0].Clone, "final-head.txt")
	if err := os.WriteFile(filepath.Join(w.state.Members[1].Clone, ".git", "info", "attributes"), []byte("refuse\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := w.Results()
	terminals := 0
	attempts := 0
	var stored []protocol.WorkspaceMemberResult
	writes := 0
	s.CommitFinal = acknowledgeWorkspaceFinal
	s.Results = func(_ context.Context, _ string, _ protocol.WorkspaceRuntimeBinding, results []protocol.WorkspaceMemberResult) error {
		attempts++
		if lostAck {
			stored = append([]protocol.WorkspaceMemberResult(nil), results...)
			writes++
		}
		return fmt.Errorf("result transport unavailable")
	}
	message := protocol.SessionEnded{Type: "session_ended", SessionID: w.state.Session, ExitCode: 1, Message: "retained member"}
	send := func(protocol.SessionEnded) error { terminals++; return nil }
	if err := s.Finalize(t.Context(), w, message, send); err == nil || terminals != 0 {
		t.Fatal("terminal delivered after failed results")
	}
	// Recreate the service and peer connection from authenticated durable state.
	freshAuthority := &reconnectWorkspaceAuthority{testWorkspaceAuthority: &testWorkspaceAuthority{bound: a.bound}, connection: "new-authenticated-connection"}
	fresh := &LocalWorkspaceService{Authority: freshAuthority, Supervisor: &testWorkspaceSupervisor{}, Storage: w.storage, TrustedLaunch: s.TrustedLaunch}
	fresh.CommitFinal = acknowledgeWorkspaceFinal
	fresh.Results = func(_ context.Context, _ string, b protocol.WorkspaceRuntimeBinding, got []protocol.WorkspaceMemberResult) error {
		attempts++
		if !reflect.DeepEqual(stored, got) {
			stored = append([]protocol.WorkspaceMemberResult(nil), got...)
			writes++
		}
		if !reflect.DeepEqual(got, want) || b != *a.bound {
			t.Fatal("final heads/errors/recovery paths changed")
		}
		return nil
	}
	if err := fresh.RecoverAll(t.Context(), send); err != nil {
		t.Fatal(err)
	}
	if terminals != 1 || attempts != 2 || writes != 1 {
		t.Fatalf("result/terminal ordering wrong: %d %d", attempts, terminals)
	}
	if err := fresh.RecoverAll(t.Context(), send); err != nil {
		t.Fatal(err)
	}
	if terminals != 1 || attempts != 2 {
		t.Fatal("acknowledged final delivery repeated")
	}
	saved, err := readPrivateWorkspace(w.record)
	if err != nil || !saved.ResultsAcknowledged || !saved.TerminalAcknowledged {
		t.Fatal("final acknowledgements not durable", err)
	}
}

func TestWorkspaceDarwinUsesLinuxImage(t *testing.T) {
	t.Setenv("BLERG_WORKSPACE_WORKER_IMAGE", "")
	image, binary, err := workspaceWorkerProgramForHost("darwin")
	if err != nil || image != "blerg-workspace-worker:latest" || binary != "" {
		t.Fatalf("Darwin worker refused or attempted Mach-O execution: %q %q %v", image, binary, err)
	}
}
func TestP2DockerImageWorkerRunsWithVMQuotaStorage(t *testing.T) {
	if os.Getenv("BLERG_P2_DOCKER_TEST") != "1" {
		t.Skip("Docker opt-in")
	}
	binary := filepath.Join(t.TempDir(), "linux-worker")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, goBinary, "build", "-o", binary, "./cmd/workspace-worker")
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOOS=linux", "CGO_ENABLED=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Linux image worker build: %v %s", err, output)
	}
	label := "p2-image-worker-" + ccUUID()
	builder := strings.TrimSpace(p2Docker(t, "create", "--label", "blerg-test-run="+label, "--entrypoint", "sleep", sandboxImage(), "600"))
	t.Cleanup(func() { cleanupP2Containers(t, label) })
	p2Docker(t, "cp", binary, builder+":/usr/local/bin/blerg-workspace-worker")
	image := "blerg-p2-image-" + ccUUID()
	t.Cleanup(func() {
		raw, err := exec.Command("docker", "image", "inspect", "--format", `{{index .Config.Labels "blerg-test-run"}}`, image).Output()
		if err == nil && strings.TrimSpace(string(raw)) == label {
			_ = exec.Command("docker", "image", "rm", image).Run()
		}
	})
	commit := exec.CommandContext(ctx, "docker", "commit", "--change", "LABEL blerg-test-run="+label, builder, image)
	if output, err := commit.CombinedOutput(); err != nil {
		t.Fatalf("commit owned worker image: %v %s", err, output)
	}

	// No host executable mount: identical selection to a Darwin host.
	t.Setenv("BLERG_WORKSPACE_WORKER_IMAGE", image)
	testP2DockerVMStorage(t)
}

func TestP2DockerSettlementRetainsUnprovedAndIgnoredBytes(t *testing.T) {
	for _, kind := range []string{"attribute-conversion", "ignored-untracked"} {
		t.Run(kind, func(t *testing.T) {
			w, _ := p2DockerWorkspace(t)
			w.storage = &realWorkerTestStorage{storage: w.storage.(*testWorkspaceStorage)}
			m := w.state.Members[0]
			var path string
			var want []byte
			if kind == "attribute-conversion" {
				childCommit(t, m.Clone, "valuable.txt")
				path = filepath.Join(m.Clone, "valuable.txt")
				want = []byte("raw\r\nbytes\r\n")
				if err := os.WriteFile(filepath.Join(m.Clone, ".gitattributes"), []byte("*.txt text eol=lf\n"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				path = filepath.Join(m.Clone, ".env")
				want = []byte("fixture-private-bytes\n")
				if err := os.WriteFile(filepath.Join(m.Clone, ".gitignore"), []byte(".env\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, want, 0600); err != nil {
				t.Fatal(err)
			}
			results, err := w.Stop(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if w.state.StorageReleased || results[0].RecoveryLocation == "" {
				t.Fatal("unproved bytes deleted")
			}
			actual, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(actual, want) {
				t.Fatal("retained bytes changed", err)
			}
			if kind == "ignored-untracked" {
				if output, err := exec.Command("git", "-C", m.Source.Repo, "show", "wip/p2-session:.env").Output(); err == nil || len(output) > 0 {
					t.Fatal("ignored private file force-added")
				}
			}
		})
	}
}

func TestP2DockerHistoricalRootOnlyRecordHasAuthenticatedAbort(t *testing.T) {
	w, d := p2DockerWorkspace(t)
	a := &historicalRuntimeAbort{testWorkspaceAuthority: &testWorkspaceAuthority{}, expected: "legacy-runtime:" + d.RuntimeID()}
	service := runtimeCrashService(w, a)
	if err := service.RecoverAll(t.Context(), func(protocol.SessionEnded) error { t.Fatal("unbound launch finalized"); return nil }); err != nil {
		t.Fatal(err)
	}
	if !a.authorized {
		t.Fatal("historical cleanup bypassed authority")
	}
	state, err := readPrivateWorkspace(w.record)
	if err != nil || state.RuntimeIdentity != "" || state.Stopped {
		t.Fatal("historical root-only record not recoverable", err)
	}
	if _, err := os.Stat(state.Members[0].Clone); err != nil {
		t.Fatal("historical abort discarded work", err)
	}
}

type historicalRuntimeAbort struct {
	*testWorkspaceAuthority
	expected   string
	authorized bool
}

func (a *historicalRuntimeAbort) Abort(_ context.Context, _ string, _ protocol.WorkspaceManifest, token string, _ protocol.SupervisorLaunch) error {
	if token != a.expected {
		return fmt.Errorf("historical runtime mismatch")
	}
	a.authorized = true
	return nil
}

func TestP2DockerAgentHostDefersTerminalUntilResultsAck(t *testing.T) {
	if os.Getenv("BLERG_P2_DOCKER_TEST") != "1" {
		t.Skip("Docker opt-in")
	}
	t.Setenv("BLERG_SANDBOX_CHILD_TEST", "1")
	_, msg, sources := workspaceServiceFixture(t)
	engine := filepath.Join(t.TempDir(), "claude")
	script := `#!/bin/sh
cat <<'JSON'
{"type":"system","subtype":"init","session_id":"final-engine"}
{"type":"assistant","message":{"content":[{"type":"text","text":"Finished."}]}}
{"type":"result","subtype":"success","is_error":false,"session_id":"final-engine","result":"Finished."}
JSON
`
	if err := os.WriteFile(engine, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	host, sender := realSandboxHost(t, map[string]string{"claude": engine})
	root := filepath.Join(t.TempDir(), "checkout")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	var fail atomic.Bool
	acknowledged := 0
	service := &LocalWorkspaceService{Authority: &testWorkspaceAuthority{}, Supervisor: &testWorkspaceSupervisor{}, Storage: &realWorkerTestStorage{storage: &testWorkspaceStorage{root: root}}, Resolve: func(context.Context, protocol.SpawnSession) ([]WorkspaceSource, string, error) {
		return sources, "", nil
	}, TrustedLaunch: func(context.Context, string) (protocol.WorkspaceManifest, error) { return *msg.Workspace, nil }, CommitFinal: acknowledgeWorkspaceFinal, Results: func(context.Context, string, protocol.WorkspaceRuntimeBinding, []protocol.WorkspaceMemberResult) error {
		if fail.Load() {
			return errors.New("result outage")
		}
		acknowledged++
		return nil
	}}
	host.cfg.Workspaces = service
	msg.InitialPrompt = "work"
	host.Spawn(msg)
	sender.waitForKind(t, "turn_done")
	sess := host.get(msg.SessionID)
	if sess == nil {
		t.Fatal("agent did not start")
	}
	// Reconnect must operate on this live AgentHost's existing workspace handle.
	liveID := sess.localWorkspace.runtime.(*DockerWorkspaceContainer).ID
	manager := &Manager{config: ManagerConfig{Workspaces: service, Command: []string{"test"}}, agents: host, client: sender}
	if err := manager.recoverWorkspaces(t.Context()); err != nil {
		t.Fatal(err)
	}
	if host.get(msg.SessionID) != sess || sess.localWorkspace.state.Stopped {
		t.Fatal("reconnect killed live agent")
	}
	if got := p2Docker(t, "inspect", "--format", "{{.Id}}", liveID); got != liveID {
		t.Fatal("reconnect replaced runtime")
	}
	if _, err := sess.localWorkspace.Sync(t.Context()); err != nil {
		t.Fatal("reconnected agent workspace unusable", err)
	}
	fail.Store(true)
	host.Kill(msg.SessionID)
	countTerminal := func() int {
		sender.mu.Lock()
		defer sender.mu.Unlock()
		n := 0
		for _, m := range sender.msgs {
			if e, ok := m.(protocol.SessionEnded); ok && e.SessionID == msg.SessionID {
				n++
			}
		}
		return n
	}
	if !slices.Contains(manager.ActiveSessionIDs(), msg.SessionID) || !slices.Contains(host.ActiveIDs(), msg.SessionID) || !slices.Contains(host.RecoveryRequiredSessions(), msg.SessionID) {
		t.Fatal("actual AgentHost outage omitted finalization from liveness")
	}
	if countTerminal() != 0 {
		t.Fatal("AgentHost delivered terminal despite failed result acknowledgement")
	}
	if sess.record != nil {
		sess.record.mu.Lock()
		deleted := sess.record.deleted
		sess.record.mu.Unlock()
		if deleted {
			t.Fatal("agent recovery record deleted before result acknowledgement")
		}
	}
	record, err := workspaceRecord(msg.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := readPrivateWorkspace(record)
	if err != nil || pending.FinalDelivery == nil || pending.ResultsAcknowledged {
		t.Fatal("retryable final results not persisted", err)
	}
	before := acknowledged
	fail.Store(false)
	if err := manager.recoverWorkspaces(t.Context()); err != nil {
		t.Fatal(err)
	}
	if countTerminal() != 1 || acknowledged != before+1 {
		t.Fatal("terminal/result acknowledgement ordering incorrect")
	}
}

func TestP2DockerSettlementProvesFetchedObjectBytesBeforeRelease(t *testing.T) {
	w, _ := p2DockerWorkspace(t)
	w.storage = &realWorkerTestStorage{storage: w.storage.(*testWorkspaceStorage)}
	m := w.state.Members[0]
	blob := childRepoGit(t, m.Source.Repo, "rev-parse", m.Source.Base+":source-one.txt")
	object := filepath.Join(m.Source.Objects, blob[:2], blob[2:])
	var corrupt bytes.Buffer
	writer := zlib.NewWriter(&corrupt)
	if _, err := writer.Write([]byte("blob 4\x00evil")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(object, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(object, corrupt.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	results, err := w.Stop(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(results[0].SyncError, "fetched tree does not hold exact saved bytes") {
		t.Fatalf("did not exercise fetched blob comparison: %+v", results[0])
	}
	if !results[0].Pending || results[0].RecoveryLocation == "" || w.state.StorageReleased {
		t.Fatal("corrupt fetched object permitted checkout release")
	}
	original, err := os.ReadFile(filepath.Join(m.Clone, "source-one.txt"))
	if err != nil || string(original) != "source-one.txt\n" {
		t.Fatal("unsaved bytes discarded", err)
	}
}
