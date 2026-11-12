package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/discovery"
	"github.com/blerglab/blerg-ai/runner/internal/gitremote"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

var discoverySlots = make(chan struct{}, 2)
var membershipCloneSlots = make(chan struct{}, 2)

// resolveBaseSlots is its own capacity, never shared with discoverySlots (BLG-501 live e2e:
// a desktop running an active Explorer/file panel can keep both of discoverySlots' two slots
// busy with ordinary list/resolve traffic; a launch-time resolve_base call sharing that pool
// could be silently dropped — not refused, not logged, just never answered — competing with
// routine UI polling for a budget that was never sized with a launch in mind).
var resolveBaseSlots = make(chan struct{}, 2)

func workspaceOperationSlots(operation string) chan struct{} {
	switch operation {
	case "clone_addition":
		return membershipCloneSlots
	case "resolve_base":
		return resolveBaseSlots
	default:
		return discoverySlots
	}
}

type discoveryOperations struct {
	sync.Mutex
	cancel map[string]context.CancelFunc
}

func (m *Manager) handleDiscoverWorkspace(raw []byte) {
	if len(raw) > 256<<10 || m.discovery == nil {
		return
	}
	var req protocol.DiscoverWorkspace
	if json.Unmarshal(raw, &req) != nil || req.RequestID == "" || len(req.RequestID) > 100 {
		return
	}
	if req.Operation == "release" {
		if req.Manifest != nil {
			m.discovery.Release(*req.Manifest)
		}
		return
	}
	if req.Operation == "cancel" {
		m.discoveryOps.Lock()
		cancel := m.discoveryOps.cancel[req.RequestID]
		m.discoveryOps.Unlock()
		if cancel != nil {
			cancel()
		}
		return
	}
	client, ok := m.client.(*WSClient)
	if !ok {
		if req.Operation == "resolve_base" {
			failurelog.Default.Event("discover_workspace:no_link", slog.LevelWarn, "component", "daemon", "operation", req.Operation, "cause", "no_websocket_client")
		}
		return
	}
	conn, linkCtx := client.discoveryLink()
	if conn == nil || linkCtx == nil {
		if req.Operation == "resolve_base" {
			failurelog.Default.Event("discover_workspace:no_link", slog.LevelWarn, "component", "daemon", "operation", req.Operation, "cause", "discovery_link_unavailable")
		}
		return
	}
	slots := workspaceOperationSlots(req.Operation)
	select {
	case slots <- struct{}{}:
	default:
		// This request is dropped silently — never answered, never retried here — so the
		// caller's own wait times out with no signal at all unless this is logged now.
		// Every other drop in this function (release/cancel/malformed/no client/no link) is
		// an ordinary, frequent no-op this daemon has always had; a resolve_base drop is not:
		// it is on a launch's own critical path, with its own capacity (resolveBaseSlots)
		// specifically so this should be rare.
		if req.Operation == "resolve_base" {
			failurelog.Default.Event("discover_workspace:slots_full", slog.LevelWarn, "component", "daemon", "operation", req.Operation)
		}
		return
	}
	wait := 3 * time.Second
	if req.Operation == "clone_addition" {
		wait = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(linkCtx, wait)
	m.discoveryOps.Lock()
	if m.discoveryOps.cancel == nil {
		m.discoveryOps.cancel = map[string]context.CancelFunc{}
	}
	if m.discoveryOps.cancel[req.RequestID] != nil {
		m.discoveryOps.Unlock()
		cancel()
		<-slots
		return
	}
	m.discoveryOps.cancel[req.RequestID] = cancel
	m.discoveryOps.Unlock()
	go func() {
		defer func() {
			cancel()
			m.discoveryOps.Lock()
			delete(m.discoveryOps.cancel, req.RequestID)
			m.discoveryOps.Unlock()
			<-slots
		}()
		var res protocol.WorkspaceDiscoveryResult
		if req.Operation == "clone_addition" {
			res = m.cloneMembershipAddition(ctx, req)
		} else {
			res = m.discovery.Handle(ctx, req)
		}
		if req.Operation == "resolve_base" && !res.OK {
			// cause is resolveBase's own distinct, fixed label for exactly which check
			// refused (baseref.go, BLG-501 design §13) — never the same ambiguous
			// "invalid_selection" ErrorCode every refusal used to collapse to; res.Cause is
			// unset (falls back to ErrorCode) only for a refusal outside resolveBase itself
			// (e.g. the generic post-op staleness recheck in Handle, service.go).
			cause := res.Cause
			if cause == "" {
				cause = res.ErrorCode
			}
			failurelog.Default.Event("discover_workspace:resolve_base_refused", slog.LevelWarn, "component", "daemon", "operation", req.Operation, "cause", cause)
		}
		if ctx.Err() != nil {
			if req.Operation == "resolve_base" {
				failurelog.Default.Event("discover_workspace:resolve_base_timeout", slog.LevelWarn, "component", "daemon", "operation", req.Operation)
			}
			return
		}
		_ = client.sendDiscoveryReply(ctx, conn, res)
	}()
}

func (m *Manager) cloneMembershipAddition(ctx context.Context, req protocol.DiscoverWorkspace) protocol.WorkspaceDiscoveryResult {
	res := protocol.WorkspaceDiscoveryResult{Type: "workspace_discovery_result", RequestID: req.RequestID}
	ref, valid := gitremote.ParseAddition(req.GitURL)
	if !valid || req.Path != gitremote.AdditionFolder(ref) || (req.Runtime != "host" && req.Runtime != "sandbox") {
		res.ErrorCode = "invalid_selection"
		return res
	}
	root := m.reposRoot()
	anchor, err := m.discovery.CloneDestination(ctx, req.ParentLaunchGeneration)
	if err != nil {
		failurelog.Default.Event("membership_clone:destination", slog.LevelWarn, "component", "daemon", "operation", "membership_clone", "cause", failurelog.Cause(err))
		res.ErrorCode = "destination_unavailable"
		if errors.Is(err, discovery.ErrBindingPending) {
			res.ErrorCode = "binding_pending"
		}
		return res
	}
	if err := EnsureClonedTarget(ctx, root, CloneTarget{ConnectedHost: true, Provider: ref.Provider, FullName: ref.FullName(), Folder: path.Join(anchor, req.Path), Token: req.GitToken, Sandbox: req.Runtime == "sandbox", HostTokenAllowed: m.config.AllowHostCredentialClone}); err != nil {
		failurelog.Default.Event("membership_clone:prepare", slog.LevelWarn, "component", "daemon", "operation", "membership_clone", "cause", failurelog.Cause(err))
		res.ErrorCode = "local_storage"
		if errors.Is(err, errCloneEnvironment) {
			res.ErrorCode = "clone_environment"
		}
		if errors.Is(err, errConnectedCloneFailed) {
			res.ErrorCode = "clone_failed"
		}
		if errors.Is(err, errConnectedCloneDestinationExists) {
			res.ErrorCode = "destination_exists"
		}
		return res
	}
	req.Operation = "addition"
	req.Path = path.Join(anchor, req.Path)
	req.Members = []protocol.SelectionRef{{Path: req.Path}}
	return m.discovery.Handle(ctx, req)
}

// PinWorkspaceSelection supplies P2's preparer with descriptor-pinned private
// sources. Preparation/clone/runtime state and recovery are owned by P2.
func (m *Manager) PinWorkspaceSelection(ctx context.Context, manifest protocol.WorkspaceManifest) (*discovery.BoundPins, error) {
	return m.discovery.PinBound(ctx, manifest)
}

// ReleaseWorkspaceSelection drops a completed or aborted launch handoff. Keep
// active parents available for child selection. It does not remove runtime data.
func (m *Manager) ReleaseWorkspaceSelection(manifest protocol.WorkspaceManifest) {
	m.discovery.Release(manifest)
}

// SnapshotWorkspaceSelection returns only private recovery input for P2's
// authenticated private launch store. Never write it in an agent workspace.
func (m *Manager) SnapshotWorkspaceSelection(manifest protocol.WorkspaceManifest) ([]byte, error) {
	return m.discovery.SnapshotBound(manifest)
}
func (m *Manager) RestoreWorkspaceSelection(ctx context.Context, manifest protocol.WorkspaceManifest, privateState []byte) error {
	return m.discovery.RestoreBound(ctx, manifest, privateState)
}
