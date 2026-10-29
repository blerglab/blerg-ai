package daemon

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/discovery"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// NewIntegratedWorkspaceService installs the real selection and authenticated
// authority adapters. Unregistered P5 lifecycles refuse; no fabricated cleanup
// proof can release storage while the remaining runtime adapters are gated.
func NewIntegratedWorkspaceService(client *WSClient, manager func() *Manager) *LocalWorkspaceService {
	authority := NewWorkspaceAuthority(client)
	var mu sync.Mutex
	held := map[string]*discovery.BoundPins{}
	service := &LocalWorkspaceService{capabilityReady: func() bool { return manager() != nil }, Authority: authority, Supervisor: newWorkspaceSupervisors(client.OwnerShellRuntime()), TrustedLaunch: authority.TrustedLaunch, Results: authority.Results}
	service.Resolve = func(ctx context.Context, msg protocol.SpawnSession) ([]WorkspaceSource, string, error) {
		if msg.Workspace == nil || !protocol.WorkspaceRuntimeEnabled(msg.Workspace.Runtime, runtime.GOOS) || manager() == nil {
			return nil, "", errors.New("trusted workspace selection unavailable")
		}
		negotiate, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if err := client.WaitWorkspaceRuntime(negotiate, msg.Workspace.Runtime); err != nil {
			return nil, "", err
		}
		response, err := authority.call(ctx, protocol.LocalWorkspaceControl{SessionID: msg.SessionID, Operation: "manifest"})
		if err != nil || response.Manifest == nil || !reflect.DeepEqual(*response.Manifest, *msg.Workspace) {
			return nil, "", errors.New("trusted workspace selection changed")
		}
		if response.ParentSessionID != "" {
			return nil, response.ParentSessionID, nil
		}
		pins, err := manager().PinWorkspaceSelection(ctx, *msg.Workspace)
		if err != nil {
			return nil, "", err
		}
		mu.Lock()
		if previous := held[msg.SessionID]; previous != nil {
			previous.Close()
		}
		held[msg.SessionID] = pins
		mu.Unlock()
		sources := make([]WorkspaceSource, 0, len(pins.Sources))
		for _, source := range pins.Sources {
			root, e := source.Directory.Stat()
			if e != nil {
				return nil, "", e
			}
			common, e := source.CommonDirectory.Stat()
			if e != nil {
				return nil, "", e
			}
			sources = append(sources, WorkspaceSource{Member: source.Member, Path: source.CanonicalPath, CommonPath: source.CommonDirectoryPath, RootIdentity: workspaceRootIdentity(root), CommonIdentity: workspaceRootIdentity(common)})
		}
		return sources, "", nil
	}
	service.AuthorizeChild = func(ctx context.Context, parent, child string) error {
		_, err := authority.call(ctx, protocol.LocalWorkspaceControl{SessionID: child, ParentSessionID: parent, Operation: "authorize_child"})
		return err
	}
	service.CancelStatus = func(ctx context.Context, id string) error {
		if manager() == nil {
			return errors.New("workspace reader coordinator unavailable")
		}
		if err := manager().explorer.Suspend(ctx, id); err != nil {
			return err
		}
		if manager().agents != nil {
			if err := manager().agents.explorer.Suspend(ctx, id); err != nil {
				return err
			}
		}
		return ctx.Err()
	}
	service.CloseStatus = func(ctx context.Context, id string) error {
		if manager() == nil {
			return errors.New("workspace reader coordinator unavailable")
		}
		if err := manager().explorer.Release(ctx, id); err != nil {
			return err
		}
		if manager().agents != nil {
			return manager().agents.explorer.Release(ctx, id)
		}
		return ctx.Err()
	}
	service.ResumeStatus = func(ctx context.Context, id string) error {
		if manager() == nil {
			return errors.New("workspace reader coordinator unavailable")
		}
		if err := manager().explorer.Resume(id); err != nil {
			return err
		}
		if manager().agents != nil {
			return manager().agents.explorer.Resume(id)
		}
		return ctx.Err()
	}
	service.CommitFinal = func(ctx context.Context, l protocol.SupervisorLaunch, f WorkspaceFinalization) (WorkspaceFinalReceipt, error) {
		receipt, err := authority.CommitFinal(ctx, l, f)
		if err == nil {
			mu.Lock()
			pins := held[l.SessionID]
			delete(held, l.SessionID)
			mu.Unlock()
			if pins != nil {
				pins.Close()
				manager().ReleaseWorkspaceSelection(pins.Manifest)
			}
		}
		return receipt, err
	}
	return service
}
