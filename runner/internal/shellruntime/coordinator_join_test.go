//go:build linux

package shellruntime

import (
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestLaunchCleanupJoinsTransportOutput(t *testing.T) {
	base, b, g, key := fixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	service := NewService(func(v RPC) error {
		if v.Type == "owner_shell_output" {
			once.Do(func() { close(entered) })
			<-release
		}
		return nil
	})
	defer close(release)
	if err := service.Register(t.Context(), new(sync.Mutex), b, base.command, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	b, _ = service.Binding(b.Session)
	config := RPC{SessionID: b.Session, Op: "configure", Binding: b, Key: key.Public().(ed25519.PublicKey), Consent: base.command.Consent, Policy: protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}}
	if !service.execute(t.Context(), config).OK {
		t.Fatal("configure")
	}
	if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "open", Grant: g}).OK {
		t.Fatal("open")
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("output did not enter transport")
	}
	result := make(chan RPC, 1)
	go func() {
		result <- service.execute(t.Context(), RPC{SessionID: b.Session, Op: "close_launch", Launch: ownershell.SupervisorIdentity(b, g).Launch})
	}()
	// A caller deadline permits refusal; it never permits an early successful proof.
	select {
	case reply := <-result:
		if reply.OK {
			t.Fatal("cleanup proof preceded output join")
		}
	case <-time.After(100 * time.Millisecond):
	}
	// The blocked send is released by the deferred close, then Shutdown joins it.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := service.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
}

func TestCleanupJoinsOwnedHelperProcessAndOutput(t *testing.T) {
	base, b, g, key := fixture(t)
	command := base.command
	command.Args = append([]string{"-c", `"$@"; sleep 0.2`, "helper-join", command.Executable}, command.Args...)
	command.Executable = "/bin/sh"
	command.Recovery = func(context.Context, protocol.SupervisorLaunch) error { return nil }
	c, err := NewCoordinator(new(sync.Mutex), b, command, key.Public().(ed25519.PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.RecoverLaunch(t.Context(), c.launch()); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
	c.sessionMu.Lock()
	peer := c.peers[g.Grant.ShellGeneration]
	c.sessionMu.Unlock()
	if _, err = c.CloseLaunch(t.Context(), c.launch(), "join"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-peer.done:
	default:
		t.Fatal("cleanup preceded helper process join")
	}
	select {
	case <-peer.forwardDone:
	default:
		t.Fatal("cleanup preceded helper output join")
	}
}

func TestFrozenDeadlineUsesOnlyHelperAcknowledgedLease(t *testing.T) {
	c, b, g, key := fixture(t)
	now := time.Now().Unix()
	g.Grant.IssuedAt = now - 10
	g.Grant.ExpiresAt = now + 20
	g = ownershell.SignGrant(key, g.Grant)
	if _, err := c.Open(t.Context(), b, g, key.Public().(ed25519.PublicKey)); err != nil {
		t.Fatal(err)
	}
	if err := c.Notify(t.Context(), c.launch(), "syncing"); err != nil {
		t.Fatal(err)
	}
	renewed := g
	renewed.Grant.IssuedAt = now
	renewed.Grant.ExpiresAt = now + 30
	renewed = ownershell.SignGrant(key, renewed.Grant)
	if err := c.Renew(t.Context(), renewed); err != nil {
		t.Fatal(err)
	}
	if got := c.FrozenUntil().Unix(); got != g.Grant.ExpiresAt {
		t.Fatal("frozen lease extended without helper acknowledgment")
	}
	if err := c.Notify(t.Context(), c.launch(), "running"); err != nil {
		t.Fatal(err)
	}
	if got := c.FrozenUntil().Unix(); got != renewed.Grant.ExpiresAt {
		t.Fatal("acknowledged renewal not observed after thaw")
	}
}

func TestCleanupJoinCannotForgetNewLifecycleEpoch(t *testing.T) {
	c, _, _, _ := fixture(t)
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer writer.Close()
	exited, forward := make(chan struct{}), make(chan struct{})
	close(exited)
	peer := &helperPeer{done: exited, forwardDone: forward, input: writer}
	c.sessionMu.Lock()
	c.command.Recovery = func(context.Context, protocol.SupervisorLaunch) error { return nil }
	c.peers["old"] = peer
	c.reserved["old"] = true
	c.sessionMu.Unlock()
	result := make(chan error, 1)
	go func() { _, err := c.CloseLaunch(t.Context(), c.launch(), "old"); result <- err }()
	// The closed input is the barrier after physical recovery and before the
	// output join. No scheduler sleep determines which lifecycle owns the maps.
	deadline := time.Now().Add(time.Second)
	for {
		_, err = writer.Write(nil)
		if errors.Is(err, os.ErrClosed) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cleanup did not enter join")
		}
		runtime.Gosched()
	}
	c.sessionMu.Lock()
	c.epoch++
	c.reserved["new"] = true
	c.sessionMu.Unlock()
	close(forward)
	if err = <-result; err == nil {
		t.Fatal("stale cleanup acknowledged new epoch")
	}
	c.sessionMu.Lock()
	preserved := c.reserved["new"]
	c.sessionMu.Unlock()
	if !preserved {
		t.Fatal("old reader join forgot new lifecycle state")
	}
}

func TestRetireJoinsPendingConfiguration(t *testing.T) {
	base, b, _, key := fixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	service := NewService(func(RPC) error { return nil })
	if err := service.Register(t.Context(), new(sync.Mutex), b, base.command, func(context.Context) error {
		if first.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b, _ = service.Binding(b.Session)
	configured := make(chan struct{})
	go func() {
		service.execute(t.Context(), RPC{SessionID: b.Session, Op: "configure", Binding: b, Key: key.Public().(ed25519.PublicKey), Consent: base.command.Consent, Policy: base.command.Policy})
		close(configured)
	}()
	<-entered
	retired := make(chan error, 1)
	go func() {
		_, err := service.Retire(t.Context(), ownershell.SupervisorIdentity(b, ownershell.SignedGrant{}).Launch)
		retired <- err
	}()
	early := false
	select {
	case <-retired:
		early = true
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-configured
	if early {
		t.Fatal("retirement proof preceded pending configuration join")
	}
	if err := <-retired; err != nil {
		t.Fatal(err)
	}
	if service.Lifecycle(b.Session) != nil {
		t.Fatal("retirement retained registration")
	}
}

func TestFreezeWithoutShellPreservesWorkspaceCopyBudget(t *testing.T) {
	base, b, _, _ := fixture(t)
	minimum := time.Now().Add(time.Duration(protocol.DefaultWorkspaceBudget().SyncSeconds-1) * time.Second)
	if base.FrozenUntil().Before(minimum) {
		t.Fatal("empty coordinator shortened workspace copy budget")
	}
	service := NewService(func(RPC) error { return nil })
	if err := service.Register(t.Context(), new(sync.Mutex), b, base.command, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	freezer := service.Lifecycle(b.Session).(interface {
		Freeze(context.Context, protocol.SupervisorLaunch) (time.Time, error)
	})
	until, err := freezer.Freeze(t.Context(), ownershell.SupervisorIdentity(b, ownershell.SignedGrant{}).Launch)
	if err != nil || until.Before(minimum) {
		t.Fatal("unconfigured lifecycle shortened workspace copy budget", err)
	}
	if err = service.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}
