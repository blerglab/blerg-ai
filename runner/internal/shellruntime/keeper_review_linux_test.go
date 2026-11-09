//go:build linux

package shellruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellkeeper"
)

func reviewRuntime(t *testing.T) (*KeeperRuntime, *KeeperBridge, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "keeper")
	if err := shellkeeper.Initialize(dir); err != nil {
		t.Fatal(err)
	}
	k := NewKeeperRuntime()
	server, err := shellkeeper.ListenService(t.Context(), dir, k.Inventory, k.Serve)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := shellkeeper.AcquireDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	view, err := shellkeeper.InspectSnapshot(t.Context(), dir, authority.Epoch)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := ConnectKeeper(t.Context(), dir, authority.Epoch, view.Generation)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = bridge.Close()
		_ = authority.Close()
		_ = server.Close()
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 3*time.Second)
		defer cancel()
		_ = k.Shutdown(ctx)
	})
	// The transport handshake precedes Serve's activation. A reply proves the
	// keeper installed this channel before tests inject output directly.
	var hello protocol.ShellLinkHello
	if err := bridge.Invoke(t.Context(), "hello", nil, &hello); err != nil {
		t.Fatal("keeper activation", err)
	}
	return k, bridge, dir
}

func TestKeeperOutputBackpressureLeavesRepliesAndControlAlive(t *testing.T) {
	k, b, _ := reviewRuntime(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var received atomic.Int32
	b.SetReceiver(func(ctx context.Context, r RPC) error {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			received.Add(1)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	var sends sync.WaitGroup
	results := make(chan error, 130)
	for i := 0; i < 130; i++ {
		sends.Add(1)
		go func() {
			defer sends.Done()
			results <- k.sendOutput(t.Context(), RPC{Version: 2, Type: "owner_shell_output", SessionID: "session", Epoch: 1, Data: []byte("private-fixture-content")})
		}()
	}
	guard, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	select {
	case <-entered:
	case <-guard.Done():
		t.Fatal("output did not reach upstream")
	}
	var hello protocol.ShellLinkHello
	if err := b.Invoke(guard, "hello", nil, &hello); err != nil {
		close(release)
		t.Fatal("blocked output blocked keeper replies", err)
	}
	select {
	case <-b.Done():
		close(release)
		t.Fatal("output burst killed control bridge")
	default:
	}
	close(release)
	done := make(chan struct{})
	go func() { sends.Wait(); close(done) }()
	select {
	case <-done:
	case <-guard.Done():
		t.Fatal("backpressure did not release producers")
	}
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal("output lost under backpressure", err)
		}
	}
	if received.Load() != 130 {
		t.Fatal("output was dropped", received.Load())
	}
}

func TestKeeperBlockedOutputOnlyFiltersItsFailedEpoch(t *testing.T) {
	k, b, _ := reviewRuntime(t)
	var received atomic.Int32
	b.SetReceiver(func(context.Context, RPC) error {
		if received.Add(1) == 1 {
			return errors.New("upstream stopped")
		}
		return nil
	})
	for i, epoch := range []uint64{1, 1, 2} {
		err := k.sendOutput(t.Context(), RPC{Version: 2, Type: "owner_shell_output", SessionID: "session", Epoch: epoch})
		if (err == nil) != (i == 2) {
			t.Fatal("failed epoch delivery", i, err)
		}
	}
	if received.Load() != 2 {
		t.Fatal("failed-epoch filter dropped new access or replayed old access", received.Load())
	}
}

func TestKeeperRegistrationPinsAndTerminalFences(t *testing.T) {
	for _, name := range []string{"owner", "launch", "terminal", "fenced", "retiring", "allowed"} {
		t.Run(name, func(t *testing.T) {
			k := NewKeeperRuntime()
			b := ownershell.Binding{Session: "session", Owner: "owner", Launch: "launch", Runtime: "host", RuntimeGeneration: 1, Root: protocol.WorkspaceRoot{Device: 1, Inode: 1}, ManifestDigest: "manifest", Live: true, Connection: "connection"}
			command := HostCommand("/proc/self/exe", Identity{Cwd: "/fixture"})
			command.PublicRoot = "/fixture"
			command.Root = b.Root
			entry := &registered{mu: new(sync.Mutex), binding: b, command: command, phase: "running"}
			k.service.launches[b.Session] = entry
			req := hostRegistration{Binding: b, Identity: command.Identity, PublicRoot: command.PublicRoot, Root: command.Root}
			switch name {
			case "owner":
				req.Binding.Owner = "other"
			case "launch":
				req.Binding.Launch = "other"
			case "terminal":
				entry.terminal = true
			case "fenced":
				entry.fenced = true
			case "retiring":
				entry.retiring = true
			}
			if err := k.register(t.Context(), req); (err == nil) != (name == "allowed") {
				t.Fatal("registration pins/fence", name, err)
			}
		})
	}
}

func TestKeeperRebindRuntimeAndReadyGatesPrecedeDispatch(t *testing.T) {
	for _, name := range []string{"rebound", "host_runtime", "unadopted"} {
		t.Run(name, func(t *testing.T) {
			k := NewKeeperRuntime()
			k.rebound = name != "rebound"
			close(k.service.done) // a removed guard must fail without starting a fake helper
			b := ownershell.Binding{Session: "session", Runtime: "host"}
			if name == "host_runtime" {
				b.Runtime = "sandbox"
			}
			e := &registered{mu: new(sync.Mutex), binding: b}
			if name == "unadopted" {
				e.retained = &retainedCoordinator{mu: new(sync.Mutex), children: map[string]*retainedChild{"live": {}}}
			}
			k.service.launches[b.Session] = e
			body, _ := json.Marshal(RPC{Version: 1, Type: "owner_shell_request", SessionID: b.Session, Op: "open"})
			_, err := k.invoke(t.Context(), "handle", body)
			if err == nil {
				t.Fatal("unproven authority dispatched", name)
			}
			if len(k.service.queues) != 0 {
				t.Fatal("refused request reached runtime queue")
			}
		})
	}
}

func TestKeeperEmptyLaunchIsDiscoverableForRetirement(t *testing.T) {
	k := NewKeeperRuntime()
	b := ownershell.Binding{Session: "session", Owner: "owner", Launch: "launch", Runtime: "host", RuntimeGeneration: 1, Connection: "connection", Live: true}
	k.service.launches[b.Session] = &registered{binding: b, mu: new(sync.Mutex), reconcile: func(context.Context) error { return nil }}
	view := shellkeeper.Snapshot{Candidates: k.Inventory()}
	b2 := newKeeperBridge(t.Context(), &orderedKeeperStream{next: make(chan struct{}), done: make(chan struct{})}, view)
	defer func() { _ = b2.Close() }()
	if !b2.HostSession(b.Session) {
		t.Fatal("empty launch vanished on restart")
	}
	if len(shellkeeper.CustodyCandidates(view.Candidates)) != 0 {
		t.Fatal("empty registration wedged drain")
	}
	finish, err := k.BeginDrain()
	if err != nil {
		t.Fatal("empty launch could not drain", err)
	}
	finish(false)
	proof, err := k.service.Retire(t.Context(), ownershell.SupervisorIdentity(b, ownershell.SignedGrant{}).Launch)
	if err != nil || !proof.ContainmentEmpty {
		t.Fatal("empty launch could not retire", err)
	}
	if len(k.Inventory()) != 0 {
		t.Fatal("retired empty launch leaked")
	}
}

func TestKeeperBridgeRepeatedConnectionsDoNotLeakFDs(t *testing.T) {
	count := func() int {
		files, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		return len(files)
	}
	// Warm up the Go poller and runtime descriptors before taking the baseline.
	warm := func() {
		t.Run("connection", func(t *testing.T) {
			_, b, _ := reviewRuntime(t)
			b.SetReceiver(func(context.Context, RPC) error { return nil })
			_ = b.Close()
		})
	}
	warm()
	runtime.GC()
	before := count()
	for i := 0; i < 12; i++ {
		warm()
	}
	runtime.GC()
	if after := count(); after > before+1 {
		t.Fatalf("keeper descriptors leaked: before=%d after=%d", before, after)
	}
}

func TestKeeperAllowsRestrictedHostLaunchRegistration(t *testing.T) {
	root := t.TempDir()
	identity, err := ResolvePersistentIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	k := NewKeeperRuntime()
	b := ownershell.Binding{Session: "restricted", Owner: "owner", Launch: "launch", Runtime: "host", RuntimeGeneration: 1, Root: identity.MemberRoot, Connection: "connection", Live: true, Child: true}
	if err = k.register(t.Context(), hostRegistration{Binding: b, Identity: identity, Root: identity.MemberRoot, PublicRoot: root}); err != nil {
		t.Fatal("restricted-tools agent could not register host workspace", err)
	}
}

func TestKeeperHelperExitSettlesOnlyProvenCleanup(t *testing.T) {
	for _, name := range []string{"normal_exit", "expiry", "uncertain"} {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			root := protocol.WorkspaceRoot{Device: 1, Inode: 1}
			identity := Identity{Cwd: "/fixture", MemberRoot: root}
			b := ownershell.Binding{Session: "session", Owner: "owner", Launch: "launch", Runtime: "host", RuntimeGeneration: 1, Root: root, ManifestDigest: "manifest"}
			custody := protocol.ShellCustody{Version: 2, Owner: b.Owner, Login: "login", Session: b.Session, Launch: b.Launch, Runtime: "host", RuntimeGeneration: 1, Root: root, Manifest: b.ManifestDigest, Desktop: "desktop", Credential: "credential", OwnershipRevision: 1, Runner: "runner", Daemon: "keeper", Shell: "shell", Nonce: "fixture", Identity: RuntimeIdentityDigest(identity), Consent: "consent", Created: now.Add(-time.Hour).Unix(), LifetimeUntil: now.Add(time.Hour).Unix()}
			registry := ownershell.NewCustodyRegistry()
			if err := registry.Reserve(custody); err != nil {
				t.Fatal(err)
			}
			if err := registry.Activate(custody.Digest()); err != nil {
				t.Fatal(err)
			}
			if name == "expiry" {
				now = now.Add(2 * time.Hour)
			}
			done := make(chan struct{})
			close(done)
			peer := &helperPeer{done: done, clean: name != "uncertain"}
			child := &retainedChild{custody: custody, peer: peer}
			var notices []RPC
			c := &retainedCoordinator{mu: new(sync.Mutex), binding: b, command: Command{Identity: identity, Root: root}, link: protocol.ShellLinkWelcome{Daemon: "keeper", Desktop: desktopauth.Binding{DesktopID: "desktop"}}, registry: registry, now: func() time.Time { return now }, children: map[string]*retainedChild{custody.Digest(): child}, send: func(_ context.Context, r RPC) error { notices = append(notices, r); return nil }}
			c.settleOnHelperExit(t.Context(), child, peer)
			if child.closed != (name != "uncertain") {
				t.Fatal("helper death became the wrong cleanup proof", name)
			}
			k := NewKeeperRuntime()
			k.service.launches[b.Session] = &registered{retained: c}
			ready := k.ready(b.Session) == nil
			if ready != (name != "uncertain") {
				t.Fatal("ended shell wedged readiness or uncertainty admitted writers", name)
			}
			if name != "uncertain" && (len(notices) != 1 || !notices[0].Ended) {
				t.Fatal("ended shell had no closed notice")
			}
		})
	}
}

func TestKeeperDispatchRejectsStaleChannelAndDurableAuthority(t *testing.T) {
	for _, name := range []string{"replaced_channel", "durable_epoch"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "keeper")
			if err := shellkeeper.Initialize(dir); err != nil {
				t.Fatal(err)
			}
			channels := make(chan *shellkeeper.Channel, 1)
			release := make(chan struct{})
			server, err := shellkeeper.ListenService(t.Context(), dir, nil, func(_ context.Context, ch *shellkeeper.Channel) error { channels <- ch; <-release; return nil })
			if err != nil {
				t.Fatal(err)
			}
			authority, err := shellkeeper.AcquireDaemon(dir)
			if err != nil {
				t.Fatal(err)
			}
			view, err := shellkeeper.InspectSnapshot(t.Context(), dir, authority.Epoch)
			if err != nil {
				t.Fatal(err)
			}
			ended := make(chan error, 1)
			go func() {
				ended <- shellkeeper.WithSession(t.Context(), dir, authority.Epoch, view.Generation, func(*shellkeeper.Channel, shellkeeper.Snapshot) error { <-release; return nil })
			}()
			t.Cleanup(func() { close(release); _ = authority.Close(); _ = server.Close(); <-ended })
			var ch *shellkeeper.Channel
			select {
			case ch = <-channels:
			case <-time.After(time.Second):
				t.Fatal("service channel unavailable")
			}
			if !ch.Current() {
				t.Fatal("fresh stream not current")
			}
			k := NewKeeperRuntime()
			k.rebound = false
			k.current = ch
			if name == "replaced_channel" {
				k.current = nil
			} else {
				_ = authority.Close()
				next, err := shellkeeper.AcquireDaemon(dir)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = next.Close() }()
			}
			if err = k.serveRequest(t.Context(), ch, bridgePacket{Kind: "call", ID: "request", Method: "rebind"}); err == nil {
				t.Fatal("stale channel dispatched an operation", name)
			}
			if k.rebound {
				t.Fatal("stale authority mutated runtime before its reply was refused", name)
			}
		})
	}
}

func TestKeeperClosedNoticesAndFailureLogsContainNoTerminalContent(t *testing.T) {
	const private = "PRIVATE_TERMINAL_FIXTURE_CONTENT_731"
	var logs bytes.Buffer
	previous := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer func() { failurelog.Default = previous }()
	t.Run("delivery", func(t *testing.T) {
		k, b, _ := reviewRuntime(t)
		var notices []RPC
		b.SetReceiver(func(_ context.Context, r RPC) error {
			if r.Ended {
				notices = append(notices, r)
				return nil
			}
			return errors.New(private)
		})
		for _, shell := range []string{"one", "two"} {
			if err := k.sendOutput(t.Context(), RPC{Version: 2, Type: "owner_shell_output", SessionID: "session", Shell: shell, Epoch: 1, Data: []byte(private)}); err == nil {
				t.Fatal("failed delivery was accepted")
			}
		}
		_ = b.Close()
		b.NotifyClosed(t.Context())
		if len(notices) != 2 {
			t.Fatal("bridge loss silently froze terminal access", len(notices))
		}
		for _, r := range notices {
			if !r.Ended || len(r.Data) != 0 || r.StartCause != "keeper_unavailable" {
				t.Fatal("closure notice copied terminal contents")
			}
		}
	})
	if logs.Len() == 0 || strings.Contains(logs.String(), private) {
		t.Fatal("failure logs omitted causes or exposed terminal contents")
	}
}

func TestKeeperEmptyLaunchRetiresThroughRestartedProxy(t *testing.T) {
	root := t.TempDir()
	identity, err := ResolvePersistentIdentity(root)
	if err != nil {
		t.Fatal(err)
	}
	k := NewKeeperRuntime()
	binding := ownershell.Binding{Session: "empty", Owner: "owner", Launch: "old-launch", Runtime: "host", RuntimeGeneration: 1, Root: identity.MemberRoot, ManifestDigest: "manifest", Connection: "old-connection", Live: true}
	command := HostCommand("/proc/self/exe", identity)
	command.Root = identity.MemberRoot
	command.PublicRoot = root
	k.service.launches[binding.Session] = &registered{binding: binding, command: command, mu: new(sync.Mutex), reconcile: func(context.Context) error { return nil }, phase: "running"}
	dir := filepath.Join(t.TempDir(), "keeper")
	if err = shellkeeper.Initialize(dir); err != nil {
		t.Fatal(err)
	}
	server, err := shellkeeper.ListenService(t.Context(), dir, k.Inventory, k.Serve)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := shellkeeper.AcquireDaemon(dir)
	if err != nil {
		t.Fatal(err)
	}
	view, err := shellkeeper.InspectSnapshot(t.Context(), dir, authority.Epoch)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := ConnectKeeper(t.Context(), dir, authority.Epoch, view.Generation)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bridge.Close(); _ = authority.Close(); _ = server.Close(); _ = k.Shutdown(t.Context()) }()
	service := NewService(func(RPC) error { return nil })
	service.SetHostBridge(bridge)
	defer func() { _ = service.Shutdown(t.Context()) }()
	if err = service.Rebind(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, err := service.Binding(binding.Session)
	if err != nil {
		t.Fatal("empty launch was not discoverable after reconnect", err)
	}
	if _, err = service.Retire(t.Context(), ownershell.SupervisorIdentity(current, ownershell.SignedGrant{}).Launch); err != nil {
		t.Fatal("retirement stayed local after restart", err)
	}
	binding.Launch = "new-launch"
	binding.RuntimeGeneration = 2
	if err = service.Register(t.Context(), new(sync.Mutex), binding, command, func(context.Context) error { return nil }); err != nil {
		t.Fatal("empty old launch wedged replacement", err)
	}
}

func TestKeeperSourceReplacementCannotRestoreOldLegacyAuthority(t *testing.T) {
	k := NewKeeperRuntime()
	k.rebound = true
	entry := &registered{mu: new(sync.Mutex), binding: ownershell.Binding{Session: "session", Runtime: "host"}}
	k.service.launches["session"] = entry
	body, _ := json.Marshal(hostSource{Session: "session", Same: false, Known: true})
	if _, err := k.invoke(t.Context(), "source", body); err == nil || !entry.consentDirty.Load() {
		t.Fatal("confirmed alias replacement did not fence legacy authority")
	}
	body, _ = json.Marshal(hostSource{Session: "session", Same: true, Known: true})
	if _, err := k.invoke(t.Context(), "source", body); err != nil || !entry.consentDirty.Load() {
		t.Fatal("alias restoration silently restored the old consent", err)
	}
}
