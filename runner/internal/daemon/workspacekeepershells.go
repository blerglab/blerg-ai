package daemon

import (
	"context"
	"log/slog"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

// Admission serializes shell forwarding with membership, without holding w.mu
// over keeper IPC. The signed private manifest is the durable negative proof;
// an absent field on an old journal is unknown, never an empty-custody proof.
func (h *hostWorkspace) admitHostShell(ctx context.Context, start func() error) error {
	admission := workspaceAdmissionLock(h.w.state.Session)
	if !admission.TryLock() {
		failurelog.Default.Event("shell_keeper.open_admission", slog.LevelWarn, "component", "shell_keeper", "stage", "open_admission", "cause", "membership_busy")
		return shellruntime.ErrClosed
	}
	defer admission.Unlock()
	if ctx.Err() != nil {
		failurelog.Default.Event("shell_keeper.open_admission", slog.LevelWarn, "component", "shell_keeper", "stage", "open_admission", "cause", "request_cancelled")
		return ctx.Err()
	}
	registry, ok := h.service.Supervisor.(*workspaceSupervisors)
	if !ok || !registry.service.HasHostBridge() {
		if err := h.beforeHostShell(); err != nil {
			return err
		}
	}
	return start()
}
func (h *hostWorkspace) beforeHostShell() error {
	w := h.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.state.HostKeeperNeverOpened {
		return nil
	}
	w.state.HostKeeperNeverOpened = false
	if err := w.save(); err != nil {
		w.state.HostKeeperNeverOpened = true // no request has been forwarded
		failurelog.Default.Event("shell_keeper.open_history", slog.LevelError, "component", "shell_keeper", "stage", "open_history", "cause", failurelog.Cause(err))
		return err
	}
	return nil
}

// Called with both membership admission and w.mu. A five-second observation
// grace may expire only for a workspace durably known never to have forwarded
// a host open. Any possible prior custody requires a real cleanup proof.
func (s *LocalWorkspaceService) membershipShellWait(w *LocalWorkspace) string {
	if s.membershipShells != nil {
		if s.membershipShells.open(w.state.Session) {
			return protocol.MembershipWaitShell
		}
		return ""
	}
	registry, ok := s.Supervisor.(*workspaceSupervisors)
	if !ok || registry.service == nil {
		return protocol.MembershipWaitShell
	}
	open, known := registry.service.WorkspaceShellsOpen(w.state.Session)
	if known {
		w.keeperWaitSince = time.Time{}
		if open {
			return protocol.MembershipWaitShell
		}
		return ""
	}
	if w.state.Host && w.state.HostKeeperNeverOpened {
		now := time.Now()
		if s.membershipClock != nil {
			now = s.membershipClock()
		}
		if w.keeperWaitSince.IsZero() {
			w.keeperWaitSince = now
		}
		if now.Sub(w.keeperWaitSince) >= 5*time.Second {
			return ""
		}
	}
	if registry.service.KeeperIncompatible() {
		return protocol.MembershipWaitKeeperIncompatible
	}
	return protocol.MembershipWaitKeeper
}
