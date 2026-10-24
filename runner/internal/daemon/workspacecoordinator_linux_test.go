//go:build linux

package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
	"golang.org/x/sys/unix"
)

func TestCoordHelperProcess(t *testing.T) {
	if len(os.Args) < 2 {
		return
	}
	if os.Args[len(os.Args)-1] == "coord-helper" {
		if shellruntime.ServeHelper(os.Stdin, os.Stdout) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) > 2 && os.Args[len(os.Args)-2] == "coord-helper" && os.Args[len(os.Args)-1] == "--probe" {
		if shellruntime.Probe(os.Stdout) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
}

type coordRig struct {
	failPhase atomic.Bool
	service   *shellruntime.Service
	mu        sync.Mutex
	pending   map[string]chan shellruntime.RPC
	output    chan []byte
	seq       atomic.Uint64
	key       ed25519.PrivateKey
	binding   ownershell.Binding
	grant     ownershell.SignedGrant
}

func newCoordRig(t *testing.T) *coordRig {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	r := &coordRig{pending: map[string]chan shellruntime.RPC{}, output: make(chan []byte, 1024), key: key}
	r.service = shellruntime.NewService(func(v shellruntime.RPC) error {
		if v.Type == "owner_shell_phase" && r.failPhase.Load() {
			return errors.New("phase transport unavailable")
		}
		if v.Type == "owner_shell_output" {
			select {
			case r.output <- v.Data:
			default:
			}
			return nil
		}
		r.mu.Lock()
		ch := r.pending[v.RequestID]
		r.mu.Unlock()
		if ch != nil {
			ch <- v
		}
		return nil
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := r.service.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return r
}
func (r *coordRig) call(ctx context.Context, v shellruntime.RPC) (shellruntime.RPC, error) {
	v.Type = "owner_shell_request"
	v.Version = 1
	v.RequestID = fmt.Sprint(r.seq.Add(1))
	ch := make(chan shellruntime.RPC, 1)
	r.mu.Lock()
	r.pending[v.RequestID] = ch
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.pending, v.RequestID); r.mu.Unlock() }()
	raw, _ := json.Marshal(v)
	r.service.HandleContext(context.WithoutCancel(ctx), raw)
	select {
	case reply := <-ch:
		if !reply.OK {
			return reply, errors.New("shell RPC refused")
		}
		return reply, nil
	case <-ctx.Done():
		return shellruntime.RPC{}, ctx.Err()
	}
}
func (r *coordRig) configure(t *testing.T, id string, consent *protocol.HostShellConsent) {
	t.Helper()
	b, err := r.service.Binding(id)
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.call(t.Context(), shellruntime.RPC{SessionID: id, Op: "configure", Binding: b, Key: r.key.Public().(ed25519.PublicKey), Policy: protocol.ShellPolicy{Version: 1, Enabled: true, Revision: 1}, Consent: consent})
	if err != nil {
		t.Fatal(err)
	}
	r.binding = result.Binding
}
func (r *coordRig) open(t *testing.T, id string, expiry int64) {
	t.Helper()
	b := r.binding
	now := time.Now().Unix()
	if expiry == 0 {
		expiry = now + 30
	}
	r.grant = ownershell.SignGrant(r.key, protocol.ShellGrant{Version: 1, Audience: b.Audience, OwnerID: b.Owner, LoginID: "login", SessionID: b.Session, LaunchGeneration: b.Launch, ConnectionID: b.Connection, ShellGeneration: id, Runtime: b.Runtime, ConsentID: b.Consent, PolicyRevision: 1, IssuedAt: now, ExpiresAt: expiry})
	if _, err := r.call(t.Context(), shellruntime.RPC{SessionID: b.Session, Op: "open", Grant: r.grant}); err != nil {
		t.Fatal(err)
	}
}
func (r *coordRig) input(t *testing.T, seq uint64, text string) {
	t.Helper()
	if _, err := r.call(t.Context(), shellruntime.RPC{SessionID: r.binding.Session, Op: "input", Grant: r.grant, Seq: seq, Data: []byte(text)}); err != nil {
		t.Fatal(err)
	}
}
func (r *coordRig) wait(t *testing.T, marker string) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	var got string
	for {
		select {
		case data := <-r.output:
			got += string(data)
			if strings.Contains(got, marker) {
				return
			}
		case <-timer.C:
			t.Fatal("shell marker missing")
		}
	}
}
func hostCoordRig(t *testing.T) (*coordRig, *workspaceSupervisors, protocol.SupervisorLaunch, string) {
	t.Helper()
	r := newCoordRig(t)
	cwd := t.TempDir()
	i, err := shellruntime.ResolveIdentity(cwd)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(cwd)
	if err != nil {
		t.Fatal(err)
	}
	b := ownershell.Binding{Session: "coord-host", Owner: "owner", Launch: "launch", Runtime: "host", RuntimeGeneration: 7, Connection: "shell-connection", Root: workspaceRootIdentity(info), ManifestDigest: "digest", Live: true, Consent: "consent", HostConsentValid: true}
	b.Audience = ownershell.RuntimeAudience(b.Connection, b.RuntimeGeneration)
	consent := &protocol.HostShellConsent{ID: b.Consent, OwnerID: b.Owner, LaunchGeneration: b.Launch, MachineID: i.Machine, Account: i.Account, UID: i.UID, Shell: i.Shell, Cwd: i.Cwd, ChallengeID: "challenge"}
	command := shellruntime.Command{Executable: os.Args[0], Args: []string{"-test.run=^TestCoordHelperProcess$", "--", "coord-helper"}, Identity: i, Consent: consent}
	// Independent kernel checks for the real test shell and detached descendant.
	reconcile := func(context.Context) error {
		for _, name := range []string{"shell.pid", "background.pid"} {
			data, e := os.ReadFile(filepath.Join(cwd, name))
			if os.IsNotExist(e) {
				continue
			}
			if e != nil {
				return e
			}
			pid, e := strconv.Atoi(strings.TrimSpace(string(data)))
			if e != nil {
				return e
			}
			if unix.Kill(pid, 0) == nil {
				return errors.New("host test writer still exists")
			}
		}
		return nil
	}
	if err = r.service.Register(t.Context(), new(sync.Mutex), b, command, reconcile); err != nil {
		t.Fatal(err)
	}
	r.configure(t, b.Session, consent)
	l := ownershell.SupervisorIdentity(b, ownershell.SignedGrant{}).Launch
	l.ConnectionID = "authority-connection"
	s := newWorkspaceSupervisors(r.service)
	s.sessions[b.Session] = &workspaceCoordinator{service: r.service, launch: l, binding: protocol.WorkspaceRuntimeBinding{Root: b.Root, ManifestDigest: b.ManifestDigest}}
	return r, s, l, cwd
}

func TestCoordMissingRegistrationRefusesProof(t *testing.T) {
	s := newWorkspaceSupervisors(shellruntime.NewService(func(shellruntime.RPC) error { return nil }))
	l := protocol.SupervisorLaunch{SessionID: "missing", LaunchGeneration: "launch", RuntimeGeneration: 1, Runtime: "sandbox", ConnectionID: "connection"}
	if _, err := s.CloseLaunch(t.Context(), l, "stop"); err == nil {
		t.Fatal("missing registration invented empty-process proof")
	}
	if _, err := s.RecoverLaunch(t.Context(), l); err == nil {
		t.Fatal("missing recovery registration invented proof")
	}
	var missing *workspaceSupervisors
	if _, err := missing.peer(l.SessionID); err == nil {
		t.Fatal("nil registration accepted")
	}
}

func TestCoordHostConcurrentSyncShellStop(t *testing.T) {
	r, s, l, cwd := hostCoordRig(t)
	r.open(t, "first", 0)
	r.input(t, 1, "echo $$ > shell.pid; setsid /bin/sh -c 'echo $$ > background.pid; while :; do sleep 1; done' </dev/null >/dev/null 2>&1 &\nprintf '\\122\\105\\101\\104\\131'\n")
	r.wait(t, "READY")
	// Completion of the background readiness write is checked independently.
	until := time.Now().Add(3 * time.Second)
	for {
		if data, _ := os.ReadFile(filepath.Join(cwd, "background.pid")); len(data) > 0 {
			break
		}
		if time.Now().After(until) {
			t.Fatal("background not started")
		}
		time.Sleep(time.Millisecond)
	}
	c, _ := s.peer(l.SessionID)
	var outer sync.Mutex
	outer.Lock()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	untilLease, err := c.freeze(ctx)
	cancel()
	outer.Unlock()
	if err != nil || !untilLease.After(time.Now()) {
		t.Fatalf("sync nested locking or lease failure: %v", err)
	}
	if _, err = r.call(t.Context(), shellruntime.RPC{SessionID: l.SessionID, Op: "input", Grant: r.grant, Seq: 2, Data: []byte("echo forbidden\n")}); err == nil {
		t.Fatal("shell input dispatched while syncing")
	}
	if err = s.Notify(t.Context(), l, "running"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			_, _ = r.call(ctx, shellruntime.RPC{SessionID: l.SessionID, Op: "input", Grant: r.grant, Seq: uint64(n + 3), Data: []byte("printf x\n")})
		}(n)
	}
	proof, err := s.CloseLaunch(t.Context(), l, "stop during output")
	wg.Wait()
	if err != nil || proof.ValidateFor(l, "") != nil {
		t.Fatalf("stop did not join exact cleanup: %v", err)
	}
	for _, name := range []string{"shell.pid", "background.pid"} {
		data, e := os.ReadFile(filepath.Join(cwd, name))
		if e != nil {
			t.Fatal(e)
		}
		pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
		if unix.Kill(pid, 0) == nil {
			t.Fatal("writer survived cleanup")
		}
	}
	if _, err = r.call(t.Context(), shellruntime.RPC{SessionID: l.SessionID, Op: "open", Grant: r.grant}); err == nil {
		t.Fatal("terminal stop revived shell")
	}
}

// This reader is waiting to enter P2 when sync takes the session lock. Suspend
// must cancel and join it without deadlocking on that same lock.
type coordWaitingReader struct {
	w       *LocalWorkspace
	entered chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (f *coordWaitingReader) Explore(ctx context.Context, req protocol.WorkspaceExplorerRequest) protocol.WorkspaceExplorerResult {
	f.calls.Add(1)
	f.once.Do(func() { close(f.entered) })
	_ = f.w.WithBoundRuntimeMember(ctx, req.WorkspaceRuntimeRef, protocol.WorkspaceMemberRef{MemberID: "one", LaunchGeneration: "generation"}, func(context.Context, WorkspaceContainer, string) error { return nil })
	return explorerFailure(req, "unavailable")
}
func (f *coordWaitingReader) ReadFile(ctx context.Context, req protocol.ReadWorkspaceFile) protocol.WorkspaceFileResult {
	f.calls.Add(1)
	<-ctx.Done()
	return protocol.WorkspaceFileResult{}
}
func TestCoordReadersCancelJoinResumeAndStop(t *testing.T) {
	w, _, _, _ := localWorkspaceFixture(t)
	f := &coordWaitingReader{w: w, entered: make(chan struct{})}
	var router WorkspaceExplorerRouter
	router.Bind(w.state.Session, f)
	defer router.Close()
	sender := &recordingSender{}
	w.mu.Lock()
	req := protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", RequestID: "pending", SessionID: w.state.Session, Operation: "status"}
	raw, _ := json.Marshal(req)
	router.Handle(t.Context(), sender, raw)
	<-f.entered
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	err := router.Suspend(ctx, w.state.Session)
	cancel()
	w.mu.Unlock()
	if err != nil {
		t.Fatal("reader cancellation failed to join under P2 lock")
	}
	before := f.calls.Load()
	req.RequestID = "suspended"
	raw, _ = json.Marshal(req)
	router.Handle(t.Context(), sender, raw)
	router.HandleFile(t.Context(), sender, protocol.ReadWorkspaceFile{RequestID: "file", SessionID: w.state.Session})
	router.mu.Lock()
	active := len(router.pending)
	router.mu.Unlock()
	if active != 0 || f.calls.Load() != before {
		t.Fatal("reader admitted while frozen")
	}
	if err := router.Resume(w.state.Session); err != nil {
		t.Fatal(err)
	}
	req.RequestID = "resumed"
	raw, _ = json.Marshal(req)
	router.Handle(t.Context(), sender, raw)
	deadline := time.Now().Add(time.Second)
	for f.calls.Load() == before {
		if time.Now().After(deadline) {
			t.Fatal("reader registration lost during sync")
		}
		time.Sleep(time.Millisecond)
	}
	if err := router.Suspend(t.Context(), w.state.Session); err != nil {
		t.Fatal(err)
	}
}

func TestCoordHostRebindRequiresFreshGrantAndExactIncarnation(t *testing.T) {
	r, s, l, cwd := hostCoordRig(t)
	r.open(t, "old", 0)
	r.input(t, 1, "echo $$ > shell.pid; printf '\\122\\105\\101\\104\\131'\n")
	r.wait(t, "READY")
	oldGrant := r.grant
	if err := r.service.Rebind(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, _ := s.peer(l.SessionID)
	next := l
	next.ConnectionID = "new-authority-connection"
	if err := c.rebind(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	if err := s.Notify(t.Context(), l, "running"); err == nil {
		t.Fatal("old authority connection accepted after rebind")
	}
	if _, err := r.call(t.Context(), shellruntime.RPC{SessionID: l.SessionID, Op: "input", Grant: oldGrant, Seq: 2, Data: []byte("echo stale\n")}); err == nil {
		t.Fatal("old grant transferred on rebind")
	}
	i, err := shellruntime.ResolveIdentity(cwd)
	if err != nil {
		t.Fatal(err)
	}
	consent := &protocol.HostShellConsent{ID: "fresh", OwnerID: "owner", LaunchGeneration: l.LaunchGeneration, MachineID: i.Machine, Account: i.Account, UID: i.UID, Shell: i.Shell, Cwd: i.Cwd, ChallengeID: "fresh-challenge"}
	r.configure(t, l.SessionID, consent)
	r.open(t, "fresh", 0)
	proof, err := s.CloseLaunch(t.Context(), next, "done")
	if err != nil || proof.ValidateFor(next, "") != nil {
		t.Fatal("fresh binding could not settle", err)
	}
}

type coordDockerReader struct {
	d     *DockerWorkspaceContainer
	calls atomic.Int32
}

func (r *coordDockerReader) Explore(ctx context.Context, req protocol.WorkspaceExplorerRequest) protocol.WorkspaceExplorerResult {
	r.calls.Add(1)
	// Exercise an actual runtime status process and cancellation; no host Git.
	_, err := r.d.command(ctx, "exec", "-w", "/workspace/members/one", r.d.ID, "git", "--no-optional-locks", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "status", "--porcelain=v2", "-z", "--no-renames", "--untracked-files=normal", "--ignore-submodules=all")
	if err != nil {
		return explorerFailure(req, "unavailable")
	}
	return protocol.WorkspaceExplorerResult{OK: true}
}
func (*coordDockerReader) Close() error { return nil }

func dockerCoordRig(t *testing.T) (*LocalWorkspace, *DockerWorkspaceContainer, *coordRig, *workspaceSupervisors) {
	t.Helper()
	binary := os.Getenv("BLERG_SHELL_HELPER_TEST_BINARY")
	if binary == "" {
		t.Skip("explicit static Docker helper required")
	}
	w, d := p2DockerWorkspace(t)
	// Only the uniquely labelled fixture container is modified.
	p2Docker(t, "cp", binary, d.ID+":/usr/local/bin/blerg-shell-supervisor")
	p2Docker(t, "exec", "--user", "0:0", d.ID, "chmod", "755", "/usr/local/bin/blerg-shell-supervisor")
	r := newCoordRig(t)
	t.Cleanup(func() { _ = d.joinFreeze() })
	s := newWorkspaceSupervisors(r.service)
	a := &testWorkspaceAuthority{}
	service := &LocalWorkspaceService{Authority: a, Supervisor: s}
	w.state.ShellEligible = true
	w.state.SupervisorLaunch, _ = a.Launch(t.Context(), w.state.Session, w.state.Launch, "token")
	if err := w.save(); err != nil {
		t.Fatal(err)
	}
	root, err := d.RootIdentity(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = service.bindAuthority(t.Context(), w, d, root); err != nil {
		t.Fatal(err)
	}
	r.configure(t, w.state.Session, nil)
	return w, d, r, s
}

func TestCoordDockerRegistrationLockOrder(t *testing.T) {
	w, d, _, s := dockerCoordRig(t)
	w.mu.Lock()
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	err := s.Notify(ctx, w.state.SupervisorLaunch, "syncing")
	cancel()
	w.mu.Unlock()
	if err != nil {
		t.Fatal("concrete registration nested P2 mutex", err)
	}
	if err = s.Notify(t.Context(), w.state.SupervisorLaunch, "running"); err != nil {
		t.Fatal(err)
	}
	if err = d.Identity(t.Context(), d.Session, d.Launch); err != nil {
		t.Fatal(err)
	}
}

func TestCoordDockerSyncStatusShellAndExactRelease(t *testing.T) {
	w, d, r, s := dockerCoordRig(t)
	session := w.state.Session
	var router WorkspaceExplorerRouter
	defer router.Close()
	reader := &coordDockerReader{d: d}
	router.Bind(session, reader)
	d.CancelStatus = func(ctx context.Context) error { return router.Suspend(ctx, session) }
	d.ResumeStatus = func(context.Context) error { return router.Resume(session) }
	d.CloseStatus = func(ctx context.Context) error { return router.Release(ctx, session) }
	r.open(t, "docker", 0)
	r.input(t, 1, "setsid /bin/sh -c 'while :; do printf x >> /workspace/background.txt; sleep 0.01; done' </dev/null >/dev/null 2>&1 &\nprintf '\\122\\105\\101\\104\\131'\n")
	r.wait(t, "READY")
	// A real freeze includes a detached shell descendant. It cannot write while
	// paused; after verified thaw the same writer must make progress again.
	w.mu.Lock()
	err := d.Freeze(t.Context())
	w.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(w.state.Root, "background.txt")
	before, _ := os.ReadFile(file)
	time.Sleep(80 * time.Millisecond)
	after, _ := os.ReadFile(file)
	if len(before) == 0 || string(before) != string(after) {
		t.Fatal("shell background descendant ran while frozen")
	}
	if _, err = r.call(t.Context(), shellruntime.RPC{SessionID: session, Op: "input", Grant: r.grant, Seq: 2, Data: []byte("echo forbidden\n")}); err == nil {
		t.Fatal("input admitted during real Docker freeze")
	}
	w.mu.Lock()
	err = d.Thaw(t.Context())
	w.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		after, _ = os.ReadFile(file)
		if len(after) > len(before) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background did not resume")
		}
		time.Sleep(5 * time.Millisecond)
	}
	childCommit(t, w.state.Members[0].Clone, "change.txt")
	sender := &recordingSender{}
	run, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	wg.Go(func() {
		for n := 0; run.Err() == nil; n++ {
			req := protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", SessionID: session, RequestID: fmt.Sprint(n), Operation: "status"}
			raw, _ := json.Marshal(req)
			router.Handle(run, sender, raw)
			time.Sleep(2 * time.Millisecond)
		}
	})
	wg.Go(func() {
		for n := 2; run.Err() == nil; {
			ctx, stop := context.WithTimeout(run, time.Second)
			reply, callErr := r.call(ctx, shellruntime.RPC{SessionID: session, Op: "input", Grant: r.grant, Seq: uint64(n), Data: []byte("printf z\n")})
			if callErr == nil && reply.OK {
				n++
			}
			stop()
			time.Sleep(5 * time.Millisecond)
		}
	})
	results, err := w.Sync(t.Context())
	cancel()
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Pending || results[0].SyncError != "" {
		t.Fatal("concrete supervisor sync failed to publish")
	}
	if reader.calls.Load() == 0 {
		t.Fatal("status did not overlap sync")
	}
	router.mu.Lock()
	registered := router.runtimes[session]
	resumed := registered != nil && !registered.suspended
	router.mu.Unlock()
	if !resumed {
		t.Fatal("sync permanently removed explorer registration")
	}
	// Stop during live output joins shell readers and unregisters exactly once.
	if _, err = w.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r.service.Lifecycle(session) != nil {
		t.Fatal("physical removal retained P5 registration")
	}
	if absent, err := d.Absent(t.Context()); err != nil || !absent {
		t.Fatal("runtime absence unproven")
	}
	router.mu.Lock()
	registered = router.runtimes[session]
	router.mu.Unlock()
	if registered != nil {
		t.Fatal("reader survived storage release boundary")
	}
	next := w.state.SupervisorLaunch
	next.ConnectionID = "retired-new-connection"
	if err := d.coordinator.rebind(t.Context(), next); err != nil {
		t.Fatal("retired coordinator refused finalization reconnect", err)
	}
	if err := s.Notify(t.Context(), next, "running"); err == nil {
		t.Fatal("retired rebind resurrected shell")
	}
	proof, err := s.CloseLaunch(t.Context(), next, "retry")
	if err != nil || proof.ValidateFor(next, "") != nil {
		t.Fatal("exact retired close was not idempotent", err)
	}
	if _, err = s.CloseLaunch(t.Context(), w.state.SupervisorLaunch, "stale"); err == nil {
		t.Fatal("retired supervisor accepted old authority connection")
	}

}

func TestCoordDockerFrozenLeaseAndCrashRecovery(t *testing.T) {
	w, d, r, _ := dockerCoordRig(t)
	r.open(t, "expiring", time.Now().Unix()+3)
	r.input(t, 1, "echo $$ > /workspace/shell.pid; printf '\\122\\105\\101\\104\\131'\n")
	r.wait(t, "READY")
	w.mu.Lock()
	err := d.Freeze(t.Context())
	w.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// No in-container helper timer can tick now. The host owner must fence it.
	deadline := time.Now().Add(6 * time.Second)
	for {
		raw, e := d.command(t.Context(), "inspect", "--format", "{{.State.Running}}", d.ID)
		if e == nil && strings.TrimSpace(string(raw)) == "false" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("paused helper outlived wall-clock lease")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err = d.Thaw(t.Context()); err == nil {
		t.Fatal("lease-fenced runtime thawed into shell admission")
	}
	// A new process has no P5 registration. Authenticated recovery registers only
	// an independently fenced physical tuple and cannot reopen that incarnation.
	fresh := newWorkspaceSupervisors(shellruntime.NewService(func(shellruntime.RPC) error { return nil }))
	if _, err = fresh.RecoverLaunch(t.Context(), w.state.SupervisorLaunch); err == nil {
		t.Fatal("restart assumed empty shell map")
	}
	peer := &DockerWorkspaceContainer{Context: d.Context, ID: d.ID, Owner: d.Owner, Session: d.Session, Launch: d.Launch, Paths: d.Paths, CancelStatus: func(context.Context) error { return nil }}
	if err = fresh.registerRecovery(t.Context(), w.state, peer); err != nil {
		t.Fatal(err)
	}
	proof, err := fresh.RecoverLaunch(t.Context(), w.state.SupervisorLaunch)
	if err != nil || proof.ValidateFor(w.state.SupervisorLaunch, "") != nil {
		t.Fatal("recovery did not prove exact physical cleanup", err)
	}
	if err = fresh.AdmitShell(t.Context(), protocol.SupervisorShell{Launch: w.state.SupervisorLaunch, ShellGeneration: "stale"}); err == nil {
		t.Fatal("orphan recovery revived old writer")
	}
	// RecoverLocalWorkspace retains the independently fenced allocation and Stop
	// settles all members before release. No host status on a sandbox clone.
	recovered, err := RecoverLocalWorkspace(t.Context(), w.state.Session, w.state.Launch, w.storage, peer)
	if err != nil {
		t.Fatal(err)
	}
	peer.Supervisor = fresh
	peer.SupervisorLaunch = w.state.SupervisorLaunch
	if _, err = recovered.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCoordShellEligibilityIsAffirmative(t *testing.T) {
	w, _, _, _ := localWorkspaceFixture(t)
	w.state.Binding = &protocol.WorkspaceRuntimeBinding{Root: w.state.RootIdentity}
	for _, eligible := range []bool{false, true} {
		w.state.ShellEligible = eligible
		b := workspaceShellBinding(w.state)
		if b.Child == eligible {
			t.Fatal("missing/restricted eligibility admitted owner shells")
		}
	}
}

func TestCoordDockerThawRequiresCurrentAuthority(t *testing.T) {
	w, d, r, _ := dockerCoordRig(t)
	r.open(t, "fenced", 0)
	w.mu.Lock()
	err := d.Freeze(t.Context())
	w.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	d.ResumeCheck = func(context.Context) error { return errors.New("authority revoked during copy") }
	w.mu.Lock()
	err = d.Thaw(t.Context())
	w.mu.Unlock()
	if err == nil {
		t.Fatal("thaw reopened shell before current authority")
	}
	if _, err = r.call(t.Context(), shellruntime.RPC{SessionID: w.state.Session, Op: "input", Grant: r.grant, Seq: 1, Data: []byte("echo stale\n")}); err == nil {
		t.Fatal("failed thaw admitted input")
	}
}

type coordClosingReader struct {
	fail   bool
	closed int
}

func (*coordClosingReader) Explore(_ context.Context, req protocol.WorkspaceExplorerRequest) protocol.WorkspaceExplorerResult {
	return explorerFailure(req, "unavailable")
}
func (r *coordClosingReader) Close() error {
	r.closed++
	if r.fail {
		return errors.New("reader join failed")
	}
	return nil
}
func TestCoordReaderReleaseRetainsFailedJoin(t *testing.T) {
	var router WorkspaceExplorerRouter
	defer router.Close()
	reader := &coordClosingReader{fail: true}
	router.Bind("session", reader)
	if err := router.Release(t.Context(), "session"); err == nil {
		t.Fatal("reader failure allowed release")
	}
	router.mu.Lock()
	entry := router.runtimes["session"]
	router.mu.Unlock()
	if entry == nil || !entry.suspended {
		t.Fatal("failed reader cleanup lost its fence")
	}
	reader.fail = false
	if err := router.Release(t.Context(), "session"); err != nil {
		t.Fatal(err)
	}
	if reader.closed != 2 {
		t.Fatal("reader cleanup was not retried exactly")
	}
}

// Host workspaces have no sandbox copy operation: exercise the real host status
// backend and live shell while the shared suspend/phase/resume boundary cycles.
func TestCoordHostStatusShellBoundaryRace(t *testing.T) {
	r, s, l, cwd := hostCoordRig(t)
	initChildRepo(t, filepath.Join(cwd, "member"))
	manifest := protocol.WorkspaceManifest{Version: 2, Kind: "repo", OwnerID: "owner", Runtime: "host", LaunchGeneration: l.LaunchGeneration, Digest: "digest", Members: []protocol.WorkspaceMember{{ID: "one", Name: "one", Path: "member", SourceIdentity: "source", CommonDirectoryIdentity: "common", Destination: "destination"}}}
	b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: l.LaunchGeneration, Generation: l.RuntimeGeneration}, Root: r.binding.Root, ManifestDigest: "digest"}
	engine, err := NewLocalExplorer(cwd, manifest, b, func() bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	var router WorkspaceExplorerRouter
	router.Bind(l.SessionID, engine)
	defer router.Close()
	defer func() { _ = engine.Close() }()
	r.open(t, "host-status", 0)
	r.input(t, 1, "cd member; printf '\\122\\105\\101\\104\\131'\n")
	r.wait(t, "READY")
	sender := &recordingSender{}
	run, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	wg.Go(func() {
		for n := 0; run.Err() == nil; n++ {
			req := protocol.WorkspaceExplorerRequest{Type: "workspace_explorer", SessionID: l.SessionID, RequestID: fmt.Sprint(n), Operation: "status", Path: "member", MemberID: "one", WorkspaceRuntimeRef: b.WorkspaceRuntimeRef}
			raw, _ := json.Marshal(req)
			router.Handle(run, sender, raw)
			time.Sleep(time.Millisecond)
		}
	})
	wg.Go(func() {
		for seq := uint64(2); run.Err() == nil; {
			reply, err := r.call(run, shellruntime.RPC{SessionID: l.SessionID, Op: "input", Grant: r.grant, Seq: seq, Data: []byte("printf h\n")})
			if err == nil && reply.OK {
				seq++
			}
			time.Sleep(time.Millisecond)
		}
	})
	for range 3 {
		if err := router.Suspend(t.Context(), l.SessionID); err != nil {
			t.Fatal(err)
		}
		if err := s.Notify(t.Context(), l, "syncing"); err != nil {
			t.Fatal(err)
		}
		if err := s.Notify(t.Context(), l, "running"); err != nil {
			t.Fatal(err)
		}
		if err := router.Resume(l.SessionID); err != nil {
			t.Fatal(err)
		}
	}
	cancel()
	wg.Wait()
	if err := router.Suspend(t.Context(), l.SessionID); err != nil {
		t.Fatal(err)
	}
	// Independently demand one successful real host status result.
	req := protocol.WorkspaceExplorerRequest{Operation: "status", Path: "member", MemberID: "one", WorkspaceRuntimeRef: b.WorkspaceRuntimeRef}
	if res := engine.Explore(t.Context(), req); !res.OK {
		t.Fatal("real host status failed", res.ErrorCode)
	}
	if err := router.Release(t.Context(), l.SessionID); err != nil {
		t.Fatal(err)
	}
	proof, err := s.CloseLaunch(t.Context(), l, "host stop")
	if err != nil || proof.ValidateFor(l, "") != nil {
		t.Fatal("host race cleanup", err)
	}
}
