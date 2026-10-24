package daemon

import (
	"context"
	"errors"
	"reflect"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// BeginRuntime journals the idempotency token before requesting a generation or
// creating Docker resources. Launch MUST be idempotent for that trusted token.
func (s *LocalWorkspaceService) BeginRuntime(ctx context.Context, w *LocalWorkspace) error {
	if s == nil || s.Authority == nil || s.Supervisor == nil || s.Results == nil || s.CommitFinal == nil {
		return errors.New("runtime transaction authority unavailable")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return s.beginRuntimeLocked(ctx, w)
}

// Caller owns P2's session lock; preparation uses this before allocating storage.
func (s *LocalWorkspaceService) beginRuntimeLocked(ctx context.Context, w *LocalWorkspace) error {
	if w.state.RuntimeIdentity != "" || w.state.Stopped {
		return errors.New("runtime already allocated")
	}
	selectedContext := currentDockerContext()
	if w.state.WorkerContext != "" && w.state.WorkerContext != "test-peer" && w.state.WorkerContext != selectedContext {
		return errors.New("workspace Docker context changed since preparation")
	}
	if !w.state.RuntimePreparing {
		w.state.RuntimeToken = ccUUID()
		w.state.RuntimeContext = currentDockerContext()
		w.state.RuntimePreparing = true
	}
	if w.state.RuntimeContext == "" {
		return errors.New("docker context unavailable")
	}
	if err := w.save(); err != nil {
		return err
	}
	workspaceCrashBoundary("before-runtime-launch")
	launch, err := s.Authority.Launch(ctx, w.state.Session, w.state.Launch, w.state.RuntimeToken)
	if err != nil {
		return err
	}
	workspaceCrashBoundary("after-runtime-launch")
	if launch.Validate() != nil || launch.SessionID != w.state.Session || launch.LaunchGeneration != w.state.Launch.LaunchGeneration || launch.Runtime != "sandbox" {
		return errors.New("invalid allocated runtime launch")
	}
	w.state.SupervisorLaunch = launch
	return w.save()
}

// AbortRuntime is authenticated even when the generation reply/CAS ack was
// lost. The server must fence the token before acknowledging this idempotent
// operation; a mere matching token from a file is never authority.
func (s *LocalWorkspaceService) abortRuntime(ctx context.Context, w *LocalWorkspace) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.state.RuntimePreparing || w.state.RuntimeToken == "" {
		return errors.New("no runtime transaction to abort")
	}
	if err := s.Authority.Abort(ctx, w.state.Session, w.state.Launch, w.state.RuntimeToken, w.state.SupervisorLaunch); err != nil {
		return err
	}
	workspaceCrashBoundary("after-runtime-abort")
	out, err := workspaceDockerOutput(ctx, "--context", w.state.RuntimeContext, "ps", "-aq", "--no-trunc", "--filter", "label=blerg.runner.workspace-start="+w.state.RuntimeToken)
	if err != nil {
		return err
	}
	for _, id := range strings.Fields(string(out)) {
		d := &DockerWorkspaceContainer{Context: w.state.RuntimeContext, ID: id, Owner: sandboxOwner(), Session: w.state.Session, Launch: w.state.Launch.LaunchGeneration}
		if err := d.Remove(ctx); err != nil {
			return err
		}
	}
	workspaceCrashBoundary("after-pending-runtime-remove")
	remaining, err := workspaceDockerOutput(ctx, "--context", w.state.RuntimeContext, "ps", "-aq", "--filter", "label=blerg.runner.workspace-start="+w.state.RuntimeToken)
	if err != nil || strings.TrimSpace(string(remaining)) != "" {
		return errors.Join(err, errors.New("pending runtime absence unproved"))
	}
	w.state.RuntimePreparing = false
	w.state.RuntimeToken = ""
	w.state.RuntimeIdentity = ""
	w.state.RuntimeRoot = protocol.WorkspaceRoot{}
	w.state.Binding = nil
	w.state.BindingIntent = nil
	w.state.SupervisorLaunch = protocol.SupervisorLaunch{}
	w.state.Unavailable = false
	w.runtime = nil
	w.authorize = nil
	return w.save()
}

// Finalize freezes a retryable payload before its first send. Results are
// acknowledged before any terminal event; neither failure deletes recovery.
// Results must implement idempotent replacement for this runtime/member tuple.
func (s *LocalWorkspaceService) Finalize(ctx context.Context, w *LocalWorkspace, message protocol.SessionEnded, send func(protocol.SessionEnded) error) error {
	ctx = cleanupContext(ctx)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.validateIntentJournal(true); err != nil {
		return err
	}
	if workspaceFinalComplete(w.state) {
		return w.save()
	}
	if !w.state.Stopped || w.state.ActiveWorker != "" || w.state.RuntimePreparing {
		return errors.New("workspace writers are not proved absent")
	}
	if w.state.FinalDelivery == nil {
		if w.state.StopDelivery != nil {
			message = *w.state.StopDelivery
		}
		if message.SessionID != w.state.Session || message.Type != "session_ended" {
			return errors.New("final delivery session mismatch")
		}
		message.WorkspaceResults = nil
		w.state.FinalDelivery = &message
		w.state.FinalResults = w.results()
	}
	// Retry a failed journal write before any remote side effect, even when the
	// frozen payload already exists in memory from the failed attempt.
	workspaceCrashBoundary("before-final-payload-save")
	if err := w.save(); err != nil {
		return err
	}
	if !w.state.ResultsAcknowledged {
		if w.authorize == nil || w.state.Binding == nil {
			return errors.New("final result authority unavailable")
		}
		if err := w.authorize(ctx); err != nil {
			return err
		}
		if err := w.state.Launch.ValidateResults(append([]protocol.WorkspaceMemberResult(nil), w.state.FinalResults...)); err != nil {
			return err
		}
		publish := func() error {
			return s.Results(ctx, w.state.Session, *w.state.Binding, append([]protocol.WorkspaceMemberResult(nil), w.state.FinalResults...))
		}
		if authority, ok := s.Authority.(workspaceCleanupAuthority); ok {
			publish = func() error {
				return authority.CleanupResults(ctx, w.state.SupervisorLaunch, *w.state.Binding, append([]protocol.WorkspaceMemberResult(nil), w.state.FinalResults...))
			}
		}
		if err := publish(); err != nil {
			return err
		}
		workspaceCrashBoundary("after-final-results")
		w.state.ResultsAcknowledged = true
		if err := w.save(); err != nil {
			return err
		}
	}
	if s.CommitFinal == nil || w.state.Binding == nil {
		return errors.New("durable terminal acknowledgement unavailable")
	}
	transaction := WorkspaceFinalization{Binding: *w.state.Binding, Message: *w.state.FinalDelivery, Results: append([]protocol.WorkspaceMemberResult(nil), w.state.FinalResults...)}
	receipt, err := s.CommitFinal(ctx, w.state.SupervisorLaunch, transaction)
	if err != nil {
		return err
	}
	if receipt.Digest != transaction.Digest() {
		return errors.New("terminal receipt does not match immutable outcome")
	}
	workspaceCrashBoundary("after-terminal-receipt")
	// Raw Sender.Send is only a notification, never evidence of persistence.
	// The authenticated transaction above has already persisted this outcome.
	if send != nil {
		_ = send(*w.state.FinalDelivery)
	}
	workspaceCrashBoundary("after-terminal-delivery")
	w.state.TerminalReceipt = receipt.Digest
	w.state.TerminalAcknowledged = true
	return w.save()
}

// Round-1 records can predate the start-token journal. They cannot be resumed
// as writers. The independent authority must explicitly fence the historical
// immutable runtime ID; absence of a local binding never grants that authority.
func (s *LocalWorkspaceService) abortRootOnly(ctx context.Context, w *LocalWorkspace) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state.Binding != nil || w.state.RuntimeIdentity == "" || w.state.RuntimePreparing {
		return errors.New("not a root-only runtime")
	}
	if err := s.Authority.Abort(ctx, w.state.Session, w.state.Launch, "legacy-runtime:"+w.state.RuntimeIdentity, w.state.SupervisorLaunch); err != nil {
		return err
	}
	dockerContext, id, ok := strings.Cut(w.state.RuntimeIdentity, ":")
	if !ok {
		return errors.New("invalid historical runtime ID")
	}
	d := &DockerWorkspaceContainer{Context: dockerContext, ID: id, Owner: sandboxOwner(), Session: w.state.Session, Launch: w.state.Launch.LaunchGeneration}
	if err := d.Remove(ctx); err != nil {
		return err
	}
	w.state.RuntimeIdentity = ""
	w.state.RuntimeRoot = protocol.WorkspaceRoot{}
	w.state.BindingIntent = nil
	w.state.SupervisorLaunch = protocol.SupervisorLaunch{}
	w.state.Unavailable = false
	return w.save()
}

// WorkspaceFinalization is a P2 adapter transaction, not a new P0 wire type.
// CommitFinal must authenticate the current transport and compare the complete
// launch/runtime/root tuple. It atomically persists these exact results and end
// outcome, or proves the identical transaction already committed, including
// after end/token revocation. Conflicting outcomes and stale generations refuse.
// A socket write or queue admission MUST NOT produce WorkspaceFinalReceipt.
// Its digest deliberately excludes ConnectionID: a new authenticated connection
// must reconcile a lost receipt without changing the immutable outcome.
type WorkspaceFinalization = protocol.WorkspaceFinalization
type WorkspaceFinalReceipt = protocol.WorkspaceFinalReceipt

func (s *LocalWorkspaceService) reconnectHosted(ctx context.Context, w *LocalWorkspace, report func(protocol.SessionEnded) error) error {
	if w.host != nil {
		return w.host.reconnect(ctx, report)
	}
	w.mu.Lock()
	if workspaceFinalComplete(w.state) {
		message := *w.state.FinalDelivery
		w.mu.Unlock()
		return s.Finalize(ctx, w, message, report)
	}
	if w.state.FinalDelivery != nil && w.state.ResultsAcknowledged {
		message := *w.state.FinalDelivery
		w.mu.Unlock()
		return s.Finalize(ctx, w, message, report)
	}
	launch, err := s.TrustedLaunch(ctx, w.state.Session)
	if err != nil || !reflect.DeepEqual(launch, w.state.Launch) {
		w.mu.Unlock()
		return errors.Join(err, errors.New("hosted workspace launch changed"))
	}
	if w.state.Binding == nil || w.state.RuntimePreparing {
		// A canceling spawn joins preparation before setting this durable stop.
		// Never abort an in-process dispatch merely because it has not bound yet.
		stop, pending := w.state.StopRequested, w.state.RuntimePreparing
		w.mu.Unlock()
		if !stop {
			return nil
		}
		if pending {
			if err := s.abortRuntime(ctx, w); err != nil {
				return err
			}
		}
		return s.cleanupPrepared(ctx, w, report)
	}
	old := w.state.SupervisorLaunch
	binding := *w.state.Binding
	resumed, err := s.Authority.Recover(ctx, old, binding)
	if err != nil {
		resumed, err = workspaceRecoverCleanup(ctx, s.Authority, old, binding)
		if err == nil {
			w.state.StopRequested = true
			ctx = cleanupContext(ctx)
		}
	}
	if err != nil || resumed.Validate() != nil || resumed.SessionID != old.SessionID || resumed.LaunchGeneration != old.LaunchGeneration || resumed.RuntimeGeneration != old.RuntimeGeneration || resumed.Runtime != old.Runtime {
		w.mu.Unlock()
		return errors.Join(err, errors.New("hosted workspace reconnect not authorized"))
	}
	if err := workspaceCurrentAuthority(ctx, s.Authority, resumed, binding); err != nil {
		w.mu.Unlock()
		return err
	}
	if d, ok := w.runtime.(*DockerWorkspaceContainer); ok {
		if d.coordinator != nil {
			var rebindErr error
			if w.state.StopRequested || w.state.Stopped {
				rebindErr = d.coordinator.rebindStopping(ctx, resumed)
			} else {
				rebindErr = d.coordinator.rebind(ctx, resumed)
			}
			if rebindErr != nil {
				w.mu.Unlock()
				return rebindErr
			}
		}
		d.SupervisorLaunch = resumed
	}
	w.state.SupervisorLaunch = resumed
	w.authorize = func(ctx context.Context) error { return workspaceCurrentAuthority(ctx, s.Authority, resumed, binding) }
	err = w.save()
	stopRequested := w.state.StopRequested
	var message *protocol.SessionEnded
	if w.state.FinalDelivery != nil {
		delivery := *w.state.FinalDelivery
		message = &delivery
	}
	w.mu.Unlock()
	if err != nil {
		return err
	}
	if stopRequested && message == nil {
		if _, err := w.Stop(ctx); err != nil {
			return err
		}
		message = &protocol.SessionEnded{Type: "session_ended", SessionID: old.SessionID}
	}
	if message != nil {
		return s.Finalize(ctx, w, *message, report)
	}
	return nil
}

func workspaceFinalComplete(state workspacePrivateManifest) bool {
	if !state.TerminalAcknowledged || state.TerminalReceipt == "" || state.FinalDelivery == nil || state.Binding == nil {
		return false
	}
	transaction := WorkspaceFinalization{Binding: *state.Binding, Message: *state.FinalDelivery, Results: state.FinalResults}
	return state.TerminalReceipt == transaction.Digest()
}

// RequestStop persists the exact teardown outcome before driver/runtime effects.
// Retries preserve the first accepted outcome through a failed Stop or restart.
func (w *LocalWorkspace) RequestStop(message protocol.SessionEnded) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.validateIntentJournal(false); err != nil {
		return err
	}
	if message.Type != "session_ended" || message.SessionID != w.state.Session {
		return errors.New("stop delivery session mismatch")
	}
	if w.state.StopDelivery == nil {
		message.WorkspaceResults = nil
		w.state.StopDelivery = &message
	}
	w.state.StopRequested = true
	workspaceCrashBoundary("before-stop-intent-save")
	if err := w.save(); err != nil {
		return err
	}
	workspaceCrashBoundary("after-stop-intent")
	return nil
}

// Retrying a failed write may differ only in the intent/receipt fields this
// transaction owns. Never overwrite corrupt or replaced private authority.
func (w *LocalWorkspace) validateIntentJournal(final bool) error {
	if w.state.Host {
		return w.validateHostJournal()
	}
	saved, err := readPrivateWorkspace(w.record)
	if err != nil {
		return err
	}
	current := w.state
	if final {
		saved.FinalDelivery, current.FinalDelivery = nil, nil
		saved.FinalResults, current.FinalResults = nil, nil
		saved.ResultsAcknowledged, current.ResultsAcknowledged = false, false
		saved.TerminalAcknowledged, current.TerminalAcknowledged = false, false
		saved.TerminalReceipt, current.TerminalReceipt = "", ""
	} else {
		saved.StopRequested, current.StopRequested = false, false
		saved.StopDelivery, current.StopDelivery = nil, nil
	}
	if !reflect.DeepEqual(saved, current) {
		return errors.New("private workspace transaction authority changed")
	}
	return nil
}

// No agent or P5 shell has been admitted on this path. RecoverAll has joined
// preparation workers and abortRuntime has independently proved token-labelled
// container absence. Keep the allocation and pending member outcomes rather
// than inventing a supervisor receipt or deleting unresolved work.
func (s *LocalWorkspaceService) cleanupPrepared(ctx context.Context, w *LocalWorkspace, send func(protocol.SessionEnded) error) error {
	a, ok := s.Authority.(workspacePreparationCleanupAuthority)
	if !ok {
		return errors.New("preparation cleanup authority unavailable")
	}
	w.mu.Lock()
	if w.state.RuntimeIdentity != "" || w.state.RuntimePreparing || w.state.ActiveWorker != "" {
		w.mu.Unlock()
		return errors.New("prepared workspace cleanup unproven")
	}
	if w.state.RuntimeToken == "" {
		w.state.RuntimeToken = ccUUID()
	}
	if err := w.save(); err != nil {
		w.mu.Unlock()
		return err
	}
	l, err := a.CleanupLaunch(ctx, w.state.Session, w.state.Launch, w.state.RuntimeToken)
	if err != nil {
		w.mu.Unlock()
		return err
	}
	b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: l.LaunchGeneration, Generation: l.RuntimeGeneration}, ManifestDigest: w.state.Launch.Digest, Root: w.state.RootIdentity}
	w.state.SupervisorLaunch = l
	w.state.BindingIntent = &b
	if err = w.save(); err == nil {
		err = a.CleanupBind(ctx, l, b)
	}
	if err != nil {
		w.mu.Unlock()
		return err
	}
	w.state.Binding, w.state.BindingIntent = &b, nil
	w.state.StopRequested, w.state.Stopped = true, true
	for i := range w.state.Members {
		w.state.Members[i].Result.Pending = true
		w.state.Members[i].Result.SyncError = "preparation stopped before agent admission"
		w.state.Members[i].Result.RecoveryLocation = w.state.Members[i].Clone
	}
	w.authorize = func(ctx context.Context) error { return s.currentWorkspace(ctx, w) }
	err = w.save()
	w.mu.Unlock()
	if err != nil {
		return err
	}
	return s.Finalize(ctx, w, protocol.SessionEnded{Type: "session_ended", SessionID: l.SessionID, Message: "workspace preparation interrupted; allocation retained"}, send)
}
