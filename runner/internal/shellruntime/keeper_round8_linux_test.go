package shellruntime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/keepertest"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellkeeper"
	"golang.org/x/sys/unix"
)

type round8Client struct {
	service   *Service
	bridge    *ReconnectingHostBridge
	authority *shellkeeper.DaemonAuthority
	frames    chan RPC
	closed    bool
}

var round8ExpiresAt = time.Now().Add(24 * time.Hour)

func round8Connect(t *testing.T, state, journal string) *round8Client {
	t.Helper()
	c := &round8Client{frames: make(chan RPC, 256)}
	var err error
	c.authority, err = shellkeeper.AcquireDaemon(filepath.Join(state, "shell-keeper"))
	if err != nil {
		t.Fatal(err)
	}
	c.service = NewService(func(r RPC) error {
		if r.Type == "daemon_shell_hello" && r.ShellHello != nil {
			hello := *r.ShellHello
			welcome := protocol.ShellLinkWelcome{Version: 2, Runner: "runner", Daemon: hello.Daemon, Challenge: hello.Challenge, Desktop: desktopauth.Binding{DesktopID: "desktop", OwnerID: "owner", TokenID: "credential", Revision: 1, ExpiresAt: round8ExpiresAt}}
			call, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			return c.service.AcceptShellLink(call, hello, &welcome)
		}
		select {
		case c.frames <- r:
			return nil
		case <-t.Context().Done():
			return t.Context().Err()
		}
	})
	if err = c.service.ConfigureHostRetirements(journal); err != nil {
		t.Fatal(err)
	}
	c.bridge = NewReconnectingHostBridge(t.Context(), func(ctx context.Context) (HostBridge, error) {
		snapshot, err := shellkeeper.InspectSnapshot(ctx, filepath.Join(state, "shell-keeper"), c.authority.Epoch)
		if err != nil {
			return nil, err
		}
		b, err := ConnectKeeper(ctx, filepath.Join(state, "shell-keeper"), c.authority.Epoch, snapshot.Generation)
		if err != nil {
			return nil, err
		}
		return b, nil
	})
	c.service.SetHostBridge(c.bridge)
	caps := protocol.WorkspaceCapabilities{ManifestVersion: protocol.WorkspaceVersion, ShellVersion: 1, LinuxHost: true, ShellPersistence: 2, PersistentHost: true}
	c.service.SetCapabilities(caps, caps)
	t.Cleanup(func() { c.close(t) })
	return c
}
func (c *round8Client) ready(t *testing.T) {
	t.Helper()
	guard, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := c.bridge.WaitReady(guard); err != nil {
		t.Fatal("real keeper not ready", err)
	}
}
func (c *round8Client) close(t *testing.T) {
	t.Helper()
	if c.closed {
		return
	}
	c.closed = true
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.service.Shutdown(ctx); err != nil {
		t.Error(err)
	}
	_ = c.bridge.Close()
	_ = c.authority.Close()
}
func (c *round8Client) call(t *testing.T, session string, r RPC) RPC {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	r.Version = 2
	r.SessionID = session
	result, err := c.service.Call(ctx, r)
	if err != nil || !result.OK {
		t.Fatalf("real keeper %s refused: %v reason=%s cause=%s", r.Op, err, result.ErrorCode, result.StartCause)
	}
	return result
}
func (c *round8Client) output(t *testing.T, session string, g ownershell.SignedGrant, marker string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	var output []byte
	for {
		select {
		case r := <-c.frames:
			if r.Type != "owner_shell_output" || r.Epoch != g.Grant.AccessEpoch {
				continue
			}
			output = append(output, r.Data...)
			end := r.Offset + uint64(len(r.Data))
			c.call(t, session, RPC{Op: "credit", Grant: g, Offset: end})
			c.call(t, session, RPC{Op: "receipt", Grant: g, Offset: end})
			if bytes.Contains(output, []byte(marker)) {
				return
			}
		case <-ctx.Done():
			t.Fatal("shell did not produce expected marker")
		}
	}
}
func round8Register(t *testing.T, c *round8Client, b ownershell.Binding, identity Identity) {
	t.Helper()
	command := HostCommand("unused-daemon-path", identity)
	command.Root = b.Root
	command.PublicRoot = identity.Cwd
	command.SourceLive = func() bool { return true }
	command.SourceState = func() (bool, bool) { return true, true }
	if err := c.service.Register(t.Context(), new(sync.Mutex), b, command, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	c.ready(t)
	if _, err := c.service.Binding(b.Session); err != nil {
		t.Fatal("queued registration was not applied", err)
	}
}

func round8Open(t *testing.T, c *round8Client) (ownershell.Binding, protocol.ShellCustody, protocol.ShellGrant, ed25519.PrivateKey, Identity, int) {
	t.Helper()
	base, b, original, key := fixture(t)
	identity, err := ResolvePersistentIdentity(base.command.Identity.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	caps := protocol.WorkspaceCapabilities{ManifestVersion: protocol.WorkspaceVersion, ShellVersion: 1, LinuxHost: true, ShellPersistence: 2, PersistentHost: true}
	b.ServerCapabilities, b.RuntimeCapabilities = caps, caps
	round8Register(t, c, b, identity)
	bound, err := c.service.Binding(b.Session)
	if err != nil {
		t.Fatal(err)
	}
	c.call(t, b.Session, RPC{Op: "configure", Binding: bound, Key: key.Public().(ed25519.PublicKey), Policy: base.command.Policy})
	actor := ownershell.Actor{Kind: "human", Owner: b.Owner, Login: "login"}
	challenge := c.call(t, b.Session, RPC{Op: "consent_challenge", Actor: actor})
	accepted := c.call(t, b.Session, RPC{Op: "consent_accept", Actor: actor, PersistentConsent: challenge.PersistentConsent})
	hello := c.service.ShellHello()
	now := time.Now()
	custody := protocol.ShellCustody{Version: 2, Owner: b.Owner, Login: "login", Session: b.Session, Launch: b.Launch, Runtime: "host", RuntimeGeneration: b.RuntimeGeneration, Root: b.Root, Manifest: b.ManifestDigest, Desktop: "desktop", Credential: "credential", OwnershipRevision: 1, Runner: "runner", Daemon: hello.Daemon, Shell: "round8-shell", Nonce: "round8-private-custody", Identity: RuntimeIdentityDigest(identity), Consent: accepted.PersistentConsent.Host.ID, Created: now.Unix(), LifetimeUntil: now.Add(time.Hour).Unix()}
	grant := original.Grant
	grant.Version = 2
	grant.ConsentID = custody.Consent
	grant.ConnectionID = bound.Connection
	grant.Audience = bound.Audience
	grant.CustodyID = custody.Digest()
	grant.ShellGeneration = custody.Shell
	grant.AccessEpoch = 1
	grant.IssuedAt = now.Unix()
	grant.ExpiresAt = grant.IssuedAt + 30
	signed := ownershell.SignGrant(key, grant)
	c.call(t, b.Session, RPC{Op: "open", Custody: &custody, Grant: signed, Window: ownershell.ServerOutputWindow})
	command := fmt.Sprintf("ROUND8_STATE=survived; printf '%%s\\n' \"$$\" > %s; printf 'ROUND8_READY\\n'\n", filepath.Join(identity.Cwd, "shell.pid"))
	c.call(t, b.Session, RPC{Op: "input", Grant: signed, Seq: 1, Data: []byte(command)})
	c.output(t, b.Session, signed, "ROUND8_READY\r\n")
	raw, err := os.ReadFile(filepath.Join(identity.Cwd, "shell.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	return b, custody, grant, key, identity, pid
}

func TestRound8DurableRetirementAfterOfflineMembershipAndRestart(t *testing.T) {
	binary := keepertest.Build(t)
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprint("restart_", restart), func(t *testing.T) {
			state := t.TempDir()
			journal := filepath.Join(t.TempDir(), "retirements")
			if err := keepertest.Run(t, binary, state, "shell-keeper", "init"); err != nil {
				t.Fatal(err)
			}
			process := keepertest.Start(t, binary, state)
			client := round8Connect(t, state, journal)
			client.ready(t)
			b, _, _, _, _, pid := round8Open(t, client)
			fd, err := unix.PidfdOpen(pid, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = unix.Close(fd) }()
			process.Signal(t, syscall.SIGSTOP)
			guard, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			if err = client.bridge.WaitUnavailable(guard); err != nil {
				t.Fatal("stopped socket appeared live", err)
			}
			_, known := client.service.WorkspaceShellsOpen(b.Session)
			_, privilegedKnown := client.service.PrivilegedShellOpen(b.Session)
			if known || privilegedKnown {
				t.Fatal("unavailable keeper claimed known shell absence")
			}
			if err = client.service.UpdateManifest(b.Session, b.ManifestDigest, "offline-manifest"); err != nil {
				t.Fatal(err)
			}
			local, err := client.service.WorkspaceBinding(b.Session)
			if err != nil || local.ManifestDigest != "offline-manifest" {
				t.Fatal("local membership not committed", err)
			}
			if err = client.service.RetireWorkspace(t.Context(), ownershell.SupervisorIdentity(local, ownershell.SignedGrant{}).Launch); err != nil {
				t.Fatal(err)
			}
			if _, err = os.Stat(filepath.Join(journal, retirementName(b.Session))); err != nil {
				t.Fatal("offline intent not durable", err)
			}
			if restart {
				client.close(t)
				client = round8Connect(t, state, journal)
			}
			process.Signal(t, syscall.SIGCONT)
			client.ready(t)
			var remote ownershell.Binding
			check, stop := context.WithTimeout(t.Context(), time.Second)
			defer stop()
			if err = client.bridge.Invoke(check, "binding", b.Session, &remote); !errors.Is(err, ErrClosed) {
				t.Fatal("offline digest change vetoed terminal retirement")
			}
			if _, err = os.Stat(filepath.Join(journal, retirementName(b.Session))); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("accepted retirement journal leaked", err)
			}
			polls := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			if n, err := unix.Poll(polls, 5000); err != nil || n != 1 {
				t.Fatal("retired real shell survived", err)
			}
		})
	}
}

func TestRound8CompatibleDifferentImageAdoptsLiveShell(t *testing.T) {
	binary := keepertest.Build(t)
	next := keepertest.DifferentImage(t, binary)
	state := t.TempDir()
	if err := keepertest.Run(t, binary, state, "shell-keeper", "init"); err != nil {
		t.Fatal(err)
	}
	process := keepertest.Start(t, binary, state)
	journal := filepath.Join(t.TempDir(), "retirements")
	first := round8Connect(t, state, journal)
	first.ready(t)
	b, custody, grant, key, identity, pid := round8Open(t, first)
	if err := keepertest.Run(t, next, state, "shell-keeper", "install", binary); err != nil {
		t.Fatal("different image refused live custody", err)
	}
	if err := keepertest.Run(t, next, state, "shell-keeper", "check-update"); err != nil {
		t.Fatal("maintenance vetoed live old keeper", err)
	}
	if err := syscall.Kill(process.Cmd.Process.Pid, 0); err != nil {
		t.Fatal("install ended keeper", err)
	}
	first.close(t)
	second := round8Connect(t, state, journal)
	second.ready(t)
	round8Register(t, second, b, identity)
	bound, err := second.service.Binding(b.Session)
	if err != nil {
		t.Fatal(err)
	}
	registry := ownershell.NewCustodyRegistry()
	if err = registry.Reserve(custody); err != nil {
		t.Fatal(err)
	}
	if err = registry.Activate(custody.Digest()); err != nil {
		t.Fatal(err)
	}
	registry.Suspend(custody.PhysicalScope())
	inventory := registry.Snapshot(custody.PhysicalScope(), "round8-inventory")
	observed := second.call(t, b.Session, RPC{Op: "inventory", Inventory: &inventory})
	if observed.Inventory == nil {
		t.Fatal("old keeper lost custody")
	}
	grant.AccessEpoch++
	grant.ConnectionID = bound.Connection
	grant.Audience = bound.Audience
	grant.IssuedAt = time.Now().Unix()
	grant.ExpiresAt = grant.IssuedAt + 30
	signed := ownershell.SignGrant(key, grant)
	second.call(t, b.Session, RPC{Op: "prepare", Binding: bound, Transaction: "round8-adopt", Inventory: observed.Inventory, Key: key.Public().(ed25519.PublicKey), Grants: []ownershell.SignedGrant{signed}})
	second.call(t, b.Session, RPC{Op: "commit", Transaction: "round8-adopt"})
	second.call(t, b.Session, RPC{Op: "input", Grant: signed, Seq: 1, Data: []byte("printf 'ROUND8_AFTER:%s\\n' \"$ROUND8_STATE\"\n")})
	second.output(t, b.Session, signed, "ROUND8_AFTER:survived\r\n")
	if err = syscall.Kill(pid, 0); err != nil {
		t.Fatal("adoption replaced real shell", err)
	}
	second.call(t, b.Session, RPC{Op: "close", Custody: &custody, Transaction: "round8-close"})
	if err = keepertest.Run(t, next, state, "shell-keeper", "check-update"); err != nil {
		t.Fatal("empty old keeper replacement", err)
	}
	process.Wait(t)
	guard, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	if err = second.bridge.WaitUnavailable(guard); err != nil {
		t.Fatal(err)
	}
	keepertest.Start(t, binary, state)
	second.ready(t)
	view, err := shellkeeper.InspectSnapshot(guard, filepath.Join(state, "shell-keeper"), second.authority.Epoch)
	if err != nil || view.Generation == custody.Daemon {
		t.Fatal("empty old keeper did not yield to the installed image", err)
	}
}

func TestRound8JournalFailureCannotForgetIntentOrPermitShells(t *testing.T) {
	for _, mode := range []os.FileMode{0755, 0777} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "retirements")
			if err := os.Mkdir(dir, mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, mode); err != nil {
				t.Fatal(err)
			}
			s := NewService(func(RPC) error { return nil })
			defer func() { _ = s.Shutdown(t.Context()) }()
			if err := s.ConfigureHostRetirements(dir); err != nil {
				t.Fatal("repair blocked retirement store", err)
			}
			if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0700 {
				t.Fatal("journal permissions not repaired", err)
			}
			if err := s.BeginWorkspaceRetirement("session"); err != nil {
				t.Fatal("repair blocked agent Stop", err)
			}
		})
	}
}

func TestRound8ShutdownDuringRealKeeperStall(t *testing.T) {
	binary := keepertest.Build(t)
	state := t.TempDir()
	if err := keepertest.Run(t, binary, state, "shell-keeper", "init"); err != nil {
		t.Fatal(err)
	}
	p := keepertest.Start(t, binary, state)
	c := round8Connect(t, state, filepath.Join(t.TempDir(), "retirements"))
	c.ready(t)
	p.Signal(t, syscall.SIGSTOP)
	guard, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	if err := c.bridge.WaitUnavailable(guard); err != nil {
		t.Fatal("silent open socket remained available", err)
	}
	if err := c.service.BeginWorkspaceRetirement("offline-workspace"); err != nil {
		t.Fatal(err)
	}
	c.close(t)
}

func TestRound8KeeperDeathMidOperation(t *testing.T) {
	binary := keepertest.Build(t)
	state := t.TempDir()
	if err := keepertest.Run(t, binary, state, "shell-keeper", "init"); err != nil {
		t.Fatal(err)
	}
	p := keepertest.Start(t, binary, state)
	c := round8Connect(t, state, filepath.Join(t.TempDir(), "retirements"))
	c.ready(t)
	p.Signal(t, syscall.SIGSTOP)
	// A real authenticated stream remains open but the peer cannot read or reply.
	guard, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { var out []hostObservation; done <- c.bridge.Invoke(guard, "heartbeat", nil, &out) }()
	p.Signal(t, syscall.SIGKILL)
	select {
	case err := <-done:
		if !errors.Is(err, ErrKeeperUnavailable) {
			t.Fatal("transport death did not map to keeper_unavailable", err)
		}
	case <-guard.Done():
		t.Fatal("operation stuck after peer death")
	}
	if err := c.service.BeginWorkspaceRetirement("agent-still-runs"); err != nil {
		t.Fatal("agent retirement failed after keeper death", err)
	}
}

func TestRound8HeartbeatNeverWaitsOnCustodyLocks(t *testing.T) {
	s := NewService(func(RPC) error { return nil })
	defer func() { _ = s.Shutdown(t.Context()) }()
	k := &KeeperRuntime{service: s}
	s.mu.Lock()
	k.observed.Store(&[]hostObservation{{Workspace: "busy"}})
	done := make(chan []hostObservation, 1)
	go func() { done <- k.heartbeatSnapshot() }()
	select {
	case observed := <-done:
		if len(observed) != 1 || !observed[0].Open || !observed[0].Privileged {
			t.Fatal("busy workspace was not conservative")
		}
	case <-time.After(time.Second):
		s.mu.Unlock()
		t.Fatal("heartbeat waited on custody lock")
	}
	s.mu.Unlock()
}

func TestRound8RetirementDetachesActiveSlotBeforeCleanup(t *testing.T) {
	s := NewService(func(RPC) error { return nil })
	k := &KeeperRuntime{service: s}
	e, _ := round6HostEntry()
	// Hold physical cleanup so the test observes acceptance independently of it.
	e.opMu.Lock()
	s.launches[e.binding.Session] = e
	k.detachWorkspace(e.binding.Session)
	s.mu.Lock()
	_, active := s.launches[e.binding.Session]
	_, pending := s.hostRetiring[e]
	s.mu.Unlock()
	e.opMu.Unlock()
	if active || !pending {
		t.Fatal("retired launch still consumes admission capacity")
	}
	if !e.terminal || !e.fenced {
		t.Fatal("detached custody was not terminal")
	}
	if err := s.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRound8RetirementIsDurableBeforeLocalEffects(t *testing.T) {
	s := NewService(func(RPC) error { return nil })
	defer func() { _ = s.Shutdown(t.Context()) }()
	dir := filepath.Join(t.TempDir(), "retirements")
	if err := s.ConfigureHostRetirements(dir); err != nil {
		t.Fatal(err)
	}
	s.SetHostBridge(RefusedHostBridge())
	e, _ := round6HostEntry()
	s.launches[e.binding.Session] = e
	if err := s.BeginWorkspaceRetirement(e.binding.Session); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, retirementName(e.binding.Session))); err != nil {
		t.Fatal("healthy store did not persist retirement", err)
	}
	if !e.terminal || !e.fenced {
		t.Fatal("durable intent did not fence local shells")
	}
}

func TestRound8MembershipWithLiveShellAcrossOutage(t *testing.T) {
	binary := keepertest.Build(t)
	state := t.TempDir()
	if err := keepertest.Run(t, binary, state, "shell-keeper", "init"); err != nil {
		t.Fatal(err)
	}
	p := keepertest.Start(t, binary, state)
	c := round8Connect(t, state, filepath.Join(t.TempDir(), "retirements"))
	c.ready(t)
	b, _, _, _, _, pid := round8Open(t, c)
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unix.Close(fd) }()
	p.Signal(t, syscall.SIGSTOP)
	guard, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err = c.bridge.WaitUnavailable(guard); err != nil {
		t.Fatal(err)
	}
	// Simulate replay of a locally committed manifest after an outage. Fresh
	// membership proposals are refused on unknown state by the daemon gate.
	if err = c.service.UpdateManifest(b.Session, b.ManifestDigest, "new-membership"); err != nil {
		t.Fatal(err)
	}
	p.Signal(t, syscall.SIGCONT)
	c.ready(t)
	var remote ownershell.Binding
	if err = c.bridge.Invoke(guard, "workspace_binding", b.Session, &remote); err != nil || remote.ManifestDigest != b.ManifestDigest {
		t.Fatal("membership reconciliation retired potentially live custody", err)
	}
	if err = syscall.Kill(pid, 0); err != nil {
		t.Fatal("membership killed live shell", err)
	}
	local, err := c.service.WorkspaceBinding(b.Session)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.service.RetireWorkspace(guard, ownershell.SupervisorIdentity(local, ownershell.SignedGrant{}).Launch); err != nil {
		t.Fatal("explicit Stop failed", err)
	}
	// Unlike membership, explicit Stop may close live custody.
	c.service.mu.Lock()
	intent := c.service.hostIntents[b.Session]
	c.service.mu.Unlock()
	if intent != "" {
		c.service.applyHostRetirement(guard, b.Session, intent)
	}
	if n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, 5000); err != nil || n != 1 {
		t.Fatal("explicit Stop did not close shell", err)
	}
}

func TestRound8RetirementBackoff(t *testing.T) {
	previous := time.Duration(0)
	for _, want := range []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, time.Minute, time.Minute} {
		got := nextRetirementBackoff(previous)
		if got != want {
			t.Fatalf("retirement retries: got %s, want %s", got, want)
		}
		previous = got
	}
}

func TestRound8UnavailableShellRefusalIsLogged(t *testing.T) {
	// failurelog.Default is process global. Isolate its capture from readers
	// completing disposable transports in other tests.
	if os.Getenv("BLERG_ROUND8_REFUSAL_LOG_ORACLE") != "1" {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRound8UnavailableShellRefusalIsLogged$")
		cmd.Env = append(os.Environ(), "BLERG_ROUND8_REFUSAL_LOG_ORACLE=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("refusal logging oracle: %v %s", err, out)
		}
		return
	}
	var logs bytes.Buffer
	previous := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer func() { failurelog.Default = previous }()
	s := NewService(func(RPC) error { return nil })
	defer func() { _ = s.Shutdown(t.Context()) }()
	s.SetHostBridge(RefusedHostBridge())
	result := s.executeBridged(t.Context(), RPC{SessionID: "session", Op: "input", Data: []byte("PRIVATE_ROUND8_TERMINAL_CANARY")})
	if result.OK || result.StartCause != "keeper_unavailable" || result.ErrorCode != string(ownershell.ReasonRuntimeStart) {
		t.Fatal("outage misreported as custody fencing")
	}
	if !strings.Contains(logs.String(), "operation_unavailable") {
		t.Fatal("shell refusal had no failurelog cause")
	}
	if strings.Contains(logs.String(), "PRIVATE_ROUND8_TERMINAL_CANARY") {
		t.Fatal("terminal contents logged")
	}
}

func TestRound8IncompatibleProtocolCostsShellsOnly(t *testing.T) {
	binary := keepertest.BuildProtocol(t, shellkeeper.Version+1)
	state := t.TempDir()
	if err := keepertest.Run(t, binary, state, "shell-keeper", "init"); err != nil {
		t.Fatal(err)
	}
	keepertest.Start(t, binary, state)
	c := round8Connect(t, state, filepath.Join(t.TempDir(), "retirements"))
	if _, err := shellkeeper.InspectSnapshot(t.Context(), filepath.Join(state, "shell-keeper"), c.authority.Epoch); err == nil {
		t.Fatal("incompatible protocol attached")
	}
	e, _ := round6HostEntry()
	c.service.mu.Lock()
	c.service.launches[e.binding.Session] = e
	c.service.mu.Unlock()
	if err := c.service.UpdateManifest(e.binding.Session, e.binding.ManifestDigest, "agent-membership"); err != nil {
		t.Fatal("incompatible keeper blocked agent", err)
	}
	if err := c.service.BeginWorkspaceRetirement(e.binding.Session); err != nil {
		t.Fatal("incompatible keeper blocked Stop", err)
	}
	if !strings.Contains(ErrKeeperUnavailable.Error(), "reconnects") || strings.Contains(ErrKeeperUnavailable.Error(), "restart") || !strings.Contains(ErrKeeperUnavailable.Error(), "agents keep running") {
		t.Fatal("generic outage must describe reconnecting without restart advice")
	}
	destination := filepath.Join(t.TempDir(), "daemon")
	if err := keepertest.Run(t, binary, state, "shell-keeper", "install", destination); err != nil {
		t.Fatal("protocol blocked installation", err)
	}
}

func TestRound8HeartbeatBypassesOperationQueue(t *testing.T) {
	k, b, _ := reviewRuntime(t)
	k.operation.Lock()
	defer k.operation.Unlock()
	guard, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	var observed []hostObservation
	if err := b.Invoke(guard, "heartbeat", nil, &observed); err != nil {
		t.Fatal("heartbeat queued behind workspace operation", err)
	}
}

func TestRound8RetirementUsesWorkspaceKeyOverRealWire(t *testing.T) {
	k, b, _ := reviewRuntime(t)
	if err := b.Invoke(t.Context(), "rebind", nil, nil); err != nil {
		t.Fatal(err)
	}
	e, _ := round6HostEntry()
	e.binding.ManifestDigest = "keeper-still-has-old-membership"
	k.service.mu.Lock()
	k.service.launches[e.binding.Session] = e
	k.service.mu.Unlock()
	if err := b.Invoke(t.Context(), "retire_workspace", workspaceRetirement{Version: 0, Workspace: e.binding.Session}, nil); err == nil {
		t.Fatal("invalid retirement record accepted")
	}
	if err := b.Invoke(t.Context(), "retire_workspace", workspaceRetirement{Version: 1, Workspace: e.binding.Session, ID: strings.Repeat("a", 64)}, nil); err != nil {
		t.Fatal("workspace retirement was vetoed by stale pins", err)
	}
	k.service.mu.Lock()
	_, active := k.service.launches[e.binding.Session]
	k.service.mu.Unlock()
	if active {
		t.Fatal("workspace retirement left active launch")
	}
}

func TestRound8JournalRejectsInvalidRecords(t *testing.T) {
	for _, bad := range []string{"version", "workspace", "nonce", "filename", "trailing"} {
		t.Run(bad, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "retirements")
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			record := workspaceRetirement{Version: 1, Workspace: "workspace", ID: strings.Repeat("a", 64)}
			name := retirementName(record.Workspace)
			switch bad {
			case "version":
				record.Version = 0
			case "workspace":
				record.Workspace = ""
			case "nonce":
				record.ID = "short"
			case "filename":
				name = retirementName("other")
			}
			data, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			if bad == "trailing" {
				data = append(data, []byte(" {}")...)
			}
			if err = os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
				t.Fatal(err)
			}
			s := NewService(func(RPC) error { return nil })
			defer func() { _ = s.Shutdown(t.Context()) }()
			if err = s.ConfigureHostRetirements(dir); err != nil {
				t.Fatal("quarantine failed", bad, err)
			}
			if _, exists := s.hostIntents[record.Workspace]; exists {
				t.Fatal("invalid retirement state accepted", bad)
			}
			if _, err = os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
				t.Fatal("invalid record not quarantined", bad)
			}
		})
	}
}

func TestRound8HeartbeatHasBoundedDeadline(t *testing.T) {
	observed := false
	backend := &reviewBridge{invoke: func(ctx context.Context, _ string, _, _ any) error {
		deadline, ok := ctx.Deadline()
		observed = ok && deadline.Before(time.Now().Add(time.Second))
		return context.DeadlineExceeded
	}}
	b := &ReconnectingHostBridge{ctx: t.Context(), current: backend, usable: true, changed: make(chan struct{}), failed: make(chan struct{}, 1)}
	if err := b.heartbeat(backend); err == nil {
		t.Fatal("heartbeat ignored transport silence")
	}
	if !observed {
		t.Fatal("heartbeat has no bounded liveness deadline")
	}
}
