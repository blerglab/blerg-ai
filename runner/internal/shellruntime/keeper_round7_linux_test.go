package shellruntime

import (
	"context"
	"errors"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
)

func TestWorkspaceAdoptionPendingDoesNotRebindOtherSessions(t *testing.T) {
	s := NewService(func(RPC) error { return nil })
	defer func() { _ = s.Shutdown(t.Context()) }()
	e, _ := round6HostEntry()
	s.launches[e.binding.Session] = e
	backend := &reviewBridge{invoke: func(_ context.Context, m string, in, out any) error {
		if m == "binding" || m == "workspace_binding" {
			*out.(*ownershell.Binding) = e.binding
			return nil
		}
		if m == "call" {
			return ErrAdoptionPending
		}
		return nil
	}}
	manager := &ReconnectingHostBridge{ctx: t.Context(), ready: make(chan struct{}), current: backend, usable: true, changed: make(chan struct{}), refresh: make(chan struct{}, 1)}
	s.SetHostBridge(manager)
	l := ownershell.SupervisorIdentity(e.binding, ownershell.SignedGrant{}).Launch
	if err := s.WorkspaceLifecycle(e.binding.Session).Notify(t.Context(), l, "syncing"); err != nil {
		t.Fatal(err)
	}
	if !manager.available() || len(manager.refresh) != 0 {
		t.Fatal("session adoption refusal suspended unrelated keeper sessions")
	}
	if err := manager.Invoke(t.Context(), "call", RPC{}, nil); !errors.Is(err, ErrAdoptionPending) {
		t.Fatal("policy refusal lost its type", err)
	}
	if !manager.available() || len(manager.refresh) != 0 {
		t.Fatal("policy refusal triggered global recovery")
	}
}

func TestWorkspaceManifestReconciliationRefusesChangedPinsAndUnprovenCustody(t *testing.T) {
	for _, scenario := range []string{"owner", "launch", "root", "generation", "unproven"} {
		t.Run(scenario, func(t *testing.T) {
			s := NewService(func(RPC) error { return nil })
			_, r := round6HostEntry()
			remote := r.Binding
			r.Binding.ManifestDigest = "new"
			switch scenario {
			case "owner":
				remote.Owner = "other"
			case "launch":
				remote.Launch = "other"
			case "root":
				remote.Root.Inode++
			case "generation":
				remote.RuntimeGeneration++
			}
			registered := false
			s.SetHostBridge(&reviewBridge{invoke: func(_ context.Context, m string, in, out any) error {
				switch m {
				case "binding", "workspace_binding":
					*out.(*ownershell.Binding) = remote
				case "retire_workspace":
					if scenario == "unproven" {
						return ErrClosed
					}
				case "register":
					registered = true
				}
				return nil
			}})
			if err := s.reconcileWorkspaceRegistration(t.Context(), r, false); err == nil || registered {
				t.Fatal("unproven custody was replaced", err, registered)
			}
		})
	}
}
