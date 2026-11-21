//go:build linux

package shellruntime

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellkeeper"
)

func TestAdoptionPendingSurvivesRealKeeperWire(t *testing.T) {
	k, b, _ := reviewRuntime(t)
	if err := b.Invoke(t.Context(), "rebind", nil, nil); err != nil {
		t.Fatal(err)
	}
	k.service.mu.Lock()
	k.service.launches["pending"] = &registered{retained: &retainedCoordinator{mu: new(sync.Mutex), children: map[string]*retainedChild{"shell": {}}}}
	k.service.mu.Unlock()
	t.Cleanup(func() { k.service.mu.Lock(); delete(k.service.launches, "pending"); k.service.mu.Unlock() })
	if err := b.Invoke(t.Context(), "ready", "pending", nil); !errors.Is(err, ErrAdoptionPending) {
		t.Fatal("adoption pending lost its typed wire cause", err)
	}
}

func TestMissingContainmentRefusesKeeperWithoutSweeping(t *testing.T) {
	swept := false
	err := prepareKeeperRuntime(func() (string, error) { return "", errors.New("fixture has no cgroup2") }, func() error { swept = true; return nil })
	if !errors.Is(err, shellkeeper.ErrUnsupported) || swept {
		t.Fatal("unsupported containment retried or swept without membership", err, swept)
	}
}

func TestWorkspacePhaseFencesShellAccessBeforeForwarding(t *testing.T) {
	for _, name := range []string{"fenced", "terminal", "syncing", "running"} {
		t.Run(name, func(t *testing.T) {
			s := NewService(func(RPC) error { return nil })
			e, _ := round6HostEntry()
			s.launches[e.binding.Session] = e
			switch name {
			case "fenced":
				e.fenced = true
			case "terminal":
				e.terminal = true
			case "syncing":
				e.phase = "syncing"
			}
			calls := 0
			s.SetHostBridge(&reviewBridge{invoke: func(_ context.Context, method string, in, out any) error {
				if method == "call" {
					calls++
					out.(*RPC).OK = true
				}
				return nil
			}})
			result := s.executeBridged(t.Context(), RPC{Version: 1, SessionID: e.binding.Session, Op: "input"})
			if result.OK != (name == "running") || calls != map[bool]int{false: 0, true: 1}[name == "running"] {
				t.Fatal("local workspace fence was bypassed", name, result.OK, calls)
			}
		})
	}
}

func round6HostEntry() (*registered, hostRegistration) {
	b := ownershell.Binding{Session: "session", Owner: "owner", Launch: "launch", Runtime: "host", RuntimeGeneration: 1, Root: protocol.WorkspaceRoot{Device: 1, Inode: 1}, ManifestDigest: "manifest", Live: true, Connection: "local-connection"}
	command := HostCommand("/proc/self/exe", Identity{Cwd: "/fixture"})
	command.PublicRoot = "/fixture"
	command.Root = b.Root
	e := &registered{mu: new(sync.Mutex), binding: b, command: command, phase: "running", reconcile: func(context.Context) error { return nil }}
	return e, hostRegistration{Binding: b, Identity: command.Identity, PublicRoot: command.PublicRoot, Root: command.Root}
}

func TestWorkspaceLocalReceiptsNeverProveKeeperCustody(t *testing.T) {
	for _, name := range []string{"connection", "owned_coordinator", "owned_retained", "owned_history", "retained_history", "operation", "terminal", "not_live", "paused", "invalid_phase", "valid"} {
		t.Run(name, func(t *testing.T) {
			s := NewService(func(RPC) error { return nil })
			s.SetHostBridge(RefusedHostBridge())
			e, _ := round6HostEntry()
			s.launches[e.binding.Session] = e
			l := ownershell.SupervisorIdentity(e.binding, ownershell.SignedGrant{}).Launch
			n := workspaceHostLifecycle{s, l}
			op := "recover"
			switch name {
			case "connection":
				n.launch.ConnectionID = "stale"
			case "owned_coordinator":
				e.coordinator = &Coordinator{}
			case "owned_retained":
				e.retained = &retainedCoordinator{}
			case "owned_history":
				e.containmentHistory = []*Coordinator{{}}
			case "retained_history":
				e.retainedHistory = []*retainedCoordinator{{}}
			case "operation":
				op = "open"
			case "terminal":
				e.terminal = true
			case "not_live":
				e.binding.Live = false
			case "paused":
				e.phase = "paused"
			case "invalid_phase":
				op = "phase"
			}
			r, err := n.call(t.Context(), n.launch, op, "")
			if (err == nil) != (name == "valid") {
				t.Fatal("agent receipt crossed custody/operation boundary", name, err)
			}
			if name == "valid" && (r.Cleanup == nil || r.Cleanup.ConnectionID != "local-connection") {
				t.Fatal("receipt lost daemon-local scope")
			}
			if err := n.AdmitShell(t.Context(), protocol.SupervisorShell{Launch: l}); err == nil {
				t.Fatal("agent lifecycle admitted a shell")
			}
		})
	}
}
