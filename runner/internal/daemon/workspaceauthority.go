package daemon

import (
	"context"
	"errors"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// WorkspaceRuntimeAuthority is supplied by the authenticated dispatch adapter.
// Launch returns server-allocated generation/current connection state, never
// fields from an agent frame. Launch runs before Docker creation; its last
// argument is a journalled idempotency token, never a Docker ID. Bind must await
// the server's write-once CAS. Abort independently fences that token (including
// lost replies); Recover authenticates a connection change to the same binding.
// Current must compare connection, incarnation, root and generation against the
// trusted current binding on every operation, including result delivery.
type WorkspaceRuntimeAuthority interface {
	Abort(context.Context, string, protocol.WorkspaceManifest, string, protocol.SupervisorLaunch) error
	Recover(context.Context, protocol.SupervisorLaunch, protocol.WorkspaceRuntimeBinding) (protocol.SupervisorLaunch, error)
	Launch(context.Context, string, protocol.WorkspaceManifest, string) (protocol.SupervisorLaunch, error)
	Bind(context.Context, protocol.SupervisorLaunch, protocol.WorkspaceRuntimeBinding) error
	Current(context.Context, protocol.SupervisorLaunch, protocol.WorkspaceRuntimeBinding) error
}

func (s *LocalWorkspaceService) bindAuthority(ctx context.Context, w *LocalWorkspace, d *DockerWorkspaceContainer, root protocol.WorkspaceRoot) error {
	launch := w.state.SupervisorLaunch
	if err := launch.Validate(); err != nil {
		return err
	}
	if launch.SessionID != w.state.Session || launch.LaunchGeneration != w.state.Launch.LaunchGeneration || launch.Runtime != "sandbox" {
		return errors.New("runtime launch authority mismatch")
	}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: launch.LaunchGeneration, Generation: launch.RuntimeGeneration}, ManifestDigest: w.state.Launch.Digest, Root: root}
	if err := binding.Validate(w.state.Launch); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.state.BindingIntent = &binding
	if err := w.save(); err != nil {
		return err
	}
	workspaceCrashBoundary("before-runtime-cas")
	if err := s.Authority.Bind(ctx, launch, binding); err != nil {
		return err
	}
	workspaceCrashBoundary("after-runtime-cas")
	w.state.Binding = &binding
	w.state.SupervisorLaunch = launch
	w.authorize = func(ctx context.Context) error { return s.currentWorkspace(ctx, w) }
	d.Supervisor = s.Supervisor
	d.SupervisorLaunch = launch
	d.ResumeCheck = func(ctx context.Context) error { return w.authorize(ctx) }
	if err := w.authorize(ctx); err != nil {
		return err
	}
	if registry, ok := s.Supervisor.(*workspaceSupervisors); ok {
		if err := registry.register(ctx, w.state, d); err != nil {
			return err
		}
	}
	w.state.RuntimePreparing = false
	w.state.BindingIntent = nil
	workspaceCrashBoundary("before-runtime-ready-save")
	if err := w.save(); err != nil {
		return err
	}
	workspaceCrashBoundary("after-runtime-ready-save")
	return nil
}

// WithBoundRuntimeMember carries the runtime generation in addition to member
// selection. Both entry and delivery validate authenticated current authority.
func (w *LocalWorkspace) WithBoundRuntimeMember(ctx context.Context, runtime protocol.WorkspaceRuntimeRef, ref protocol.WorkspaceMemberRef, operation func(context.Context, WorkspaceContainer, string) error) error {
	if err := workspaceReadLock(ctx, w.mu); err != nil {
		return err
	}
	defer w.mu.Unlock()
	if !runtime.Matches(w.state.Binding) || w.authorize == nil {
		return errors.New("stale or unbound runtime member request")
	}
	if err := w.withRuntimeMember(ctx, ref, operation); err != nil {
		return err
	}
	return w.authorize(ctx)
}

// AdmitShell shares the same mutex as freeze/stop and checks the exact trusted
// launch. P5 still owns grants and shell containment; this only reserves its
// admission while the runtime is demonstrably running.
func (s *LocalWorkspaceService) AdmitShell(ctx context.Context, shell protocol.SupervisorShell) error {
	if s == nil || s.Supervisor == nil {
		return errors.New("workspace supervisor unavailable")
	}
	s.mu.Lock()
	w := s.sessions[shell.SessionID]
	s.mu.Unlock()
	if w == nil {
		return errors.New("workspace session unavailable")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if shell.Launch != w.state.SupervisorLaunch || shell.Validate() != nil || w.state.Stopped || w.state.StopRequested || w.state.Unavailable || w.authorize == nil {
		return errors.New("workspace shell admission fenced")
	}
	if err := w.validateShared(ctx); err != nil {
		return err
	}
	return s.Supervisor.AdmitShell(ctx, shell)
}
