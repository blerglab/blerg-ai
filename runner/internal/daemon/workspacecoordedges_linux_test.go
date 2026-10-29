//go:build linux

package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestCoordPreparePersistsShellEligibility(t *testing.T) {
	useWorkspaceTestContext(t)
	fixture, _, _, sources := localWorkspaceFixture(t)
	for _, restricted := range []bool{false, true} {
		storage := &testWorkspaceStorage{root: t.TempDir()}
		service := &LocalWorkspaceService{Authority: &testWorkspaceAuthority{}, Supervisor: &testWorkspaceSupervisor{}, Storage: storage, CommitFinal: acknowledgeWorkspaceFinal, Results: func(context.Context, string, protocol.WorkspaceRuntimeBinding, []protocol.WorkspaceMemberResult) error {
			return nil
		}, Resolve: func(context.Context, protocol.SpawnSession) ([]WorkspaceSource, string, error) {
			return sources, "", nil
		}}
		id := "eligible"
		if restricted {
			id = "restricted"
		}
		w, err := service.Prepare(t.Context(), protocol.SpawnSession{SessionID: id, Sandbox: true, Kind: "agent", RestrictTools: restricted, Workspace: &fixture.state.Launch, WorkspaceCapabilities: protocol.WorkspaceCapabilities{ManifestVersion: 2, LinuxHost: true, Sandbox: true, Cluster: true}})
		if err != nil {
			t.Fatal(err)
		}
		saved, err := readPrivateWorkspace(w.record)
		if err != nil {
			t.Fatal(err)
		}
		if saved.ShellEligible == restricted {
			t.Fatal("trusted dispatch eligibility not durably registered")
		}
	}
}

func TestCoordDockerStopFrozenShellAndReconnectStopIntent(t *testing.T) {
	w, d, r, s := dockerCoordRig(t)
	r.open(t, "stop-frozen", 0)
	r.input(t, 1, "echo $$ > /workspace/stop-shell.pid; printf '\\122\\105\\101\\104\\131'\n")
	r.wait(t, "READY")
	if err := w.RequestStop(protocol.SessionEnded{Type: "session_ended", SessionID: w.state.Session}); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	err := d.Freeze(t.Context())
	w.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	// Reconnect with a durable stop intent must fence, never RecoverLaunch into
	// running. A paused helper cannot reply; physical namespace fencing is needed.
	next := w.state.SupervisorLaunch
	next.ConnectionID = "stopping-reconnect"
	if err = d.coordinator.rebindStopping(t.Context(), next); err != nil {
		t.Fatal("terminal stop reconnect required live recovery", err)
	}
	w.state.SupervisorLaunch = next
	d.SupervisorLaunch = next
	if err = w.save(); err != nil {
		t.Fatal(err)
	}
	if _, err = w.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if absent, e := d.Absent(t.Context()); e != nil || !absent {
		t.Fatal("uncertain shell cleanup released live runtime")
	}
	if err = s.Notify(t.Context(), next, "running"); err == nil {
		t.Fatal("stop-intent reconnect resurrected shell")
	}
	if _, err = d.coordinator.service.Binding(w.state.Session); err == nil {
		t.Fatal("stopped shell registration retained")
	}
}

func TestCoordDockerStopFrozenDirect(t *testing.T) {
	w, d, r, _ := dockerCoordRig(t)
	r.open(t, "stop-direct", 0)
	w.mu.Lock()
	err := d.Freeze(t.Context())
	w.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = w.Stop(t.Context()); err != nil {
		t.Fatal("stop could not fence uncertain helper namespace", err)
	}
	if absent, err := d.Absent(t.Context()); err != nil || !absent {
		t.Fatal("direct stop did not prove exact absence")
	}
}

func TestCoordDockerRetirementRetryPreservesRegistration(t *testing.T) {
	_, d, r, _ := dockerCoordRig(t)
	if err := d.StopWriters(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.failPhase.Store(true)
	if err := d.Remove(t.Context()); err == nil {
		t.Fatal("retirement failure silently released registration")
	}
	if r.service.Lifecycle(d.Session) == nil {
		t.Fatal("failed retirement lost cleanup registration")
	}
	r.failPhase.Store(false)
	if err := d.Remove(t.Context()); err != nil {
		t.Fatal("retirement retry could not join retained registration", err)
	}
	if r.service.Lifecycle(d.Session) != nil {
		t.Fatal("retirement retry retained registration after proof")
	}
}

func TestCoordMemberReaderAPIsCancelUnderLifecycleLock(t *testing.T) {
	w, _, _, _ := localWorkspaceFixture(t)
	for _, bound := range []bool{false, true} {
		w.mu.Lock()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		result := make(chan error, 1)
		go func() {
			operation := func(context.Context, WorkspaceContainer, string) error { return nil }
			if bound {
				result <- w.WithBoundRuntimeMember(ctx, protocol.WorkspaceRuntimeRef{}, protocol.WorkspaceMemberRef{}, operation)
			} else {
				result <- w.WithRuntimeMember(ctx, protocol.WorkspaceMemberRef{}, operation)
			}
		}()
		var err error
		select {
		case err = <-result:
		case <-time.After(time.Second):
			w.mu.Unlock()
			t.Fatal("canceled member API waited on lifecycle mutex")
		}
		w.mu.Unlock()
		if !errors.Is(err, context.Canceled) {
			t.Fatal("member API failed to report cancellation", err)
		}
	}
}
