package daemon

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/discovery"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

var discoverySlots = make(chan struct{}, 2)

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
		return
	}
	conn, linkCtx := client.discoveryLink()
	if conn == nil || linkCtx == nil {
		return
	}
	select {
	case discoverySlots <- struct{}{}:
	default:
		return
	}
	ctx, cancel := context.WithTimeout(linkCtx, 3*time.Second)
	m.discoveryOps.Lock()
	if m.discoveryOps.cancel == nil {
		m.discoveryOps.cancel = map[string]context.CancelFunc{}
	}
	if m.discoveryOps.cancel[req.RequestID] != nil {
		m.discoveryOps.Unlock()
		cancel()
		<-discoverySlots
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
			<-discoverySlots
		}()
		res := m.discovery.Handle(ctx, req)
		if ctx.Err() != nil {
			return
		}
		_ = client.sendDiscoveryReply(ctx, conn, res)
	}()
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
