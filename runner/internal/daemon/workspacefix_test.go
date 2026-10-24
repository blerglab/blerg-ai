package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceExpiredCopyDeadlineStartsNoGit(t *testing.T) {
	w, _, _, _ := localWorkspaceFixture(t)
	marker := filepath.Join(t.TempDir(), "started")
	bin := t.TempDir()
	script := "#!/bin/sh\nprintf started > '" + marker + "'\nsleep 1\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	m := w.state.Members[0]
	if _, err := fetchSandboxClone(m.Clone, w.state.Session, &m.Source, time.Now().Add(-time.Second)); err == nil {
		t.Fatal("expired copy admitted")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("Git started after copy deadline")
	}
}

type lostWorkspaceRemoval struct{ *testWorkspaceRuntime }

func (r *lostWorkspaceRemoval) Remove(context.Context) error {
	r.gone = true
	return errors.New("acknowledgement lost")
}
func TestWorkspaceRecoveryAfterLostRemovalAcknowledgement(t *testing.T) {
	w, r, s, _ := localWorkspaceFixture(t)
	childCommit(t, w.state.Members[0].Clone, "saved.txt")
	w.runtime = &lostWorkspaceRemoval{r}
	if _, err := w.Stop(t.Context()); err == nil {
		t.Fatal("expected lost removal acknowledgement")
	}
	recovered, err := RecoverLocalWorkspace(t.Context(), w.state.Session, w.state.Launch, s, r)
	if err != nil {
		t.Fatal("exact-ID absence did not recover:", err)
	}
	results, err := recovered.Stop(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Pending || results[0].Head == results[0].Base {
		t.Fatalf("offline copy lost work: %+v", results)
	}
}

func TestWorkspaceRecoverMemberReleaseIntentAfterDeletion(t *testing.T) {
	w, r, s, _ := localWorkspaceFixture(t)
	w.state.Stopped = true
	r.gone = true
	m := &w.state.Members[0]
	m.SettledClean = true
	m.SavedTreeHead = m.Result.Head
	m.Result.Pending = false
	m.Result.SyncError = ""
	m.ReleasePending = true
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(m.Clone); err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverLocalWorkspace(t.Context(), w.state.Session, w.state.Launch, s, r)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered.state.Members[0].Released || recovered.state.Members[0].ReleasePending {
		t.Fatal("deleted member release did not reconcile")
	}
}
func TestWorkspaceRecoverAllocationReleaseAfterDeletion(t *testing.T) {
	w, r, s, _ := localWorkspaceFixture(t)
	w.state.Stopped = true
	w.state.AllocationReleasePending = true
	r.gone = true
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	if err := s.Release(t.Context(), w.state.Session, w.state.Root); err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverLocalWorkspace(t.Context(), w.state.Session, w.state.Launch, s, r)
	if err != nil {
		t.Fatal(err)
	}
	if !recovered.state.StorageReleased {
		t.Fatal("allocation release did not reconcile")
	}
}
func TestWorkspaceRecoverPartialPreparation(t *testing.T) {
	w, _, s, _ := localWorkspaceFixture(t)
	w.runtime = nil
	w.state.RuntimeIdentity = ""
	w.state.Preparing = true
	w.state.Members = w.state.Members[:1]
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverLocalWorkspace(t.Context(), w.state.Session, w.state.Launch, s, nil)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.state.Preparing || len(recovered.state.Members) != 2 {
		t.Fatal("partial preparation did not complete")
	}
}

func cleanupP2Containers(t *testing.T, label string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "label=blerg-test-run="+label).Output()
	if err == nil {
		for _, id := range strings.Fields(string(raw)) {
			_ = exec.CommandContext(ctx, "docker", "rm", "-f", id).Run()
		}
	}
}

type realWorkerTestStorage struct{ storage *testWorkspaceStorage }

func (s *realWorkerTestStorage) Allocate(ctx context.Context, session string, b protocol.WorkspaceBudget) (string, error) {
	return s.storage.Allocate(ctx, session, b)
}
func (s *realWorkerTestStorage) Validate(ctx context.Context, session, root string, b protocol.WorkspaceBudget) error {
	return s.storage.Validate(ctx, session, root, b)
}
func (s *realWorkerTestStorage) Release(ctx context.Context, session, root string) error {
	return s.storage.Release(ctx, session, root)
}

func TestP2DockerCappedCopyWorker(t *testing.T) {
	w, _ := p2DockerWorkspace(t)
	fake := w.storage.(*testWorkspaceStorage)
	w.storage = &realWorkerTestStorage{storage: fake}
	childCommit(t, w.state.Members[0].Clone, "capped.txt")
	results, err := w.Sync(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Pending || results[0].SyncError != "" {
		t.Fatalf("bounded worker did not copy: %+v", results)
	}
}

func TestP2DockerContainedSettlementIgnoresAgentFilters(t *testing.T) {
	w, _ := p2DockerWorkspace(t)
	fake := w.storage.(*testWorkspaceStorage)
	w.storage = &realWorkerTestStorage{storage: fake}
	m := w.state.Members[0]
	if err := os.WriteFile(filepath.Join(m.Clone, "uncommitted.txt"), []byte("keep this work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.Clone, ".gitattributes"), []byte("*.txt filter=agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(m.Clone, ".git", "config"), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("\n[filter \"agent\"]\nclean = touch /workspace/filter-ran\nrequired = true\n")
	_ = f.Close()
	results, err := w.Stop(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Pending || results[0].RecoveryLocation != "" {
		t.Fatalf("contained settlement failed: %+v", results)
	}
	text := childRepoGit(t, m.Source.Repo, "show", "wip/p2-session:uncommitted.txt")
	if text != "keep this work" {
		t.Fatal("uncommitted work lost")
	}
	if _, err := os.Stat(filepath.Join(w.state.Root, "filter-ran")); !os.IsNotExist(err) {
		t.Fatal("agent filter executed")
	}
}

type workspaceCrashFixture struct{ Record, Root, Gone, Stage string }

func workspaceCrashTestMain(args []string) bool {
	if len(args) != 2 || args[0] != "--blerg-workspace-crash-test" {
		return false
	}
	var fixture workspaceCrashFixture
	raw, err := os.ReadFile(args[1])
	if err != nil {
		os.Exit(125)
	}
	if json.Unmarshal(raw, &fixture) != nil {
		os.Exit(125)
	}
	state, err := readPrivateWorkspace(fixture.Record)
	if err != nil {
		os.Exit(125)
	}
	w := &LocalWorkspace{mu: workspaceLock(state.Session), record: fixture.Record, state: state, storage: &testWorkspaceStorage{root: fixture.Root}}
	runtime := &testWorkspaceRuntime{w: w, clean: true}
	w.runtime = &crashWorkspaceRuntime{testWorkspaceRuntime: runtime, gone: fixture.Gone}
	workspaceCrashHook = func(stage string) {
		if stage == fixture.Stage {
			os.Exit(86)
		}
	}
	if state.Preparing {
		_, err = RecoverLocalWorkspace(context.Background(), state.Session, state.Launch, w.storage, nil)
	} else {
		_, err = w.Stop(context.Background())
	}
	if err != nil {
		os.Exit(125)
	}
	os.Exit(0)
	return true
}

type crashWorkspaceRuntime struct {
	*testWorkspaceRuntime
	gone string
}

func (r *crashWorkspaceRuntime) Remove(context.Context) error {
	return os.WriteFile(r.gone, []byte("gone"), 0o600)
}
func (r *crashWorkspaceRuntime) Absent(context.Context) (bool, error) {
	_, err := os.Stat(r.gone)
	return err == nil, nil
}

func TestWorkspaceProcessCrashJournals(t *testing.T) {
	for _, stage := range []string{"after-remove", "after-member-delete", "after-allocation-release", "after-clone"} {
		t.Run(stage, func(t *testing.T) {
			w, _, storage, _ := localWorkspaceFixture(t)
			childCommit(t, w.state.Members[0].Clone, "crash-work.txt")
			gone := filepath.Join(t.TempDir(), "runtime-gone")
			if stage == "after-clone" {
				w.state.Preparing = true
				w.state.RuntimeIdentity = ""
				w.state.Members = w.state.Members[:1]
			}
			if err := w.save(); err != nil {
				t.Fatal(err)
			}
			fixture := workspaceCrashFixture{Record: w.record, Root: w.state.Root, Gone: gone, Stage: stage}
			raw, err := json.Marshal(fixture)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "fixture.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary, "--blerg-workspace-crash-test", path)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 86 {
				t.Fatalf("crash boundary not reached: %v %s", err, out)
			}
			runtime := &crashWorkspaceRuntime{testWorkspaceRuntime: &testWorkspaceRuntime{w: w}, gone: gone}
			var peer WorkspaceContainer = runtime
			if stage == "after-clone" {
				peer = nil
			}
			recovered, err := RecoverLocalWorkspace(t.Context(), w.state.Session, w.state.Launch, storage, peer)
			if err != nil {
				t.Fatal("process-crash recovery:", err)
			}
			if stage == "after-clone" {
				if recovered.state.Preparing || len(recovered.state.Members) != 2 {
					t.Fatal("preparation incomplete")
				}
				return
			}
			results, err := recovered.Stop(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if results[0].Pending {
				t.Fatalf("crash work not copied: %+v", results)
			}
			if got := childRepoGit(t, w.state.Members[0].Source.Repo, "show", "wip/p2-session:crash-work.txt"); got != "crash-work.txt" {
				t.Fatal("crash lost commits")
			}
		})
	}
}

func TestP2DockerWorkerRejectsUnboundedCgroup(t *testing.T) {
	if os.Getenv("BLERG_P2_DOCKER_TEST") != "1" {
		t.Skip("set BLERG_P2_DOCKER_TEST=1")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	label := "p2-worker-bound-" + ccUUID()
	t.Cleanup(func() { cleanupP2Containers(t, label) })
	request := workspaceWorkerRequest{Action: "not-a-command", Deadline: time.Now().Add(time.Minute)}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "docker", "run", "--rm", "-i", "--pull=never", "--network=none", "--label", "blerg-test-run="+label, "--mount", sandboxBindMount(binary, "/worker-test")+",readonly", "--entrypoint", "/worker-test", sandboxImage(), workspaceWorkerFlag)
	command.Stdin = strings.NewReader(string(raw))
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("worker failed before refusal: %v %s", err, out)
	}
	var response workspaceWorkerResponse
	if err := json.Unmarshal(out, &response); err != nil || !strings.Contains(response.Error, "aggregate workspace worker memory") {
		t.Fatalf("unbounded worker accepted: %s %v", out, err)
	}
}

type uncertainWorkspaceProbe struct{ *testWorkspaceRuntime }

func (r *uncertainWorkspaceProbe) Head(context.Context, string) (string, error) {
	return "", &workspaceProbeUncertain{errors.New("lost exec acknowledgement")}
}
func TestWorkspaceProbeTimeoutPersistsTerminalFence(t *testing.T) {
	w, r, s, _ := localWorkspaceFixture(t)
	w.runtime = &uncertainWorkspaceProbe{r}
	if _, err := w.Sync(t.Context()); err == nil {
		t.Fatal("uncertain probe accepted")
	}
	if !w.state.Unavailable || !w.state.ProbeUncertain || r.freeze != 0 {
		t.Fatal("uncertain probe did not fence before freeze")
	}
	if _, err := w.Sync(t.Context()); err == nil {
		t.Fatal("second probe admitted")
	}
	recovered, err := RecoverLocalWorkspace(t.Context(), w.state.Session, w.state.Launch, s, r)
	if err != nil {
		t.Fatal(err)
	}
	if !r.gone || !recovered.state.Stopped {
		t.Fatal("uncertain runtime resumed before removal proof")
	}
}

func TestWorkspaceRecoverPreparedBeforeRuntimeCreation(t *testing.T) {
	w, _, s, _ := localWorkspaceFixture(t)
	w.state.RuntimeIdentity = ""
	w.runtime = nil
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverLocalWorkspace(t.Context(), w.state.Session, w.state.Launch, s, nil)
	if err != nil {
		t.Fatal("prepared allocation could not resume dispatch:", err)
	}
	if recovered.state.Stopped || len(recovered.state.Members) != 2 {
		t.Fatal("prepared membership lost")
	}
}
