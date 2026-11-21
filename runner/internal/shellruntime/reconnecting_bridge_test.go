package shellruntime

import (
	"context"
	"errors"
	"github.com/blerglab/blerg-ai/contracts/desktopauth"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type reconnectFixture struct {
	done     chan struct{}
	notices  chan struct{}
	receiver func(context.Context, RPC) error
	once     sync.Once
}

func (f *reconnectFixture) Invoke(context.Context, string, any, any) error { return nil }
func (f *reconnectFixture) Metadata(string, any, any) error                { return nil }
func (f *reconnectFixture) HostSession(string) bool                        { return true }
func (f *reconnectFixture) SetReceiver(r func(context.Context, RPC) error) { f.receiver = r }
func (f *reconnectFixture) Close() error                                   { f.once.Do(func() { close(f.done) }); return nil }
func (f *reconnectFixture) Done() <-chan struct{}                          { return f.done }
func (f *reconnectFixture) NotifyClosed(context.Context)                   { close(f.notices) }

func TestReconnectingBridgeRefusesOfflineNotifiesAndRecoversWithoutOwnerExit(t *testing.T) {
	dial := make(chan *reconnectFixture)
	retry := make(chan struct{})
	waiting := make(chan struct{}, 3)
	b := newReconnectingHostBridge(t.Context(), func(ctx context.Context) (HostBridge, error) {
		select {
		case f := <-dial:
			if f == nil {
				return nil, ErrKeeperUnavailable
			}
			return f, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}, func(ctx context.Context) bool {
		waiting <- struct{}{}
		select {
		case <-retry:
			return true
		case <-ctx.Done():
			return false
		}
	})
	defer func() { _ = b.Close() }()
	recovered := make(chan struct{}, 2)
	b.SetReceiver(func(context.Context, RPC) error { return nil })
	b.SetRecovery(func(context.Context) error { recovered <- struct{}{}; return nil })
	guard, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if !errors.Is(b.Invoke(guard, "call", nil, nil), ErrKeeperUnavailable) {
		t.Fatal("offline shell was not refused")
	}
	dial <- nil
	select {
	case <-waiting:
	case <-guard.Done():
		t.Fatal("missing keeper did not schedule retry")
	}
	retry <- struct{}{}
	first := &reconnectFixture{done: make(chan struct{}), notices: make(chan struct{})}
	dial <- first
	select {
	case <-recovered:
	case <-guard.Done():
		t.Fatal("keeper did not recover")
	}
	if err := b.WaitReady(guard); err != nil {
		t.Fatal(err)
	}
	if err := b.Invoke(guard, "call", nil, nil); err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	select {
	case <-first.notices:
	case <-guard.Done():
		t.Fatal("bridge loss froze terminal without notice")
	}
	<-waiting
	if !errors.Is(b.Invoke(guard, "call", nil, nil), ErrKeeperUnavailable) {
		t.Fatal("dead bridge stayed cached")
	}
	retry <- struct{}{}
	next := &reconnectFixture{done: make(chan struct{}), notices: make(chan struct{})}
	dial <- next
	select {
	case <-recovered:
	case <-guard.Done():
		t.Fatal("bridge was not replaced")
	}
	if !b.HostSession("session") {
		t.Fatal("restored host session not routed")
	}
}

type recoveryReviewBridge struct{ reviewBridge }

func (b *recoveryReviewBridge) Metadata(m string, in, out any) error {
	return b.Invoke(context.Background(), m, in, out)
}

func TestServiceRecoveryRetriesRegistrationAndWaitsForCorrelatedShellWelcome(t *testing.T) {
	var offline atomic.Bool
	offline.Store(true)
	var registrations atomic.Int32
	hello := protocol.ShellLinkHello{Version: 2, Daemon: "keeper", Challenge: "fresh"}
	requested := make(chan protocol.ShellLinkHello, 1)
	s := NewService(func(r RPC) error {
		if r.ShellHello != nil {
			requested <- *r.ShellHello
		}
		return nil
	})
	b := ownershell.Binding{Session: "session", Owner: "owner", Launch: "launch", Runtime: "host", RuntimeGeneration: 1, Connection: "connection", Live: true}
	command := Command{Identity: Identity{Cwd: "/fixture"}, PublicRoot: "/fixture", Root: protocol.WorkspaceRoot{Device: 1, Inode: 1}}
	s.SetHostBridge(&recoveryReviewBridge{reviewBridge{invoke: func(_ context.Context, m string, in, out any) error {
		if offline.Load() {
			return ErrKeeperUnavailable
		}
		switch m {
		case "hello":
			*out.(*protocol.ShellLinkHello) = hello
		case "register":
			r := in.(hostRegistration)
			if r.Binding != b || r.Identity != command.Identity || r.Root != command.Root || r.PublicRoot != command.PublicRoot {
				t.Error("retry lost trusted registration pins")
			}
			registrations.Add(1)
		}
		return nil
	}}})
	if err := s.Register(t.Context(), new(sync.Mutex), b, command, func(context.Context) error { return nil }); err != nil {
		t.Fatal("keeper absence paused agent workspace registration", err)
	}
	if s.launches[b.Session] == nil {
		t.Fatal("offline registration lost retry pins")
	}
	offline.Store(false)
	result := make(chan error, 1)
	guard, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	go func() { result <- s.recoverHostBridge(guard) }()
	select {
	case actual := <-requested:
		if actual != hello {
			t.Fatal("wrong refresh hello")
		}
	case <-guard.Done():
		t.Fatal("recovery did not refresh shell authority")
	}
	welcome := protocol.ShellLinkWelcome{Version: 2, Runner: "runner", Daemon: hello.Daemon, Challenge: hello.Challenge, Desktop: desktopauth.Binding{DesktopID: "desktop", OwnerID: "owner", TokenID: "credential", Revision: 1, ExpiresAt: time.Now().Add(time.Hour)}}
	stale := hello
	stale.Challenge = "stale"
	staleWelcome := welcome
	staleWelcome.Challenge = stale.Challenge
	if err := s.AcceptShellLink(guard, stale, &staleWelcome); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	pending := s.hostRefreshDone != nil
	s.mu.Unlock()
	if !pending {
		t.Fatal("stale welcome acknowledged current recovery")
	}
	if err := s.AcceptShellLink(guard, hello, &welcome); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-guard.Done():
		t.Fatal("shell adoption never retried after keeper recovery")
	}
	// Recovery and the pending host job may both reconcile this launch; the keeper's
	// register is idempotent for identical pins, which every call checked above.
	if registrations.Load() < 1 {
		t.Fatal("trusted registration was not retried", registrations.Load())
	}
}

func TestManagedKeeperTransportCannotBlockAgentConnectionHooks(t *testing.T) {
	entered := make(chan struct{})
	manager := NewReconnectingHostBridge(t.Context(), func(ctx context.Context) (HostBridge, error) { close(entered); <-ctx.Done(); return nil, ctx.Err() })
	defer func() { _ = manager.Close() }()
	var calls atomic.Int32
	blocked := &reviewBridge{invoke: func(ctx context.Context, _ string, _, _ any) error { calls.Add(1); <-ctx.Done(); return ctx.Err() }}
	service := NewService(func(RPC) error { return nil })
	service.SetHostBridge(manager)
	<-entered
	manager.mu.Lock()
	manager.current = blocked
	manager.mu.Unlock()
	guard, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := service.Rebind(guard); err != nil {
		t.Fatal("agent reconnect waited on keeper rebind", err)
	}
	_ = service.ShellHello()
	service.SetCapabilities(protocol.WorkspaceCapabilities{}, protocol.WorkspaceCapabilities{})
	if calls.Load() != 0 {
		t.Fatal("agent connection hook made synchronous keeper IPC", calls.Load())
	}
}

func TestReconnectingBridgePublishesOnlyFullyRecoveredShellTransport(t *testing.T) {
	peer := &reconnectFixture{done: make(chan struct{}), notices: make(chan struct{})}
	entered, release := make(chan struct{}), make(chan struct{})
	b := newReconnectingHostBridge(t.Context(), func(context.Context) (HostBridge, error) { return peer, nil }, func(ctx context.Context) bool { <-ctx.Done(); return false })
	defer func() { _ = b.Close() }()
	b.SetReceiver(func(context.Context, RPC) error { return nil })
	b.SetRecovery(func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	<-entered
	for _, method := range []string{"call", "handle", "binding", "ready", "source"} {
		if !errors.Is(b.Invoke(t.Context(), method, nil, nil), ErrKeeperUnavailable) {
			t.Fatal("half-recovered transport admitted shell operation", method)
		}
	}
	close(release)
	guard, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := b.WaitReady(guard); err != nil {
		t.Fatal(err)
	}
	if err := b.Invoke(guard, "call", nil, nil); err != nil {
		t.Fatal("completed recovery did not admit shell operations", err)
	}
	_ = peer.Close()
	if !errors.Is(b.Invoke(guard, "call", nil, nil), ErrKeeperUnavailable) {
		t.Fatal("closed channel retained public shell readiness")
	}
}

func TestRecoveryRequiresCapabilityAcknowledgementBeforePublication(t *testing.T) {
	hello := protocol.ShellLinkHello{Version: 2, Daemon: "keeper", Challenge: "fresh"}
	capEntered, release := make(chan struct{}), make(chan struct{})
	var capCalls atomic.Int32
	peer := &recoveryReviewBridge{reviewBridge{invoke: func(ctx context.Context, method string, _, out any) error {
		switch method {
		case "hello":
			*out.(*protocol.ShellLinkHello) = hello
		case "capabilities":
			if capCalls.Add(1) == 2 {
				close(capEntered)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
		return nil
	}}}
	manager := newReconnectingHostBridge(t.Context(), func(context.Context) (HostBridge, error) { return peer, nil }, func(ctx context.Context) bool { <-ctx.Done(); return false })
	var service *Service
	service = NewService(func(r RPC) error {
		if r.ShellHello == nil {
			return nil
		}
		welcome := protocol.ShellLinkWelcome{Version: 2, Runner: "runner", Daemon: hello.Daemon, Challenge: hello.Challenge, Desktop: desktopauth.Binding{DesktopID: "desktop", OwnerID: "owner", TokenID: "credential", Revision: 1, ExpiresAt: time.Now().Add(time.Hour)}}
		return service.AcceptShellLink(t.Context(), hello, &welcome)
	})
	service.SetCapabilities(protocol.WorkspaceCapabilities{}, protocol.WorkspaceCapabilities{})
	service.SetHostBridge(manager)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer func() { unblock(); _ = manager.Close() }()
	guard, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	select {
	case <-capEntered:
	case <-guard.Done():
		t.Fatal("recovery did not join capability acknowledgement after welcome")
	}
	if !errors.Is(manager.Invoke(guard, "call", nil, nil), ErrKeeperUnavailable) {
		t.Fatal("welcome published shell access before capability acknowledgement")
	}
	unblock()
	if err := manager.WaitReady(guard); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryRequestFencesShellsBeforeBackgroundWorkerCanRun(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	peer := &reconnectFixture{done: make(chan struct{}), notices: make(chan struct{})}
	manager := newReconnectingHostBridge(t.Context(), func(context.Context) (HostBridge, error) { return peer, nil }, func(ctx context.Context) bool { <-ctx.Done(); return false })
	manager.SetReceiver(func(context.Context, RPC) error { return nil })
	manager.SetCapabilityUpdate(func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	manager.SetRecovery(func(context.Context) error { return nil })
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer func() { unblock(); _ = manager.Close() }()
	guard, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := manager.WaitReady(guard); err != nil {
		t.Fatal(err)
	}
	manager.RequestCapabilities()
	select {
	case <-entered:
	case <-guard.Done():
		t.Fatal("worker never entered capability update")
	}
	manager.RequestRecovery()
	if !errors.Is(manager.Invoke(guard, "call", nil, nil), ErrKeeperUnavailable) {
		t.Fatal("pending recovery retained public shell access while worker was busy")
	}
	unblock()
	if err := manager.WaitReady(guard); err != nil {
		t.Fatal(err)
	}
}
