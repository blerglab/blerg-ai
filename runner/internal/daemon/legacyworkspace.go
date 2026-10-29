package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/ownershell"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/shellruntime"
)

var (
	errLegacyProjectMissing = errors.New("session is still resuming or has no project directory")
	errLegacyProjectChanged = errors.New("project directory is missing or changed")
	errLegacyUnsupported    = errors.New("daemon does not support this workspace runtime")
	errLegacyOwnership      = errors.New("session workspace ownership could not be verified")
)

func legacyAttachmentCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errLegacyProjectMissing):
		return "project_missing"
	case errors.Is(err, errLegacyProjectChanged):
		return "project_changed"
	case errors.Is(err, errLegacyUnsupported):
		return "unsupported"
	case errors.Is(err, errLegacyOwnership):
		return "ownership_changed"
	default:
		return "attach_failed"
	}
}

// A companion owns only explorer descriptors and shell helpers. The existing
// engine/terminal keeps its recovery record and lifecycle.
type legacySessionWorkspace struct {
	session         string
	mu              sync.Mutex
	reader          WorkspaceExplorer
	binding         protocol.WorkspaceRuntimeBinding
	manifest        protocol.WorkspaceManifest
	source          workspaceFileBinding
	authorize       func(context.Context, protocol.WorkspaceRoot) (protocol.LocalWorkspaceControlResult, error)
	shells          *shellruntime.Service
	closed          bool
	shellRegistered bool
	onClose         func()
}

func (l *legacySessionWorkspace) current(ctx context.Context) bool {
	if l.closed || l.source.live == nil || !l.source.live() || ctx.Err() != nil {
		return false
	}
	reply, err := l.authorize(ctx, l.binding.Root)
	return err == nil && reply.OK && reply.Binding == l.binding && reply.Manifest != nil && reply.Manifest.Digest == l.manifest.Digest && l.source.live()
}
func (l *legacySessionWorkspace) Explore(ctx context.Context, req protocol.WorkspaceExplorerRequest) protocol.WorkspaceExplorerResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	if req.SessionID != l.session || req.WorkspaceRuntimeRef != l.binding.WorkspaceRuntimeRef || !l.current(ctx) {
		return explorerFailure(req, "unavailable")
	}
	result := l.reader.Explore(ctx, req)
	if !l.current(ctx) {
		return explorerFailure(req, "unavailable")
	}
	return result
}
func (l *legacySessionWorkspace) ReadFile(ctx context.Context, req protocol.ReadWorkspaceFile) protocol.WorkspaceFileResult {
	l.mu.Lock()
	defer l.mu.Unlock()
	failed := protocol.WorkspaceFileResult{Type: "workspace_file_result", RequestID: req.RequestID, SessionID: req.SessionID, WorkspaceRuntimeRef: req.WorkspaceRuntimeRef, ErrorCode: "unavailable"}
	if req.SessionID != l.session || req.WorkspaceRuntimeRef != l.binding.WorkspaceRuntimeRef || !l.current(ctx) {
		return failed
	}
	inner := req
	name, ok := runtimeFileReference(req.Path, l.source.root, l.source.alias)
	if !ok {
		failed.ErrorCode = "forbidden"
		return failed
	}
	inner.Path = name
	reader, ok := l.reader.(WorkspaceExplorerFileReader)
	if !ok {
		return failed
	}
	result := reader.ReadFile(ctx, inner)
	if filepath.IsAbs(req.Path) {
		result.RequestedPath = filepath.Clean(req.Path)
	}
	if !l.current(ctx) {
		return failed
	}
	return result
}
func (l *legacySessionWorkspace) Close() error { return l.closeContext(context.Background()) }

func (l *legacySessionWorkspace) closeContext(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	var err error
	if l.shellRegistered {
		b, e := l.shells.Binding(l.session)
		if e != nil {
			err = e
		} else {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			_, err = l.shells.Retire(ctx, ownershell.SupervisorIdentity(b, ownershell.SignedGrant{}).Launch)
			cancel()
			if err == nil {
				l.shellRegistered = false
			}
		}
	}
	if closer, ok := l.reader.(io.Closer); ok {
		err = errors.Join(err, closer.Close())
	}
	if err == nil && l.onClose != nil {
		l.onClose()
	}
	return err
}

func (m *Manager) handleAttachSessionWorkspace(raw []byte) {
	var req shellruntime.RPC
	if len(raw) > 4096 || json.Unmarshal(raw, &req) != nil || req.Type != "attach_session_workspace" || req.Op != "attach_workspace" || req.Version != 1 || req.SessionID == "" {
		return
	}
	select {
	case legacyWorkspaceSlots <- struct{}{}:
	default:
		// Attachment is idempotent: the server pauses briefly and asks again.
		replyBusy("legacy_attach", func() error {
			return m.client.Send(shellruntime.RPC{Type: "owner_shell_result", Version: 1, SessionID: req.SessionID, RequestID: req.RequestID, Op: req.Op, ErrorCode: "busy"})
		})
		return
	}
	go func() {
		defer func() { <-legacyWorkspaceSlots }()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := m.attachLegacyWorkspace(ctx, req.SessionID)
		_ = m.client.Send(shellruntime.RPC{Type: "owner_shell_result", Version: 1, SessionID: req.SessionID, RequestID: req.RequestID, Op: req.Op, OK: err == nil, ErrorCode: legacyAttachmentCode(err)})
	}()
}

var legacyWorkspaceSlots = make(chan struct{}, 2)

func (m *Manager) attachLegacyWorkspace(ctx context.Context, id string) error {
	client, ok := m.client.(*WSClient)
	if !ok || m.config.Workspaces == nil {
		return errors.New("workspace service is not ready")
	}
	authority, ok := m.config.Workspaces.Authority.(*WorkspaceAuthority)
	if !ok {
		return errors.New("workspace authority is not ready")
	}
	authorize := func(ctx context.Context, root protocol.WorkspaceRoot) (protocol.LocalWorkspaceControlResult, error) {
		return authority.call(ctx, protocol.LocalWorkspaceControl{Operation: "legacy_attach", SessionID: id, Binding: protocol.WorkspaceRuntimeBinding{Root: root}})
	}
	if err := client.WaitWorkspace(ctx); err != nil {
		return errLegacyUnsupported
	}
	caps, _ := client.negotiatedWorkspace.Load().(protocol.WorkspaceCapabilities)
	return m.attachLegacyWorkspaceWith(ctx, id, caps, client.OwnerShellRuntimeContext(ctx), authorize)
}

func (m *Manager) attachLegacyWorkspaceWith(ctx context.Context, id string, caps protocol.WorkspaceCapabilities, shells *shellruntime.Service, authorize func(context.Context, protocol.WorkspaceRoot) (protocol.LocalWorkspaceControlResult, error)) error {
	lock := workspaceAdmissionLock(id)
	if err := workspaceReadLock(ctx, lock); err != nil {
		return err
	}
	defer lock.Unlock()
	source := m.workspaceFileBinding(id)
	if source.live == nil || !source.live() {
		return errLegacyProjectMissing
	}
	runtime := "host"
	if source.inRuntimeOnly {
		runtime = "sandbox"
	}
	if !caps.Supports(runtime, false) {
		return errLegacyUnsupported
	}
	m.legacyMu.Lock()
	previous := m.legacyWorkspaces[id]
	m.legacyMu.Unlock()
	if previous != nil {
		previous.mu.Lock()
		defer previous.mu.Unlock()
		if !previous.current(ctx) {
			return errLegacyProjectChanged
		}
		if caps.Supports(runtime, true) && !previous.shellRegistered {
			reply, err := authorize(ctx, previous.binding.Root)
			if err != nil || !reply.OK {
				return errLegacyOwnership
			}
			remote, _ := previous.reader.(*remoteWorkspaceExplorer)
			return m.registerLegacyShell(ctx, previous, remote, reply, shells)
		}
		return nil
	}
	// Never replace a v2 launch registration.
	router := &m.explorer
	if m.agents != nil && m.agents.get(id) != nil {
		router = &m.agents.explorer
	}
	if m.agents != nil {
		if session := m.agents.get(id); session != nil && session.workspaceV2 {
			return nil
		}
	}
	router.mu.Lock()
	entry := router.runtimes[id]
	modern := false
	if entry != nil {
		_, legacy := entry.runtime.(*legacySandboxExplorer)
		modern = !legacy
	}
	router.mu.Unlock()
	if modern {
		return nil
	}
	root := workspaceRootIdentity(source.info)
	var remote *remoteWorkspaceExplorer
	if source.inRuntimeOnly {
		legacy := &legacySandboxExplorer{id: id, alias: source.root, live: source.live}
		if m.legacySandboxReader != nil {
			remote = m.legacySandboxReader(ctx, id, source)
		} else {
			remote = legacy.reader(ctx)
			clone, err := loadSandboxClone(id)
			if err != nil {
				return errors.New("sandbox clone authority is missing")
			}
			if remote != nil {
				remote.start.GitTargets = map[string]string{"repo": clone.Objects}
			}
		}
		if remote == nil || remote.startHelper(ctx) != nil {
			_ = legacy.Close()
			return errors.New("sandbox workspace helper is not ready")
		}
		root = remote.expected.Binding.Root
		if err := remote.stopHelper(); err != nil {
			return err
		}
	} else {
		// As with scalar file reads (#165), resolve the launch alias only
		// against its pinned target, then traverse the canonical descriptor.
		alias := source.root
		canonical, err := filepath.EvalSymlinks(alias)
		if err != nil {
			return errLegacyProjectChanged
		}
		info, err := os.Lstat(canonical)
		if err != nil || source.info == nil || !info.IsDir() || !os.SameFile(source.info, info) || !workspacePublicRoot(canonical) {
			return errLegacyProjectChanged
		}
		live, pinned := source.live, source.info
		source.root, source.alias = canonical, alias
		source.live = func() bool {
			if !live() {
				return false
			}
			current, err := os.Stat(alias)
			return err == nil && current.IsDir() && os.SameFile(pinned, current)
		}
		if !source.live() {
			return errLegacyProjectChanged
		}
	}
	reply, err := authorize(ctx, root)
	if err != nil || !reply.OK || reply.Manifest == nil || reply.Binding.Validate(*reply.Manifest) != nil || reply.Binding.Root != root || reply.Manifest.Runtime != runtime || reply.Manifest.LaunchGeneration != "legacy-"+id || reply.Launch.Validate() != nil || reply.Launch.LaunchGeneration != reply.Binding.LaunchGeneration || reply.Launch.RuntimeGeneration != reply.Binding.Generation || reply.Launch.SessionID != id {
		return errLegacyOwnership
	}
	companion := &legacySessionWorkspace{session: id, binding: reply.Binding, manifest: *reply.Manifest, source: source, authorize: authorize, shells: shells}
	if remote != nil {
		pins := remote.expected
		pins.Binding = reply.Binding
		remote.start = WorkspaceExplorerStart{SessionID: id, Manifest: *reply.Manifest, Runtime: reply.Binding.WorkspaceRuntimeRef, Binding: &reply.Binding, GitPins: pins.Pins}
		remote.expected = pins
		companion.reader = remote
		if err = remote.startHelper(ctx); err != nil {
			_ = remote.Close()
			return err
		}
	} else {
		companion.reader, err = NewLocalExplorer(source.root, *reply.Manifest, reply.Binding, source.live)
		if err != nil {
			return err
		}
	}
	success := false
	defer func() {
		if !success {
			_ = companion.closeContext(ctx)
		}
	}()
	if caps.Supports(runtime, true) {
		if err := m.registerLegacyShell(ctx, companion, remote, reply, shells); err != nil {
			return err
		}
	}
	if !companion.current(ctx) {
		return errors.New("workspace changed during attachment")
	}
	m.legacyMu.Lock()
	if m.legacyWorkspaces == nil {
		m.legacyWorkspaces = map[string]*legacySessionWorkspace{}
	}
	m.legacyWorkspaces[id] = companion
	companion.onClose = func() {
		m.legacyMu.Lock()
		if m.legacyWorkspaces[id] == companion {
			delete(m.legacyWorkspaces, id)
		}
		m.legacyMu.Unlock()
	}
	m.legacyMu.Unlock()
	router.Bind(id, companion)
	success = true
	if !source.live() {
		router.Unbind(id)
		return errLegacyProjectMissing
	}
	return nil
}

func (m *Manager) registerLegacyShell(ctx context.Context, companion *legacySessionWorkspace, remote *remoteWorkspaceExplorer, reply protocol.LocalWorkspaceControlResult, shells *shellruntime.Service) error {
	if !reply.ShellEligible {
		return nil
	}
	if reply.Binding != companion.binding || reply.Manifest == nil || reply.Manifest.Digest != companion.manifest.Digest {
		return errLegacyOwnership
	}
	command, reconcile, err := m.legacyShellCommand(ctx, companion.session, companion.source, remote, reply, shells)
	if err != nil {
		return err
	}
	b := ownershell.Binding{Session: companion.session, Owner: reply.Manifest.OwnerID, Launch: reply.Manifest.LaunchGeneration, Connection: reply.Launch.ConnectionID, RuntimeGeneration: reply.Binding.Generation, Root: companion.binding.Root, ManifestDigest: reply.Manifest.Digest, Runtime: companion.manifest.Runtime, Folder: "repository", Live: true}
	b.Audience = ownershell.RuntimeAudience(b.Connection, b.RuntimeGeneration)
	if err = shells.RegisterWithAdmission(ctx, new(sync.Mutex), b, command, reconcile, func(ctx context.Context, start func() error) error {
		if !companion.source.live() {
			return shellruntime.ErrClosed
		}
		current, err := companion.authorize(ctx, companion.binding.Root)
		if err != nil || !current.OK || current.Binding != reply.Binding {
			return shellruntime.ErrClosed
		}
		return start()
	}); err != nil {
		return err
	}
	companion.shellRegistered = true
	companion.shells = shells
	return nil
}

func (m *Manager) legacyShellCommand(ctx context.Context, id string, source workspaceFileBinding, remote *remoteWorkspaceExplorer, reply protocol.LocalWorkspaceControlResult, shells *shellruntime.Service) (shellruntime.Command, func(context.Context) error, error) {
	reconcile := func(context.Context) error { return shells.ProveRecordedShellsEmpty(id) }
	if source.inRuntimeOnly {
		command := SandboxShellCommand(remote.container, fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), shellruntime.Identity{Cwd: sandboxWorkdir})
		command.Args = append([]string{"--context", remote.dockerContext}, command.Args...)
		command.PublicRoot, command.Root = sandboxWorkdir, reply.Binding.Root
		command.SourceLive = source.live
		physical := &DockerWorkspaceContainer{Context: remote.dockerContext, ID: remote.container}
		reconcile = func(ctx context.Context) error {
			if err := shells.ProveRecordedShellsEmpty(id); err != nil {
				return err
			}
			raw, err := physical.command(ctx, "exec", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), physical.ID, "env", "-i", "PATH=/usr/bin:/bin", "python3", "-I", "-S", "-c", workspaceShellProbe)
			if err != nil || strings.TrimSpace(string(raw)) != "empty" {
				return errors.New("sandbox orphan shell cleanup is unproved")
			}
			return nil
		}
		return command, reconcile, nil
	}
	record, err := workspaceRecord(id)
	if err != nil {
		return shellruntime.Command{}, nil, err
	}
	record += ".legacy-helpers"
	controller, err := hostHelperIdentityFor(os.Getpid())
	if err != nil {
		return shellruntime.Command{}, nil, err
	}
	if _, statErr := os.Lstat(record); statErr == nil {
		old, err := readPrivateWorkspace(record)
		if err != nil {
			return shellruntime.Command{}, nil, err
		}
		if old.Session != id || old.Launch.Digest != reply.Manifest.Digest || old.RootIdentity != reply.Binding.Root {
			return shellruntime.Command{}, nil, errors.New("shell helper history changed")
		}
		if old.HostController != controller || len(old.HostHelpers) > 0 {
			if err := hostControllerGone(old.HostController); err != nil {
				return shellruntime.Command{}, nil, err
			}
		}
		for _, helper := range old.HostHelpers {
			if err := reapHostHelper(ctx, helper); err != nil {
				return shellruntime.Command{}, nil, err
			}
		}
	} else if !os.IsNotExist(statErr) {
		return shellruntime.Command{}, nil, statErr
	}
	journal := &LocalWorkspace{mu: new(sync.Mutex), record: record, state: workspacePrivateManifest{Session: id, Launch: *reply.Manifest, RootIdentity: reply.Binding.Root, HostController: controller}}
	if err := journal.save(); err != nil {
		return shellruntime.Command{}, nil, err
	}
	identity, err := shellruntime.ResolveIdentity(source.root)
	if err != nil {
		return shellruntime.Command{}, nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return shellruntime.Command{}, nil, err
	}
	command := shellruntime.HostCommand(executable, identity)
	if m.config.Workspaces.HostCommand != nil {
		command = m.config.Workspaces.HostCommand(identity)
	}
	command.PublicRoot, command.Root = source.root, reply.Binding.Root
	// Describe revalidates the canonical root; this also rechecks the alias the
	// session was attached through, so a retargeted alias fences a running shell.
	command.SourceLive = source.live
	command.OwnedStarted = func(pid int) error {
		helper, err := hostHelperIdentityFor(pid)
		if err != nil {
			return err
		}
		journal.mu.Lock()
		defer journal.mu.Unlock()
		if len(journal.state.HostHelpers) >= 512 {
			return errors.New("shell helper history exhausted")
		}
		journal.state.HostHelpers = append(journal.state.HostHelpers, helper)
		return journal.save()
	}
	return command, reconcile, nil
}

// Called after both reconnect recovery and initial agent adoption. Requests from
// the dock/capability endpoint retry anything that was not ready at this point.
func (m *Manager) attachLegacyWorkspaces() {
	ids := []string{}
	m.mu.Lock()
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	if m.agents != nil {
		ids = append(ids, m.agents.ActiveIDs()...)
	}
	for _, id := range ids {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = m.attachLegacyWorkspace(ctx, id)
		cancel()
	}
}
