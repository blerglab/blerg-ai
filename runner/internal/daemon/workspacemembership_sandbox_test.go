package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/workspaceselection"
)

type sandboxMembership struct {
	t       *testing.T
	server  *membershipServer
	service *LocalWorkspaceService
	w       *LocalWorkspace
	runtime *testWorkspaceRuntime
	msg     protocol.WorkspaceMembershipApply
	source  string
	clone   string
}

func sandboxMembershipSetup(t *testing.T) *sandboxMembership {
	t.Helper()
	w, runtime, _, _ := localWorkspaceFixture(t)
	return sandboxMembershipFrom(t, w, runtime)
}

func sandboxMembershipFrom(t *testing.T, w *LocalWorkspace, runtime *testWorkspaceRuntime) *sandboxMembership {
	t.Helper()
	w.state.Launch.Digest = workspaceselection.Digest(w.state.Launch)
	l := protocol.SupervisorLaunch{SessionID: w.state.Session, LaunchGeneration: w.state.Launch.LaunchGeneration, RuntimeGeneration: 1, Runtime: "sandbox", ConnectionID: "connection"}
	b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: l.LaunchGeneration, Generation: 1}, ManifestDigest: w.state.Launch.Digest, Root: w.state.RootIdentity}
	w.state.Binding, w.state.SupervisorLaunch = &b, l
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "three")
	initChildRepo(t, source)
	childCommit(t, source, "source-three.txt")
	member := protocol.WorkspaceMember{ID: "three", Name: "three", Path: "three", SourceIdentity: "source-three", CommonDirectoryIdentity: "common-three", Destination: "destination-three"}
	next := w.state.Launch
	next.Revision = 1
	next.Members = append(append([]protocol.WorkspaceMember(nil), w.state.Launch.Members...), member)
	next.Digest = workspaceselection.Digest(next)
	server := &membershipServer{phase: "authorized", proposed: next}
	pins := &membershipPins{dir: source, common: filepath.Join(source, ".git")}
	service := hostLifecycleTestService(t, server)
	service.Supervisor = &phaseRecorder{}
	service.TurnIdle = func(string) bool { return true }
	service.Discovery = func() membershipDiscovery { return pins }
	service.membershipShells = &membershipShells{}
	service.sessions = map[string]*LocalWorkspace{w.state.Session: w}
	msg := protocol.WorkspaceMembershipApply{SessionID: w.state.Session, OperationID: membershipTestOp, Kind: "add", MemberID: "three", Proposed: next, OldDigest: w.state.Launch.Digest, Source: protocol.SelectionRef{Path: "three", SourceIdentity: "source-three"}}
	return &sandboxMembership{t: t, server: server, service: service, w: w, runtime: runtime, msg: msg, source: source, clone: filepath.Join(w.state.Root, "three")}
}

func (f *sandboxMembership) saved() workspacePrivateManifest {
	f.t.Helper()
	state, err := readPrivateWorkspace(f.w.record)
	if err != nil {
		f.t.Fatal(err)
	}
	return state
}

func TestMembershipSandboxAddOwnsItsObjectsAndDelivers(t *testing.T) {
	f := sandboxMembershipSetup(t)
	f.service.ApplyMembership(t.Context(), f.msg)
	state := f.saved()
	if state.Membership != nil || len(state.Members) != 3 || state.Launch.Digest != f.msg.Proposed.Digest || !state.Members[2].Source.OwnObjects || f.runtime.thaw != 1 {
		t.Fatalf("state %+v thaws=%d reports=%+v", state.Membership, f.runtime.thaw, f.server.reports)
	}
	// A running container has no mount for this source: nothing may borrow from it.
	raw, err := os.ReadFile(filepath.Join(f.clone, ".git", "objects", "info", "alternates"))
	if err != nil && !os.IsNotExist(err) || strings.TrimSpace(string(raw)) != "" {
		t.Fatalf("alternates %q %v", raw, err)
	}
	if out, err := exec.Command("git", "-C", f.clone, "cat-file", "-e", "HEAD^{tree}").CombinedOutput(); err != nil {
		t.Fatalf("clone cannot read its own objects: %v: %s", err, out)
	}
	childCommit(t, f.clone, "from-the-agent.txt")
	if _, err := f.w.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	head, ok := readCloneRef(f.source, "refs/heads/"+f.w.branch())
	if !ok || head != f.w.state.Members[2].Result.Head {
		t.Fatalf("delivery to the member's own branch: %q ok=%v result=%+v", head, ok, f.w.state.Members[2].Result)
	}
	// The aggregate reopen a restart performs accepts the appended member.
	if err := f.w.validateAllocation(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestMembershipSandboxNeverReplacesTheAgentsFolder(t *testing.T) {
	f := sandboxMembershipSetup(t)
	if err := os.MkdirAll(f.clone, 0700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(f.clone, "keep")
	if err := os.WriteFile(keep, []byte("agent work"), 0600); err != nil {
		t.Fatal(err)
	}
	f.service.ApplyMembership(t.Context(), f.msg)
	if len(f.server.reports) != 1 || f.server.reports[0].Refusal != protocol.MembershipRefuseCollision || f.runtime.thaw != 1 {
		t.Fatalf("reports=%+v thaws=%d", f.server.reports, f.runtime.thaw)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("the agent's folder was touched", err)
	}
	if state := f.saved(); state.Membership != nil || len(state.Members) != 2 {
		t.Fatalf("state %+v", state.Membership)
	}
}

func TestMembershipSandboxReplacedSourceRefuses(t *testing.T) {
	f := sandboxMembershipSetup(t)
	f.service.Discovery = func() membershipDiscovery { return &membershipPins{err: errors.New("stale")} }
	f.service.ApplyMembership(t.Context(), f.msg)
	if len(f.server.reports) != 1 || f.server.reports[0].Refusal != protocol.MembershipRefuseSource || f.runtime.thaw != 1 || f.server.commits != 0 {
		t.Fatalf("reports=%+v thaws=%d commits=%d", f.server.reports, f.runtime.thaw, f.server.commits)
	}
	if _, err := os.Lstat(f.clone); !os.IsNotExist(err) {
		t.Fatal("clone created for a refused source")
	}
}

func TestMembershipSandboxRefusedCommitDiscardsByIdentity(t *testing.T) {
	f := sandboxMembershipSetup(t)
	f.server.commitErr = &WorkspaceRefusedError{Reason: "workspace membership changed"}
	f.service.ApplyMembership(t.Context(), f.msg)
	if _, err := os.Lstat(f.clone); !os.IsNotExist(err) {
		t.Fatal("prepared allocation retained after a refused commit")
	}
	if state := f.saved(); state.Membership != nil || len(state.Members) != 2 || f.runtime.thaw != 1 {
		t.Fatalf("state %+v thaws=%d", state.Membership, f.runtime.thaw)
	}
}

// A replaced allocation is retained with its journal, never removed by pathname.
func TestMembershipSandboxDiscardRefusesAReplacedAllocation(t *testing.T) {
	f := sandboxMembershipSetup(t)
	f.server.commitErr = errors.New("connection lost")
	f.service.ApplyMembership(t.Context(), f.msg)
	if state := f.saved(); state.Membership == nil || state.Membership.Phase != membershipPrepared || f.runtime.thaw != 0 {
		t.Fatalf("unknown outcome must stay fenced: %+v thaws=%d", state.Membership, f.runtime.thaw)
	}
	if err := os.Rename(f.clone, f.clone+".moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f.clone, 0700); err != nil {
		t.Fatal(err)
	}
	f.server.commitErr = &WorkspaceRefusedError{Reason: "workspace membership changed"}
	f.service.ApplyMembership(t.Context(), f.msg)
	if _, err := os.Stat(f.clone); err != nil {
		t.Fatal("replacement removed", err)
	}
	if state := f.saved(); state.Membership == nil || f.runtime.thaw != 0 {
		t.Fatalf("journal lost: %+v thaws=%d", state.Membership, f.runtime.thaw)
	}
}

func TestMembershipSandboxCrashAtEachPhase(t *testing.T) {
	for _, c := range []struct {
		stage     string
		committed bool
	}{
		{"membership-fencing", false},
		{"membership-fenced", false},
		{"membership-cloned", false},
		{"membership-prepared", false},
		{"membership-after-commit", true},
		{"membership-state-saved", true},
		{"membership-live-installed", true},
		{"membership-journal-closed", true},
	} {
		t.Run(c.stage, func(t *testing.T) {
			f := sandboxMembershipSetup(t)
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
			want, members := f.msg.OldDigest, 2
			if c.committed {
				want, members = f.msg.Proposed.Digest, 3
			}
			if open := c.committed && c.stage != "membership-journal-closed"; (saved.Membership != nil) != open {
				t.Fatalf("journal after reconciliation: %+v", saved.Membership)
			}
			if saved.Launch.Digest != want || saved.Binding.ManifestDigest != want || len(saved.Members) != members {
				t.Fatalf("restored to the wrong revision: %+v", saved.Launch)
			}
			if c.committed {
				f.w.state = state
				f.runtime.frozen = false
				f.service.ApplyMembership(t.Context(), f.msg)
				if f.saved().Membership != nil || f.server.phase != "installed" {
					t.Fatalf("the repeated apply did not finish: %+v %s", f.saved().Membership, f.server.phase)
				}
			}
		})
	}
}

func sandboxRemoval(f *sandboxMembership, id string) protocol.WorkspaceMembershipApply {
	next := f.w.state.Launch
	next.Revision++
	next.Members = nil
	for _, member := range f.w.state.Launch.Members {
		if member.ID != id {
			next.Members = append(next.Members, member)
		}
	}
	next.Digest = workspaceselection.Digest(next)
	f.server.proposed, f.server.phase, f.server.reports = next, "authorized", nil
	return protocol.WorkspaceMembershipApply{SessionID: f.w.state.Session, OperationID: "operation-2", Kind: "remove", MemberID: id, Proposed: next, OldDigest: f.w.state.Launch.Digest}
}

func TestMembershipSandboxRemoveRefusesUnfinishedWork(t *testing.T) {
	f := sandboxMembershipSetup(t)
	two := filepath.Join(f.w.state.Root, "two")
	draft := filepath.Join(two, "draft.txt")
	if err := os.WriteFile(draft, []byte("unfinished"), 0600); err != nil {
		t.Fatal(err)
	}
	refused := func(what string) {
		t.Helper()
		f.service.ApplyMembership(t.Context(), sandboxRemoval(f, "two"))
		if len(f.server.reports) != 1 || f.server.reports[0].Refusal != protocol.MembershipRefuseWork {
			t.Fatalf("%s: reports=%+v", what, f.server.reports)
		}
		if state := f.saved(); state.Membership != nil || len(state.Members) != 2 {
			t.Fatalf("%s: state %+v", what, state.Membership)
		}
	}
	refused("uncommitted")
	if err := os.Remove(draft); err != nil {
		t.Fatal(err)
	}
	childCommit(t, two, "undelivered.txt")
	refused("undelivered")
	if _, err := f.w.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Delivered: the membership ends and the checkout stays.
	f.service.ApplyMembership(t.Context(), sandboxRemoval(f, "two"))
	state := f.saved()
	if state.Membership != nil || len(state.Members) != 1 || state.Members[0].Member.ID != "one" {
		t.Fatalf("reports=%+v state %+v", f.server.reports, state.Members)
	}
	if _, err := os.Stat(filepath.Join(two, ".git")); err != nil {
		t.Fatal("the removed checkout was deleted", err)
	}
}

func TestMembershipSandboxRemoveRefusesTheAnchoredRepository(t *testing.T) {
	f := sandboxMembershipSetup(t)
	f.service.ApplyMembership(t.Context(), sandboxRemoval(f, "one"))
	if len(f.server.reports) != 1 || f.server.reports[0].Refusal != protocol.MembershipRefuseAnchor {
		t.Fatalf("reports=%+v", f.server.reports)
	}
}

// A failed thaw leaves the commit standing and the journal in place: the sweep's
// repeated apply finishes it, and only then is the change reported installed.
func TestMembershipSandboxThawFailureIsRetried(t *testing.T) {
	f := sandboxMembershipSetup(t)
	f.runtime.failThaw = true
	f.service.ApplyMembership(t.Context(), f.msg)
	state := f.saved()
	if state.Membership == nil || state.Membership.Phase != membershipCommitted || state.Launch.Digest != f.msg.Proposed.Digest || !state.Unavailable {
		t.Fatalf("journal %+v unavailable=%v", state.Membership, state.Unavailable)
	}
	if f.server.installed != 0 || f.server.phase != "committed" {
		t.Fatalf("installed=%d phase=%s with the runtime still frozen", f.server.installed, f.server.phase)
	}
	if len(f.server.reports) != 1 || f.server.reports[0].Waiting != protocol.MembershipWaitInstall {
		t.Fatalf("reports %+v", f.server.reports)
	}
	f.runtime.failThaw = false
	f.service.ApplyMembership(t.Context(), f.msg)
	state = f.saved()
	if state.Membership != nil || state.Unavailable || f.server.installed != 1 || f.runtime.frozen {
		t.Fatalf("journal %+v unavailable=%v installed=%d frozen=%v", state.Membership, state.Unavailable, f.server.installed, f.runtime.frozen)
	}
}

// The helper is rebuilt after the thaw. A failure there keeps the journal, and the
// retry finishes the reader without thawing a container that is already running.
func TestMembershipSandboxReaderFailureIsRetried(t *testing.T) {
	f := sandboxMembershipSetup(t)
	fails := true
	f.w.reader = &boundWorkspaceExplorer{workspace: f.w, remote: &remoteWorkspaceExplorer{command: func() *exec.Cmd {
		if fails {
			return exec.Command("false")
		}
		prepared, _ := json.Marshal(helperPrepared{WorkspaceRuntimePrepared: protocol.WorkspaceRuntimePrepared{Type: "workspace_runtime_prepared", SessionID: f.w.state.Session, Binding: *f.w.state.Binding}})
		return exec.Command("sh", "-c", `printf '%s\n' "$0"; cat >/dev/null`, string(prepared))
	}}}
	f.service.ApplyMembership(t.Context(), f.msg)
	state := f.saved()
	if state.Membership == nil || !state.Membership.Thawed || f.server.installed != 0 || state.Unavailable {
		t.Fatalf("journal %+v installed=%d unavailable=%v", state.Membership, f.server.installed, state.Unavailable)
	}
	if len(f.server.reports) != 1 || f.server.reports[0].Waiting != protocol.MembershipWaitInstall {
		t.Fatalf("reports %+v", f.server.reports)
	}
	thaws := f.runtime.thaw
	fails = false
	f.service.ApplyMembership(t.Context(), f.msg)
	defer func() { _ = f.w.reader.remote.Close() }()
	if f.runtime.thaw != thaws {
		t.Fatalf("thawed a running container: %d -> %d", thaws, f.runtime.thaw)
	}
	if state = f.saved(); state.Membership != nil || f.server.installed != 1 || f.w.reader.remote.failed {
		t.Fatalf("journal %+v installed=%d failed=%v reports=%+v", state.Membership, f.server.installed, f.w.reader.remote.failed, f.server.reports)
	}
}

// An earlier unpause can take effect before a later step fails. The retry finds a
// running container, which is success, not a second failure.
func TestDockerThawRetryAcceptsARunningContainer(t *testing.T) {
	id := strings.Repeat("a", 64)
	t.Setenv("PATH", writeScript(t, "docker", `
case "$3" in
unpause) echo "Container is not paused" >&2; exit 1 ;;
inspect)
	if [ "$4" = "--format" ]; then echo "true false"; exit 0; fi
	echo '[{"ID":"`+id+`","Config":{"Labels":{"`+sandboxContextLabel+`":"default","`+sandboxOwnerLabel+`":"owner","`+workspaceSessionLabel+`":"session","`+workspaceLaunchLabel+`":"launch"}}}]'
	;;
esac
`)+string(os.PathListSeparator)+os.Getenv("PATH"))
	d := &DockerWorkspaceContainer{Context: "default", ID: id, Owner: "owner", Session: "session", Launch: "launch"}
	if err := d.Thaw(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// A daemon dies during installation. Startup moves the commit forward, but only a
// runtime that came back can close it: the server's retries must outlive a reopen
// that fails after the restart, and the acknowledgment must wait for the reader.
func TestMembershipStartupKeepsRecoveryUntilTheWorkspaceReopens(t *testing.T) {
	f := sandboxMembershipSetup(t)
	type crash struct{}
	workspaceCrashHook = func(stage string) {
		if stage == "membership-live-installed" {
			panic(crash{})
		}
	}
	defer func() { workspaceCrashHook = nil }()
	func() {
		defer func() { _ = recover() }()
		f.service.ApplyMembership(t.Context(), f.msg)
	}()
	workspaceCrashHook = nil
	state, err := f.service.reconcileMembershipJournal(t.Context(), f.saved(), f.w.record)
	if err != nil {
		t.Fatal(err)
	}
	if saved := f.saved(); saved.Membership == nil || saved.Membership.Phase != membershipCommitted || saved.Launch.Digest != f.msg.Proposed.Digest {
		t.Fatalf("journal %+v", saved.Membership)
	}
	if f.server.installed != 0 || f.server.phase != "committed" {
		t.Fatalf("acknowledged before reopening: installed=%d phase=%s", f.server.installed, f.server.phase)
	}

	// Recovery registered the workspace and reopened its runtime; the reader fails.
	f.w.state = state
	f.runtime.frozen = false
	thaws := f.runtime.thaw
	fails := true
	f.w.reader = &boundWorkspaceExplorer{workspace: f.w, remote: &remoteWorkspaceExplorer{command: func() *exec.Cmd {
		if fails {
			return exec.Command("false")
		}
		prepared, _ := json.Marshal(helperPrepared{WorkspaceRuntimePrepared: protocol.WorkspaceRuntimePrepared{Type: "workspace_runtime_prepared", SessionID: f.w.state.Session, Binding: *f.w.state.Binding}})
		return exec.Command("sh", "-c", `printf '%s\n' "$0"; cat >/dev/null`, string(prepared))
	}}}
	defer func() { _ = f.w.reader.remote.Close() }()
	f.service.ApplyMembership(t.Context(), f.msg)
	if f.saved().Membership == nil || f.server.installed != 0 {
		t.Fatalf("a failed reopen ended the retries: installed=%d", f.server.installed)
	}
	fails = false
	f.service.ApplyMembership(t.Context(), f.msg)
	if f.saved().Membership != nil || f.server.installed != 1 || f.runtime.thaw != thaws {
		t.Fatalf("journal %+v installed=%d thaws %d -> %d", f.saved().Membership, f.server.installed, thaws, f.runtime.thaw)
	}
}

// After the thaw nothing fences the agent, so a retry must not delete a clone, even
// one that would pass its proof. The next fenced change does.
func TestReservedSandboxRetryAfterThawDeletesNothing(t *testing.T) {
	f := reservedSandbox(t)
	f.service.ApplyMembership(t.Context(), f.msg)
	if _, err := f.w.Sync(t.Context()); err != nil {
		t.Fatal(err)
	}
	type crash struct{}
	workspaceCrashHook = func(stage string) {
		if stage == "membership-state-saved" {
			panic(crash{})
		}
	}
	defer func() { workspaceCrashHook = nil }()
	removal := sandboxRemoval(f, "three")
	func() {
		defer func() { _ = recover() }()
		f.service.ApplyMembership(t.Context(), removal)
	}()
	workspaceCrashHook = nil
	if len(f.w.state.ReservedRetired) != 1 || f.w.state.Membership == nil {
		t.Fatalf("retired %+v journal %+v", f.w.state.ReservedRetired, f.w.state.Membership)
	}
	f.w.state.Membership.Thawed = true
	f.runtime.frozen = false
	f.service.ApplyMembership(t.Context(), removal)
	if _, err := os.Lstat(f.clone); err != nil {
		t.Fatalf("deleted with writers running: %v", err)
	}
	if state := f.saved(); state.Membership != nil || len(state.ReservedRetired) != 1 {
		t.Fatalf("journal %+v retired %+v", state.Membership, state.ReservedRetired)
	}
}
