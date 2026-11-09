//go:build linux

package shellruntime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellkeeper"
)

type keeperCrashBoot struct {
	Dir, Generation string
	Custody         protocol.ShellCustody
	Grant           protocol.ShellGrant
	Key             ed25519.PrivateKey
	Welcome         protocol.ShellLinkWelcome
	Binding         ownershell.Binding
	Identity        Identity
}

type orderedKeeperStream struct {
	packets []bridgePacket
	next    chan struct{}
	done    chan struct{}
	once    sync.Once
	gate    chan struct{}
}

func (s *orderedKeeperStream) Send(any) error { return nil }
func (s *orderedKeeperStream) Close() error {
	s.once.Do(func() { close(s.done) })
	return nil
}
func (s *orderedKeeperStream) Receive(out any) error {
	if s.gate != nil {
		<-s.gate
		s.gate = nil
	}
	if len(s.packets) == 0 {
		close(s.next)
		<-s.done
		return ErrClosed
	}
	*out.(*bridgePacket) = s.packets[0]
	s.packets = s.packets[1:]
	return nil
}

func TestKeeperRepliesRemainIndependentOfOutputDelivery(t *testing.T) {
	body, _ := json.Marshal(RPC{Type: "owner_shell_output", SessionID: "session", Data: []byte("private-output")})
	gate := make(chan struct{})
	stream := &orderedKeeperStream{packets: []bridgePacket{{Kind: "output", ID: "frame", Body: body}, {Kind: "reply", ID: "hello", OK: true}}, next: make(chan struct{}), done: make(chan struct{}), gate: gate}
	bridge := newKeeperBridge(t.Context(), stream, shellkeeper.Snapshot{})
	defer func() { _ = bridge.Close() }()
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	bridge.SetReceiver(func(context.Context, RPC) error { close(entered); <-release; return nil })
	wait := make(chan bridgePacket, 1)
	bridge.mu.Lock()
	bridge.pending["hello"] = wait
	bridge.mu.Unlock()
	close(gate)
	guard, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-guard.Done():
		t.Fatal("output did not begin")
	}
	select {
	case reply := <-wait:
		if !reply.OK {
			t.Fatal("reply lost")
		}
	case <-guard.Done():
		t.Fatal("reply waited behind blocked terminal")
	}
}

// Runs as a separate disposable daemon process; it owns daemon.lock, an
// authenticated channel and an active adopted lease when the parent SIGKILLs it.
func TestKeeperDaemonCrashFixture(t *testing.T) {
	path := os.Getenv("BLERG_KEEPER_CRASH_FIXTURE")
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var boot keeperCrashBoot
	if json.Unmarshal(data, &boot) != nil {
		t.Fatal("invalid crash fixture")
	}
	authority, err := shellkeeper.AcquireDaemon(boot.Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = authority.Close() }()
	bridge, err := ConnectKeeper(t.Context(), boot.Dir, authority.Epoch, boot.Generation)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close() }()
	frames := make(chan RPC, 128)
	service := NewService(func(r RPC) error {
		select {
		case frames <- r:
			return nil
		default:
			return ErrClosed
		}
	})
	defer func() { _ = service.Shutdown(t.Context()) }()
	service.SetHostBridge(bridge)
	caps := protocol.WorkspaceCapabilities{ManifestVersion: protocol.WorkspaceVersion, ShellVersion: 1, LinuxHost: true, ShellPersistence: 2, PersistentHost: true}
	service.SetCapabilities(caps, caps)
	if err = service.Rebind(t.Context()); err != nil {
		t.Fatal(err)
	}
	hello := service.ShellHello()
	welcome := boot.Welcome
	welcome.Challenge = hello.Challenge
	if hello.Daemon != boot.Welcome.Daemon {
		t.Fatal("keeper custodian changed")
	}
	if err = service.AcceptShellLink(t.Context(), hello, &welcome); err != nil {
		t.Fatal(err)
	}
	command := HostCommand("unused-daemon-path", boot.Identity)
	command.Root = boot.Binding.Root
	command.PublicRoot = boot.Identity.Cwd
	if err = service.Register(t.Context(), new(sync.Mutex), boot.Binding, command, func(context.Context) error { return nil }); err != nil {
		t.Fatal("child could not rebind exact workspace", err)
	}
	b, err := service.Binding(boot.Custody.Session)
	if err != nil {
		t.Fatal(err)
	}
	call := func(r RPC) RPC {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		r.Version = 2
		r.SessionID = b.Session
		reply, err := service.Call(ctx, r)
		if err != nil {
			t.Fatalf("child %s: %v", r.Op, err)
		}
		return reply
	}
	registry := ownershell.NewCustodyRegistry()
	if err = registry.Reserve(boot.Custody); err != nil {
		t.Fatal(err)
	}
	if err = registry.Activate(boot.Custody.Digest()); err != nil {
		t.Fatal(err)
	}
	registry.Suspend(boot.Custody.PhysicalScope())
	inventory := registry.Snapshot(boot.Custody.PhysicalScope(), "child-inventory")
	observed := call(RPC{Op: "inventory", Inventory: &inventory})
	if observed.Inventory == nil {
		t.Fatal("missing child inventory")
	}
	grant := boot.Grant
	grant.AccessEpoch++
	grant.ConnectionID = b.Connection
	grant.Audience = b.Audience
	grant.IssuedAt = time.Now().Unix()
	grant.ExpiresAt = grant.IssuedAt + 30
	signed := ownershell.SignGrant(boot.Key, grant)
	call(RPC{Op: "prepare", Binding: b, Transaction: "child-adopt", Inventory: observed.Inventory, Key: boot.Key.Public().(ed25519.PublicKey), Grants: []ownershell.SignedGrant{signed}})
	call(RPC{Op: "commit", Transaction: "child-adopt"})
	call(RPC{Op: "input", Grant: signed, Seq: 1, Data: []byte("printf 'KEEPER_CHILD_READY:%s\\n' \"$KEEPER_STATE\"\n")})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var output []byte
	for {
		select {
		case r := <-frames:
			if r.Type != "owner_shell_output" || r.Epoch != signed.Grant.AccessEpoch {
				continue
			}
			output = append(output, r.Data...)
			end := r.Offset + uint64(len(r.Data))
			call(RPC{Op: "credit", Grant: signed, Offset: end})
			call(RPC{Op: "receipt", Grant: signed, Offset: end})
			if bytes.Contains(output, []byte("KEEPER_CHILD_READY:same-tree\r\n")) {
				stop, cancel := signal.NotifyContext(t.Context(), syscall.SIGTERM)
				fmt.Fprintln(os.Stdout, "ready")
				<-stop.Done()
				cancel()
				return
			}
		case <-ctx.Done():
			t.Fatal("child could not reach original shell")
		}
	}
}

func TestMain(m *testing.M) {
	// The production launcher deliberately scrubs GORACE. A probe in this
	// test image reexecs before doing work, retaining race detection while
	// removing the artificial one-second test-runtime exit pause.
	if len(os.Args) == 4 && os.Args[1] == "shell-supervisor" && os.Args[2] == "--keeper-restart" && (os.Args[3] == "--probe" || os.Args[3] == "--probe-persistence") && os.Getenv("GORACE") != "atexit_sleep_ms=0" {
		if syscall.Exec("/proc/self/exe", os.Args, append(os.Environ(), "GORACE=atexit_sleep_ms=0")) != nil {
			os.Exit(93)
		}
	}
	if len(os.Args) >= 3 && os.Args[1] == "shell-supervisor" && os.Args[2] == "--keeper-restart" {
		var err error
		switch {
		case len(os.Args) == 3:
			err = ServeKeeperHelper(os.Stdin, os.Stdout)
		case len(os.Args) == 4 && os.Args[3] == "--probe":
			err = Probe(os.Stdout)
		case len(os.Args) == 4 && os.Args[3] == "--probe-persistence":
			err = ProbeKeeperPersistence(os.Stdin, os.Stdout)
		default:
			os.Exit(92)
		}
		if errors.Is(err, ErrIdentityChanged) {
			os.Exit(3)
		}
		if err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestKeeperAttachJoinsSuspensionBeforePublishingNewChannel(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keeper")
	if err := shellkeeper.Initialize(dir); err != nil {
		t.Fatal(err)
	}
	broker := NewKeeperRuntime()
	outputJoined := make(chan struct{})
	result := make(chan error, 1)
	var once sync.Once
	child := &retainedChild{outputDone: outputJoined}
	child.cancel = func() {
		once.Do(func() {
			result <- broker.send(RPC{Type: "owner_shell_output", Data: []byte("prior-owner-output")})
			close(outputJoined)
		})
	}
	c := &retainedCoordinator{mu: new(sync.Mutex), active: true, registry: ownershell.NewCustodyRegistry(), children: map[string]*retainedChild{"old": child}}
	broker.service.launches["old"] = &registered{retained: c}
	server, err := shellkeeper.ListenService(t.Context(), dir, nil, broker.Serve)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	authority, err := shellkeeper.AcquireDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = authority.Close() }()
	view, err := shellkeeper.InspectSnapshot(t.Context(), dir, authority.Epoch)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := ConnectKeeper(t.Context(), dir, authority.Epoch, view.Generation)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close() }()
	bridge.SetReceiver(func(context.Context, RPC) error { return nil })
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("old output reached new channel before adoption")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("suspension did not join old output")
	}
}

func TestUpstreamOutputLossSuspendsAccessWithoutLosingKeeperChannel(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keeper")
	if err := shellkeeper.Initialize(dir); err != nil {
		t.Fatal(err)
	}
	broker := NewKeeperRuntime()
	server, err := shellkeeper.ListenService(t.Context(), dir, nil, broker.Serve)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	authority, err := shellkeeper.AcquireDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = authority.Close() }()
	view, err := shellkeeper.InspectSnapshot(t.Context(), dir, authority.Epoch)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := ConnectKeeper(t.Context(), dir, authority.Epoch, view.Generation)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close() }()
	bridge.SetReceiver(func(context.Context, RPC) error { return errors.New("upstream link closed") })
	if err = bridge.Invoke(t.Context(), "rebind", nil, nil); err != nil {
		t.Fatal(err)
	}
	suspended := make(chan struct{}, 1)
	outputDone := make(chan struct{})
	close(outputDone)
	c := &retainedCoordinator{mu: new(sync.Mutex), active: true, highestEpoch: 1, registry: ownershell.NewCustodyRegistry(), children: map[string]*retainedChild{"live": {outputDone: outputDone, cancel: func() { suspended <- struct{}{} }}}}
	broker.service.mu.Lock()
	broker.service.launches["live"] = &registered{retained: c}
	broker.service.mu.Unlock()
	if err = broker.sendOutput(t.Context(), RPC{Version: 2, Type: "owner_shell_output", SessionID: "live", Epoch: 1, Data: []byte("test-output")}); err == nil {
		t.Fatal("failed upstream delivery was acknowledged")
	}
	select {
	case <-suspended:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream failure did not suspend shell access")
	}
	var hello protocol.ShellLinkHello
	if err = bridge.Invoke(t.Context(), "hello", nil, &hello); err != nil || hello.Daemon != view.Generation {
		t.Fatal("upstream loss destroyed keeper rekey channel", err)
	}
	// Retire the fake before server shutdown's second suspension.
	broker.service.mu.Lock()
	delete(broker.service.launches, "live")
	broker.service.mu.Unlock()
}

func TestKeeperDrainClosesExecutionAdmissionBeforeInventory(t *testing.T) {
	broker := NewKeeperRuntime()
	finish, err := broker.beginDrain(func() []shellkeeper.Candidate {
		if broker.service.link.TryRLock() {
			broker.service.link.RUnlock()
			t.Error("drain observed inventory without joining in-flight execution")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	reply := broker.service.execute(t.Context(), RPC{Version: 2, SessionID: "unallocated", Op: "describe"})
	if reply.OK || reply.ErrorCode != string(ownershell.ReasonRuntimeFenced) {
		t.Fatal("queued operation entered a drained runtime")
	}
	if err = broker.ready("unallocated"); err == nil {
		t.Fatal("drain permitted native writer admission")
	}
	finish(false)
	if err = broker.ready("unallocated"); err != nil {
		t.Fatal("aborted drain wedged admission", err)
	}
	broker.service.launches["closing"] = &registered{mu: new(sync.Mutex), retained: &retainedCoordinator{mu: new(sync.Mutex), children: map[string]*retainedChild{"unknown": {custody: protocol.ShellCustody{Shell: "unknown-writer"}, closing: "uncertain"}}}}
	if finish, err = broker.BeginDrain(); err == nil {
		finish(false)
		t.Fatal("closing label became physical cleanup")
	}
	broker.service.mu.Lock()
	delete(broker.service.launches, "closing")
	broker.service.mu.Unlock()
	finish, err = broker.BeginDrain()
	if err != nil {
		t.Fatal(err)
	}
	finish(true)
	if err = broker.ready("unallocated"); err == nil {
		t.Fatal("committed drain reopened admission")
	}
}

func TestLateOutputFailureCannotSuspendNewAccessEpoch(t *testing.T) {
	broker := NewKeeperRuntime()
	done := make(chan struct{})
	close(done)
	c := &retainedCoordinator{mu: new(sync.Mutex), active: true, highestEpoch: 2, registry: ownershell.NewCustodyRegistry(), children: map[string]*retainedChild{"live": {outputDone: done, cancel: func() {}}}}
	broker.service.launches["session"] = &registered{retained: c}
	if err := broker.suspendOutput(t.Context(), failedOutput{Session: "session", Epoch: 1}); err != nil {
		t.Fatal(err)
	}
	if !c.active {
		t.Fatal("late output failure suspended a newer committed epoch")
	}
	if err := broker.suspendOutput(t.Context(), failedOutput{Session: "session", Epoch: 2}); err != nil {
		t.Fatal(err)
	}
	if c.active {
		t.Fatal("current failed epoch retained shell access")
	}
}

func TestKeeperConnectionCancellationClosesStream(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keeper")
	if err := shellkeeper.Initialize(dir); err != nil {
		t.Fatal(err)
	}
	broker := NewKeeperRuntime()
	server, err := shellkeeper.ListenService(t.Context(), dir, nil, broker.Serve)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = server.Close() }()
	authority, err := shellkeeper.AcquireDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = authority.Close() }()
	view, err := shellkeeper.InspectSnapshot(t.Context(), dir, authority.Epoch)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	bridge, err := ConnectKeeper(ctx, dir, authority.Epoch, view.Generation)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close() }()
	cancel()
	select {
	case <-bridge.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled daemon left its keeper stream alive")
	}
}

type routingBridge struct {
	receiver func(context.Context, RPC) error
	methods  []string
}

func (b *routingBridge) Invoke(_ context.Context, method string, _ any, _ any) error {
	b.methods = append(b.methods, method)
	return ErrClosed
}
func (*routingBridge) HostSession(string) bool                                 { return true }
func (b *routingBridge) SetReceiver(receiver func(context.Context, RPC) error) { b.receiver = receiver }
func (b *routingBridge) Metadata(method string, _ any, _ any) error {
	b.methods = append(b.methods, method)
	return ErrClosed
}
func (*routingBridge) Close() error { return nil }

func TestKeeperFailureCannotFallBackToDaemonOwnedHostHelpers(t *testing.T) {
	base, b, _, _ := fixture(t)
	service := NewService(func(RPC) error { return nil })
	service.SetHostBridge(RefusedHostBridge())
	command := base.command
	command.OwnedStarted = func(int) error { t.Error("refused keeper allocated a daemon-owned helper"); return ErrClosed }
	if err := service.Register(t.Context(), new(sync.Mutex), b, command, func(context.Context) error { return nil }); err != nil {
		t.Fatal("keeper refusal blocked agent registration", err)
	}
	if !service.bridged(b.Session) {
		t.Fatal("offline host registration lost keeper routing")
	}
	result := service.executeBridged(t.Context(), RPC{Version: 1, SessionID: b.Session, RequestID: "offline-open", Op: "open"})
	if result.OK || result.ErrorCode == "" {
		t.Fatal("refused keeper admitted a host shell")
	}
}

func TestKeeperRoutingCannotCaptureSandboxOrClusterRegistrations(t *testing.T) {
	for _, runtime := range []string{"sandbox", "cluster"} {
		t.Run(runtime, func(t *testing.T) {
			base, b, _, _ := fixture(t)
			b.Runtime = runtime
			bridge := &routingBridge{}
			service := NewService(func(RPC) error { return nil })
			service.SetHostBridge(bridge)
			if err := service.Register(t.Context(), new(sync.Mutex), b, base.command, func(context.Context) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if service.bridged(b.Session) {
				t.Fatal("host bridge captured isolated runtime")
			}
			got, err := service.Binding(b.Session)
			if err != nil || got.Runtime != runtime {
				t.Fatal("runtime binding crossed host boundary", err)
			}
			if len(bridge.methods) != 0 {
				t.Fatal("isolated registration called host keeper", bridge.methods)
			}
		})
	}
}

// Requires its own real ptrace tracer: run through /tmp/cx/outside.sh.
func TestKeeperBridgeRestartAdoptsSameTracerAndShell(t *testing.T) {
	for _, termination := range []syscall.Signal{syscall.SIGKILL, syscall.SIGTERM} {
		t.Run(termination.String(), func(t *testing.T) { keeperRestartAdoption(t, termination) })
	}
}
func TestKeeperUncleanHelperDeathEventuallySettlesSpawnedObserver(t *testing.T) {
	keeperRestartAdoptionMode(t, syscall.SIGKILL, true)
}
func keeperRestartAdoption(t *testing.T, termination syscall.Signal) {
	keeperRestartAdoptionMode(t, termination, false)
}
func keeperRestartAdoptionMode(t *testing.T, termination syscall.Signal, killHelper bool) {
	// The real bundled probes have no race-runtime exit pause. Keep the
	// detector enabled without spending the request budget on that test delay.
	t.Setenv("GORACE", "atexit_sleep_ms=0")
	base, b, original, key := fixture(t)
	identity, err := ResolvePersistentIdentity(base.command.Identity.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "keeper")
	if err = shellkeeper.Initialize(dir); err != nil {
		t.Fatal(err)
	}
	broker := NewKeeperRuntime()
	detached := make(chan struct{}, 4)
	server, err := shellkeeper.ListenService(t.Context(), dir, broker.Inventory, func(ctx context.Context, ch *shellkeeper.Channel) error {
		err := broker.Serve(ctx, ch)
		detached <- struct{}{}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		call, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = broker.Shutdown(call)
		_ = server.Close()
	})
	first, err := shellkeeper.AcquireDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	view, err := shellkeeper.InspectSnapshot(t.Context(), dir, first.Epoch)
	if err != nil {
		t.Fatal(err)
	}
	caps := protocol.WorkspaceCapabilities{ManifestVersion: protocol.WorkspaceVersion, ShellVersion: 1, LinuxHost: true, ShellPersistence: 2, PersistentHost: true}
	frames := make(chan RPC, 128)
	makeClient := func(epoch shellkeeper.Epoch) (*Service, *KeeperBridge) {
		t.Helper()
		bridge, err := ConnectKeeper(t.Context(), dir, epoch, view.Generation)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = bridge.Close() })
		service := NewService(func(r RPC) error {
			select {
			case frames <- r:
				return nil
			default:
				return ErrClosed
			}
		})
		service.SetHostBridge(bridge)
		service.SetCapabilities(caps, caps)
		if err = service.Rebind(t.Context()); err != nil {
			t.Fatal(err)
		}
		return service, bridge
	}
	firstService, firstBridge := makeClient(first.Epoch)
	hello := firstService.ShellHello()
	welcome := protocol.ShellLinkWelcome{Version: 2, Daemon: hello.Daemon, Challenge: hello.Challenge, Runner: "runner", Desktop: desktopauth.Binding{DesktopID: "desktop", OwnerID: b.Owner, TokenID: "credential", Revision: 1, ExpiresAt: time.Now().Add(8 * 24 * time.Hour)}}
	if err = firstService.AcceptShellLink(t.Context(), hello, &welcome); err != nil {
		t.Fatal(err)
	}
	command := HostCommand("unused-daemon-path", identity)
	command.Root = b.Root
	command.PublicRoot = identity.Cwd
	if err = firstService.Register(t.Context(), new(sync.Mutex), b, command, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	bound, err := firstService.Binding(b.Session)
	if err != nil {
		t.Fatal(err)
	}
	call := func(service *Service, r RPC) (RPC, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		r.Version = 2
		r.SessionID = b.Session
		return service.Call(ctx, r)
	}
	must := func(service *Service, r RPC) RPC {
		t.Helper()
		reply, err := call(service, r)
		if err != nil || !reply.OK {
			t.Fatalf("keeper %s refused: %v reason=%s stage=%s", r.Op, err, reply.ErrorCode, reply.StartCause)
		}
		return reply
	}
	must(firstService, RPC{Op: "configure", Binding: bound, Key: key.Public().(ed25519.PublicKey), Policy: base.command.Policy})
	actor := ownershell.Actor{Kind: "human", Owner: b.Owner, Login: "login"}
	consent := must(firstService, RPC{Op: "consent_challenge", Actor: actor})
	accepted := must(firstService, RPC{Op: "consent_accept", Actor: actor, PersistentConsent: consent.PersistentConsent})
	now := time.Now()
	custody := protocol.ShellCustody{Version: 2, Owner: b.Owner, Login: "login", Session: b.Session, Launch: b.Launch, Runtime: "host", RuntimeGeneration: b.RuntimeGeneration, Root: b.Root, Manifest: b.ManifestDigest, Desktop: "desktop", Credential: "credential", OwnershipRevision: 1, Runner: "runner", Daemon: hello.Daemon, Shell: "keeper-shell", Nonce: "keeper-private-custody", Identity: RuntimeIdentityDigest(identity), Consent: accepted.PersistentConsent.Host.ID, Created: now.Unix(), LifetimeUntil: now.Add(time.Hour).Unix()}
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
	must(firstService, RPC{Op: "open", Custody: &custody, Grant: signed, Window: ownershell.ServerOutputWindow})
	peer := broker.service.launches[b.Session].retained.children[custody.Digest()].peer
	helperPID := peer.cmd.Process.Pid
	must(firstService, RPC{Op: "input", Grant: signed, Seq: 1, Data: []byte("KEEPER_STATE='same-tree'; printf 'KEEPER_READY\\n'\n")})
	receive := func(service *Service, g ownershell.SignedGrant, marker string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		var data []byte
		for {
			select {
			case r := <-frames:
				if r.Type != "owner_shell_output" || r.Epoch != g.Grant.AccessEpoch {
					continue
				}
				data = append(data, r.Data...)
				end := r.Offset + uint64(len(r.Data))
				must(service, RPC{Op: "credit", Grant: g, Offset: end})
				must(service, RPC{Op: "receipt", Grant: g, Offset: end})
				if bytes.Contains(data, []byte(marker)) {
					return
				}
			case <-ctx.Done():
				t.Fatal("keeper output missing", marker)
			}
		}
	}
	receive(firstService, signed, "KEEPER_READY\r\n")
	lifecycle := firstService.Lifecycle(b.Session)
	if lifecycle == nil {
		t.Fatal("missing native lifecycle")
	}
	if err = lifecycle.Notify(t.Context(), ownershell.SupervisorIdentity(bound, ownershell.SignedGrant{}).Launch, "syncing"); err != nil {
		t.Fatal("could not freeze before crash", err)
	}
	_ = firstBridge.Close()
	select {
	case <-detached:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon loss did not join suspension")
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	bootPath := filepath.Join(t.TempDir(), "daemon-fixture.json")
	bootData, err := json.Marshal(keeperCrashBoot{Dir: dir, Generation: view.Generation, Custody: custody, Grant: grant, Key: key, Welcome: welcome, Binding: b, Identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(bootPath, bootData, 0600); err != nil {
		t.Fatal(err)
	}
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestKeeperDaemonCrashFixture$")
	child.Env = append(os.Environ(), "BLERG_KEEPER_CRASH_FIXTURE="+bootPath)
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	childLog, err := os.CreateTemp(t.TempDir(), "daemon-log-")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = childLog.Close() }()
	child.Stderr = childLog
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
		_ = child.Process.Kill()
		_ = child.Wait()
		logs, _ := os.ReadFile(childLog.Name())
		t.Fatalf("disposable daemon readiness: %v %s", err, logs)
	}
	// A real reinstall of an identical image has no swap and retains custody.
	destination := filepath.Join(t.TempDir(), "daemon")
	image, err := os.ReadFile("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(destination, image, 0700); err != nil {
		t.Fatal(err)
	}
	if err = shellkeeper.Install(t.Context(), dir, destination); err != nil {
		t.Fatal("reinstall ended or refused same-image custody", err)
	}
	if err = child.Process.Signal(termination); err != nil {
		t.Fatal(err)
	}
	err = child.Wait()
	var killed *exec.ExitError
	if termination == syscall.SIGKILL && (!errors.As(err, &killed) || killed.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL) || termination == syscall.SIGTERM && err != nil {
		t.Fatal("daemon termination was not the requested lifecycle", err)
	}
	select {
	case <-detached:
	case <-time.After(5 * time.Second):
		t.Fatal("SIGKILL did not suspend keeper access")
	}
	next, err := shellkeeper.AcquireDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = next.Close() }()
	candidates, err := shellkeeper.InspectSnapshot(t.Context(), dir, next.Epoch)
	if err != nil || candidates.Generation != view.Generation || len(candidates.Candidates) != 1 {
		t.Fatal("restart lost custody or generation", err)
	}
	nextService, _ := makeClient(next.Epoch)
	if err = nextService.HostCustodyReady(t.Context(), b.Session); err == nil {
		t.Fatal("unreconciled custody admitted writers")
	}
	nextHello := nextService.ShellHello()
	if nextHello.Daemon != hello.Daemon || nextHello.Challenge == hello.Challenge {
		t.Fatal("keeper identity changed or stale challenge reused")
	}
	welcome.Challenge = nextHello.Challenge
	if err = nextService.AcceptShellLink(t.Context(), nextHello, &welcome); err != nil {
		t.Fatal(err)
	}
	if err = nextService.Register(t.Context(), new(sync.Mutex), b, command, func(context.Context) error { return nil }); err != nil {
		t.Fatal("registration replaced retained authority", err)
	}
	bound, err = nextService.Binding(b.Session)
	if err != nil {
		t.Fatal(err)
	}
	if reply, err := call(nextService, RPC{Op: "input", Grant: signed, Seq: 2, Data: []byte("exit\n")}); err == nil || reply.OK {
		t.Fatal("old grant crossed daemon epoch")
	}
	registry := ownershell.NewCustodyRegistry()
	if err = registry.Reserve(custody); err != nil {
		t.Fatal(err)
	}
	if err = registry.Activate(custody.Digest()); err != nil {
		t.Fatal(err)
	}
	registry.Suspend(custody.PhysicalScope())
	inventory := registry.Snapshot(custody.PhysicalScope(), "keeper-restart-challenge")
	observed := must(nextService, RPC{Op: "inventory", Inventory: &inventory})
	if observed.Inventory == nil {
		t.Fatal("runtime omitted inventory")
	}
	matched, err := registry.Reconcile(*observed.Inventory, inventory.Challenge)
	if err != nil || len(matched.Eligible) != 1 {
		t.Fatal("restart custody mismatch", err)
	}
	_, freshKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	grant.AccessEpoch = 3
	grant.ConnectionID = bound.Connection
	grant.Audience = bound.Audience
	grant.IssuedAt = time.Now().Unix()
	grant.ExpiresAt = grant.IssuedAt + 30
	fresh := ownershell.SignGrant(freshKey, grant)
	must(nextService, RPC{Op: "prepare", Binding: bound, Transaction: "keeper-restart", Inventory: observed.Inventory, Key: freshKey.Public().(ed25519.PublicKey), Grants: []ownershell.SignedGrant{fresh}})
	remoteBridge := nextService.bridge().(*KeeperBridge)
	remoteBridge.mu.Lock()
	notice, noted := remoteBridge.notices[b.Session+"\x00"+custody.Shell]
	remoteBridge.mu.Unlock()
	if !noted || notice.Custody == nil || *notice.Custody != custody || notice.Epoch != fresh.Grant.AccessEpoch {
		t.Fatal("silent adopted shell would freeze without a bridge-loss notice")
	}
	if err = nextService.HostCustodyReady(t.Context(), b.Session); err == nil {
		t.Fatal("prepare alone admitted writers")
	}
	must(nextService, RPC{Op: "commit", Transaction: "keeper-restart"})
	if err = nextService.HostCustodyReady(t.Context(), b.Session); err != nil {
		t.Fatal("committed adoption still fenced writers", err)
	}
	must(nextService, RPC{Op: "input", Grant: fresh, Seq: 1, Data: []byte("printf 'KEEPER_RESUMED:%s\\n' \"$KEEPER_STATE\"\n")})
	receive(nextService, fresh, "KEEPER_RESUMED:same-tree\r\n")
	if peer.cmd.Process.Pid != helperPID || broker.service.launches[b.Session].retained.children[custody.Digest()].peer != peer {
		t.Fatal("adoption recreated helper")
	}
	if killHelper {
		if err := peer.cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
	} else {
		must(nextService, RPC{Op: "input", Grant: fresh, Seq: 2, Data: []byte("exit\n")})
	}
	childState := broker.service.launches[b.Session].retained.children[custody.Digest()]
	select {
	case <-childState.settled:
	case <-time.After(5 * time.Second):
		t.Fatal("helper exit never settled custody")
	}
	if !childState.closed {
		t.Fatal("clean helper exit retained a writer reservation")
	}
	if err := broker.ready(b.Session); err != nil {
		t.Fatal("proven dead helper wedged session admission", err)
	}
	if killHelper {
		if len(shellkeeper.CustodyCandidates(broker.Inventory())) != 0 {
			t.Fatal("proven dead helper still wedges installation")
		}
		finish, err := broker.BeginDrain()
		if err != nil {
			t.Fatal("proven dead helper still refuses drain", err)
		}
		finish(false)
	}
	closed := must(nextService, RPC{Op: "close", Custody: &custody, Transaction: "keeper-cleanup"})
	if closed.CustodyCleanup == nil || closed.CustodyCleanup.ValidateFor(custody, "keeper-cleanup", nextHello.Daemon) != nil {
		t.Fatal("physical cleanup proof missing")
	}
	if len(shellkeeper.CustodyCandidates(broker.Inventory())) != 0 {
		t.Fatal("closed custody still owns writers")
	}
}
