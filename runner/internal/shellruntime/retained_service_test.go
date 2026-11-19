//go:build linux

package shellruntime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestRetainedServiceHelperProcess(t *testing.T) {
	args := os.Args
	if len(args) > 3 && args[len(args)-1] == "--probe" && args[len(args)-3] == "retained-clock-helper" {
		if Probe(os.Stdout) != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(args) < 3 || args[len(args)-2] != "retained-clock-helper" {
		return
	}
	path := args[len(args)-1]
	boot := time.Now()
	clock := func() (time.Time, time.Duration, error) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return time.Time{}, 0, err
		}
		shift, err := strconv.ParseInt(string(raw), 10, 64)
		if err != nil {
			return time.Time{}, 0, err
		}
		return time.Now().Add(time.Duration(shift)), time.Since(boot) + time.Duration(shift), nil
	}
	if serveHelperWithResolvers(os.Stdin, os.Stdout, ResolveIdentity, ResolvePersistentIdentity, time.Now, func() (ownershell.LeaseClock, error) { return clock, nil }) != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestRetainedServiceReconnectKeepsSameShellAfterThirtyMinutes(t *testing.T) {
	base, b, old, key := fixture(t)
	b.Runtime = "host"
	b.Container = ""
	caps := protocol.WorkspaceCapabilities{ManifestVersion: protocol.WorkspaceVersion, ShellVersion: 1, LinuxHost: true, ShellPersistence: 2, PersistentHost: true}
	b.ServerCapabilities = caps
	b.RuntimeCapabilities = caps
	identity, err := ResolvePersistentIdentity(base.command.Identity.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	clockPath := filepath.Join(t.TempDir(), "clock")
	if err := os.WriteFile(clockPath, []byte("0"), 0600); err != nil {
		t.Fatal(err)
	}
	var offset atomic.Int64
	now := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	var starts atomic.Int32
	command := base.command
	command.Container = b.Container
	command.Identity = identity
	command.Root = b.Root
	command.PublicRoot = identity.Cwd
	command.Consent = nil
	command.Args = []string{"GORACE=atexit_sleep_ms=0", os.Args[0], "-test.run=^TestRetainedServiceHelperProcess$", "--", "retained-clock-helper", clockPath}
	command.OwnedStarted = func(int) error { starts.Add(1); return nil }
	frames := make(chan RPC, 128)
	service := NewService(func(r RPC) error {
		select {
		case frames <- r:
			return nil
		default:
			return ownershell.ErrLimit
		}
	})
	service.now = now
	service.probePersistent = func(context.Context, Command) (Identity, error) { return identity, nil }
	service.SetCapabilities(caps, caps)
	hello := service.ShellHello()
	welcome := protocol.ShellLinkWelcome{Version: 2, Runner: "runner", Daemon: hello.Daemon, Challenge: hello.Challenge, Desktop: desktopauth.Binding{DesktopID: "desktop", OwnerID: b.Owner, TokenID: "credential", Revision: 1, ExpiresAt: now().Add(8 * 24 * time.Hour)}}
	if err := service.AcceptShellLink(t.Context(), hello, &welcome); err != nil {
		t.Fatal(err)
	}
	if err := service.Register(t.Context(), new(sync.Mutex), b, command, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})
	b, err = service.Binding(b.Session)
	if err != nil {
		t.Fatal(err)
	}
	call := func(r RPC) RPC { r.Version = 2; r.SessionID = b.Session; return service.execute(t.Context(), r) }
	if !call(RPC{Op: "configure", Binding: b, Key: key.Public().(ed25519.PublicKey), Policy: command.Policy}).OK {
		t.Fatal("retained configuration refused")
	}
	c := protocol.ShellCustody{Container: b.Container, Version: 2, Owner: b.Owner, Login: "login", Session: b.Session, Launch: b.Launch, Runtime: b.Runtime, RuntimeGeneration: b.RuntimeGeneration, Root: b.Root, Manifest: b.ManifestDigest, Desktop: "desktop", Credential: "credential", OwnershipRevision: 1, Runner: "runner", Daemon: hello.Daemon, Shell: "retained-child", Nonce: "private-custody", Identity: RuntimeIdentityDigest(identity), Consent: "consent", Created: now().Unix(), LifetimeUntil: now().Add(7 * 24 * time.Hour).Unix()}
	c.Consent = acceptServiceConsent(t, service, b, "login")
	grant := old.Grant
	grant.Version = 2
	grant.Runtime = b.Runtime
	grant.ConsentID = c.Consent
	grant.ConnectionID = b.Connection
	grant.Audience = b.Audience
	grant.ShellGeneration = c.Shell
	grant.CustodyID = c.Digest()
	grant.AccessEpoch = 1
	grant.IssuedAt = now().Unix()
	grant.ExpiresAt = grant.IssuedAt + 30
	signed := ownershell.SignGrant(key, grant)
	coordinator := service.launches[b.Session].retained
	coordinator.now = func() time.Time { return now().Add(-10 * time.Second) }
	refused := call(RPC{Op: "open", Custody: &c, Grant: signed, Window: ownershell.ServerOutputWindow})
	if refused.OK || !refused.AllocationRefused || len(coordinator.children) != 0 {
		t.Fatal("clock refusal did not confirm no allocation")
	}
	for _, acceptance := range service.launches[b.Session].consents {
		if acceptance.used != "" {
			t.Fatal("clock refusal consumed consent")
		}
	}
	coordinator.now = func() time.Time { return now().Add(-2 * time.Second) }
	if opened := call(RPC{Op: "open", Custody: &c, Grant: signed, Window: ownershell.ServerOutputWindow}); !opened.OK {
		t.Fatalf("retained open refused: reason=%s cause=%s", opened.ErrorCode, opened.StartCause)
	}
	if !call(RPC{Op: "input", Grant: signed, Seq: 1, Data: []byte("TOKEN_KEEP='same-process'; printf 'READY_KEEP\\n'\n")}).OK {
		t.Fatal("initial input refused")
	}
	receive := func(g ownershell.SignedGrant, marker string) {
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
				if !call(RPC{Op: "credit", Grant: g, Offset: end}).OK {
					t.Fatal("credit refused")
				}
				if !call(RPC{Op: "receipt", Grant: g, Offset: end}).OK {
					t.Fatal("server receipt refused")
				}
				if bytes.Contains(data, []byte(marker)) {
					return
				}
			case <-ctx.Done():
				t.Fatal("shell output missing", marker)
			}
		}
	}
	receive(signed, "READY_KEEP\r\n")
	if err := service.Rebind(t.Context()); err != nil {
		t.Fatal(err)
	}
	shift := 31 * time.Minute
	nextFile := clockPath + ".next"
	if err := os.WriteFile(nextFile, []byte(strconv.FormatInt(int64(shift), 10)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(nextFile, clockPath); err != nil {
		t.Fatal(err)
	}
	offset.Store(int64(shift))
	if call(RPC{Op: "input", Grant: signed, Seq: 2, Data: []byte("exit\n")}).OK {
		t.Fatal("old epoch accepted input after sleep")
	}
	hello = service.ShellHello()
	welcome.Challenge = hello.Challenge
	if err := service.AcceptShellLink(t.Context(), hello, &welcome); err != nil {
		t.Fatal(err)
	}
	previous := b
	b, err = service.Binding(b.Session)
	if err != nil {
		t.Fatal(err)
	}
	if b.Connection == previous.Connection {
		t.Fatal("reconnect reused old connection")
	}
	server := ownershell.NewCustodyRegistry()
	if err := server.Reserve(c); err != nil {
		t.Fatal(err)
	}
	if err := server.Activate(c.Digest()); err != nil {
		t.Fatal(err)
	}
	server.Suspend(c.PhysicalScope())
	inventory := server.Snapshot(c.PhysicalScope(), "fresh-inventory")
	observed := call(RPC{Op: "inventory", Inventory: &inventory})
	if !observed.OK || observed.Inventory == nil {
		t.Fatal("runtime inventory refused")
	}
	matched, err := server.Reconcile(*observed.Inventory, inventory.Challenge)
	if err != nil || len(matched.Eligible) != 1 {
		t.Fatal("same custody not mutually claimed", err)
	}
	_, freshKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	next := grant
	next.AccessEpoch = 2
	next.ConnectionID = b.Connection
	next.Audience = b.Audience
	next.IssuedAt = now().Unix()
	next.ExpiresAt = next.IssuedAt + 30
	fresh := ownershell.SignGrant(freshKey, next)
	if !call(RPC{Op: "prepare", Binding: b, Transaction: "resume", Inventory: observed.Inventory, Key: freshKey.Public().(ed25519.PublicKey), Grants: []ownershell.SignedGrant{fresh}}).OK {
		t.Fatal("rekey prepare refused")
	}
	if call(RPC{Op: "input", Grant: fresh, Seq: 1, Data: []byte("exit\n")}).OK {
		t.Fatal("prepare opened access")
	}
	if !call(RPC{Op: "commit", Transaction: "resume"}).OK {
		t.Fatal("rekey commit refused")
	}
	if !call(RPC{Op: "input", Grant: fresh, Seq: 1, Data: []byte("printf 'RESUMED:%s\\n' \"$TOKEN_KEEP\"\n")}).OK {
		t.Fatal("fresh input refused")
	}
	receive(fresh, "RESUMED:same-process\r\n")
	if starts.Load() != 1 {
		t.Fatal("reconnect created a replacement helper")
	}
	closed := call(RPC{Op: "close", Custody: &c, Transaction: "finished"})
	if !closed.OK || closed.CustodyCleanup == nil || closed.CustodyCleanup.ValidateFor(c, "finished", hello.Daemon) != nil {
		t.Fatal("custody cleanup proof missing")
	}
}

// Native P2 barriers must precede queue waits: a prepare can be in helper IO
// while the workspace owner starts syncing or closing the physical launch.
func TestRetainedNativeBarrierPreemptsCommandQueue(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(strconv.FormatBool(terminal), func(t *testing.T) {
			_, b, _, _ := fixture(t)
			service := NewService(func(RPC) error { return nil })
			c := &retainedCoordinator{mu: new(sync.Mutex), active: true, phase: "running", revision: 1, children: map[string]*retainedChild{}, registry: ownershell.NewCustodyRegistry(), binding: b}
			entry := &registered{binding: b, retained: c, mu: c.mu}
			service.launches[b.Session] = entry
			l := ownershell.SupervisorIdentity(b, ownershell.SignedGrant{}).Launch
			entry.opMu.Lock()
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			n := nativeSupervisor{service: service, launch: l}
			if terminal {
				_, _ = n.CloseLaunch(ctx, l, "test")
			} else {
				_, _ = n.Freeze(ctx, l)
			}
			c.mu.Lock()
			active, ended, revision := c.active, c.terminal, c.revision
			c.mu.Unlock()
			entry.opMu.Unlock()
			if active || revision <= 1 || ended != terminal {
				t.Fatalf("barrier waited behind command queue: active=%t terminal=%t revision=%d", active, ended, revision)
			}
		})
	}
}

func TestManagerServiceReconnectKeepsSameShellAfterThirtyMinutes(t *testing.T) {
	base, b, _, _ := fixture(t)
	b.Runtime = "host"
	b.Container = ""
	caps := protocol.WorkspaceCapabilities{ManifestVersion: protocol.WorkspaceVersion, ShellVersion: 1, LinuxHost: true, ShellPersistence: 2, PersistentHost: true}
	b.ServerCapabilities = caps
	b.RuntimeCapabilities = caps
	identity, err := ResolvePersistentIdentity(base.command.Identity.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	clockPath := filepath.Join(t.TempDir(), "clock")
	if err := os.WriteFile(clockPath, []byte("0"), 0600); err != nil {
		t.Fatal(err)
	}
	var offset atomic.Int64
	now := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	var starts atomic.Int32
	command := base.command
	command.Container = b.Container
	command.Identity = identity
	command.Root = b.Root
	command.PublicRoot = identity.Cwd
	command.Consent = nil
	command.Args = []string{"GORACE=atexit_sleep_ms=0", os.Args[0], "-test.run=^TestRetainedServiceHelperProcess$", "--", "retained-clock-helper", clockPath}
	command.OwnedStarted = func(int) error { starts.Add(1); return nil }
	frames := make(chan RPC, 128)
	service := NewService(func(r RPC) error {
		select {
		case frames <- r:
			return nil
		default:
			return ownershell.ErrLimit
		}
	})
	service.now = now
	service.probePersistent = func(context.Context, Command) (Identity, error) { return identity, nil }
	service.SetCapabilities(caps, caps)
	hello := service.ShellHello()
	welcome := protocol.ShellLinkWelcome{Version: 2, Runner: "runner", Daemon: hello.Daemon, Challenge: hello.Challenge, Desktop: desktopauth.Binding{DesktopID: "desktop", OwnerID: b.Owner, TokenID: "credential", Revision: 1, ExpiresAt: now().Add(8 * 24 * time.Hour)}}
	if err := service.AcceptShellLink(t.Context(), hello, &welcome); err != nil {
		t.Fatal(err)
	}
	if err := service.Register(t.Context(), new(sync.Mutex), b, command, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})
	b, err = service.Binding(b.Session)
	if err != nil {
		t.Fatal(err)
	}

	peer := &retainedServiceManagerPeer{service: service, streams: map[string]chan ownershell.PersistentFrame{}}
	run, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		for {
			select {
			case <-run.Done():
				return
			case frame := <-frames:
				if frame.Type != "owner_shell_output" || frame.Custody == nil {
					continue
				}
				peer.mu.Lock()
				out := peer.streams[frame.Custody.Digest()]
				if out != nil {
					select {
					case out <- ownershell.PersistentFrame{HistoryFrame: ownershell.HistoryFrame{Offset: frame.Offset, End: frame.End, Gap: frame.Gap, Data: frame.Data}, Epoch: frame.Epoch, Ended: frame.Ended}:
					default:
					}
				}
				peer.mu.Unlock()
			}
		}
	}()
	authority := retainedManagerAuthority{policy: command.Policy, until: now().Add(8 * 24 * time.Hour)}
	manager, err := ownershell.New(authority, retainedManagerResolver{service: service, session: b.Session}, peer, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	welcome.Runner = manager.Incarnation()
	if err = service.AcceptShellLink(t.Context(), hello, &welcome); err != nil {
		t.Fatal(err)
	}
	manager.SetPolicy(t.Context(), command.Policy)
	t.Cleanup(func() { manager.Shutdown(context.Background()) })
	actor := ownershell.Actor{Kind: "human", Owner: b.Owner, Login: "login"}
	info, err := manager.Open(t.Context(), actor, b.Session)
	if err != nil {
		t.Fatal(err)
	}
	attached, err := manager.AttachWithReplay(t.Context(), actor, info.Handle, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.InputFrame(t.Context(), actor, info.Handle, attached.ID, 1, []byte("KEEP_MANAGER='same-shell'; printf 'MANAGER_READY\\n'\n"), true); err != nil {
		t.Fatal(err)
	}
	receive := func(at ownershell.Attachment, marker string) {
		t.Helper()
		var data []byte
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case chunk, ok := <-at.Output:
				if !ok {
					t.Fatal("attachment ended before output")
				}
				data = append(data, chunk...)
				if bytes.Contains(data, []byte(marker)) {
					return
				}
			case <-timer.C:
				t.Fatalf("missing output %s", marker)
			}
		}
	}
	receive(attached, "MANAGER_READY\r\n")
	service.SetCapabilities(protocol.WorkspaceCapabilities{}, protocol.WorkspaceCapabilities{})
	manager.Unavailable(t.Context())
	if err = service.Rebind(t.Context()); err != nil {
		t.Fatal(err)
	}
	shift := 31 * time.Minute
	nextFile := clockPath + ".next"
	if err = os.WriteFile(nextFile, []byte(strconv.FormatInt(int64(shift), 10)), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(nextFile, clockPath); err != nil {
		t.Fatal(err)
	}
	offset.Store(int64(shift))
	manager.Tick(t.Context())
	hello = service.ShellHello()
	welcome.Challenge = hello.Challenge
	if err = service.AcceptShellLink(t.Context(), hello, &welcome); err != nil {
		t.Fatal(err)
	}
	manager.SetPolicy(t.Context(), command.Policy)
	attached, err = manager.AttachWithReplay(t.Context(), actor, info.Handle, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = manager.InputFrame(t.Context(), actor, info.Handle, attached.ID, 1, []byte("printf 'MANAGER_RESUMED:%s\\n' \"$KEEP_MANAGER\"\n"), true); err != nil {
		t.Fatal(err)
	}
	receive(attached, "MANAGER_RESUMED:same-shell\r\n")
	if _, err := manager.Open(t.Context(), actor, b.Session); !ownershell.Unknown(err) {
		t.Fatal("degraded installation probe admitted a replacement shell", err)
	}
	if starts.Load() != 1 {
		t.Fatalf("reconnect created %d helpers", starts.Load())
	}
}

type retainedManagerAuthority struct {
	policy protocol.ShellPolicy
	until  time.Time
}

func (a retainedManagerAuthority) Authorize(context.Context, ownershell.Actor) (protocol.ShellPolicy, error) {
	return a.policy, nil
}
func (a retainedManagerAuthority) AuthorizePersistent(context.Context, ownershell.Actor) (protocol.ShellPolicy, time.Time, error) {
	return a.policy, a.until, nil
}

type retainedManagerResolver struct {
	service *Service
	session string
}

func (r retainedManagerResolver) Resolve(ctx context.Context, _ string) (ownershell.Binding, error) {
	b, err := r.service.Binding(r.session)
	if err != nil {
		return b, err
	}
	if b.ShellLink.Version != 2 {
		return b, ownershell.ErrUnknown
	}
	reply := r.service.execute(ctx, RPC{Version: 2, SessionID: r.session, Op: "describe"})
	if !reply.OK {
		if reply.ErrorCode == string(ownershell.ReasonUnavailable) {
			return b, ownershell.ErrUnknown
		}
		return b, ownershell.ErrDenied
	}
	return reply.Binding, nil
}

type retainedServiceManagerPeer struct {
	ownershell.Supervisor
	service *Service
	mu      sync.Mutex
	streams map[string]chan ownershell.PersistentFrame
}

func (p *retainedServiceManagerPeer) call(ctx context.Context, session string, r RPC) (RPC, error) {
	r.Version = 2
	r.SessionID = session
	reply := p.service.execute(ctx, r)
	if !reply.OK {
		return reply, ownershell.Refuse(ownershell.RuntimeReason(reply.ErrorCode))
	}
	return reply, nil
}
func (p *retainedServiceManagerPeer) stream(g ownershell.SignedGrant) <-chan ownershell.PersistentFrame {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(chan ownershell.PersistentFrame, 16)
	p.streams[g.Grant.CustodyID] = out
	return out
}
func (p *retainedServiceManagerPeer) OpenCustody(ctx context.Context, b ownershell.Binding, c protocol.ShellCustody, g ownershell.SignedGrant, key ed25519.PublicKey) (<-chan ownershell.PersistentFrame, error) {
	if _, err := p.call(ctx, b.Session, RPC{Op: "configure", Binding: b, Key: key, Policy: protocol.ShellPolicy{Version: 1, Enabled: true, Revision: g.Grant.PolicyRevision}}); err != nil {
		return nil, err
	}
	out := p.stream(g)
	_, err := p.call(ctx, b.Session, RPC{Op: "open", Custody: &c, Grant: g, Window: ownershell.ServerOutputWindow})
	return out, err
}
func (p *retainedServiceManagerPeer) Renew(ctx context.Context, g ownershell.SignedGrant) error {
	_, err := p.call(ctx, g.Grant.SessionID, RPC{Op: "renew", Grant: g})
	return err
}
func (p *retainedServiceManagerPeer) Input(ctx context.Context, g ownershell.SignedGrant, seq uint64, data []byte) error {
	_, err := p.call(ctx, g.Grant.SessionID, RPC{Op: "input", Grant: g, Seq: seq, Data: data})
	return err
}
func (p *retainedServiceManagerPeer) SuspendCustody(ctx context.Context, b ownershell.Binding) error {
	_, err := p.call(ctx, b.Session, RPC{Op: "suspend"})
	return err
}
func (p *retainedServiceManagerPeer) InventoryCustody(ctx context.Context, b ownershell.Binding, v ownershell.CustodyInventory) (ownershell.CustodyInventory, error) {
	reply, err := p.call(ctx, b.Session, RPC{Op: "inventory", Inventory: &v})
	if reply.Inventory == nil {
		return ownershell.CustodyInventory{}, ownershell.ErrUnknown
	}
	return *reply.Inventory, err
}
func (p *retainedServiceManagerPeer) PrepareCustody(ctx context.Context, b ownershell.Binding, tx string, v ownershell.CustodyInventory, key ed25519.PublicKey, gs []ownershell.SignedGrant) error {
	_, err := p.call(ctx, b.Session, RPC{Op: "prepare", Binding: b, Transaction: tx, Inventory: &v, Key: key, Grants: gs})
	return err
}
func (p *retainedServiceManagerPeer) CommitCustody(ctx context.Context, b ownershell.Binding, tx string, gs []ownershell.SignedGrant) (map[string]<-chan ownershell.PersistentFrame, error) {
	streams := map[string]<-chan ownershell.PersistentFrame{}
	for _, g := range gs {
		streams[g.Grant.CustodyID] = p.stream(g)
	}
	_, err := p.call(ctx, b.Session, RPC{Op: "commit", Transaction: tx})
	return streams, err
}
func (p *retainedServiceManagerPeer) CloseCustody(ctx context.Context, c protocol.ShellCustody, tx string) (protocol.CustodyCleanup, error) {
	reply, err := p.call(ctx, c.Session, RPC{Op: "close", Custody: &c, Transaction: tx})
	if reply.CustodyCleanup == nil {
		return protocol.CustodyCleanup{}, ownershell.ErrUnknown
	}
	return *reply.CustodyCleanup, err
}
func (p *retainedServiceManagerPeer) ReceiptCustody(ctx context.Context, g ownershell.SignedGrant, offset uint64) error {
	if _, err := p.call(ctx, g.Grant.SessionID, RPC{Op: "credit", Grant: g, Offset: offset}); err != nil {
		return err
	}
	_, err := p.call(ctx, g.Grant.SessionID, RPC{Op: "receipt", Grant: g, Offset: offset})
	return err
}

func TestRetainedColdCleanupNeedsIndependentProofAndCannotKillLiveSibling(t *testing.T) {
	base, b, _, _ := fixture(t)
	s := NewService(func(RPC) error { return nil })
	hello := s.ShellHello()
	w := protocol.ShellLinkWelcome{Version: 2, Runner: "new-runner", Daemon: hello.Daemon, Challenge: hello.Challenge, Desktop: desktopauth.Binding{DesktopID: "desktop", OwnerID: b.Owner, TokenID: "new-credential", Revision: 2, ExpiresAt: time.Now().Add(8 * 24 * time.Hour)}}
	if err := s.AcceptShellLink(t.Context(), hello, &w); err != nil {
		t.Fatal(err)
	}
	calls := 0
	proven := false
	entry := &registered{mu: new(sync.Mutex), binding: b, command: base.command, phase: "running", reconcile: func(context.Context) error {
		calls++
		if !proven {
			return ownershell.ErrUnknown
		}
		return nil
	}}
	s.launches[b.Session] = entry
	c := protocol.ShellCustody{Container: b.Container, Version: 2, Owner: b.Owner, Login: "login", Session: b.Session, Launch: b.Launch, Runtime: b.Runtime, RuntimeGeneration: b.RuntimeGeneration, Root: b.Root, Manifest: b.ManifestDigest, Desktop: "desktop", Credential: "old-credential", OwnershipRevision: 1, Runner: "old-runner", Daemon: "old-daemon", Shell: "old-shell", Nonce: "old-nonce", Identity: "old-identity", Consent: "consent", Created: time.Now().Unix(), LifetimeUntil: time.Now().Add(7 * 24 * time.Hour).Unix()}
	if err := s.custody.Reserve(c); err != nil {
		t.Fatal(err)
	}
	_ = s.custody.Activate(c.Digest())
	s.custody.DaemonRestart(c.PhysicalScope(), hello.Daemon)
	request := RPC{Version: 2, SessionID: b.Session, Op: "close", Custody: &c, Transaction: "cold-close"}
	if s.execute(t.Context(), request).OK || s.custody.Count(c.PhysicalScope()) != 1 {
		t.Fatal("absence became cleanup proof")
	}
	proven = true
	result := s.execute(t.Context(), request)
	if !result.OK || result.CustodyCleanup == nil || result.CustodyCleanup.ValidateFor(c, "cold-close", hello.Daemon) != nil || s.custody.Count(c.PhysicalScope()) != 0 {
		t.Fatal("independent cold cleanup did not settle exact custody")
	}
	sibling := c
	sibling.Shell = "new-shell"
	sibling.Nonce = "new-nonce"
	entry.retained = &retainedCoordinator{mu: entry.mu, binding: b, children: map[string]*retainedChild{sibling.Digest(): {custody: sibling}}, registry: s.custody}
	before := calls
	if s.execute(t.Context(), request).OK || calls != before {
		t.Fatal("old cleanup recovered a namespace with a live sibling")
	}
}

func TestRetainedIncompleteAndStaleWelcomeStaySuspended(t *testing.T) {
	service := NewService(func(RPC) error { return nil })
	hello := service.ShellHello()
	c := &retainedCoordinator{mu: new(sync.Mutex), registry: ownershell.NewCustodyRegistry(), children: map[string]*retainedChild{}, active: true}
	service.launches["session"] = &registered{retained: c}
	if err := service.AcceptShellLink(t.Context(), hello, nil); err != nil {
		t.Fatal(err)
	}
	if c.terminal || c.active {
		t.Fatal("incomplete handshake destroyed custody or kept access")
	}
	if err := service.Rebind(t.Context()); err != nil {
		t.Fatal(err)
	}
	next := service.ShellHello()
	if next.Challenge == hello.Challenge {
		t.Fatal("reconnect reused hello challenge")
	}
	welcome := protocol.ShellLinkWelcome{Version: 2, Runner: "runner", Daemon: next.Daemon, Challenge: next.Challenge, Desktop: desktopauth.Binding{DesktopID: "desktop", OwnerID: "owner", TokenID: "credential", Revision: 1, ExpiresAt: time.Now().Add(time.Hour)}}
	c.link = welcome
	if err := service.AcceptShellLink(t.Context(), next, &welcome); err != nil {
		t.Fatal(err)
	}
	if err := service.AcceptShellLink(t.Context(), hello, nil); err != nil {
		t.Fatal(err)
	}
	if c.terminal || service.persistentLink == nil || service.persistentLink.Challenge != next.Challenge {
		t.Fatal("old timer invalidated new welcome")
	}
}

func acceptServiceConsent(t *testing.T, s *Service, b ownershell.Binding, login string) string {
	t.Helper()
	a := ownershell.Actor{Kind: "human", Owner: b.Owner, Login: login}
	reply := s.execute(t.Context(), RPC{Version: 2, SessionID: b.Session, Op: "consent_challenge", Actor: a})
	if !reply.OK || reply.PersistentConsent == nil {
		t.Fatal("host consent challenge refused")
	}
	accepted := s.execute(t.Context(), RPC{Version: 2, SessionID: b.Session, Op: "consent_accept", Actor: a, PersistentConsent: reply.PersistentConsent})
	if !accepted.OK {
		t.Fatal("host consent acceptance refused")
	}
	return accepted.PersistentConsent.Host.ID
}
func (r retainedManagerResolver) ConsumePersistentConsent(ctx context.Context, a ownershell.Actor, b ownershell.Binding) (string, error) {
	reply := r.service.execute(ctx, RPC{Version: 2, SessionID: b.Session, Op: "consent_challenge", Actor: a})
	if !reply.OK || reply.PersistentConsent == nil {
		return "", ownershell.ErrDenied
	}
	accepted := r.service.execute(ctx, RPC{Version: 2, SessionID: b.Session, Op: "consent_accept", Actor: a, PersistentConsent: reply.PersistentConsent})
	if !accepted.OK {
		return "", ownershell.ErrDenied
	}
	return accepted.PersistentConsent.Host.ID, nil
}

func TestRetainedIncompleteWelcomeSuspendsEveryLaunchAfterJoinError(t *testing.T) {
	s := NewService(func(RPC) error { return nil })
	h := s.ShellHello()
	var all []*retainedCoordinator
	for n := 0; n < 8; n++ {
		c := &retainedCoordinator{mu: new(sync.Mutex), active: true, phase: "running", registry: ownershell.NewCustodyRegistry(), children: map[string]*retainedChild{"child": {outputDone: make(chan struct{})}}}
		s.launches[strconv.Itoa(n)] = &registered{retained: c}
		all = append(all, c)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.AcceptShellLink(ctx, h, nil); err == nil {
		t.Fatal("output join error hidden")
	}
	for _, c := range all {
		if c.active {
			t.Fatal("later launch still has access after failed suspension join")
		}
	}
}
