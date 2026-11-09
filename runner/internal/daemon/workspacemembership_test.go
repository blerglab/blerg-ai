package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/discovery"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

const membershipTestOp = "operation-1"

// membershipServer stands in for the server's journal: commit is a real
// compare-and-set, so a lost reply still leaves the operation committed.
type membershipServer struct {
	testWorkspaceAuthority
	mu         sync.Mutex
	phase      string
	proposed   protocol.WorkspaceManifest
	commits    int
	installed  int
	reports    []protocol.WorkspaceMembershipReport
	loseReply  bool
	commitErr  error
	statusErr  error
	commitFrom protocol.WorkspaceRuntimeBinding
}

func (a *membershipServer) MembershipStatus(context.Context, string, string) (string, protocol.WorkspaceManifest, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.phase, a.proposed, a.statusErr
}
func (a *membershipServer) MembershipCommit(_ context.Context, _ protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding, _, receipt string) (protocol.WorkspaceRuntimeBinding, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.commitErr != nil {
		return protocol.WorkspaceRuntimeBinding{}, a.commitErr
	}
	if receipt == "" {
		return protocol.WorkspaceRuntimeBinding{}, errors.New("no receipt")
	}
	a.commits++
	a.phase, a.commitFrom = "committed", b
	if a.loseReply {
		a.loseReply = false
		return protocol.WorkspaceRuntimeBinding{}, errors.New("reply lost")
	}
	b.ManifestDigest = a.proposed.Digest
	return b, nil
}
func (a *membershipServer) MembershipInstalled(context.Context, string, string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.installed++
	a.phase = "installed"
	return nil
}
func (a *membershipServer) MembershipReport(_ context.Context, _ string, r protocol.WorkspaceMembershipReport) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reports = append(a.reports, r)
	if r.Refusal != "" {
		a.phase = "refused"
	}
	return nil
}

type membershipWriters struct {
	freezeErr      error
	freezes, thaws int
	frozenAtThaw   func()
}

func (w *membershipWriters) Start(*exec.Cmd) error { return nil }
func (w *membershipWriters) Freeze(context.Context) error {
	if w.freezeErr != nil {
		return w.freezeErr
	}
	w.freezes++
	return nil
}
func (w *membershipWriters) Thaw(context.Context) error {
	w.thaws++
	if w.frozenAtThaw != nil {
		w.frozenAtThaw()
	}
	return nil
}
func (*membershipWriters) OnLimit(func(string))         {}
func (w *membershipWriters) Stop(context.Context) error { return nil }

type membershipPins struct {
	dir, common string
	err         error
}

func (p *membershipPins) PinAddition(_ context.Context, _ string, _ protocol.SelectionRef, m protocol.WorkspaceMember) (*discovery.SourcePin, error) {
	if p.err != nil {
		return nil, p.err
	}
	dir, err := os.Open(p.dir)
	if err != nil {
		return nil, err
	}
	common, err := os.Open(p.common)
	if err != nil {
		_ = dir.Close()
		return nil, err
	}
	return &discovery.SourcePin{Member: m, CanonicalPath: p.dir, CommonDirectoryPath: p.common, Directory: dir, CommonDirectory: common}, nil
}
func (p *membershipPins) RebindMembership(context.Context, protocol.WorkspaceManifest, []string) error {
	return nil
}

type membershipShells struct {
	busy    bool
	rebound []protocol.WorkspaceRuntimeBinding
}

func (s *membershipShells) open(string) bool { return s.busy }
func (s *membershipShells) rebind(b protocol.WorkspaceRuntimeBinding) error {
	s.rebound = append(s.rebound, b)
	return nil
}

type membershipFixture struct {
	t       *testing.T
	server  *membershipServer
	service *LocalWorkspaceService
	w       *LocalWorkspace
	writers *membershipWriters
	shells  *membershipShells
	pins    *membershipPins
	msg     protocol.WorkspaceMembershipApply
	old     protocol.WorkspaceManifest
}

func membershipSetup(t *testing.T) *membershipFixture {
	t.Helper()
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	root := t.TempDir()
	first, second := filepath.Join(root, "one"), filepath.Join(root, "three")
	for _, dir := range []string{first, second, filepath.Join(first, ".git"), filepath.Join(second, ".git")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	member := func(id string) protocol.WorkspaceMember {
		return protocol.WorkspaceMember{ID: id, Name: id, Path: id, SourceIdentity: "source-" + id, CommonDirectoryIdentity: "common-" + id, Destination: "source-" + id}
	}
	old := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner", Runtime: "host", LaunchGeneration: "launch", Members: []protocol.WorkspaceMember{member("one")}}
	old.Digest = workspaceselection.Digest(old)
	next := old
	next.Revision = 1
	next.Members = []protocol.WorkspaceMember{member("one"), member("three")}
	next.Digest = workspaceselection.Digest(next)

	server := &membershipServer{phase: "authorized", proposed: next}
	writers := &membershipWriters{}
	shells := &membershipShells{}
	pins := &membershipPins{dir: second, common: filepath.Join(second, ".git")}
	service := hostLifecycleTestService(t, server)
	service.Supervisor = &phaseRecorder{}
	service.TurnIdle = func(string) bool { return true }
	service.Discovery = func() membershipDiscovery { return pins }
	service.membershipShells = shells
	identity, err := workspaceDirectoryIdentity(first)
	if err != nil {
		t.Fatal(err)
	}
	common, err := workspaceDirectoryIdentity(filepath.Join(first, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := workspaceRecord("session")
	if err != nil {
		t.Fatal(err)
	}
	l := protocol.SupervisorLaunch{SessionID: "session", LaunchGeneration: "launch", RuntimeGeneration: 1, Runtime: "host", ConnectionID: "connection"}
	b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}, ManifestDigest: old.Digest, Root: identity}
	source := WorkspaceSource{Member: old.Members[0], Path: first, CommonPath: filepath.Join(first, ".git"), RootIdentity: identity, CommonIdentity: common}
	w := &LocalWorkspace{mu: workspaceLock("session"), record: record, state: workspacePrivateManifest{Version: privateWorkspaceVersion, Host: true, HostDriverStopped: true, Session: "session", Launch: old, Root: first, RootIdentity: identity, Binding: &b, SupervisorLaunch: l, RuntimeToken: "token", Sources: []WorkspaceSource{source}, Members: []workspacePrivateMember{{Member: old.Members[0], Clone: first, CloneIdentity: identity}}}}
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	w.host = &hostWorkspace{w: w, writers: writers, service: service}
	service.sessions = map[string]*LocalWorkspace{"session": w}
	msg := protocol.WorkspaceMembershipApply{SessionID: "session", OperationID: membershipTestOp, Kind: "add", MemberID: "three", Proposed: next, OldDigest: old.Digest, Source: protocol.SelectionRef{Path: "three", SourceIdentity: "source-three"}}
	return &membershipFixture{t: t, server: server, service: service, w: w, writers: writers, shells: shells, pins: pins, msg: msg, old: old}
}

func (f *membershipFixture) saved() workspacePrivateManifest {
	f.t.Helper()
	state, err := readPrivateWorkspace(f.w.record)
	if err != nil {
		f.t.Fatal(err)
	}
	return state
}

func TestMembershipApplyInstallsBeforeThawing(t *testing.T) {
	f := membershipSetup(t)
	f.writers.frozenAtThaw = func() {
		// The acknowledgment follows: it ends the server's retries, and a thaw can fail.
		if len(f.shells.rebound) != 1 || f.saved().Binding.ManifestDigest != f.msg.Proposed.Digest || f.server.installed != 0 {
			t.Error("writers thawed before the revision was installed, or acknowledged before they ran")
		}
	}
	f.service.ApplyMembership(t.Context(), f.msg)
	state := f.saved()
	if state.Launch.Digest != f.msg.Proposed.Digest || len(state.Sources) != 2 || len(state.Members) != 2 || state.Binding.ManifestDigest != f.msg.Proposed.Digest || state.Membership != nil {
		t.Fatalf("state %+v", state)
	}
	if f.writers.freezes != 1 || f.writers.thaws != 1 || f.server.installed != 1 {
		t.Fatalf("freezes=%d thaws=%d installed=%d", f.writers.freezes, f.writers.thaws, f.server.installed)
	}
	// A repeated apply after a lost install reply only acknowledges again.
	f.service.ApplyMembership(t.Context(), f.msg)
	if f.writers.freezes != 1 || f.server.commits != 1 || f.server.installed != 2 {
		t.Fatalf("repeat changed state: freezes=%d commits=%d", f.writers.freezes, f.server.commits)
	}
}

func TestMembershipWaitsWithoutFreezing(t *testing.T) {
	for name, arrange := range map[string]func(*membershipFixture){
		protocol.MembershipWaitTurn:  func(f *membershipFixture) { f.service.TurnIdle = func(string) bool { return false } },
		protocol.MembershipWaitShell: func(f *membershipFixture) { f.shells.busy = true },
	} {
		t.Run(name, func(t *testing.T) {
			f := membershipSetup(t)
			arrange(f)
			f.service.ApplyMembership(t.Context(), f.msg)
			if f.writers.freezes != 0 || f.server.commits != 0 || len(f.server.reports) != 1 || f.server.reports[0].Waiting != name {
				t.Fatalf("freezes=%d commits=%d reports=%+v", f.writers.freezes, f.server.commits, f.server.reports)
			}
		})
	}
}

func TestMembershipUnprovenFenceReopensAndStaysPending(t *testing.T) {
	f := membershipSetup(t)
	f.writers.freezeErr = errors.New("waiting for freeze acknowledgement")
	f.service.ApplyMembership(t.Context(), f.msg)
	if state := f.saved(); state.Membership != nil || state.Launch.Digest != f.old.Digest {
		t.Fatalf("state %+v", state)
	}
	if f.server.commits != 0 || f.writers.thaws != 1 || f.server.phase != "authorized" {
		t.Fatalf("commits=%d thaws=%d phase=%s", f.server.commits, f.writers.thaws, f.server.phase)
	}
}

func TestMembershipReplacedSourceRefusesAndRestoresTheOldRevision(t *testing.T) {
	f := membershipSetup(t)
	f.pins.err = errors.New("stale")
	f.service.ApplyMembership(t.Context(), f.msg)
	if state := f.saved(); state.Membership != nil || state.Launch.Digest != f.old.Digest || len(state.Sources) != 1 {
		t.Fatalf("state %+v", state)
	}
	if f.server.commits != 0 || f.writers.thaws != 1 || len(f.server.reports) != 1 || f.server.reports[0].Refusal != protocol.MembershipRefuseSource {
		t.Fatalf("commits=%d thaws=%d reports=%+v", f.server.commits, f.writers.thaws, f.server.reports)
	}
}

func TestMembershipRefusedCommitReopens(t *testing.T) {
	f := membershipSetup(t)
	f.server.commitErr = &WorkspaceRefusedError{Reason: "workspace membership changed"}
	f.service.ApplyMembership(t.Context(), f.msg)
	if state := f.saved(); state.Membership != nil || state.Launch.Digest != f.old.Digest || f.writers.thaws != 1 {
		t.Fatalf("state %+v thaws=%d", state, f.writers.thaws)
	}
}

// Unknown outcome: storage is retained, writers stay fenced, and the next apply
// resumes from the journal without freezing twice or committing a new receipt.
func TestMembershipLostCommitReplyKeepsWritersFencedThenInstalls(t *testing.T) {
	f := membershipSetup(t)
	f.server.loseReply = true
	f.service.ApplyMembership(t.Context(), f.msg)
	state := f.saved()
	if state.Membership == nil || state.Membership.Phase != membershipPrepared || f.writers.thaws != 0 {
		t.Fatalf("journal %+v thaws=%d", state.Membership, f.writers.thaws)
	}
	f.service.ApplyMembership(t.Context(), f.msg)
	state = f.saved()
	if state.Membership != nil || state.Launch.Digest != f.msg.Proposed.Digest || f.writers.freezes != 1 || f.writers.thaws != 1 || f.server.commits != 2 {
		t.Fatalf("journal %+v freezes=%d thaws=%d commits=%d", state.Membership, f.writers.freezes, f.writers.thaws, f.server.commits)
	}
}

// A reconnect must move the binding before Recover presents it.
func TestMembershipReconnectSettlesACommitWhoseReplyWasLost(t *testing.T) {
	f := membershipSetup(t)
	f.server.loseReply = true
	f.service.ApplyMembership(t.Context(), f.msg)
	if err := f.service.settleHostedMembership(t.Context(), f.w); err != nil {
		t.Fatal(err)
	}
	if state := f.saved(); state.Membership != nil || state.Binding.ManifestDigest != f.msg.Proposed.Digest || f.writers.thaws != 1 || f.server.phase != "installed" {
		t.Fatalf("state %+v thaws=%d phase=%s", state.Membership, f.writers.thaws, f.server.phase)
	}
}

// Every journal phase, as a daemon that dies there and restarts would find it.
func TestMembershipCrashAtEachPhase(t *testing.T) {
	for _, c := range []struct {
		stage     string
		committed bool
	}{
		{"membership-fencing", false},
		{"membership-fenced", false},
		{"membership-prepared", false},
		{"membership-after-commit", true},
		{"membership-state-saved", true},
		{"membership-live-installed", true},
		{"membership-journal-closed", true},
	} {
		t.Run(c.stage, func(t *testing.T) {
			f := membershipSetup(t)
			type crash struct{}
			workspaceCrashHook = func(stage string) {
				if stage == c.stage {
					panic(crash{})
				}
			}
			defer func() { workspaceCrashHook = nil }()
			func() {
				defer func() {
					if v := recover(); v != nil {
						if _, ok := v.(crash); !ok {
							panic(v)
						}
					}
				}()
				f.service.ApplyMembership(t.Context(), f.msg)
			}()
			workspaceCrashHook = nil
			state, err := f.service.reconcileMembershipJournal(t.Context(), f.saved(), f.w.record)
			if err != nil {
				t.Fatal(err)
			}
			saved := f.saved()
			// A commit keeps its journal until the recovered runtime reopens.
			if open := c.committed && c.stage != "membership-journal-closed"; (saved.Membership != nil) != open || (state.Membership != nil) != open {
				t.Fatalf("journal after reconciliation: %+v", saved.Membership)
			}
			want, members := f.old.Digest, 1
			if c.committed {
				want, members = f.msg.Proposed.Digest, 2
			}
			if saved.Launch.Digest != want || saved.Binding.ManifestDigest != want || len(saved.Sources) != members || len(saved.Members) != members {
				t.Fatalf("restored to the wrong revision: %+v", saved.Launch)
			}
			if !c.committed && f.server.phase != "authorized" {
				t.Fatalf("server phase %s: an uncommitted change must stay retryable", f.server.phase)
			}
			if c.committed {
				if f.server.phase != "committed" {
					t.Fatalf("server phase %s before the runtime reopened", f.server.phase)
				}
				f.w.state = state
				f.service.ApplyMembership(t.Context(), f.msg)
				if f.saved().Membership != nil || f.server.phase != "installed" {
					t.Fatalf("the repeated apply did not finish: %+v %s", f.saved().Membership, f.server.phase)
				}
			}
		})
	}
}

// With no answer the journal is retained: no guess in either direction.
func TestMembershipRestartWithoutAnAnswerRetainsTheJournal(t *testing.T) {
	f := membershipSetup(t)
	f.server.loseReply = true
	f.service.ApplyMembership(t.Context(), f.msg)
	f.server.statusErr = errors.New("workspace authority disconnected")
	if _, err := f.service.reconcileMembershipJournal(t.Context(), f.saved(), f.w.record); err == nil {
		t.Fatal("reconciled without a server answer")
	}
	if state := f.saved(); state.Membership == nil || state.Membership.Phase != membershipPrepared {
		t.Fatalf("journal %+v", state.Membership)
	}
}

// Adoption: the folder must be the very directory this runtime is pinned to.
func TestMembershipAdoptionRefusesAReplacedFolder(t *testing.T) {
	f := membershipSetup(t)
	f.w.state.Sources, f.w.state.Members = nil, nil
	folder := protocol.WorkspaceManifest{Version: 2, Kind: "folder", OwnerID: "owner", Runtime: "host", LaunchGeneration: "launch", Members: []protocol.WorkspaceMember{}}
	folder.Digest = workspaceselection.Digest(folder)
	f.w.state.Launch = folder
	f.w.state.Binding.ManifestDigest = folder.Digest
	adopted := f.msg.Proposed.Members[1]
	adopted.Path = "."
	next := folder
	next.Revision, next.Members = 1, []protocol.WorkspaceMember{adopted}
	next.Digest = workspaceselection.Digest(next)
	f.msg.Proposed, f.msg.OldDigest, f.server.proposed = next, folder.Digest, next
	if err := f.w.save(); err != nil {
		t.Fatal(err)
	}
	f.service.ApplyMembership(t.Context(), f.msg) // the pin is another directory
	if len(f.server.reports) != 1 || f.server.reports[0].Refusal != protocol.MembershipRefuseSource || f.server.commits != 0 {
		t.Fatalf("reports=%+v commits=%d", f.server.reports, f.server.commits)
	}
	if state := f.saved(); len(state.Launch.Members) != 0 || f.writers.thaws != 1 {
		t.Fatalf("failed adoption left the zero-member authority: %+v", state.Launch)
	}
	// The real pinned folder adopts.
	f.server.reports, f.server.phase = nil, "authorized"
	f.pins.dir, f.pins.common = f.w.state.Root, filepath.Join(f.w.state.Root, ".git")
	f.service.ApplyMembership(t.Context(), f.msg)
	if state := f.saved(); len(state.Launch.Members) != 1 || len(state.Sources) != 1 {
		t.Fatalf("adoption did not install: %+v %+v", state.Launch, f.server.reports)
	}
}

func TestMembershipOldServerAuthorityRefusesQuietly(t *testing.T) {
	f := membershipSetup(t)
	f.service.Authority = &testWorkspaceAuthority{}
	f.service.ApplyMembership(t.Context(), f.msg)
	if f.writers.freezes != 0 {
		t.Fatal("froze writers without membership authority")
	}
}

func TestMembershipPrivilegedFenceReportsPendingUntilClosed(t *testing.T) {
	f := membershipSetup(t)
	sup := &phaseRecorder{privileged: true}
	f.service.Supervisor = sup
	f.service.ApplyMembership(t.Context(), f.msg)
	if f.writers.freezes != 0 || f.server.commits != 0 || len(f.server.reports) != 1 || f.server.reports[0].Waiting != protocol.MembershipWaitShell {
		t.Fatalf("unsafe change or missing pending reason: freezes=%d commits=%d reports=%+v", f.writers.freezes, f.server.commits, f.server.reports)
	}
	sup.privileged = false
	f.service.ApplyMembership(t.Context(), f.msg)
	if f.server.commits != 1 || f.server.installed != 1 {
		t.Fatalf("closed shell did not unblock change: commits=%d installed=%d reports=%+v", f.server.commits, f.server.installed, f.server.reports)
	}
}
