package ownershell

import (
	"context"
	"crypto/ed25519"
	"sync"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// LifecycleBridge connects the byte transport to the shared P0b lifecycle. The
// peer is the runtime coordinator, using its one session mutex for admission,
// phase changes and process creation. The transport must not acknowledge Close
// until the peer has supplied an exact no-writers proof.
//
// A byte driver alone is never eligible for production enablement. Runtime peers
// must implement containment, local grant expiry and recovery before this bridge
// is provisioned over their authenticated connection.
// AdmittedSupervisor must recheck the reservation and current phase under the
// SAME P0b coordinator mutex immediately before process creation. Admit alone
// is not permission to exec after a concurrent freeze/stop.
type AdmittedSupervisor interface {
	Supervisor
	OpenReserved(context.Context, protocol.SupervisorShell, Binding, SignedGrant, ed25519.PublicKey) (<-chan []byte, error)
}
type LifecycleBridge struct {
	AdmittedSupervisor
	Peer   protocol.RuntimeSupervisor
	mu     sync.Mutex
	shells map[string]protocol.SupervisorShell
}

func NewLifecycleBridge(driver AdmittedSupervisor, peer protocol.RuntimeSupervisor) *LifecycleBridge {
	return &LifecycleBridge{AdmittedSupervisor: driver, Peer: peer, shells: make(map[string]protocol.SupervisorShell)}
}
func SupervisorIdentity(b Binding, g SignedGrant) protocol.SupervisorShell {
	return protocol.SupervisorShell{Launch: protocol.SupervisorLaunch{SessionID: b.Session, LaunchGeneration: b.Launch, RuntimeGeneration: b.RuntimeGeneration, Runtime: b.Runtime, ConnectionID: b.Connection}, ShellGeneration: g.Grant.ShellGeneration}
}

func (b *LifecycleBridge) Open(ctx context.Context, binding Binding, g SignedGrant, key ed25519.PublicKey) (<-chan []byte, error) {
	if err := b.Reserve(ctx, binding, g); err != nil {
		return nil, err
	}
	return b.OpenReserved(ctx, SupervisorIdentity(binding, g), binding, g, key)
}

// Reserve is called by an authenticated runtime transport before starting any
// writers. The runtime must retain this reservation through Open under its
// coordinator mutex; Notify(non-running) invalidates pending reservations too.
func (b *LifecycleBridge) Reserve(ctx context.Context, binding Binding, g SignedGrant) error {
	shell := SupervisorIdentity(binding, g)
	if b.Peer == nil || b.AdmittedSupervisor == nil || shell.Validate() != nil {
		return ErrDenied
	}
	if err := b.Peer.AdmitShell(ctx, shell); err != nil {
		return err
	}
	b.mu.Lock()
	b.shells[g.Grant.ShellGeneration] = shell
	b.mu.Unlock()
	return nil
}
func (b *LifecycleBridge) Close(ctx context.Context, g SignedGrant) error {
	b.mu.Lock()
	shell, ok := b.shells[g.Grant.ShellGeneration]
	b.mu.Unlock()
	if !ok || b.Peer == nil {
		return ErrDenied
	}
	proof, err := b.Peer.CloseShell(ctx, shell, "owner_shell_closed")
	if err != nil {
		return err
	}
	if err = proof.ValidateFor(shell.Launch, shell.ShellGeneration); err != nil {
		return err
	}
	b.mu.Lock()
	delete(b.shells, g.Grant.ShellGeneration)
	b.mu.Unlock()
	return nil
}

// CloseLaunch is storage-release authority only for shell writers. The caller
// must additionally obtain P2/P3's agent/runtime cleanup proof.
func (b *LifecycleBridge) CloseLaunch(ctx context.Context, launch protocol.SupervisorLaunch, reason string) (protocol.SupervisorCleanup, error) {
	if b.Peer == nil || launch.Validate() != nil {
		return protocol.SupervisorCleanup{}, ErrDenied
	}
	proof, err := b.Peer.CloseLaunch(ctx, launch, reason)
	if err == nil {
		err = proof.ValidateFor(launch, "")
	}
	return proof, err
}
func (b *LifecycleBridge) RecoverLaunch(ctx context.Context, launch protocol.SupervisorLaunch) (protocol.SupervisorCleanup, error) {
	if b.Peer == nil || launch.Validate() != nil {
		return protocol.SupervisorCleanup{}, ErrDenied
	}
	proof, err := b.Peer.RecoverLaunch(ctx, launch)
	if err == nil {
		err = proof.ValidateFor(launch, "")
	}
	return proof, err
}

// Recovered clears only an uncertain manager fence for the exact current live
// binding after ALL orphan/visible supervisors have been reconciled. It never
// restores a handle, attachment, grant or login.
func (m *Manager) Recovered(ctx context.Context, a Actor, session string, proof protocol.SupervisorCleanup) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.resolver == nil {
		return ErrDenied
	}
	epoch := m.identityStamp(a, Binding{Session: session})
	runtimeEpoch := m.runtimeEpoch[proof.Launch]
	var binding Binding
	err := m.external(ctx, func(call context.Context) error {
		var e error
		binding, e = m.resolver.Resolve(call, session)
		return e
	})
	if err != nil || epoch != m.identityStamp(a, binding) || runtimeEpoch != m.runtimeEpoch[proof.Launch] || binding.Session != session || !binding.permits(a) {
		return ErrDenied
	}
	expected := SupervisorIdentity(binding, SignedGrant{}).Launch
	if err = proof.ValidateFor(expected, ""); err != nil {
		return err
	}
	var policy protocol.ShellPolicy
	if m.authority == nil {
		return ErrDenied
	}
	err = m.external(ctx, func(call context.Context) error { var e error; policy, e = m.authority.Authorize(call, a); return e })
	if err != nil || epoch != m.identityStamp(a, binding) || runtimeEpoch != m.runtimeEpoch[proof.Launch] || m.pending(a, binding) || !m.available || !m.policy.Enabled || !policy.Enabled || policy.Revision != m.policy.Revision {
		return ErrDenied
	}
	key := binding.Session + "/" + binding.Launch
	if m.cleanupPending[key] != 0 {
		return ErrDenied
	}
	for _, shell := range m.shells {
		if shell.binding.Session == session {
			return ErrDenied
		}
	}
	delete(m.runtimeFenced, expected)
	delete(m.fenced, expected)
	delete(m.retryAfter, expected)
	return nil
}

func (b *LifecycleBridge) Failure(g SignedGrant) error {
	if diagnostic, ok := b.AdmittedSupervisor.(RuntimeDiagnostic); ok {
		return diagnostic.Failure(g)
	}
	return nil
}
