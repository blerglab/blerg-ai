package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/blerglab/blerg-ai/runner/internal/discovery"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func (m *Manager) handleWorkspaceExplorer(raw []byte) {
	var envelope struct {
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return
	}
	if m.agents != nil && m.agents.get(envelope.SessionID) != nil {
		m.agents.HandleWorkspaceExplorer(context.Background(), raw)
		return
	}
	m.explorer.Handle(context.Background(), m.client, raw)
}

// The lifecycle mutex is acquired without waiting: router cancellation/join may
// itself run while Sync/Stop owns it. No queued reader can deadlock that join or
// start a Docker exec after freeze. Authority is checked at entry and delivery.
type boundWorkspaceExplorer struct {
	workspace *LocalWorkspace
	remote    *remoteWorkspaceExplorer
}

func (w *LocalWorkspace) runtimeExplorer(ctx context.Context, d *DockerWorkspaceContainer) (WorkspaceExplorer, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state.Binding == nil || w.authorize == nil || w.runtime != d {
		return nil, errors.New("workspace explorer binding unavailable")
	}
	if err := w.authorize(ctx); err != nil {
		return nil, err
	}
	binding := *w.state.Binding
	start := WorkspaceExplorerStart{SessionID: w.state.Session, Manifest: w.state.Launch, Runtime: binding.WorkspaceRuntimeRef, Binding: &binding, GitTargets: map[string]string{}}
	for _, member := range w.state.Members {
		start.GitTargets[member.Member.ID] = member.Source.Objects
	}
	remote := newDockerWorkspaceExplorer(d.Context, d.ID, start)
	if err := remote.startHelper(ctx); err != nil {
		return nil, err
	}
	return &boundWorkspaceExplorer{workspace: w, remote: remote}, nil
}
func (r *boundWorkspaceExplorer) run(ctx context.Context, ref protocol.WorkspaceRuntimeRef, operation func()) string {
	w := r.workspace
	if !w.mu.TryLock() {
		return "busy"
	}
	defer w.mu.Unlock()
	if !ref.Matches(w.state.Binding) || w.state.Stopped || w.state.StopRequested || w.state.Unavailable || w.authorize == nil || ctx.Err() != nil || w.authorize(ctx) != nil {
		return "unavailable"
	}
	operation()
	if r.remote.failed {
		w.state.Unavailable = true
		_ = w.save()
		return "unavailable"
	}
	if ctx.Err() != nil || w.authorize(ctx) != nil {
		return "unavailable"
	}
	return ""
}
func (r *boundWorkspaceExplorer) Explore(ctx context.Context, req protocol.WorkspaceExplorerRequest) protocol.WorkspaceExplorerResult {
	result := explorerFailure(req, "unavailable")
	if code := r.run(ctx, req.WorkspaceRuntimeRef, func() { result = r.remote.Explore(ctx, req) }); code != "" {
		return explorerFailure(req, code)
	}
	return result
}
func (r *boundWorkspaceExplorer) ReadFile(ctx context.Context, req protocol.ReadWorkspaceFile) protocol.WorkspaceFileResult {
	failed := protocol.WorkspaceFileResult{Type: "workspace_file_result", RequestID: req.RequestID, SessionID: req.SessionID, WorkspaceRuntimeRef: req.WorkspaceRuntimeRef, ErrorCode: "unavailable"}
	result := failed
	if code := r.run(ctx, req.WorkspaceRuntimeRef, func() {
		inner := req
		if strings.HasPrefix(req.Path, "/") {
			name, ok := runtimeFileReference(req.Path, sandboxWorkdir, r.workspace.state.Root)
			if !ok {
				result.ErrorCode = "forbidden"
				return
			}
			inner.Path = name
		}
		result = r.remote.ReadFile(ctx, inner)
		if strings.HasPrefix(req.Path, "/") {
			result.RequestedPath = filepath.Clean(req.Path)
		}
	}); code != "" {
		failed.ErrorCode = code
		return failed
	}
	return result
}

// CloseForLifecycle runs under P2's transaction lock. The caller persists any
// close error and retains storage; reacquiring that lock here would deadlock.
func (r *boundWorkspaceExplorer) CloseForLifecycle(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.remote.Close()
}

func (r *boundWorkspaceExplorer) Close() error {
	err := r.remote.Close()
	if err != nil {
		w := r.workspace
		w.mu.Lock()
		w.state.Unavailable = true
		_ = w.save()
		w.mu.Unlock()
	}
	return err
}

// Host readers use the real P1 handoff, never browser paths or manifest.Root.
// The private namespace projects synthetic members; a physical folder remains
// a normal descriptor-backed tree including its non-repository files.
type authorizedHostExplorer struct {
	workspace *LocalWorkspace
	mu        sync.Mutex
	reader    WorkspaceExplorer
	authority WorkspaceRuntimeAuthority
	launch    protocol.SupervisorLaunch
	binding   protocol.WorkspaceRuntimeBinding
	aliases   map[string]string
	closePins func()
	closed    bool
}

func (r *authorizedHostExplorer) current(ctx context.Context) bool {
	if r.closed || ctx.Err() != nil {
		return false
	}
	if r.authority.Current(ctx, r.launch, r.binding) == nil {
		return true
	}
	launch, err := r.authority.Recover(ctx, r.launch, r.binding)
	if err != nil {
		return false
	}
	r.launch = launch
	return r.authority.Current(ctx, r.launch, r.binding) == nil
}
func (r *authorizedHostExplorer) Explore(ctx context.Context, req protocol.WorkspaceExplorerRequest) protocol.WorkspaceExplorerResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	if req.SessionID != r.launch.SessionID || !req.Matches(&r.binding) || !r.current(ctx) {
		return explorerFailure(req, "unavailable")
	}
	result := r.reader.Explore(ctx, req)
	if !r.current(ctx) {
		return explorerFailure(req, "unavailable")
	}
	return result
}
func (r *authorizedHostExplorer) ReadFile(ctx context.Context, req protocol.ReadWorkspaceFile) protocol.WorkspaceFileResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	failed := protocol.WorkspaceFileResult{Type: "workspace_file_result", RequestID: req.RequestID, SessionID: req.SessionID, WorkspaceRuntimeRef: req.WorkspaceRuntimeRef, ErrorCode: "unavailable"}
	if req.SessionID != r.launch.SessionID || !req.Matches(&r.binding) || !r.current(ctx) {
		return failed
	}
	inner := req
	if strings.HasPrefix(req.Path, "/") {
		found := false
		for alias, prefix := range r.aliases {
			if name, ok := runtimeFileReference(req.Path, alias); ok {
				inner.Path = filepath.ToSlash(filepath.Join(prefix, name))
				found = true
				break
			}
		}
		if !found {
			failed.ErrorCode = "forbidden"
			return failed
		}
	}
	reader, ok := r.reader.(WorkspaceExplorerFileReader)
	if !ok {
		return failed
	}
	result := reader.ReadFile(ctx, inner)
	if strings.HasPrefix(req.Path, "/") {
		result.RequestedPath = filepath.Clean(req.Path)
	}
	if !r.current(ctx) {
		return failed
	}
	return result
}
func (r *authorizedHostExplorer) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if r.closePins != nil {
		defer r.closePins()
	}
	if c, ok := r.reader.(io.Closer); ok {
		return c.Close()
	}
	return nil
}
func (m *Manager) prepareHostWorkspace(ctx context.Context, msg protocol.SpawnSession) (string, WorkspaceExplorer, error) {
	return m.prepareHostWorkspaceForPlatform(ctx, msg, runtime.GOOS)
}

// Keep the platform refusal ahead of selection pinning, journaling and authority
// allocation. The seam exercises Darwin admission on Linux without enabling it.
func (m *Manager) prepareHostWorkspaceForPlatform(ctx context.Context, msg protocol.SpawnSession, platform string) (string, WorkspaceExplorer, error) {
	if !protocol.WorkspaceRuntimeEnabled("host", platform) {
		return "", nil, errors.New("workspace sessions are not supported on macOS hosts yet")
	}
	if !msg.WorkspaceCapabilities.Supports("host", false) {
		return "", nil, errors.New("host workspace runtime is unnegotiated")
	}
	service := m.config.Workspaces
	if service == nil || service.Authority == nil || msg.Workspace == nil || msg.Workspace.Runtime != "host" {
		return "", nil, errors.New("host workspace authority unavailable")
	}
	admission := workspaceAdmissionLock(msg.SessionID)
	admission.Lock()
	defer admission.Unlock()
	pins, err := m.PinWorkspaceSelection(ctx, *msg.Workspace)
	if err != nil {
		return "", nil, err
	}
	success := false
	defer func() {
		if !success {
			pins.Close()
		}
	}()
	token := ccUUID()
	intent, err := service.beginHostIntent(msg, pins, token)
	if err != nil {
		return "", nil, err
	}
	launch, err := service.Authority.Launch(ctx, msg.SessionID, *msg.Workspace, token)
	if err != nil {
		return "", nil, err
	}
	// The signed host handoff owns failures too. Keep its exact cleanup token
	// until recovery commits a receipt; aborting here would revoke that cleanup.
	if launch.Validate() != nil || launch.SessionID != msg.SessionID || launch.Runtime != "host" || launch.LaunchGeneration != pins.Manifest.LaunchGeneration {
		return "", nil, errors.New("host launch authority changed")
	}
	intent.state.SupervisorLaunch = launch
	if err = intent.save(); err != nil {
		return "", nil, err
	}
	info, err := pins.Anchor.Stat()
	if err != nil {
		return "", nil, err
	}
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: launch.LaunchGeneration, Generation: launch.RuntimeGeneration}, ManifestDigest: pins.Manifest.Digest, Root: workspaceRootIdentity(info)}
	wrapper := &authorizedHostExplorer{aliases: map[string]string{}, authority: service.Authority, launch: launch, binding: binding, closePins: func() { pins.Close(); m.ReleaseWorkspaceSelection(pins.Manifest) }}
	// P1's synthetic anchor has no physical relationship to member paths.
	synthetic := hostSelectionIsVirtual(pins)
	workdir := pins.AnchorPath
	if !synthetic {
		wrapper.aliases[pins.AnchorPath] = ""
	}
	for _, source := range pins.Sources {
		wrapper.aliases[source.CanonicalPath] = source.Member.Path
	}
	if synthetic {
		sources := map[string]HostWorkspaceSource{}
		for _, source := range pins.Sources {
			st, e := source.Directory.Stat()
			if e != nil {
				return "", nil, e
			}
			sources[source.Member.ID] = HostWorkspaceSource{Path: source.CanonicalPath, Root: workspaceRootIdentity(st)}
		}
		wrapper.reader, err = NewVirtualHostExplorer(pins.AnchorPath, pins.Manifest, binding, sources, func() bool { return !wrapper.closed })
		workdir = pins.Sources[0].CanonicalPath
	} else {
		wrapper.reader, err = NewLocalExplorer(pins.AnchorPath, pins.Manifest, binding, func() bool { return !wrapper.closed })
	}
	if err != nil {
		return "", nil, err
	}
	intent.state.BindingIntent = &binding
	if err = intent.save(); err != nil {
		_ = wrapper.Close()
		return "", nil, err
	}
	if err = service.Authority.Bind(ctx, launch, binding); err != nil {
		_ = wrapper.Close()
		return "", nil, err
	}
	if !wrapper.current(ctx) {
		_ = wrapper.Close()
		return "", nil, errors.New("host workspace authority changed")
	}
	workspace, err := service.prepareHostLifecycle(ctx, msg, pins, launch, binding, token, workdir, wrapper)
	if err != nil {
		_ = wrapper.Close()
		return "", nil, err
	}
	wrapper.workspace = workspace
	success = true
	return filepath.Clean(workdir), wrapper, nil
}

// Terminal sandboxes use the same in-container legacy reader as agent sessions.
func (m *Manager) bindTerminalExplorer(id string) {
	binding := m.workspaceFileBinding(id)
	if binding.inRuntimeOnly && binding.live != nil {
		m.BindWorkspaceExplorer(id, &legacySandboxExplorer{id: id, alias: binding.root, live: binding.live})
	}
}

func hostSelectionIsVirtual(pins *discovery.BoundPins) bool {
	for _, source := range pins.Sources {
		if filepath.Clean(source.CanonicalPath) != filepath.Join(pins.AnchorPath, filepath.FromSlash(source.Member.Path)) {
			return true
		}
	}
	return false
}
