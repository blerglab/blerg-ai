//go:build linux

package shellruntime

import (
	"context"
	"crypto/ed25519"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os"
	"sync"
	"testing"
	"time"
)

func TestCloseBeforeConfigureCannotStartARevokedPendingShell(t *testing.T) {
	base, b, g, key := fixture(t)
	service := NewService(func(RPC) error { return nil })
	if err := service.Register(t.Context(), new(sync.Mutex), b, base.command, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	b, _ = service.Binding(b.Session)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})
	closeReply := service.execute(t.Context(), RPC{SessionID: b.Session, Op: "close", Grant: g})
	if !closeReply.OK || closeReply.Cleanup == nil || closeReply.Cleanup.ValidateFor(ownershell.SupervisorIdentity(b, g).Launch, g.Grant.ShellGeneration) != nil {
		t.Fatal("pending admission close proof missing")
	}
	config := RPC{SessionID: b.Session, Op: "configure", Binding: b, Key: key.Public().(ed25519.PublicKey), Consent: base.command.Consent, Policy: protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}}
	if !service.execute(t.Context(), config).OK {
		t.Fatal("valid configuration refused")
	}
	if service.execute(t.Context(), RPC{SessionID: b.Session, Op: "open", Grant: g}).OK {
		t.Fatal("revoked pending grant started a later helper")
	}
	config.Policy.Revision = 2
	if !service.execute(t.Context(), config).OK {
		t.Fatal("fresh revision refused")
	}
	config.Policy.Revision = 1
	if service.execute(t.Context(), config).OK {
		t.Fatal("stale policy configuration downgraded runtime")
	}
}
func TestServiceRequiresRealReconciliationAndFreshConnection(t *testing.T) {
	base, b, _, _ := fixture(t)
	service := NewService(func(RPC) error { return nil })
	if err := service.Register(t.Context(), new(sync.Mutex), b, base.command, nil); err == nil {
		t.Fatal("orphan reconciliation optional")
	}
	if err := service.Register(t.Context(), new(sync.Mutex), b, base.command, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	before, err := service.Binding(b.Session)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Rebind(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := service.Binding(b.Session)
	if err != nil {
		t.Fatal(err)
	}
	if after.Connection == before.Connection || after.Audience == before.Audience || after.HostConsentValid || after.Consent != "" {
		t.Fatal("connection replacement retained grants or consent")
	}
	lifecycle := service.Lifecycle(b.Session)
	if lifecycle == nil {
		t.Fatal("unconfigured lifecycle missing")
	}
	proof, err := lifecycle.RecoverLaunch(t.Context(), ownershell.SupervisorIdentity(after, ownershell.SignedGrant{}).Launch)
	if err != nil || proof.ValidateFor(ownershell.SupervisorIdentity(after, ownershell.SignedGrant{}).Launch, "") != nil {
		t.Fatal("prepared empty launch recovery unavailable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err = service.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPreconfigurationLaunchFenceSurvivesRunningAndConfigure(t *testing.T) {
	for _, phase := range []string{"close", "paused", "stopping", "ended", "discarded", "replacing", "failed_cleanup"} {
		t.Run(phase, func(t *testing.T) {
			base, b, _, key := fixture(t)
			service := NewService(func(RPC) error { return nil })
			fail := false
			if err := service.Register(t.Context(), new(sync.Mutex), b, base.command, func(context.Context) error {
				if fail {
					return ErrClosed
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			b, _ = service.Binding(b.Session)
			t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
			l := ownershell.SupervisorIdentity(b, ownershell.SignedGrant{}).Launch
			native := service.Lifecycle(b.Session)
			if phase == "close" || phase == "failed_cleanup" {
				fail = phase == "failed_cleanup"
				proof, err := native.CloseLaunch(t.Context(), l, "storage_settlement")
				if !fail && (err != nil || proof.ValidateFor(l, "") != nil) {
					t.Fatal("missing exact all-launch proof", err)
				}
				if fail && err == nil {
					t.Fatal("uncertain cleanup acknowledged")
				}
			} else if err := native.Notify(t.Context(), l, protocol.SupervisorPhase(phase)); err != nil {
				t.Fatal(err)
			}
			_ = native.Notify(t.Context(), l, "running")
			config := RPC{SessionID: b.Session, Op: "configure", Binding: b, Key: key.Public().(ed25519.PublicKey), Consent: base.command.Consent, Policy: base.command.Policy}
			if service.execute(t.Context(), config).OK {
				t.Fatal("configuration cleared the pre-helper lifecycle fence")
			}
			fail = false
			proof, err := native.RecoverLaunch(t.Context(), l)
			terminal := phase == "stopping" || phase == "ended" || phase == "discarded" || phase == "replacing"
			if terminal {
				if err == nil || service.execute(t.Context(), config).OK {
					t.Fatal("terminal incarnation resurrected")
				}
			} else {
				if err != nil || proof.ValidateFor(l, "") != nil {
					t.Fatal("verified live recovery refused", err)
				}
				if !service.execute(t.Context(), config).OK {
					t.Fatal("explicit recovery did not permit configuration")
				}
			}
		})
	}
}

func TestHostIdentityTransitionRotatesConsentEvenAfterLabelsReturn(t *testing.T) {
	base, b, _, key := fixture(t)
	service := NewService(func(RPC) error { return nil })
	if err := service.Register(t.Context(), new(sync.Mutex), b, base.command, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	first := service.execute(t.Context(), RPC{SessionID: b.Session, Op: "describe"})
	actual := base.command.Identity
	service.resolveIdentity = func(string) (Identity, error) { changed := actual; changed.UID++; return changed, nil }
	middle := service.execute(t.Context(), RPC{SessionID: b.Session, Op: "describe"})
	service.resolveIdentity = ResolveIdentity
	last := service.execute(t.Context(), RPC{SessionID: b.Session, Op: "describe"})
	if !first.OK || !middle.OK || !last.OK || first.Identity != last.Identity {
		t.Fatal("identity observation failed")
	}
	if first.Binding.Connection == middle.Binding.Connection || middle.Binding.Connection == last.Binding.Connection || first.Binding.Connection == last.Binding.Connection {
		t.Fatal("old consent incarnation revived after identity labels returned")
	}
	config := RPC{SessionID: b.Session, Op: "configure", Binding: first.Binding, Key: key.Public().(ed25519.PublicKey), Consent: base.command.Consent, Policy: base.command.Policy}
	if service.execute(t.Context(), config).OK {
		t.Fatal("old accepted consent configured changed launch")
	}
	l := ownershell.SupervisorIdentity(last.Binding, ownershell.SignedGrant{}).Launch
	if _, err := service.Lifecycle(b.Session).RecoverLaunch(t.Context(), l); err != nil {
		t.Fatal(err)
	}
	if service.execute(t.Context(), config).OK {
		t.Fatal("cleanup recovery restored historical consent")
	}
}

func TestRunningHelperIdentityChangeCannotRestoreConsentAfterRecovery(t *testing.T) {
	base, b, g, key := fixture(t)
	command := base.command
	command.Args = []string{"GORACE=atexit_sleep_ms=0", os.Args[0], "-test.run=^TestRuntimeHelperProcess$", "--", "p5-identity-helper"}
	service := NewService(func(RPC) error { return nil })
	if err := service.Register(t.Context(), new(sync.Mutex), b, command, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Shutdown(context.Background()) })
	b, _ = service.Binding(b.Session)
	config := RPC{SessionID: b.Session, Op: "configure", Binding: b, Key: key.Public().(ed25519.PublicKey), Consent: command.Consent, Policy: command.Policy}
	if !service.execute(t.Context(), config).OK || !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "open", Grant: g}).OK {
		t.Fatal("real monitored helper refused")
	}
	entry := service.launches[b.Session]
	entry.coordinator.sessionMu.Lock()
	peer := entry.coordinator.peers[g.Grant.ShellGeneration]
	entry.coordinator.sessionMu.Unlock()
	select {
	case <-peer.done:
	case <-time.After(5 * time.Second):
		t.Fatal("helper identity monitor did not stop and reap")
	}
	// The daemon now sees A again; only the running helper observed transient B.
	description := service.execute(t.Context(), RPC{SessionID: b.Session, Op: "describe"})
	if !description.OK || description.Binding.Connection == b.Connection {
		t.Fatal("running-helper identity transition retained historical consent incarnation")
	}
	l := ownershell.SupervisorIdentity(description.Binding, ownershell.SignedGrant{}).Launch
	if _, err := service.Lifecycle(b.Session).RecoverLaunch(t.Context(), l); err != nil {
		t.Fatal(err)
	}
	if service.execute(t.Context(), config).OK {
		t.Fatal("old consent revived after helper cleanup recovery")
	}
}

func TestServiceRequiresNegotiatedCapabilitiesForShellAdmission(t *testing.T) {
	base, b, g, key := fixture(t)
	service := NewService(func(RPC) error { return nil })
	service.SetCapabilities(protocol.WorkspaceCapabilities{}, protocol.WorkspaceCapabilities{})
	if err := service.Register(t.Context(), new(sync.Mutex), b, base.command, func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = service.Shutdown(ctx)
	})
	described := service.execute(t.Context(), RPC{SessionID: b.Session, Op: "describe"})
	if !described.OK || described.Binding.ServerCapabilities.ShellVersion != 0 || described.Binding.RuntimeCapabilities.ShellVersion != 0 {
		t.Fatal("registration invented handshake support", described.Binding)
	}
	full := protocol.WorkspaceCapabilities{ManifestVersion: 2, ShellVersion: 1}
	for _, caps := range []protocol.WorkspaceCapabilities{{}, {ManifestVersion: 2}, {ManifestVersion: 3, ShellVersion: 1}} {
		service.SetCapabilities(caps, full)
		binding := service.execute(t.Context(), RPC{SessionID: b.Session, Op: "describe"}).Binding
		if service.execute(t.Context(), RPC{SessionID: b.Session, Op: "configure", Binding: binding, Key: key.Public().(ed25519.PublicKey), Policy: protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}, Consent: base.command.Consent}).OK {
			t.Fatal("unnegotiated shell configured", caps)
		}
	}
	service.SetCapabilities(full, full)
	binding := service.execute(t.Context(), RPC{SessionID: b.Session, Op: "describe"}).Binding
	if !service.execute(t.Context(), RPC{SessionID: b.Session, Op: "configure", Binding: binding, Key: key.Public().(ed25519.PublicKey), Policy: protocol.ShellPolicy{Version: 1, Revision: 1, Enabled: true}, Consent: base.command.Consent}).OK {
		t.Fatal("negotiated shell refused")
	}
	service.SetCapabilities(protocol.WorkspaceCapabilities{}, protocol.WorkspaceCapabilities{})
	if service.execute(t.Context(), RPC{SessionID: b.Session, Op: "open", Grant: g}).OK {
		t.Fatal("capability loss admitted a shell")
	}
	launch := ownershell.SupervisorIdentity(b, ownershell.SignedGrant{}).Launch
	reply := service.execute(t.Context(), RPC{SessionID: b.Session, Op: "close_launch", Launch: launch})
	if !reply.OK || reply.Cleanup == nil || reply.Cleanup.ValidateFor(launch, "") != nil {
		t.Fatal("capability loss revoked exact lifecycle cleanup", reply)
	}
}
