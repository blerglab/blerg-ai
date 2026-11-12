package shellruntime

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// HostBridge is local install authority, never a browser or runtime RPC field.
// Other runtimes remain in their existing isolated supervisor paths.
type HostBridge interface {
	Invoke(context.Context, string, any, any) error
	Metadata(string, any, any) error
	HostSession(string) bool
	SetReceiver(func(context.Context, RPC) error)
	Close() error
}
type refusedHostBridge struct{}

func (refusedHostBridge) Invoke(context.Context, string, any, any) error {
	_ = bridgeFailure("unavailable", ErrKeeperUnavailable)
	return ErrKeeperUnavailable
}
func (refusedHostBridge) HostSession(string) bool { return false }
func (refusedHostBridge) Metadata(string, any, any) error {
	_ = bridgeFailure("unavailable", ErrKeeperUnavailable)
	return ErrKeeperUnavailable
}
func (refusedHostBridge) SetReceiver(func(context.Context, RPC) error) {}
func (refusedHostBridge) Close() error                                 { return nil }
func RefusedHostBridge() HostBridge                                    { return refusedHostBridge{} }

func bridgeFailure(stage string, err error) error {
	failurelog.Default.Event("shell_keeper.bridge_"+stage, slog.LevelError, "component", "shell_keeper", "stage", stage, "cause", failurelog.Cause(err))
	return ErrClosed
}

type hostRegistration struct {
	Binding    ownershell.Binding     `json:"binding"`
	Identity   Identity               `json:"identity"`
	Root       protocol.WorkspaceRoot `json:"root"`
	PublicRoot string                 `json:"public_root"`
}
type hostCapabilities struct {
	Server, Runtime protocol.WorkspaceCapabilities
}
type hostLink struct {
	Hello   protocol.ShellLinkHello
	Welcome *protocol.ShellLinkWelcome
}
type hostManifest struct{ Session, Old, Next string }

func (s *Service) SetHostBridge(bridge HostBridge) {
	s.mu.Lock()
	s.hostBridge = bridge
	s.mu.Unlock()
	bridge.SetReceiver(func(ctx context.Context, r RPC) error {
		if r.Type == "owner_shell_output" {
			return s.outputSender()(ctx, r)
		}
		s.deliver(r)
		return nil
	})
	if recovering, ok := bridge.(interface {
		SetRecovery(func(context.Context) error)
	}); ok {
		if managed, ok := bridge.(*ReconnectingHostBridge); ok {
			managed.SetCapabilityUpdate(s.syncHostCapabilities)
			managed.SetObserver(func() uint64 { s.mu.Lock(); defer s.mu.Unlock(); return s.hostObservationRevision }, func(observed []hostObservation, started uint64) {
				s.mu.Lock()
				defer s.mu.Unlock()
				if started != s.hostObservationRevision {
					return
				}
				s.hostObserved = map[string]hostObservation{}
				for _, entry := range observed {
					s.hostObserved[entry.Workspace] = entry
				}
				s.signalHostChangeLocked()
			})
		}
		recovering.SetRecovery(s.recoverHostBridge)
	}
	s.mu.Lock()
	if len(s.hostIntents) > 0 {
		s.wakeHostWorkLocked()
	}
	s.mu.Unlock()
}

func (s *Service) syncHostCapabilities(ctx context.Context) error {
	if caps := s.capabilities.Load(); caps != nil {
		return s.bridgeCall(ctx, "capabilities", hostCapabilities{caps.server, caps.runtime}, nil)
	}
	return nil
}

func (s *Service) recoverHostBridge(ctx context.Context) error {
	if err := s.bridgeCall(ctx, "rebind", nil, nil); err != nil {
		return err
	}
	s.mu.Lock()
	entries := make(map[string]*registered, len(s.launches))
	retirements := map[string]string{}
	for workspace, id := range s.hostIntents {
		retirements[workspace] = id
	}
	for id, e := range s.launches {
		entries[id] = e
	}
	s.mu.Unlock()
	for workspace, id := range retirements {
		s.applyHostRetirement(ctx, workspace, id)
	}
	var registrations []hostRegistration
	for id, e := range entries {
		e.mu.Lock()
		live := !e.terminal && !e.fenced
		e.mu.Unlock()
		s.mu.Lock()
		if live && s.launches[id] == e && !e.retiring && s.hostIntents[id] == "" && e.binding.Runtime == "host" {
			registrations = append(registrations, hostRegistration{e.binding, e.command.Identity, e.command.Root, e.command.PublicRoot})
		}
		s.mu.Unlock()
	}
	for _, r := range registrations {
		if err := s.reconcileWorkspaceRegistration(ctx, r, true); err != nil {
			_ = bridgeFailure("registration_reconnect", err)
		}
	}
	if caps := s.capabilities.Load(); caps != nil {
		if err := s.bridgeCall(ctx, "capabilities", hostCapabilities{caps.server, caps.runtime}, nil); err != nil {
			return err
		}
	}
	var hello protocol.ShellLinkHello
	if err := s.bridgeBackground("hello", nil, &hello); err != nil {
		return err
	}
	if hello.Version != 2 {
		return ErrKeeperUnavailable
	}
	welcome := make(chan struct{})
	s.mu.Lock()
	s.hostHello = hello
	s.hostRefreshHello = hello
	s.hostRefreshDone = welcome
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.hostRefreshDone == welcome {
			s.hostRefreshDone = nil
		}
		s.mu.Unlock()
	}()
	// A new keeper incarnation needs fresh upstream authority, independently
	// of the agent transport and its active turns.
	if err := s.send(RPC{Type: "daemon_shell_hello", ShellHello: &hello}); err != nil {
		_ = bridgeFailure("reconnect_hello", err)
		return err
	}
	select {
	case <-welcome:
		s.mu.Lock()
		if len(s.hostJobs) > 0 || len(s.hostIntents) > 0 {
			s.wakeHostWorkLocked()
		}
		s.mu.Unlock()
		return s.syncHostCapabilities(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Service) bridge() HostBridge  { s.mu.Lock(); defer s.mu.Unlock(); return s.hostBridge }
func (s *Service) HasHostBridge() bool { return s.bridge() != nil }
func (s *Service) bridged(session string) bool {
	b := s.bridge()
	if b == nil {
		return false
	}
	s.mu.Lock()
	entry := s.launches[session]
	localHost := entry != nil && entry.binding.Runtime == "host"
	s.mu.Unlock()
	if entry != nil {
		return localHost
	}
	return b.HostSession(session)
}
func (s *Service) bridgeCall(ctx context.Context, method string, in, out any) error {
	b := s.bridge()
	if b == nil {
		return ErrClosed
	}
	return b.Invoke(ctx, method, in, out)
}
func (s *Service) bridgeBackground(method string, in, out any) error {
	b := s.bridge()
	if b == nil {
		return ErrClosed
	}
	return b.Metadata(method, in, out)
}
func (s *Service) registerBridged(_ context.Context, mu *sync.Mutex, b ownershell.Binding, command Command, reconcile func(context.Context) error, start func(context.Context, func() error) error) error {
	if mu == nil || reconcile == nil || command.Container != "" || !b.Live || b.Cron || ownershell.SupervisorIdentity(b, ownershell.SignedGrant{}).Launch.Validate() != nil {
		return bridgeFailure("registration_shape", ErrClosed)
	}
	s.mu.Lock()
	if s.launches[b.Session] != nil {
		s.mu.Unlock()
		return bridgeFailure("registration_duplicate", ErrClosed)
	}
	s.launches[b.Session] = &registered{binding: b, command: command, mu: mu, atomicStart: start, reconcile: reconcile, phase: "running", closedShells: map[string]bool{}}
	s.mu.Unlock()
	// Registration is shell availability, never agent admission. Keep the
	// trusted local pins for retry even when the keeper has not started yet.
	s.queueHostControl(b.Session)
	return nil
}

type hostSource struct {
	Session     string
	Same, Known bool
}

// Closures are daemon authority and never serialized. Recheck source pins for
// each use, and retain the outer admission lock through the actual open reply.
func (s *Service) executeBridged(ctx context.Context, r RPC) RPC {
	result := RPC{Version: r.Version, Type: "owner_shell_result", SessionID: r.SessionID, RequestID: r.RequestID, Op: r.Op, ErrorCode: string(ownershell.ReasonRuntimeFenced)}
	s.mu.Lock()
	entry := s.launches[r.SessionID]
	s.mu.Unlock()
	sourceRefused := false
	call := func() error {
		if !s.hostBridgeReady() {
			_ = bridgeFailure("operation_unavailable", ErrKeeperUnavailable)
			return s.hostUnavailableError()
		}
		s.mu.Lock()
		unavailable := s.hostJobs[r.SessionID] != 0
		pending := s.hostIntents[r.SessionID] != ""
		s.mu.Unlock()
		if unavailable {
			_ = bridgeFailure("operation_pending", ErrKeeperUnavailable)
			return ErrKeeperUnavailable
		}
		if pending {
			return bridgeFailure("operation_retired", ErrClosed)
		}
		if entry != nil && (sourceChecked(r.Op) || r.Op == "prepare" || r.Op == "commit" || r.Op == "receipt" || r.Op == "credit") {
			entry.mu.Lock()
			blocked := entry.terminal || entry.fenced || entry.phase != "running"
			entry.mu.Unlock()
			if blocked {
				return bridgeFailure("workspace_launch_fenced", ErrClosed)
			}
		}
		if bridge, ok := s.bridge().(*ReconnectingHostBridge); ok && !bridge.available() {
			_ = bridgeFailure("operation_unavailable", ErrKeeperUnavailable)
			return ErrKeeperUnavailable
		}
		if _, recovering := s.bridge().(*ReconnectingHostBridge); recovering && entry != nil {
			s.mu.Lock()
			req := hostRegistration{entry.binding, entry.command.Identity, entry.command.Root, entry.command.PublicRoot}
			s.mu.Unlock()
			if err := s.reconcileWorkspaceRegistration(ctx, req, false); err != nil {
				return err
			}
		}
		if sourceChecked(r.Op) || r.Op == "prepare" || r.Op == "commit" || r.Op == "receipt" || r.Op == "credit" {
			sourceRefused = !s.checkBridgedSource(ctx, entry, r.SessionID)
			if sourceRefused {
				if bridge, ok := s.bridge().(*ReconnectingHostBridge); ok && !bridge.available() {
					return ErrKeeperUnavailable
				}
				return ErrClosed
			}
		}
		if r.Version == 1 && r.Op == "credit" {
			err := s.bridgeCall(ctx, "handle", r, nil)
			result.OK = err == nil
			return err
		}
		if r.Op == "open" && entry != nil && entry.command.BeforeHostShell != nil {
			if err := entry.command.BeforeHostShell(); err != nil {
				return bridgeFailure("open_history", err)
			}
		}
		err := s.bridgeCall(ctx, "call", r, &result)
		if err == nil && result.OK && r.Op == "open" {
			s.mu.Lock()
			s.hostObservationRevision++
			state := s.hostObserved[r.SessionID]
			state.Workspace = r.SessionID
			state.Open = true
			state.Privileged = state.Privileged || r.Grant.Grant.Privileged
			s.hostObserved[r.SessionID] = state
			s.signalHostChangeLocked()
			s.mu.Unlock()
		}
		return err
	}
	var err error
	if entry != nil && entry.atomicStart != nil && r.Op == "open" {
		err = entry.atomicStart(ctx, call)
	} else {
		err = call()
	}
	if err != nil {
		result.OK = false
		result.ErrorCode = string(ownershell.ReasonRuntimeFenced)
		if sourceRefused {
			result.ErrorCode = string(ownershell.ReasonRuntimeIdentity)
		}
		if errors.Is(err, ErrKeeperUnavailable) {
			result.ErrorCode = string(ownershell.ReasonRuntimeStart)
			result.StartCause = "keeper_unavailable"
			if errors.Is(err, ErrKeeperIncompatible) {
				result.StartCause = "keeper_incompatible"
			}
		}
	}
	result.RequestID = r.RequestID
	result.SessionID, result.Op, result.Version = r.SessionID, r.Op, r.Version
	return result
}
func (s *Service) checkBridgedSource(ctx context.Context, entry *registered, session string) bool {
	if entry == nil {
		_ = bridgeFailure("source_registration_missing", ErrClosed)
		return false
	}
	same, known := true, true
	if entry.command.SourceState != nil {
		same, known = entry.command.SourceState()
	} else if entry.command.SourceLive != nil {
		same = entry.command.SourceLive()
		known = same
	}
	if entry.command.SourceLive != nil && !entry.command.SourceLive() {
		same = false
	}
	if err := s.bridgeCall(ctx, "source", hostSource{session, same, known}, nil); err != nil || !same || !known {
		_ = bridgeFailure("source_authority", ErrClosed)
		return false
	}
	return true
}

// HostCustodyReady is also used by native writer admission. A transport error
// is never an empty inventory or permission to start another workspace writer.
func (s *Service) HostCustodyReady(ctx context.Context, session string) error {
	if s.bridge() == nil {
		return nil
	}
	if session == "" {
		return ErrKeeperUnavailable
	}
	if err := s.bridgeCall(ctx, "ready", session, nil); err != nil {
		if errors.Is(err, ErrAdoptionPending) {
			return ErrAdoptionPending
		}
		return ErrKeeperUnavailable
	}
	return nil
}

var ErrAdoptionPending = errors.New("host shells are waiting for adoption; reconnect their terminals before starting more work")
var ErrKeeperUnavailable = errors.New("host shells unavailable while the keeper reconnects; agents keep running")
var ErrKeeperIncompatible = errors.Join(ErrKeeperUnavailable, errors.New("keeper protocol incompatible"))

func (s *Service) hostUnavailableError() error {
	if b, ok := s.bridge().(*ReconnectingHostBridge); ok {
		return b.UnavailableError()
	}
	return ErrKeeperUnavailable
}

func (s *Service) UsesHostKeeper() bool { return s.bridge() != nil }
func (s *Service) HostKeeperReconnects() bool {
	_, ok := s.bridge().(*ReconnectingHostBridge)
	return ok
}
func (s *Service) KeeperPersistenceReady() bool { return s.keeperPersistence.Load() }
func (s *Service) WaitHostKeeper(ctx context.Context) error {
	if managed, ok := s.bridge().(*ReconnectingHostBridge); ok {
		return managed.WaitReady(ctx)
	}
	return nil
}

// ProbeHostKeeper verifies the actual installed keeper image and clock profile;
// the daemon's own helper may have different sleep capabilities (notably WSL).
func (s *Service) ProbeHostKeeper(ctx context.Context, command Command) error {
	err := s.bridgeCall(ctx, "probe", hostRegistration{Identity: command.Identity, Root: command.Root, PublicRoot: command.PublicRoot}, nil)
	s.keeperPersistence.Store(err == nil)
	return err
}
