//go:build linux

package shellruntime

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestClusterShellAtomicAdmissionFreezeAndCleanupEvidence(t *testing.T) {
	base, b, g, key := fixture(t)
	service := NewService(func(RPC) error { return nil })
	allowed := false
	if err := service.RegisterWithAdmission(t.Context(), &sync.Mutex{}, b, base.command, func(context.Context) error { return service.ProveRecordedShellsEmpty(b.Session) }, func(_ context.Context, start func() error) error {
		if !allowed {
			return ErrClosed
		}
		return start()
	}); err != nil {
		t.Fatal(err)
	}
	b, _ = service.Binding(b.Session)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})
	if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "configure", Binding: b, Key: key.Public().(ed25519.PublicKey), Consent: base.command.Consent, Policy: base.command.Policy}).OK {
		t.Fatal("configuration")
	}
	if service.execute(t.Context(), RPC{SessionID: b.Session, Op: "open", Grant: g}).OK {
		t.Fatal("PTY bypassed workspace admission")
	}
	if err := service.ProveRecordedShellsEmpty(b.Session); err != nil {
		t.Fatal("refusal created writer", err)
	}
	allowed = true
	if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "open", Grant: g}).OK {
		t.Fatal("admitted PTY failed")
	}
	if err := service.ProveRecordedShellsEmpty(b.Session); err == nil {
		t.Fatal("live PTY fabricated empty evidence")
	}
	pidFile := filepath.Join(base.command.Identity.Cwd, "shell.pid")
	c := service.launches[b.Session].coordinator
	if err := c.Input(t.Context(), g, 1, []byte("echo $$ > "+pidFile+"\n")); err != nil {
		t.Fatal(err)
	}
	var pid int
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(pidFile)
		pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
		if pid > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("PTY did not run")
	}
	if err := service.ProcessPhase(t.Context(), b.Session, true); err != nil {
		t.Fatal(err)
	}
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil || !strings.Contains(string(status), "tracing stop") {
		t.Fatal("shell was not frozen", err)
	}
	if err = service.ProcessPhase(t.Context(), b.Session, false); err != nil {
		t.Fatal(err)
	}
	if _, err = c.CloseLaunch(t.Context(), c.launch(), "test"); err != nil {
		t.Fatal(err)
	}
	if err = service.ProveRecordedShellsEmpty(b.Session); err != nil {
		t.Fatal("exact cleanup not recognized", err)
	}
}

func TestClusterLostShellHelperCannotReconcileFromFreshMap(t *testing.T) {
	base, b, g, key := fixture(t)
	service := NewService(func(RPC) error { return nil })
	if err := service.Register(t.Context(), &sync.Mutex{}, b, base.command, func(context.Context) error { return service.ProveRecordedShellsEmpty(b.Session) }); err != nil {
		t.Fatal(err)
	}
	b, _ = service.Binding(b.Session)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})
	if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "configure", Binding: b, Key: key.Public().(ed25519.PublicKey), Consent: base.command.Consent, Policy: base.command.Policy}).OK {
		t.Fatal("configuration")
	}
	if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "open", Grant: g}).OK {
		t.Fatal("open")
	}
	c := service.launches[b.Session].coordinator
	peer := c.peers[g.Grant.ShellGeneration]
	if peer == nil {
		t.Fatal("missing owned helper")
	}
	if err := peer.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-peer.done:
	case <-time.After(3 * time.Second):
		t.Fatal("owned helper not reaped")
	}
	if _, err := c.CloseLaunch(t.Context(), c.launch(), "lost helper"); err == nil {
		t.Fatal("uncertain helper cleanup became fabricated receipt")
	}
	if service.ProveRecordedShellsEmpty(b.Session) == nil {
		t.Fatal("uncertain prior coordinator forgotten")
	}
}

func TestClusterCleanExpiredPTYDoesNotBlockWriterFreeze(t *testing.T) {
	base, b, g, key := fixture(t)
	service := NewService(func(RPC) error { return nil })
	if err := service.Register(t.Context(), &sync.Mutex{}, b, base.command, func(context.Context) error { return service.ProveRecordedShellsEmpty(b.Session) }); err != nil {
		t.Fatal(err)
	}
	b, _ = service.Binding(b.Session)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})
	if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "configure", Binding: b, Key: key.Public().(ed25519.PublicKey), Consent: base.command.Consent, Policy: base.command.Policy}).OK {
		t.Fatal("configuration")
	}
	if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "open", Grant: g}).OK {
		t.Fatal("open")
	}
	c := service.launches[b.Session].coordinator
	peer := c.peers[g.Grant.ShellGeneration]
	// The helper has independently closed/reaped its PTY (as on lease expiry),
	// before the coordinator processes its close notification.
	if err := peer.call(t.Context(), HelperFrame{Op: "close", Grant: g}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-peer.done:
	case <-time.After(3 * time.Second):
		t.Fatal("helper not reaped")
	}
	if err := service.ProcessPhase(t.Context(), b.Session, true); err != nil {
		t.Fatal("clean empty shell fenced agent freeze", err)
	}
	if err := service.ProcessPhase(t.Context(), b.Session, false); err != nil {
		t.Fatal("clean empty shell fenced thaw", err)
	}
	if _, err := c.CloseLaunch(t.Context(), c.launch(), "test"); err != nil {
		t.Fatal(err)
	}
}
