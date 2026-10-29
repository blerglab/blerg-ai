package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type runtimeServerFixture struct {
	Token   string
	Launch  protocol.SupervisorLaunch
	Binding *protocol.WorkspaceRuntimeBinding
	Aborted bool
}
type durableRuntimeAuthority struct {
	path, record string
	lostBind     bool
}

func (a *durableRuntimeAuthority) read() (runtimeServerFixture, error) {
	var s runtimeServerFixture
	raw, err := os.ReadFile(a.path)
	if err == nil {
		err = json.Unmarshal(raw, &s)
	}
	return s, err
}
func (a *durableRuntimeAuthority) save(s runtimeServerFixture) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeWorkspacePrivate(a.path, raw)
}
func (a *durableRuntimeAuthority) Launch(_ context.Context, id string, m protocol.WorkspaceManifest, token string) (protocol.SupervisorLaunch, error) {
	private, err := readPrivateWorkspace(a.record)
	if err != nil || private.RuntimeToken != token || !private.RuntimePreparing {
		return protocol.SupervisorLaunch{}, errors.New("launch intent not durable before generation allocation")
	}
	old, err := a.read()
	generation := int64(7)
	if err == nil {
		if old.Token == token {
			return old.Launch, nil
		}
		if !old.Aborted {
			return protocol.SupervisorLaunch{}, errors.New("old generation not fenced")
		}
		generation = old.Launch.RuntimeGeneration + 1
	} else if !os.IsNotExist(err) {
		return protocol.SupervisorLaunch{}, err
	}
	launch := protocol.SupervisorLaunch{SessionID: id, LaunchGeneration: m.LaunchGeneration, RuntimeGeneration: generation, Runtime: "sandbox", ConnectionID: "trusted-connection"}
	return launch, a.save(runtimeServerFixture{Token: token, Launch: launch})
}
func (a *durableRuntimeAuthority) Bind(_ context.Context, l protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding) error {
	private, err := readPrivateWorkspace(a.record)
	if err != nil || private.BindingIntent == nil || *private.BindingIntent != b || private.SupervisorLaunch != l {
		return errors.New("binding intent not durable before CAS")
	}
	s, err := a.read()
	if err != nil {
		return err
	}
	if s.Launch != l || s.Aborted {
		return errors.New("CAS not authorized")
	}
	if s.Binding != nil && *s.Binding != b {
		return errors.New("binding replacement refused")
	}
	s.Binding = &b
	if err := a.save(s); err != nil {
		return err
	}
	if a.lostBind {
		return errors.New("CAS acknowledgement lost")
	}
	return nil
}
func (a *durableRuntimeAuthority) Current(_ context.Context, l protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding) error {
	s, err := a.read()
	if err != nil {
		return err
	}
	if s.Aborted || s.Launch != l || s.Binding == nil || *s.Binding != b {
		return errors.New("stale authority")
	}
	return nil
}
func (a *durableRuntimeAuthority) Abort(_ context.Context, id string, m protocol.WorkspaceManifest, token string, l protocol.SupervisorLaunch) error {
	s, err := a.read()
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if s.Token != token || s.Launch.SessionID != id || s.Launch.LaunchGeneration != m.LaunchGeneration || (l.RuntimeGeneration != 0 && l != s.Launch) {
		return errors.New("abort authority refused")
	}
	s.Aborted = true
	return a.save(s)
}
func (a *durableRuntimeAuthority) Recover(ctx context.Context, l protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding) (protocol.SupervisorLaunch, error) {
	return l, a.Current(ctx, l, b)
}

type runtimeCrashFixture struct{ Record, Root, Server, Stage string }

func workspaceRound2CrashMain(args []string) bool {
	if workspaceRound3QuotaCrashMain(args) {
		return true
	}
	if len(args) != 2 || args[0] != "--p2-runtime-crash" {
		return false
	}
	raw, err := os.ReadFile(args[1])
	if err != nil {
		os.Exit(2)
	}
	var f runtimeCrashFixture
	if json.Unmarshal(raw, &f) != nil {
		os.Exit(2)
	}
	_ = os.Setenv(DaemonStateDirEnv, filepath.Dir(filepath.Dir(f.Record)))
	state, err := readPrivateWorkspace(f.Record)
	if err != nil {
		os.Exit(2)
	}
	w := &LocalWorkspace{mu: workspaceLock(state.Session), state: state, record: f.Record, storage: &testWorkspaceStorage{root: f.Root}}
	authority := &durableRuntimeAuthority{path: f.Server, record: f.Record, lostBind: f.Stage == "after-runtime-abort" || f.Stage == "after-pending-runtime-remove"}
	service := runtimeCrashService(w, authority)
	workspaceCrashHook = func(stage string) {
		if stage == f.Stage {
			os.Exit(86)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	if service.BeginRuntime(ctx, w) != nil {
		cancel()
		os.Exit(3)
	}
	id, err := round2Runtime(ctx, w)
	if err != nil {
		cancel()
		os.Exit(4)
	}
	if _, err := service.Runtime(ctx, w, id); err != nil && authority.lostBind {
		_ = service.RecoverAll(ctx, func(protocol.SessionEnded) error { return nil })
	}
	if f.Stage == "after-stop-intent" || f.Stage == "after-final-results" || f.Stage == "after-terminal-delivery" || f.Stage == "after-terminal-commit" || f.Stage == "after-terminal-receipt" {
		if f.Stage == "after-stop-intent" {
			_ = w.RequestStop(protocol.SessionEnded{Type: "session_ended", SessionID: state.Session, ExitCode: 1, Message: "exact interrupted teardown"})
		}
		if _, err := w.Stop(ctx); err != nil {
			cancel()
			os.Exit(6)
		}
		message := protocol.SessionEnded{Type: "session_ended", SessionID: state.Session}
		if err := service.Finalize(ctx, w, message, func(message protocol.SessionEnded) error { return runtimeFixtureTerminal(f.Server, message) }); err != nil {
			cancel()
			os.Exit(7)
		}
	}
	cancel()
	os.Exit(5)
	return true
}
func runtimeCrashService(w *LocalWorkspace, a WorkspaceRuntimeAuthority) *LocalWorkspaceService {
	return &LocalWorkspaceService{Authority: a, Supervisor: &testWorkspaceSupervisor{}, Storage: w.storage, TrustedLaunch: func(context.Context, string) (protocol.WorkspaceManifest, error) { return w.state.Launch, nil }, CommitFinal: func(ctx context.Context, launch protocol.SupervisorLaunch, f WorkspaceFinalization) (WorkspaceFinalReceipt, error) {
		if peer, ok := a.(*durableRuntimeAuthority); ok {
			raw, _ := json.Marshal(f)
			if _, err := os.Stat(peer.path + ".completion"); os.IsNotExist(err) {
				if err := peer.Current(ctx, launch, f.Binding); err != nil {
					return WorkspaceFinalReceipt{}, err
				}
			}
			if err := runtimeFixtureOnce(peer.path+".completion", raw); err != nil {
				return WorkspaceFinalReceipt{}, err
			}
			if err := runtimeFixtureTerminal(peer.path, f.Message); err != nil {
				return WorkspaceFinalReceipt{}, err
			}
			workspaceCrashBoundary("after-terminal-commit")
		}
		return acknowledgeWorkspaceFinal(ctx, launch, f)
	}, Results: func(_ context.Context, _ string, b protocol.WorkspaceRuntimeBinding, results []protocol.WorkspaceMemberResult) error {
		if peer, ok := a.(*durableRuntimeAuthority); ok {
			if _, err := os.Stat(peer.path + ".terminal"); err == nil {
				return errors.New("results cannot be persisted after terminal")
			}
			raw, err := json.Marshal(struct {
				Binding protocol.WorkspaceRuntimeBinding
				Results []protocol.WorkspaceMemberResult
			}{b, results})
			if err != nil {
				return err
			}
			return runtimeFixtureOnce(peer.path+".results", raw)
		}
		return nil
	}}
}
func TestP2DockerRuntimeBindingProcessCrashTransactions(t *testing.T) {
	if os.Getenv("BLERG_P2_DOCKER_TEST") != "1" {
		t.Skip("Docker opt-in")
	}
	old := currentDockerContext
	currentDockerContext = realDockerContext
	t.Cleanup(func() { currentDockerContext = old })
	for _, stage := range []string{"before-runtime-launch", "after-runtime-launch", "after-runtime-create", "before-runtime-root-save", "after-runtime-root-save", "before-runtime-cas", "after-runtime-cas", "before-runtime-ready-save", "after-runtime-ready-save", "after-runtime-abort", "after-pending-runtime-remove", "after-final-results", "after-terminal-delivery", "after-terminal-commit", "after-terminal-receipt", "after-stop-intent"} {
		t.Run(stage, func(t *testing.T) {
			w, _, storage, _ := localWorkspaceFixture(t)
			w.state.RuntimeIdentity = ""
			w.state.RuntimeRoot = protocol.WorkspaceRoot{}
			w.runtime = nil
			if err := w.save(); err != nil {
				t.Fatal(err)
			}
			fixture := runtimeCrashFixture{Record: w.record, Root: storage.root, Server: filepath.Join(t.TempDir(), "server.json"), Stage: stage}
			raw, _ := json.Marshal(fixture)
			path := filepath.Join(t.TempDir(), "fixture.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(binary, "--p2-runtime-crash", path)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			state, readErr := readPrivateWorkspace(w.record)
			if readErr != nil {
				t.Fatal(readErr)
			}
			token := state.RuntimeToken
			t.Cleanup(func() { cleanupRound2Runtime(t, state.RuntimeContext, token) })
			if !errors.As(err, &exit) || exit.ExitCode() != 86 {
				t.Fatalf("required crash boundary not reached: %v %s", err, output)
			}
			authority := &durableRuntimeAuthority{path: fixture.Server, record: w.record}
			service := runtimeCrashService(w, authority)
			resultCalls := 0
			persist := service.Results
			service.Results = func(ctx context.Context, id string, b protocol.WorkspaceRuntimeBinding, r []protocol.WorkspaceMemberResult) error {
				resultCalls++
				return persist(ctx, id, b, r)
			}
			terminals := 0

			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			if err := service.RecoverAll(ctx, func(message protocol.SessionEnded) error {
				terminals++
				return runtimeFixtureTerminal(fixture.Server, message)
			}); err != nil {
				t.Fatal("pending runtime recovery failed:", err)
			}
			saved, err := readPrivateWorkspace(w.record)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "after-stop-intent" || stage == "after-runtime-ready-save" || stage == "after-final-results" || stage == "after-terminal-delivery" || stage == "after-terminal-commit" || stage == "after-terminal-receipt" {
				if !saved.TerminalAcknowledged || terminals != 1 {
					t.Fatal("ready launch not settled")
				}
				if stage == "after-stop-intent" && (saved.FinalDelivery.Message != "exact interrupted teardown" || saved.FinalDelivery.ExitCode != 1) {
					t.Fatal("lost teardown outcome across crash")
				}
				if stage == "after-terminal-delivery" && resultCalls != 0 {
					t.Fatal("replayed results after terminal was accepted")
				}
				if stage == "after-final-results" && resultCalls != 1 {
					t.Fatal("lost result acknowledgement was not replayed")
				}
			} else {
				if saved.RuntimePreparing || saved.RuntimeIdentity != "" || saved.Binding != nil || saved.Stopped || len(saved.Members) != 2 {
					t.Fatal("pending launch did not return to prepared state")
				}
				if _, err := os.Stat(saved.Members[0].Clone); err != nil {
					t.Fatal("abort discarded prepared work", err)
				}
			}
			if err := service.RecoverAll(ctx, func(message protocol.SessionEnded) error {
				terminals++
				return runtimeFixtureTerminal(fixture.Server, message)
			}); err != nil {
				t.Fatal("reconciliation not idempotent", err)
			}
		})
	}
}
func cleanupRound2Runtime(t *testing.T, dockerContext, token string) {
	t.Helper()
	if token == "" || dockerContext == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ids, err := workspaceDockerOutput(ctx, "--context", dockerContext, "ps", "-aq", "--no-trunc", "--filter", "label=blerg.runner.workspace-start="+token)
	if err != nil {
		t.Error(err)
		return
	}
	for _, id := range strings.Fields(string(ids)) {
		label, err := workspaceDockerOutput(ctx, "--context", dockerContext, "inspect", "--format", `{{index .Config.Labels "blerg.runner.workspace-start"}}`, id)
		if err == nil && strings.TrimSpace(string(label)) == token {
			_, _ = workspaceDockerOutput(ctx, "--context", dockerContext, "rm", "-f", id)
		}
	}
}
func TestWorkspaceEntryProfileDeniesEveryHardlinkVariant(t *testing.T) {
	var p struct {
		DefaultAction string
		Syscalls      []struct {
			Names    []string
			Action   string
			ErrnoRet int
		}
	}
	if err := json.Unmarshal(workspaceEntryProfile, &p); err != nil {
		t.Fatal(err)
	}
	if p.DefaultAction == "SCMP_ACT_ALLOW" {
		t.Fatal("Docker default restrictions relaxed")
	}
	counts := map[string]int{}
	for _, r := range p.Syscalls {
		for _, n := range r.Names {
			if n == "link" || n == "linkat" {
				if r.Action != "SCMP_ACT_ERRNO" || r.ErrnoRet != 18 {
					t.Fatal("hardlink alias permitted")
				}
				counts[n]++
			}
		}
	}
	if !reflect.DeepEqual(counts, map[string]int{"link": 1, "linkat": 1}) {
		t.Fatal("hardlink policy incomplete", counts)
	}
}

func TestP2DockerBindingLostAcknowledgementHasAuthenticatedAbort(t *testing.T) {
	if os.Getenv("BLERG_P2_DOCKER_TEST") != "1" {
		t.Skip("Docker opt-in")
	}
	old := currentDockerContext
	currentDockerContext = realDockerContext
	t.Cleanup(func() { currentDockerContext = old })
	w, _, _, _ := localWorkspaceFixture(t)
	w.state.RuntimeIdentity = ""
	w.runtime = nil
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	a := &durableRuntimeAuthority{path: filepath.Join(t.TempDir(), "server.json"), record: w.record, lostBind: true}
	service := runtimeCrashService(w, a)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	if err := service.BeginRuntime(ctx, w); err != nil {
		t.Fatal(err)
	}
	token := w.state.RuntimeToken
	t.Cleanup(func() { cleanupRound2Runtime(t, w.state.RuntimeContext, token) })
	id, err := round2Runtime(ctx, w)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Runtime(ctx, w, id); err == nil {
		t.Fatal("lost CAS acknowledgement accepted")
	}
	saved, err := readPrivateWorkspace(w.record)
	if err != nil || !saved.RuntimePreparing || saved.Binding != nil || saved.BindingIntent == nil {
		t.Fatal("lost acknowledgement not journalled", err)
	}
	refused := runtimeCrashService(w, &refuseRuntimeAbort{a})
	if err := refused.RecoverAll(ctx, func(protocol.SessionEnded) error { t.Fatal("pending launch finalized"); return nil }); err == nil {
		t.Fatal("unauthenticated abort accepted")
	}
	out, err := workspaceDockerOutput(ctx, "inspect", "--format", "{{.State.Running}}", id)
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatal("refused abort changed runtime")
	}
	a.lostBind = false
	if err := service.RecoverAll(ctx, func(protocol.SessionEnded) error { t.Fatal("pending launch finalized"); return nil }); err != nil {
		t.Fatal(err)
	}
	prepared := service.sessions[w.state.Session]
	if prepared == nil {
		t.Fatal("aborted allocation could not redispatch")
	}
	if err := service.BeginRuntime(ctx, prepared); err != nil {
		t.Fatal(err)
	}
	if prepared.state.SupervisorLaunch.RuntimeGeneration != 8 {
		t.Fatal("new dispatch reused aborted generation")
	}
	if err := service.abortRuntime(ctx, prepared); err != nil {
		t.Fatal(err)
	}
}

type refuseRuntimeAbort struct{ *durableRuntimeAuthority }

func (*refuseRuntimeAbort) Abort(context.Context, string, protocol.WorkspaceManifest, string, protocol.SupervisorLaunch) error {
	return errors.New("independent recovery authorization refused")
}

func TestWorkspaceFinalTerminalRechecksCurrentAuthority(t *testing.T) {
	w, _, _, _ := localWorkspaceFixture(t)
	w.state.Stopped = true
	b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: w.state.Launch.LaunchGeneration, Generation: 7}, ManifestDigest: w.state.Launch.Digest, Root: protocol.WorkspaceRoot{Inode: 1}}
	a := &testWorkspaceAuthority{bound: &b}
	w.state.Binding = &b
	w.authorize = func(ctx context.Context) error { return a.Current(ctx, protocol.SupervisorLaunch{}, b) }
	persisted, terminals := 0, 0
	service := &LocalWorkspaceService{CommitFinal: acknowledgeWorkspaceFinal, Results: func(context.Context, string, protocol.WorkspaceRuntimeBinding, []protocol.WorkspaceMemberResult) error {
		persisted++
		a.stale = true
		return nil
	}}
	service.CommitFinal = func(ctx context.Context, launch protocol.SupervisorLaunch, f WorkspaceFinalization) (WorkspaceFinalReceipt, error) {
		if err := w.authorize(ctx); err != nil {
			return WorkspaceFinalReceipt{}, err
		}
		return acknowledgeWorkspaceFinal(ctx, launch, f)
	}
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	message := protocol.SessionEnded{Type: "session_ended", SessionID: w.state.Session}
	send := func(protocol.SessionEnded) error { terminals++; return nil }
	if err := service.Finalize(t.Context(), w, message, send); err == nil || terminals != 0 || !w.state.ResultsAcknowledged {
		t.Fatal("stale terminal delivery admitted")
	}
	a.stale = false
	if err := service.Finalize(t.Context(), w, message, send); err != nil || terminals != 1 || persisted != 1 {
		t.Fatal("acknowledged results not replayable", err)
	}
}

type missingTreeProofStorage struct{ *testWorkspaceStorage }

func (s *missingTreeProofStorage) workspaceWorker() workspaceWorker { return missingTreeProofWorker{} }

type missingTreeProofWorker struct{ testDirectWorkspaceWorker }

func (w missingTreeProofWorker) Run(ctx context.Context, r workspaceWorkerRequest) (workspaceWorkerResponse, error) {
	response, err := w.testDirectWorkspaceWorker.Run(ctx, r)
	if r.Action == "copy" {
		for i := range response.State.Members {
			response.State.Members[i].SavedTreeHead = ""
		}
	}
	return response, err
}
func TestWorkspaceSingleMemberRequiresFetchedTreeProof(t *testing.T) {
	parent, _, _, sources := localWorkspaceFixture(t)
	launch := parent.state.Launch
	launch.Kind = "repo"
	launch.LaunchGeneration = "single-proof"
	launch.Members = append([]protocol.WorkspaceMember(nil), launch.Members[0])
	launch.Members[0].Path = "."
	source := sources[0]
	source.Member = launch.Members[0]
	storage := &testWorkspaceStorage{root: filepath.Join(t.TempDir(), "checkout")}
	if err := os.Mkdir(storage.root, 0700); err != nil {
		t.Fatal(err)
	}
	w, err := PrepareLocalWorkspace(t.Context(), "single-proof", launch, []WorkspaceSource{source}, storage)
	if err != nil {
		t.Fatal(err)
	}
	runtime := &testWorkspaceRuntime{w: w}
	if _, err := w.BindRuntime(t.Context(), runtime); err != nil {
		t.Fatal(err)
	}
	w.storage = &missingTreeProofStorage{storage}
	if _, err := w.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if w.state.StorageReleased {
		t.Fatal("single-member storage released without fetched byte proof")
	}
	if _, err := os.Stat(w.state.Root); err != nil {
		t.Fatal("unproved checkout disappeared")
	}
}

func TestWorkspacePendingBindingDoesNotAdmitOperations(t *testing.T) {
	w, r, _, _ := localWorkspaceFixture(t)
	w.state.RuntimePreparing = true
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := w.WithRuntimeMember(t.Context(), w.state.Members[0].Result.WorkspaceMemberRef, func(context.Context, WorkspaceContainer, string) error { called = true; return nil }); err == nil || called {
		t.Fatal("pending runtime admitted member operation")
	}
	if _, err := w.Sync(t.Context()); err == nil || r.freeze != 0 {
		t.Fatal("pending runtime admitted copy")
	}
}
func TestWorkspaceDefaultQuotaAuthorityUsesDockerVM(t *testing.T) {
	if _, ok := defaultWorkspaceStorage().(*DockerWorkspaceStorage); !ok {
		t.Fatal("sandbox requires host mount privilege or host-only quota backend")
	}
}

// Model durable idempotent server effects independently of daemon state.
func runtimeFixtureOnce(path string, raw []byte) error {
	old, err := os.ReadFile(path)
	if err == nil {
		if !bytes.Equal(old, raw) {
			return errors.New("server replay payload changed")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return err
	}
	return writeWorkspacePrivate(path, raw)
}
func runtimeFixtureTerminal(server string, message protocol.SessionEnded) error {
	if _, err := os.Stat(server + ".results"); err != nil {
		return errors.New("terminal arrived before saved results")
	}
	raw, err := json.Marshal(message)
	if err != nil {
		return err
	}
	return runtimeFixtureOnce(server+".terminal", raw)
}

func TestWorkspaceWorkerUsesPinnedRuntimeContext(t *testing.T) {
	w, _, _, _ := localWorkspaceFixture(t)
	old := currentDockerContext
	currentDockerContext = func() string { return "different-current-context" }
	t.Cleanup(func() { currentDockerContext = old })
	w.state.RuntimeContext = "pinned-runtime-context"
	if err := w.beginWorker(t.Context()); err != nil {
		t.Fatal(err)
	}
	if w.state.WorkerContext != "pinned-runtime-context" {
		t.Fatal("worker left the session's Docker context")
	}
}
