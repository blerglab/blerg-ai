package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

// An incompatible keeper will not come back by waiting, so membership names it
// as its own cause instead of the reconnect wait that offers shell cleanup.
func TestRound11IncompatibleKeeperHasItsOwnMembershipWait(t *testing.T) {
	f := membershipSetup(t)
	runtime := shellruntime.NewService(func(shellruntime.RPC) error { return nil })
	defer func() { _ = runtime.Shutdown(context.Background()) }()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	dialed := make(chan struct{}, 8)
	runtime.SetHostBridge(shellruntime.NewReconnectingHostBridge(ctx, func(context.Context) (shellruntime.HostBridge, error) {
		dialed <- struct{}{}
		return nil, shellruntime.ErrKeeperIncompatible
	}))
	// The bridge retries one dial at a time and records each failure before the
	// next attempt, so a second dial proves the first refusal was recorded.
	for range 2 {
		select {
		case <-dialed:
		case <-time.After(10 * time.Second): // failure guard only
			t.Fatal("keeper dial never retried")
		}
	}
	if !runtime.KeeperIncompatible() {
		t.Fatal("incompatible dial not reported")
	}
	registry := newWorkspaceSupervisors(runtime)
	f.service.Supervisor = registry
	f.service.membershipShells = nil
	f.w.state.HostWorkDir = f.w.state.Root
	f.w.authorize = func(context.Context) error { return nil }
	if err := f.w.save(); err != nil {
		t.Fatal(err)
	}
	if err := registry.registerHost(t.Context(), f.w.host); err != nil {
		t.Fatal(err)
	}
	f.service.ApplyMembership(t.Context(), f.msg)
	if f.server.installed != 0 || len(f.server.reports) != 1 || f.server.reports[0].Waiting != protocol.MembershipWaitKeeperIncompatible {
		t.Fatal("incompatible keeper reported as a reconnect wait", f.server.reports)
	}
}
