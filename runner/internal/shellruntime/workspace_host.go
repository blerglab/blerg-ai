package shellruntime

import (
	"context"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// WorkspaceBinding is the daemon's pinned launch registration, independent of
// keeper availability. It is not shell authority or a keeper custody receipt.
func (s *Service) WorkspaceBinding(session string) (ownershell.Binding, error) {
	if !s.bridged(session) {
		return s.Binding(session)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.launches[session]
	if e == nil || e.binding.Runtime != "host" {
		return ownershell.Binding{}, bridgeFailure("workspace_binding", ErrClosed)
	}
	return e.binding, nil
}

func (s *Service) WorkspaceLifecycle(session string) protocol.RuntimeSupervisor {
	if !s.bridged(session) {
		return s.Lifecycle(session)
	}
	b, err := s.WorkspaceBinding(session)
	if err != nil {
		return nil
	}
	return workspaceHostLifecycle{s, ownershell.SupervisorIdentity(b, ownershell.SignedGrant{}).Launch}
}

type workspaceHostLifecycle struct {
	service *Service
	launch  protocol.SupervisorLaunch
}

// KeeperIncompatible reports a keeper that answers with a protocol this daemon
// cannot speak: waiting will not help, so the person needs its remedy.
func (s *Service) KeeperIncompatible() bool {
	managed, ok := s.bridge().(*ReconnectingHostBridge)
	return ok && managed.incompatible()
}

func (s *Service) hostBridgeReady() bool {
	b := s.bridge()
	if managed, ok := b.(*ReconnectingHostBridge); ok {
		return managed.available()
	}
	_, refused := b.(refusedHostBridge)
	return b != nil && !refused
}

func sameWorkspaceBinding(a, b ownershell.Binding) bool {
	return a.Owner == b.Owner && a.Session == b.Session && a.Launch == b.Launch && a.Runtime == b.Runtime && a.RuntimeGeneration == b.RuntimeGeneration && a.Root == b.Root && a.ManifestDigest == b.ManifestDigest
}

// Reconcile only this session. A changed manifest never adopts or implicitly
// retires live old custody: prove it empty before replacing its registration.
func (s *Service) reconcileWorkspaceRegistration(ctx context.Context, r hostRegistration, resume bool) error {
	var remote ownershell.Binding
	err := s.bridgeCall(ctx, "workspace_binding", r.Binding.Session, &remote)
	if err == nil && sameWorkspaceBinding(r.Binding, remote) && !resume {
		return nil
	}
	if err == nil && remote.ManifestDigest != r.Binding.ManifestDigest {
		old := r
		old.Binding.ManifestDigest = remote.ManifestDigest
		if !sameWorkspaceBinding(old.Binding, remote) {
			return bridgeFailure("workspace_reconcile_pins", ErrClosed)
		}
		var open bool
		if err := s.bridgeCall(ctx, "workspace_shells_open", old.Binding.Session, &open); err != nil {
			return err
		}
		if open {
			return ErrAdoptionPending
		}
		if err = s.bridgeCall(ctx, "retire_workspace", workspaceRetirement{Version: 1, Workspace: old.Binding.Session}, nil); err != nil {
			return err
		}
	}
	return s.bridgeCall(ctx, "register", r, nil)
}

// WorkspaceShellsOpen concerns agent membership, not a custody proof. An
// unavailable keeper fences shell access and defers fresh membership changes;
// it cannot veto agent turns or Stop.
func (s *Service) WorkspaceShellsOpen(session string) (open, known bool) {
	if !s.bridged(session) {
		return s.ProveRecordedShellsEmpty(session) != nil, true
	}
	if !s.hostBridgeReady() {
		return false, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, observed := s.hostObserved[session]
	return state.Open, observed
}

func (n workspaceHostLifecycle) call(ctx context.Context, l protocol.SupervisorLaunch, op string, phase protocol.SupervisorPhase) (RPC, error) {
	if l != n.launch || ctx.Err() != nil {
		return RPC{}, bridgeFailure("workspace_launch", ErrClosed)
	}
	s := n.service
	s.mu.Lock()
	e := s.launches[l.SessionID]
	s.mu.Unlock()
	if e == nil {
		return RPC{}, bridgeFailure("workspace_registration", ErrClosed)
	}
	// This executes only lifecycle operations against the daemon-owned empty
	// shadow. No shell can ever be opened through this interface.
	r := RPC{Version: 1, SessionID: l.SessionID, Op: op, Launch: l, Phase: phase}
	local, err := n.local(ctx, r, e)
	if err != nil {
		return local, err
	}
	s.queueHostControl(l.SessionID)
	return local, nil
}

func (n workspaceHostLifecycle) local(ctx context.Context, r RPC, e *registered) (RPC, error) {
	s := n.service
	e.opMu.Lock()
	defer e.opMu.Unlock()
	s.mu.Lock()
	current := s.launches[r.SessionID] == e && !e.retiring
	s.mu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if !current || ctx.Err() != nil || ownershell.SupervisorIdentity(e.binding, ownershell.SignedGrant{}).Launch != n.launch || e.binding.Runtime != "host" || e.coordinator != nil || e.retained != nil || len(e.containmentHistory) != 0 || len(e.retainedHistory) != 0 {
		return RPC{}, bridgeFailure("workspace_shadow_custody", ErrClosed)
	}
	result := RPC{OK: true}
	switch r.Op {
	case "freeze":
		e.phase = "syncing"
		e.hostPhaseDirty = true
		result.FrozenUntil = time.Now().Add(time.Duration(protocol.DefaultWorkspaceBudget().SyncSeconds) * time.Second).Unix()
	case "phase":
		if !r.Phase.Valid() {
			return RPC{}, bridgeFailure("workspace_phase", ErrClosed)
		}
		e.phase = r.Phase
		e.hostPhaseDirty = true
		if r.Phase != "running" && r.Phase != "syncing" {
			e.fenced = true
			if r.Phase != "paused" {
				e.terminal = true
			}
		}
	case "recover":
		if e.terminal || !e.binding.Live || e.phase != "running" && e.phase != "syncing" {
			return RPC{}, bridgeFailure("workspace_recovery", ErrClosed)
		}
		e.fenced = false
	case "close_launch":
		e.fenced = true
	default:
		return RPC{}, bridgeFailure("workspace_operation", ErrClosed)
	}
	if r.Op == "recover" || r.Op == "close_launch" {
		result.Cleanup = &protocol.SupervisorCleanup{Launch: n.launch, ContainmentEmpty: true, PTYsClosed: true, Reaped: true}
	}
	return result, nil
}

func (n workspaceHostLifecycle) AdmitShell(context.Context, protocol.SupervisorShell) error {
	_ = bridgeFailure("workspace_shell_admission", ErrKeeperUnavailable)
	return ErrKeeperUnavailable
}
func (n workspaceHostLifecycle) CloseShell(context.Context, protocol.SupervisorShell, string) (protocol.SupervisorCleanup, error) {
	_ = bridgeFailure("workspace_shell_receipt", ErrKeeperUnavailable)
	return protocol.SupervisorCleanup{}, ErrKeeperUnavailable
}
func (n workspaceHostLifecycle) Notify(ctx context.Context, l protocol.SupervisorLaunch, p protocol.SupervisorPhase) error {
	_, err := n.call(ctx, l, "phase", p)
	return err
}
func (n workspaceHostLifecycle) Freeze(ctx context.Context, l protocol.SupervisorLaunch) (time.Time, error) {
	r, err := n.call(ctx, l, "freeze", "")
	return time.Unix(r.FrozenUntil, 0), err
}
func (n workspaceHostLifecycle) CloseLaunch(ctx context.Context, l protocol.SupervisorLaunch, _ string) (protocol.SupervisorCleanup, error) {
	return n.proof(ctx, l, "close_launch")
}
func (n workspaceHostLifecycle) RecoverLaunch(ctx context.Context, l protocol.SupervisorLaunch) (protocol.SupervisorCleanup, error) {
	return n.proof(ctx, l, "recover")
}
func (n workspaceHostLifecycle) proof(ctx context.Context, l protocol.SupervisorLaunch, op string) (protocol.SupervisorCleanup, error) {
	r, err := n.call(ctx, l, op, "")
	if err != nil || r.Cleanup == nil {
		return protocol.SupervisorCleanup{}, ErrClosed
	}
	return *r.Cleanup, r.Cleanup.ValidateFor(l, "")
}
