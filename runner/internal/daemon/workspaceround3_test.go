package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

// Test adapter only: production must return this digest only after an
// authenticated durable commit/reconciliation, never from Sender.Send.
func acknowledgeWorkspaceFinal(_ context.Context, _ protocol.SupervisorLaunch, f WorkspaceFinalization) (WorkspaceFinalReceipt, error) {
	return WorkspaceFinalReceipt{Digest: f.Digest()}, nil
}

func finalWorkspaceFixture(t *testing.T) (*LocalWorkspace, *LocalWorkspaceService) {
	t.Helper()
	w, _, _, _ := localWorkspaceFixture(t)
	w.state.Stopped = true
	w.state.Binding = &protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: w.state.Launch.LaunchGeneration, Generation: 7}, ManifestDigest: w.state.Launch.Digest, Root: protocol.WorkspaceRoot{Inode: 1}}
	w.authorize = func(context.Context) error { return nil }
	s := &LocalWorkspaceService{Results: func(context.Context, string, protocol.WorkspaceRuntimeBinding, []protocol.WorkspaceMemberResult) error {
		return nil
	}}
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	return w, s
}

func TestWorkspaceTerminalWriteIsNotReceipt(t *testing.T) {
	w, s := finalWorkspaceFixture(t)
	release := make(chan struct{})
	ts := newTestServer(t, func(c *websocket.Conn) { defer c.Close(); <-release })
	defer close(release)
	conn, _, err := websocket.DefaultDialer.Dial(ts.wsURL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := &WSClient{conn: conn}
	message := protocol.SessionEnded{Type: "session_ended", SessionID: w.state.Session, Message: "exact terminal outcome"}
	// The peer handshakes but never reads this frame. A successful production
	// WSClient write must not discard the authenticated retry journal.
	if err := client.Send(message); err != nil {
		t.Fatal(err)
	}
	err = s.Finalize(t.Context(), w, message, func(m protocol.SessionEnded) error { return client.Send(m) })
	if err == nil || workspaceFinalComplete(w.state) {
		t.Fatal("socket submission mistaken for durable acknowledgement")
	}
	state, err := readPrivateWorkspace(w.record)
	if err != nil || !slices.Contains(s.PendingIDs(), w.state.Session) || workspaceFinalComplete(state) {
		t.Fatal("restart lost pending terminal", err)
	}
	// A mismatching receipt must fail, even if a raw send succeeded.
	s.CommitFinal = func(context.Context, protocol.SupervisorLaunch, WorkspaceFinalization) (WorkspaceFinalReceipt, error) {
		return WorkspaceFinalReceipt{Digest: "different-outcome"}, nil
	}
	if err := s.Finalize(t.Context(), w, message, nil); err == nil {
		t.Fatal("conflicting receipt accepted")
	}
}

func TestWorkspaceFinalReceiptLostAckReconcilesEndedSession(t *testing.T) {
	w, s := finalWorkspaceFixture(t)
	message := protocol.SessionEnded{Type: "session_ended", SessionID: w.state.Session, ExitCode: 1, Message: "immutable outcome"}
	var committed string
	calls := 0
	s.CommitFinal = func(_ context.Context, _ protocol.SupervisorLaunch, f WorkspaceFinalization) (WorkspaceFinalReceipt, error) {
		calls++
		if committed == "" {
			committed = f.Digest()
			return WorkspaceFinalReceipt{}, errors.New("committed, receipt lost")
		}
		if f.Digest() != committed {
			t.Fatal("replay changed transaction")
		}
		return WorkspaceFinalReceipt{Digest: committed}, nil
	}
	if err := s.Finalize(t.Context(), w, message, nil); err == nil {
		t.Fatal("lost receipt discarded")
	}
	// Server ended the session. Ordinary Current/Recover and Results now refuse.
	w.authorize = func(context.Context) error { return errors.New("ended session") }
	s.Results = func(context.Context, string, protocol.WorkspaceRuntimeBinding, []protocol.WorkspaceMemberResult) error {
		t.Fatal("resent results after end")
		return nil
	}
	s.Authority = &testWorkspaceAuthority{stale: true}
	s.Supervisor = &testWorkspaceSupervisor{bad: true}
	s.TrustedLaunch = func(context.Context, string) (protocol.WorkspaceManifest, error) {
		t.Fatal("ordinary ended recovery used")
		return protocol.WorkspaceManifest{}, nil
	}
	if err := s.RecoverAll(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	state, err := readPrivateWorkspace(w.record)
	if err != nil || !workspaceFinalComplete(state) || calls != 2 || len(s.PendingIDs()) != 0 {
		t.Fatal("receipt reconciliation failed", err, calls)
	}
}

func TestWorkspaceLegacySubmissionBitRequiresReceipt(t *testing.T) {
	w, s := finalWorkspaceFixture(t)
	message := protocol.SessionEnded{Type: "session_ended", SessionID: w.state.Session}
	w.state.TerminalAcknowledged = true // old round-2 submission bit, not a receipt
	w.state.FinalDelivery = &message
	w.state.FinalResults = w.results()
	w.state.ResultsAcknowledged = true
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(s.PendingIDs(), w.state.Session) {
		t.Fatal("old submission bit lost replay")
	}
	s.CommitFinal = acknowledgeWorkspaceFinal
	if err := s.Finalize(t.Context(), w, message, nil); err != nil || !workspaceFinalComplete(w.state) {
		t.Fatal("old journal not repaired", err)
	}
}

func TestP2DockerReconnectPreservesHostedWorkspace(t *testing.T) {
	w, d := p2DockerWorkspace(t)
	a := &reconnectWorkspaceAuthority{testWorkspaceAuthority: &testWorkspaceAuthority{}, connection: "authenticated-connection"}
	supervisor := &testWorkspaceSupervisor{}
	s := &LocalWorkspaceService{Authority: a, Supervisor: supervisor, Storage: w.storage, TrustedLaunch: func(context.Context, string) (protocol.WorkspaceManifest, error) { return w.state.Launch, nil }, Results: func(context.Context, string, protocol.WorkspaceRuntimeBinding, []protocol.WorkspaceMemberResult) error {
		return nil
	}, CommitFinal: acknowledgeWorkspaceFinal, sessions: map[string]*LocalWorkspace{w.state.Session: w}}
	w.state.SupervisorLaunch, _ = a.Launch(t.Context(), w.state.Session, w.state.Launch, "token")
	if err := s.bindAuthority(t.Context(), w, d, w.state.RuntimeRoot); err != nil {
		t.Fatal(err)
	}
	old := w.state.SupervisorLaunch
	a.connection = "reconnected-connection"
	terminals := 0
	if err := s.RecoverAll(t.Context(), func(protocol.SessionEnded) error { terminals++; return nil }); err != nil {
		t.Fatal(err)
	}
	if terminals != 0 || w.state.Stopped || w.state.SupervisorLaunch.ConnectionID != a.connection || d.SupervisorLaunch != w.state.SupervisorLaunch {
		t.Fatal("reconnect stopped or failed to rebind authoritative object")
	}
	if absent, err := d.Absent(t.Context()); err != nil || absent {
		t.Fatal("live runtime removed", err)
	}
	if a.Current(t.Context(), old, *w.state.Binding) == nil {
		t.Fatal("stale connection accepted")
	}
	childCommit(t, w.state.Members[0].Clone, "after-reconnect.txt")
	if _, err := w.Sync(t.Context()); err != nil {
		t.Fatal("live object unusable after reconnect", err)
	}
	if s.sessions[w.state.Session] != w {
		t.Fatal("live handle replaced")
	}
}

func TestP2DockerQuotaKeeperStoppedRecovery(t *testing.T) {
	_, _ = p2DockerWorkspace(t) // opt-in, worker ELF and isolated daemon state
	s := &DockerWorkspaceStorage{Root: filepath.Join(t.TempDir(), "quota")}
	session := "keeper-recovery"
	budget := protocol.WorkspaceBudget{CheckoutBytes: 16 << 20, CheckoutEntries: 512}
	t.Cleanup(func() { cleanupVMStorageTest(t, s, session) })
	root, err := s.Allocate(t.Context(), session, budget)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := s.Identity(t.Context(), session, root)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.read(session)
	if err != nil {
		t.Fatal(err)
	}
	// Only this test's authenticated immutable keeper is ever stopped.
	if err := s.owned(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	p2Docker(t, "--context", r.Context, "exec", r.Container, "sh", "-c", "printf durable-bytes > /quota/"+session+"/mount/tree/exact.txt")
	// Also remove this keeper namespace's mount: a VM restart loses it too.
	p2Docker(t, "--context", r.Context, "exec", r.Container, "umount", "/quota/"+session+"/mount")
	p2Docker(t, "--context", r.Context, "stop", "-t", "1", r.Container)
	fresh := &DockerWorkspaceStorage{Root: s.Root}
	if err := fresh.Validate(t.Context(), session, root, budget); err != nil {
		t.Fatal("stopped keeper not recovered", err)
	}
	got, err := fresh.Identity(t.Context(), session, root)
	if err != nil || got != identity {
		t.Fatal("root identity changed", err, identity, got)
	}
	if got := p2Docker(t, "--context", r.Context, "exec", r.Container, "cat", "/quota/"+session+"/mount/tree/exact.txt"); got != "durable-bytes" {
		t.Fatal("recovery lost bytes")
	}
	if _, err := fresh.MountSource(t.Context(), session, root); err != nil {
		t.Fatal(err)
	}
	// Simulate process exit after the data-volume removal. The durable release
	// intent plus filesystem journal must make a fresh service finish release.
	hook := workspaceCrashHook
	workspaceCrashHook = func(stage string) {
		if stage == "after-quota-data-release" {
			panic("release crash")
		}
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("release boundary not reached")
			}
		}()
		_ = fresh.Release(t.Context(), session, root)
	}()
	workspaceCrashHook = hook
	p2Docker(t, "--context", r.Context, "stop", "-t", "1", r.Container)
	if err := (&DockerWorkspaceStorage{Root: s.Root}).Release(t.Context(), session, root); err != nil {
		t.Fatal("stopped interrupted release unrecoverable", err)
	}
	if err := fresh.Release(t.Context(), session, root); err != nil {
		t.Fatal("release not idempotent", err)
	}
}

func TestWorkspacePendingFinalizationLivenessAcrossRestart(t *testing.T) {
	w, s := finalWorkspaceFixture(t)
	s.Results = func(context.Context, string, protocol.WorkspaceRuntimeBinding, []protocol.WorkspaceMemberResult) error {
		return errors.New("outage")
	}
	_ = s.Finalize(t.Context(), w, protocol.SessionEnded{Type: "session_ended", SessionID: w.state.Session}, nil)
	h := &AgentHost{cfg: AgentHostConfig{Workspaces: s}}
	if !slices.Contains(h.ActiveIDs(), w.state.Session) || !slices.Contains(h.RecoveryRequiredSessions(), w.state.Session) {
		t.Fatal("pending session omitted from host liveness")
	}
	m := &Manager{config: ManagerConfig{Workspaces: &LocalWorkspaceService{}, Command: []string{"test"}}}
	if !slices.Contains(m.ActiveSessionIDs(), w.state.Session) {
		t.Fatal("process restart omitted authenticated journal before hello")
	}
	// Authenticated journal only; corruption cannot keep an arbitrary row live.
	if err := os.WriteFile(w.record, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(m.ActiveSessionIDs(), w.state.Session) {
		t.Fatal("corrupt journal claimed liveness")
	}
}

type quotaCrashFixture struct{ StateDir, Root, Session, Logical, Stage string }

func workspaceRound3QuotaCrashMain(args []string) bool {
	if len(args) != 2 || args[0] != "--p2-quota-crash" {
		return false
	}
	raw, err := os.ReadFile(args[1])
	if err != nil {
		os.Exit(2)
	}
	var f quotaCrashFixture
	if json.Unmarshal(raw, &f) != nil {
		os.Exit(2)
	}
	_ = os.Setenv(DaemonStateDirEnv, f.StateDir)
	workspaceCrashHook = func(stage string) {
		if stage == f.Stage {
			os.Exit(86)
		}
	}
	storage := &DockerWorkspaceStorage{Root: f.Root}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	if f.Stage == "before-quota-keeper-restart" || f.Stage == "after-quota-keeper-restart" {
		_, err = storage.Identity(ctx, f.Session, f.Logical)
	} else {
		err = storage.Release(ctx, f.Session, f.Logical)
	}
	cancel()
	if err != nil {
		os.Exit(3)
	}
	os.Exit(4)
	return true
}

func TestP2DockerQuotaKeeperProcessCrashRecovery(t *testing.T) {
	_, _ = p2DockerWorkspace(t)
	for _, stage := range []string{"before-quota-keeper-restart", "after-quota-keeper-restart", "before-quota-data-release", "after-quota-data-release", "after-quota-image-release"} {
		t.Run(stage, func(t *testing.T) {
			s := &DockerWorkspaceStorage{Root: filepath.Join(t.TempDir(), "quota")}
			session := "quota-crash"
			budget := protocol.WorkspaceBudget{CheckoutBytes: 16 << 20, CheckoutEntries: 512}
			t.Cleanup(func() { cleanupVMStorageTest(t, s, session) })
			root, err := s.Allocate(t.Context(), session, budget)
			if err != nil {
				t.Fatal(err)
			}
			pin, err := s.Identity(t.Context(), session, root)
			if err != nil {
				t.Fatal(err)
			}
			r, err := s.read(session)
			if err != nil {
				t.Fatal(err)
			}
			p2Docker(t, "--context", r.Context, "stop", "-t", "1", r.Container)
			f := quotaCrashFixture{StateDir: DaemonStateDir(), Root: s.Root, Session: session, Logical: root, Stage: stage}
			raw, _ := json.Marshal(f)
			path := filepath.Join(t.TempDir(), "fixture.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), binary, "--p2-quota-crash", path)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 86 {
				t.Fatalf("crash boundary not reached: %v %s", err, output)
			}
			fresh := &DockerWorkspaceStorage{Root: s.Root}
			if stage == "before-quota-keeper-restart" || stage == "after-quota-keeper-restart" {
				got, err := fresh.Identity(t.Context(), session, root)
				if err != nil || got != pin {
					t.Fatal("restart recovery changed root", err)
				}
			}
			if err := fresh.Release(t.Context(), session, root); err != nil {
				t.Fatal("crashed release not recovered", err)
			}
			if err := fresh.Release(t.Context(), session, root); err != nil {
				t.Fatal("release not idempotent", err)
			}
		})
	}
}

func TestP2DockerQuotaKeeperRefusesReassignedDevice(t *testing.T) {
	_, _ = p2DockerWorkspace(t)
	s := &DockerWorkspaceStorage{Root: filepath.Join(t.TempDir(), "quota")}
	session := "quota-device"
	budget := protocol.WorkspaceBudget{CheckoutBytes: 16 << 20, CheckoutEntries: 512}
	t.Cleanup(func() { cleanupVMStorageTest(t, s, session) })
	root, err := s.Allocate(t.Context(), session, budget)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.read(session)
	if err != nil {
		t.Fatal(err)
	}
	allocation, err := s.call(t.Context(), r, "quota-validate")
	if err != nil {
		t.Fatal(err)
	}
	device := allocation.QuotaDevice
	if !strings.HasPrefix(device, "/dev/loop") {
		t.Fatal("invalid test loop pin")
	}
	occupant := "/tmp/occupant-" + ccUUID() + ".img"
	// This loop association belongs exclusively to this test. Cleanup checks its
	// unique backing file before detaching; it never detaches somebody else's loop.
	t.Cleanup(func() {
		raw, err := workspaceDockerOutput(context.Background(), "--context", r.Context, "exec", r.Container, "losetup", "--noheadings", "--output", "BACK-FILE", device)
		if err == nil && strings.TrimSpace(string(raw)) == occupant {
			_, _ = workspaceDockerOutput(context.Background(), "--context", r.Context, "exec", r.Container, "losetup", "-d", device)
		}
	})
	p2Docker(t, "--context", r.Context, "exec", r.Container, "sh", "-c", "printf valuable > /quota/"+session+"/mount/tree/value; umount /quota/"+session+"/mount && truncate -s 1048576 "+occupant+" && losetup "+device+" "+occupant)
	if err := s.Validate(t.Context(), session, root, budget); err == nil || !strings.Contains(err.Error(), "pinned loop device belongs to another image") {
		t.Fatal("reassigned device not refused with saved allocation intact", err)
	}
	// Once the test's conflicting association is gone, recover the SAME image,
	// root and exact bytes. No allocation or data-volume replacement is needed.
	p2Docker(t, "--context", r.Context, "exec", r.Container, "losetup", "-d", device)
	if err := s.Validate(t.Context(), session, root, budget); err != nil {
		t.Fatal(err)
	}
	if got := p2Docker(t, "--context", r.Context, "exec", r.Container, "cat", "/quota/"+session+"/mount/tree/value"); got != "valuable" {
		t.Fatal("device conflict discarded saved bytes")
	}
	if err := s.Release(t.Context(), session, root); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceStopIntentRetriesFailedJournalWrite(t *testing.T) {
	w, s := finalWorkspaceFixture(t)
	record := w.record
	blocked := failedWorkspaceJournalPath(t)
	previous := workspaceCrashHook
	defer func() { workspaceCrashHook = previous }()
	first := protocol.SessionEnded{Type: "session_ended", SessionID: w.state.Session, ExitCode: 1, Message: "first exact outcome"}
	workspaceCrashHook = func(stage string) {
		if stage == "before-stop-intent-save" {
			w.record = blocked
		}
	}
	if err := w.RequestStop(first); err == nil {
		t.Fatal("failed journal accepted")
	}
	workspaceCrashHook = previous
	w.record = record
	if err := w.RequestStop(protocol.SessionEnded{Type: "session_ended", SessionID: w.state.Session, Message: "replacement"}); err != nil {
		t.Fatal(err)
	}
	state, err := readPrivateWorkspace(record)
	if err != nil || !state.StopRequested || state.StopDelivery == nil || state.StopDelivery.Message != first.Message {
		t.Fatal("retry skipped stop journal or changed outcome", err)
	}
	workspaceCrashHook = func(stage string) {
		if stage == "before-final-payload-save" {
			w.record = blocked
		}
	}
	if err := s.Finalize(t.Context(), w, first, nil); err == nil {
		t.Fatal("failed final journal accepted")
	}
	workspaceCrashHook = previous
	w.record = record
	s.Results = func(context.Context, string, protocol.WorkspaceRuntimeBinding, []protocol.WorkspaceMemberResult) error {
		state, err := readPrivateWorkspace(record)
		if err != nil || state.FinalDelivery == nil || state.FinalDelivery.Message != first.Message {
			t.Fatal("result effect preceded final journal", err)
		}
		return nil
	}
	s.CommitFinal = acknowledgeWorkspaceFinal
	if err := s.Finalize(t.Context(), w, first, nil); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceFinalReceiptRetriesFailedLocalSave(t *testing.T) {
	w, s := finalWorkspaceFixture(t)
	record := w.record
	calls := 0
	s.CommitFinal = func(ctx context.Context, launch protocol.SupervisorLaunch, f WorkspaceFinalization) (WorkspaceFinalReceipt, error) {
		calls++
		w.record = failedWorkspaceJournalPath(t)
		return acknowledgeWorkspaceFinal(ctx, launch, f)
	}
	message := protocol.SessionEnded{Type: "session_ended", SessionID: w.state.Session}
	if err := s.Finalize(t.Context(), w, message, nil); err == nil {
		t.Fatal("failed receipt save accepted")
	}
	w.record = record
	if err := s.Finalize(t.Context(), w, message, nil); err != nil {
		t.Fatal(err)
	}
	saved, err := readPrivateWorkspace(record)
	if err != nil || !workspaceFinalComplete(saved) || calls != 1 || len(s.PendingIDs()) != 0 {
		t.Fatal("in-memory receipt skipped durable save retry", err)
	}
}

func failedWorkspaceJournalPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "file-as-parent")
	if err := os.WriteFile(path, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(path, "record.json")
}

func TestWorkspaceIntentRetriesNeverOverwriteCorruptAuthority(t *testing.T) {
	w, s := finalWorkspaceFixture(t)
	corrupt := []byte("corrupt authority")
	if err := os.WriteFile(w.record, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	message := protocol.SessionEnded{Type: "session_ended", SessionID: w.state.Session}
	if err := w.RequestStop(message); err == nil {
		t.Fatal("stop intent overwrote corrupt authority")
	}
	if err := s.Finalize(t.Context(), w, message, nil); err == nil {
		t.Fatal("final intent overwrote corrupt authority")
	}
	raw, err := os.ReadFile(w.record)
	if err != nil || string(raw) != string(corrupt) {
		t.Fatal("corrupt authority was silently replaced", err)
	}
}
