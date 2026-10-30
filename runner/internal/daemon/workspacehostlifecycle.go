package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"github.com/blerglab/blerg-ai/runner/internal/discovery"
	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

// Host shares P2's outer lock and P5's private leaf lock, but never allocates,
// copies, commits or releases the owner's source trees. Results keep branches.
type hostWriterGroup interface {
	Start(*exec.Cmd) error
	Freeze(context.Context) error
	Thaw(context.Context) error
	Stop(context.Context) error
}

type hostWorkspace struct {
	writers hostWriterGroup
	w       *LocalWorkspace
	service *LocalWorkspaceService
	reader  *authorizedHostExplorer
	helpers *LocalWorkspace // separate signed helper journal: callbacks never enter P2
}

func (s *LocalWorkspaceService) prepareHostLifecycle(ctx context.Context, msg protocol.SpawnSession, pins *discovery.BoundPins, l protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding, token, cwd string, r *authorizedHostExplorer) (*LocalWorkspace, error) {
	registry, ok := s.Supervisor.(*workspaceSupervisors)
	if !ok || s.Results == nil || s.CommitFinal == nil {
		return nil, errors.New("complete host lifecycle unavailable")
	}
	record, err := workspaceRecord(msg.SessionID)
	if err != nil {
		return nil, err
	}
	w := &LocalWorkspace{mu: workspaceLock(msg.SessionID), record: record, state: workspacePrivateManifest{Version: privateWorkspaceVersion, Host: true, HostWorkDir: cwd, HostDriverStopped: true, Session: msg.SessionID, Launch: *msg.Workspace, Root: pins.AnchorPath, RootIdentity: b.Root, RuntimeRoot: b.Root, Binding: &b, SupervisorLaunch: l, RuntimeToken: token, RuntimeIdentity: "host:" + token, ShellEligible: !msg.RestrictTools}}
	for _, src := range pins.Sources {
		st, e := src.Directory.Stat()
		if e != nil {
			return nil, e
		}
		common, e := src.CommonDirectory.Stat()
		if e != nil {
			return nil, e
		}
		source := WorkspaceSource{Member: src.Member, Path: src.CanonicalPath, CommonPath: src.CommonDirectoryPath, RootIdentity: workspaceRootIdentity(st), CommonIdentity: workspaceRootIdentity(common)}
		w.state.Sources = append(w.state.Sources, source)
		w.state.Members = append(w.state.Members, workspacePrivateMember{Member: src.Member, Clone: src.CanonicalPath, CloneIdentity: source.RootIdentity, Result: protocol.WorkspaceMemberResult{WorkspaceMemberRef: protocol.WorkspaceMemberRef{LaunchGeneration: msg.Workspace.LaunchGeneration, MemberID: src.Member.ID}, Pending: true}})
	}
	saved, err := readPrivateWorkspace(record)
	if err != nil {
		return nil, err
	}
	w.state.HostChild, w.state.HostChildren = saved.HostChild, saved.HostChildren
	hw := &hostWorkspace{w: w, service: s, reader: r, helpers: &LocalWorkspace{mu: new(sync.Mutex), record: record + ".helpers", state: workspacePrivateManifest{Session: msg.SessionID, Launch: *msg.Workspace, RuntimeToken: token}}}
	w.host = hw
	controller, err := hostHelperIdentityFor(os.Getpid())
	if err != nil {
		return nil, err
	}
	w.state.HostController = controller
	w.authorize = func(ctx context.Context) error {
		return s.currentWorkspace(ctx, w)
	}
	if err = w.save(); err != nil {
		return nil, err
	}
	if err = hw.helpers.save(); err != nil {
		return nil, err
	}
	w.state.HostHelpersJournaled = true
	if err = w.save(); err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if s.HostExecutable != "" {
		executable = s.HostExecutable
	}
	hw.writers = hostWriterGroupFor(executable, msg.SessionID, hw.recordHelper)
	if hw.writers == nil {
		return nil, errors.New("host writer containment unsupported")
	}
	if err = registry.registerHost(ctx, hw); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.sessions == nil {
		s.sessions = map[string]*LocalWorkspace{}
	}
	s.sessions[msg.SessionID] = w
	s.mu.Unlock()
	return w, nil
}
func (w *LocalWorkspace) validateHostJournal() error {
	saved, err := readPrivateWorkspace(w.record)
	if err != nil || !saved.Host || saved.Session != w.state.Session || !reflect.DeepEqual(saved.Launch, w.state.Launch) || saved.RuntimeToken != w.state.RuntimeToken || saved.HostChild != w.state.HostChild || !reflect.DeepEqual(saved.HostChildren, w.state.HostChildren) || saved.Binding == nil || w.state.Binding == nil || *saved.Binding != *w.state.Binding {
		return errors.New("host lifecycle journal changed")
	}
	return nil
}
func (h *hostWorkspace) collect(ctx context.Context) {
	if h.w.state.HostChild {
		h.w.collectHostChildren(ctx)
		return
	}
	for i, source := range h.w.state.Sources {
		result := h.w.state.Members[i].Result
		result.Pending = true
		result.SyncError = "host member identity unavailable"
		st, err := os.Stat(source.Path)
		common, commonErr := workspaceDirectoryIdentity(source.CommonPath)
		if err == nil && workspaceRootIdentity(st) == source.RootIdentity && commonErr == nil && common == source.CommonIdentity {
			pinned, ok := pinGitDir(source.Path, st)
			if ok {
				if pinned.vet(ctx, false) {
					branch, head := &cappedOutput{max: 1024, strict: true}, &cappedOutput{max: 256, strict: true}
					e := pinned.git(ctx, branch, "symbolic-ref", "--quiet", "--short", "HEAD")
					if e == nil && pinned.git(ctx, head, "rev-parse", "--verify", "HEAD") == nil && !branch.over && !head.over {
						result.Branch = strings.TrimSpace(branch.buf.String())
						result.Head = strings.TrimSpace(head.buf.String())
						result.Pending = false
						result.SyncError = ""
					}
				}
				pinned.close()
			}
		}
		h.w.state.Members[i].Result = result
	}
}
func (h *hostWorkspace) sync(ctx context.Context) ([]protocol.WorkspaceMemberResult, error) {
	w := h.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state.StopRequested || w.state.Stopped {
		return w.results(), errors.New("host lifecycle stopping")
	}
	if err := w.authorize(ctx); err != nil {
		return w.results(), err
	}
	if h.service.CancelStatus != nil {
		if err := h.service.CancelStatus(ctx, w.state.Session); err != nil {
			return w.results(), err
		}
	}
	// P2 -> coordinator -> P5 leaf; writer-group callbacks only enter their
	// separate append journal. Keep readers fenced if any proof fails.
	if err := h.service.Supervisor.Notify(ctx, w.state.SupervisorLaunch, "syncing"); err != nil {
		return w.results(), err
	}
	if err := h.writers.Freeze(ctx); err != nil {
		// No proof that writers stopped: this turn's sync is skipped (fail closed), but the session
		// must not stay frozen and fenced. Thaw (which also cancels a freeze still in progress) and
		// reopen; only if that fails too does the session stay closed until a restart.
		return w.results(), h.reopenSkipped(ctx, fmt.Errorf("host workspace writer freeze: %w", err), "a failed freeze")
	}
	h.collect(ctx)
	if err := w.authorize(ctx); err != nil {
		var refused *WorkspaceRefusedError
		if errors.As(err, &refused) {
			// The server says this launch is over: its writers stay stopped and fenced.
			return w.results(), fmt.Errorf("this session's workspace was closed, so its processes stay stopped: %w", err)
		}
		// Not a verdict (a dropped connection, a timeout, a server error): keep the session usable,
		// skip this turn's sync and check again at the next turn end, as after a failed freeze.
		err = fmt.Errorf("this turn's sync was skipped and will be retried at the next turn end: workspace authority could not be confirmed: %w", err)
		return w.results(), h.reopenSkipped(ctx, err, "an unconfirmed authority")
	}
	if err := h.writers.Thaw(ctx); err != nil {
		return w.results(), fmt.Errorf("host workspace writer thaw: %w", err)
	}
	if err := h.service.Supervisor.Notify(ctx, w.state.SupervisorLaunch, "running"); err != nil {
		return w.results(), err
	}
	if h.service.ResumeStatus != nil {
		if err := h.service.ResumeStatus(ctx, w.state.Session); err != nil {
			return w.results(), err
		}
	}
	return w.results(), w.save()
}

// reopenSkipped thaws the writers and reopens admission after a turn-end sync was skipped (the
// caller holds the lock), and returns cause joined with anything that kept the session closed.
func (h *hostWorkspace) reopenSkipped(ctx context.Context, cause error, after string) error {
	w := h.w
	if err := h.writers.Thaw(ctx); err != nil {
		return errors.Join(cause, fmt.Errorf("host workspace writer thaw after %s: %w", after, err))
	}
	if err := h.service.Supervisor.Notify(ctx, w.state.SupervisorLaunch, "running"); err != nil {
		return errors.Join(cause, err)
	}
	if h.service.ResumeStatus != nil {
		if err := h.service.ResumeStatus(ctx, w.state.Session); err != nil {
			return errors.Join(cause, err)
		}
	}
	return cause
}
func (h *hostWorkspace) stop(ctx context.Context) ([]protocol.WorkspaceMemberResult, error) {
	w := h.w
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.validateHostJournal(); err != nil {
		return w.results(), err
	}
	if w.state.Stopped {
		return w.results(), nil
	}
	if err := w.authorize(cleanupContext(ctx)); err != nil {
		return w.results(), err
	}
	w.state.StopRequested = true
	if err := w.save(); err != nil {
		return w.results(), err
	}
	if h.writers != nil {
		if err := h.writers.Stop(ctx); err != nil {
			return w.results(), err
		}
	}
	if !w.state.HostDriverStopped {
		return w.results(), errors.New("host driver cleanup unproven")
	}
	if h.service.CloseStatus != nil {
		if err := h.service.CloseStatus(ctx, w.state.Session); err != nil {
			return w.results(), err
		}
	}
	proof, err := h.service.Supervisor.CloseLaunch(ctx, w.state.SupervisorLaunch, "host stopping")
	if err != nil || proof.ValidateFor(w.state.SupervisorLaunch, "") != nil {
		return w.results(), errors.Join(err, errors.New("host shells remain unproven"))
	}
	registry, ok := h.service.Supervisor.(*workspaceSupervisors)
	if !ok {
		return w.results(), errors.New("host registration unavailable")
	}
	if err := registry.retireHost(ctx, w.state.SupervisorLaunch); err != nil {
		return w.results(), err
	}
	h.collect(ctx)
	if err := w.releaseHostChildren(ctx); err != nil {
		return w.results(), err
	}
	w.state.Stopped = true
	return w.results(), w.save()
}
func (h *hostWorkspace) driverStarting() error {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	h.w.state.HostDriverStopped = false
	return h.w.save()
}
func (h *hostWorkspace) driverStopped() error {
	h.w.mu.Lock()
	defer h.w.mu.Unlock()
	h.w.state.HostDriverStopped = true
	return h.w.save()
}
func (h *hostWorkspace) recordHelper(pid int) error {
	identity, err := hostHelperIdentityFor(pid)
	if err != nil {
		return err
	}
	h.helpers.mu.Lock()
	defer h.helpers.mu.Unlock()
	if len(h.helpers.state.HostHelpers) >= 512 {
		return errors.New("host helper history exhausted")
	}
	h.helpers.state.HostHelpers = append(h.helpers.state.HostHelpers, identity)
	return h.helpers.save()
}
func (s *workspaceSupervisors) registerHost(ctx context.Context, h *hostWorkspace) error {
	state := h.w.state
	if _, err := s.peer(state.Session); err == nil {
		return errors.New("host supervisor already registered")
	}
	identity, err := shellruntime.ResolveIdentity(state.HostWorkDir)
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := shellruntime.HostCommand(executable, identity)
	if h.service.HostCommand != nil {
		command = h.service.HostCommand(identity)
	}
	command.PublicRoot = state.HostWorkDir
	st, err := os.Stat(state.HostWorkDir)
	if err != nil {
		return err
	}
	command.Root = workspaceRootIdentity(st)
	command.OwnedStarted = h.recordHelper
	b := workspaceShellBinding(state)
	b.Runtime = "host"
	b.Audience = ownershell.RuntimeAudience(b.Connection, b.RuntimeGeneration)
	if err = s.service.Register(ctx, new(sync.Mutex), b, command, func(context.Context) error { return s.service.ProveRecordedShellsEmpty(state.Session) }); err != nil {
		return err
	}
	c := &workspaceCoordinator{host: true, service: s.service, launch: state.SupervisorLaunch, binding: *state.Binding}
	s.mu.Lock()
	s.sessions[state.Session] = c
	s.mu.Unlock()
	return nil
}
func (s *workspaceSupervisors) retireHost(ctx context.Context, l protocol.SupervisorLaunch) error {
	c, err := s.peer(l.SessionID)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.retired || c.fencedRuntime {
		return nil
	}
	_, local, err := c.peer(l)
	if err != nil {
		return err
	}
	proof, err := c.service.Retire(ctx, local)
	if err != nil {
		return err
	}
	if err = proof.ValidateFor(local, ""); err != nil {
		return err
	}
	c.retired = true
	return nil
}
func (h *hostWorkspace) reconnect(ctx context.Context, send func(protocol.SessionEnded) error) error {
	w := h.w
	w.mu.Lock()
	if workspaceFinalComplete(w.state) || w.state.FinalDelivery != nil && w.state.ResultsAcknowledged {
		message := *w.state.FinalDelivery
		w.mu.Unlock()
		return h.service.Finalize(ctx, w, message, send)
	}
	old := w.state.SupervisorLaunch
	launch, err := h.service.Authority.Recover(ctx, old, *w.state.Binding)
	if err != nil {
		launch, err = workspaceRecoverCleanup(ctx, h.service.Authority, old, *w.state.Binding)
		if err == nil {
			w.state.StopRequested = true
			ctx = cleanupContext(ctx)
		}
	}
	if err != nil {
		w.mu.Unlock()
		return err
	}
	registry, ok := h.service.Supervisor.(*workspaceSupervisors)
	if !ok {
		w.mu.Unlock()
		return errors.New("host supervisor unavailable")
	}
	c, err := registry.peer(old.SessionID)
	if err == nil {
		err = c.rebind(ctx, launch)
	}
	if err != nil {
		w.mu.Unlock()
		return err
	}
	w.state.SupervisorLaunch = launch
	err = w.save()
	stop := w.state.StopRequested
	w.mu.Unlock()
	if err != nil {
		return err
	}
	if stop {
		if _, err = w.Stop(ctx); err != nil {
			return err
		}
		return h.service.Finalize(ctx, w, protocol.SessionEnded{Type: "session_ended", SessionID: old.SessionID}, send)
	}
	return nil
}
func (s *LocalWorkspaceService) recoverHost(ctx context.Context, state workspacePrivateManifest, record string, send func(protocol.SessionEnded) error) error {
	launch, err := s.TrustedLaunch(ctx, state.Session)
	if err != nil || !reflect.DeepEqual(launch, state.Launch) {
		return errors.Join(err, errors.New("host recovery launch changed"))
	}
	w := &LocalWorkspace{mu: workspaceLock(state.Session), state: state, record: record}
	if state.RuntimePreparing {
		if err := s.recoverHostPreparation(ctx, w); err != nil {
			return err
		}
		state = w.state
	}
	if state.FinalDelivery != nil && state.ResultsAcknowledged {
		return s.Finalize(ctx, w, *state.FinalDelivery, send)
	}
	if state.Binding == nil {
		return errors.New("host bind recovery pending")
	}
	// A persisted agent record is a request to resume, not a stop intent. The
	// new daemon must let adoption re-prove this workspace before admitting a
	// writer, including journals left by an older daemon after failed sync.
	// The previous incarnation's helpers are reaped now all the same, so a
	// session whose adoption stays paused leaves no writer behind; the caller
	// holds the admission lock, so no new helper of this session exists yet.
	if !state.StopRequested && state.FinalDelivery == nil && s.RecoverableHost != nil && s.RecoverableHost(state.Session) {
		// A controller not proven gone owns its helpers: nothing here may reap,
		// and adoption refuses the session too.
		if hostControllerGone(state.HostController) == nil {
			return reapPreviousHostHelpers(ctx, state, record)
		}
		return nil
	}
	resumed, err := workspaceRecoverCleanup(ctx, s.Authority, state.SupervisorLaunch, *state.Binding)
	if err != nil {
		return err
	}
	helpers, err := readPrivateWorkspace(record + ".helpers")
	if state.HostHelpersJournaled {
		if err != nil || helpers.RuntimeToken != state.RuntimeToken {
			return errors.New("host helper history unavailable")
		}
	} else {
		helpers.HostHelpers = nil
	}
	// Never reap helpers owned by a still-running controller.
	if !state.HostDriverStopped {
		if err := hostControllerGone(state.HostController); err != nil {
			return err
		}
	}
	for _, identity := range helpers.HostHelpers {
		if err := reapHostHelper(ctx, identity); err != nil {
			return err
		}
	}
	registry, ok := s.Supervisor.(*workspaceSupervisors)
	if !ok {
		return errors.New("host cleanup registry unavailable")
	}
	registry.mu.Lock()
	registry.sessions[state.Session] = &workspaceCoordinator{host: true, service: registry.service, launch: resumed, binding: *state.Binding, fencedRuntime: true}
	registry.mu.Unlock()
	w.state.SupervisorLaunch = resumed
	w.authorize = func(ctx context.Context) error {
		return workspaceCurrentAuthority(ctx, s.Authority, resumed, *w.state.Binding)
	}
	w.host = &hostWorkspace{w: w, service: s}
	// A running engine's recovery must independently acknowledge its teardown;
	// absence of a fresh P5 map can never stand in for this proof.
	if !state.HostDriverStopped {
		if err := hostControllerGone(state.HostController); err != nil {
			return err
		}
		w.state.HostDriverStopped = true
	}
	if err = w.save(); err != nil {
		return err
	}
	if _, err = w.Stop(ctx); err != nil {
		return err
	}
	return s.Finalize(ctx, w, protocol.SessionEnded{Type: "session_ended", SessionID: state.Session}, send)
}
func workspaceGuideForRuntime(m *protocol.WorkspaceManifest, w *LocalWorkspace) string {
	if m == nil || m.Runtime != "host" {
		return workspaceGuide(m)
	}
	if w == nil || !w.state.Host {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nWorkspace repositories (immutable launch membership):\n")
	for _, source := range w.state.Sources {
		fmt.Fprintf(&b, "- %s: %s\n", source.Member.ID, filepath.Clean(source.Path))
	}
	if w.state.HostChild {
		fmt.Fprintf(&b, "Commit in each affected member repository on wip/%s. These are your own worktrees; commits land directly in the shared repository. Shell grants are never inherited.\n", w.state.Session)
	} else {
		b.WriteString("Commit in each affected member repository; preserve each member's existing branch. Shell grants are never inherited.\n")
	}
	return b.String()
}

func (s *LocalWorkspaceService) beginHostIntent(msg protocol.SpawnSession, pins *discovery.BoundPins, token string, child bool) (*LocalWorkspace, error) {
	if _, ok := s.Supervisor.(*workspaceSupervisors); !ok || s.Results == nil || s.CommitFinal == nil {
		return nil, errors.New("complete host lifecycle unavailable")
	}
	record, err := workspaceRecord(msg.SessionID)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(record); !os.IsNotExist(err) {
		return nil, errors.New("host handoff already journalled; recover before reuse")
	}
	anchor, err := pins.Anchor.Stat()
	if err != nil {
		return nil, err
	}
	w := &LocalWorkspace{mu: workspaceLock(msg.SessionID), record: record, state: workspacePrivateManifest{Version: privateWorkspaceVersion, Host: true, HostDriverStopped: true, Session: msg.SessionID, Launch: *msg.Workspace, Root: pins.AnchorPath, RootIdentity: workspaceRootIdentity(anchor), RuntimeRoot: workspaceRootIdentity(anchor), RuntimeToken: token, RuntimePreparing: true}}
	for _, source := range pins.Sources {
		st, e := source.Directory.Stat()
		if e != nil {
			return nil, e
		}
		common, e := source.CommonDirectory.Stat()
		if e != nil {
			return nil, e
		}
		w.state.Sources = append(w.state.Sources, WorkspaceSource{Member: source.Member, Path: source.CanonicalPath, CommonPath: source.CommonDirectoryPath, RootIdentity: workspaceRootIdentity(st), CommonIdentity: workspaceRootIdentity(common)})
	}
	// Even a failure before the first allocation must never recover a child as
	// an ordinary host session over the owner's source directories.
	if child {
		w.state.HostChild = true
		w.state.Sources = nil
	}

	workspaceCrashBoundary("before-host-handoff-save")
	return w, w.save()
}

func (s *LocalWorkspaceService) recoverHostPreparation(ctx context.Context, w *LocalWorkspace) error {
	authority, ok := s.Authority.(workspacePreparationCleanupAuthority)
	if !ok {
		return errors.New("host preparation cleanup authority unavailable")
	}
	l, err := authority.CleanupLaunch(ctx, w.state.Session, w.state.Launch, w.state.RuntimeToken)
	if err != nil {
		return err
	}
	b := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: l.LaunchGeneration, Generation: l.RuntimeGeneration}, ManifestDigest: w.state.Launch.Digest, Root: w.state.RootIdentity}
	if w.state.BindingIntent != nil && *w.state.BindingIntent != b {
		return errors.New("host bind intent changed")
	}
	w.state.SupervisorLaunch = l
	w.state.BindingIntent = &b
	if err = w.save(); err != nil {
		return err
	}
	if err = authority.CleanupBind(ctx, l, b); err != nil {
		return err
	}
	w.state.Binding = &b
	w.state.BindingIntent = nil
	w.state.RuntimePreparing = false
	w.state.RuntimeIdentity = "host:" + w.state.RuntimeToken
	for _, source := range w.state.Sources {
		w.state.Members = append(w.state.Members, workspacePrivateMember{Member: source.Member, Clone: source.Path, CloneIdentity: source.RootIdentity, Result: protocol.WorkspaceMemberResult{WorkspaceMemberRef: protocol.WorkspaceMemberRef{LaunchGeneration: l.LaunchGeneration, MemberID: source.Member.ID}, Pending: true}})
	}
	if w.state.HostChild {
		w.state.Members = nil
		for _, member := range w.state.Launch.Members {
			m := workspacePrivateMember{Member: member, Result: protocol.WorkspaceMemberResult{WorkspaceMemberRef: protocol.WorkspaceMemberRef{LaunchGeneration: l.LaunchGeneration, MemberID: member.ID}, Pending: true}}
			for _, a := range w.state.HostChildren {
				if a.MemberID == member.ID {
					m.Clone, m.CloneIdentity = a.Path, a.Root
					m.Result.RecoveryLocation = a.Path
					m.Result.Base = a.Base
					break
				}
			}
			w.state.Members = append(w.state.Members, m)
		}
	}

	return w.save()
}

// resumeHostIntent reuses the authenticated launch tuple only after the old
// controller and every journalled helper are gone. Fresh P1 descriptors must
// prove the same trees; a failed freeze is never evidence of writer teardown.
func (s *LocalWorkspaceService) resumeHostIntent(ctx context.Context, msg protocol.SpawnSession, pins *discovery.BoundPins) (*LocalWorkspace, error) {
	record, err := workspaceRecord(msg.SessionID)
	if err != nil {
		return nil, err
	}
	state, err := readPrivateWorkspace(record)
	if err != nil {
		return nil, err
	}
	if !state.Host || state.Session != msg.SessionID || !reflect.DeepEqual(state.Launch, *msg.Workspace) || state.StopRequested || state.FinalDelivery != nil || state.Binding == nil || state.RuntimePreparing {
		return nil, errors.New("host workspace cannot resume from this journal")
	}
	trusted, err := s.TrustedLaunch(ctx, state.Session)
	if err != nil || !reflect.DeepEqual(trusted, state.Launch) {
		return nil, errors.New("host recovery launch changed")
	}
	anchor, err := pins.Anchor.Stat()
	if err != nil || workspaceRootIdentity(anchor) != state.Binding.Root || len(pins.Sources) != len(state.Sources) {
		return nil, errors.New("host recovery workspace changed")
	}
	for i, pin := range pins.Sources {
		source := state.Sources[i]
		root, e := pin.Directory.Stat()
		common, ce := pin.CommonDirectory.Stat()
		if e != nil || ce != nil || pin.Member != source.Member || pin.CanonicalPath != source.Path || pin.CommonDirectoryPath != source.CommonPath || workspaceRootIdentity(root) != source.RootIdentity || workspaceRootIdentity(common) != source.CommonIdentity {
			return nil, errors.New("host recovery member changed")
		}
	}
	if state.HostChild {
		if err = state.hostChildSourcesOwned(ctx); err != nil {
			return nil, err
		}
	}
	launch, err := s.Authority.Recover(ctx, state.SupervisorLaunch, *state.Binding)
	if err != nil {
		return nil, err
	}
	if err = hostControllerGone(state.HostController); err != nil {
		return nil, err
	}
	if err = reapPreviousHostHelpers(ctx, state, record); err != nil {
		return nil, err
	}
	state.SupervisorLaunch = launch
	state.HostDriverStopped = true
	w := &LocalWorkspace{mu: workspaceLock(state.Session), record: record, state: state}
	return w, w.save()
}

// Older daemons did not persist a P1 selection snapshot. Their MAC-protected
// host journal already carries the launch's paths and directory identities.
// Reopen those descriptors, then resumeHostIntent checks every identity against
// this journal and the server's immutable manifest before any writer starts.
func (s *LocalWorkspaceService) reopenHostPins(ctx context.Context, msg protocol.SpawnSession) (*discovery.BoundPins, error) {
	record, err := workspaceRecord(msg.SessionID)
	if err != nil {
		return nil, err
	}
	state, err := readPrivateWorkspace(record)
	if err != nil {
		return nil, err
	}
	if !state.Host || state.Session != msg.SessionID || !reflect.DeepEqual(state.Launch, *msg.Workspace) {
		return nil, errors.New("host recovery journal changed")
	}
	trusted, err := s.TrustedLaunch(ctx, msg.SessionID)
	if err != nil || !reflect.DeepEqual(trusted, state.Launch) {
		return nil, errors.New("host recovery launch changed")
	}
	pins := &discovery.BoundPins{Manifest: state.Launch, AnchorPath: state.Root}
	success := false
	defer func() {
		if !success {
			pins.Close()
		}
	}()
	open := func(path string) (*os.File, error) {
		canonical, e := filepath.EvalSymlinks(path)
		if e != nil || canonical != filepath.Clean(path) {
			return nil, errors.New("host recovery path changed")
		}
		return os.Open(path) //nolint:gosec // G304: path comes from the MAC-protected launch journal; resume checks the opened directory against its saved identity.
	}
	pins.Anchor, err = open(state.Root)
	if err != nil {
		return nil, err
	}
	for _, source := range state.Sources {
		dir, e := open(source.Path)
		if e != nil {
			return nil, e
		}
		pins.Sources = append(pins.Sources, discovery.SourcePin{Member: source.Member, CanonicalPath: source.Path, CommonDirectoryPath: source.CommonPath, Directory: dir})
		common, e := open(source.CommonPath)
		if e != nil {
			return nil, e
		}
		pins.Sources[len(pins.Sources)-1].CommonDirectory = common
	}
	success = true
	return pins, nil
}

// reapPreviousHostHelpers ends every helper the journal records for the
// previous controller, once that controller is proven gone. A still-running
// controller owns its helpers: nothing is reaped and recovery is refused.
func reapPreviousHostHelpers(ctx context.Context, state workspacePrivateManifest, record string) error {
	if err := hostControllerGone(state.HostController); err != nil {
		return err
	}
	if !state.HostHelpersJournaled {
		return nil
	}
	helpers, err := readPrivateWorkspace(record + ".helpers")
	if err != nil || helpers.RuntimeToken != state.RuntimeToken {
		return errors.New("host helper history unavailable")
	}
	for _, helper := range helpers.HostHelpers {
		if err = reapHostHelper(ctx, helper); err != nil {
			return err
		}
	}
	return nil
}

// hostChildSourcesOwned proves a resuming child's directories are its own
// journalled worktrees: each source is an unreleased, validated allocation
// beneath the child's private storage, so a resume can never land in the
// parent's checkout.
func (state workspacePrivateManifest) hostChildSourcesOwned(ctx context.Context) error {
	storage := filepath.Join(DaemonStateDir(), "host-children", state.Session)
	if len(state.Sources) == 0 || len(state.Sources) != len(state.HostChildren) {
		return errors.New("host child worktrees unavailable")
	}
	for _, source := range state.Sources {
		var owned *hostChildAllocation
		for i := range state.HostChildren {
			if state.HostChildren[i].MemberID == source.Member.ID {
				owned = &state.HostChildren[i]
				break
			}
		}
		if owned == nil || owned.Released || owned.Path != source.Path || owned.Root != source.RootIdentity {
			return errors.New("host child source is not its own worktree")
		}
		if rel, err := filepath.Rel(storage, source.Path); err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("host child worktree outside its storage")
		}
		if err := owned.validate(ctx); err != nil {
			return fmt.Errorf("host child worktree %s: %w", owned.MemberID, err)
		}
	}
	return nil
}
