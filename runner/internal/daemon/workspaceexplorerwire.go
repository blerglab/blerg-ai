package daemon

import (
	"context"
	"encoding/json"
	"io"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// WorkspaceExplorerRouter is a zero-value-ready fakeable runtime boundary.
// Bind only after runtime root registration succeeds. Suspend/CancelSession
// join reads before sync; Unbind also closes readers on replacement. Adapters must
// honor ctx, including cancellation of any in-container Git process group.
type WorkspaceExplorerRouter struct {
	mu       sync.Mutex
	wait     sync.WaitGroup
	closed   bool
	gitV2    map[string]bool // retain v2 identity after reader retirement
	runtimes map[string]*explorerRegistration
	pending  map[string]explorerWork
	active   int
}
type explorerRegistration struct {
	runtime   WorkspaceExplorer
	suspended bool
}
type explorerWork struct {
	session string
	cancel  context.CancelFunc
	done    chan struct{}
}

func (r *WorkspaceExplorerRouter) Bind(id string, runtime WorkspaceExplorer) {
	r.Unbind(id)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		if closer, ok := runtime.(io.Closer); ok {
			_ = closer.Close()
		}
		return
	}
	defer r.mu.Unlock()
	if r.runtimes == nil {
		r.runtimes = map[string]*explorerRegistration{}
	}
	if runtime != nil {
		if _, legacy := runtime.(*legacySandboxExplorer); !legacy {
			if r.gitV2 == nil {
				r.gitV2 = map[string]bool{}
			}
			r.gitV2[id] = true
		}
		r.runtimes[id] = &explorerRegistration{runtime: runtime}
	}
}
func (r *WorkspaceExplorerRouter) Unbind(id string) {
	r.mu.Lock()
	registration := r.runtimes[id]
	delete(r.runtimes, id)
	var done []chan struct{}
	for _, work := range r.pending {
		if work.session == id {
			work.cancel()
			done = append(done, work.done)
		}
	}
	r.mu.Unlock()
	for _, finished := range done {
		<-finished
	}
	if registration != nil {
		if closer, ok := registration.runtime.(io.Closer); ok {
			_ = closer.Close()
		}
	}
}

// CancelAll joins outstanding reads after disconnect without discarding the
// still-pinned runtime registrations. A reconnect must reauthorize them server-side.
func (r *WorkspaceExplorerRouter) CancelAll() {
	r.mu.Lock()
	var done []chan struct{}
	for _, work := range r.pending {
		work.cancel()
		done = append(done, work.done)
	}
	r.mu.Unlock()
	for _, finished := range done {
		<-finished
	}
}

// CancelSession joins readers without discarding pinned registration. Lifecycle
// adapters serialize new reads against freeze/stop with their session mutex.
func (r *WorkspaceExplorerRouter) CancelSession(id string) {
	_ = r.CancelSessionContext(context.Background(), id)
}

// CancelSessionContext is shared by ordinary cancellation and the coordinator's
// suspended lifecycle boundary. It retains the reader instead of closing it.
func (r *WorkspaceExplorerRouter) CancelSessionContext(ctx context.Context, id string) error {
	r.mu.Lock()
	var done []chan struct{}
	for _, work := range r.pending {
		if work.session == id {
			work.cancel()
			done = append(done, work.done)
		}
	}
	r.mu.Unlock()
	for _, finished := range done {
		select {
		case <-finished:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return ctx.Err()
}
func (r *WorkspaceExplorerRouter) Handle(parent context.Context, sender Sender, raw []byte) {
	if len(raw) > 16384 {
		return
	}
	var req protocol.WorkspaceExplorerRequest
	if json.Unmarshal(raw, &req) != nil || req.RequestID == "" || len(req.RequestID) > 100 || req.SessionID == "" || len(req.SessionID) > 100 {
		return
	}
	r.mu.Lock()
	if req.Type == "cancel_workspace_explorer" {
		if work, ok := r.pending[req.RequestID]; ok && work.session == req.SessionID {
			work.cancel()
		}
		r.mu.Unlock()
		return
	}
	runtime := r.runtimes[req.SessionID]
	if runtime == nil || runtime.suspended || r.active >= 2 {
		code := "unavailable"
		if runtime != nil && !runtime.suspended {
			code = "busy"
		}
		r.mu.Unlock()
		queueExplorerRefusal(sender, explorerFailure(req, code))
		return
	}
	if _, ok := r.pending[req.RequestID]; ok {
		r.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	if r.pending == nil {
		r.pending = map[string]explorerWork{}
	}
	done := make(chan struct{})
	r.pending[req.RequestID] = explorerWork{session: req.SessionID, cancel: cancel, done: done}
	r.active++
	r.wait.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wait.Done()
		defer close(done)
		defer func() { cancel(); r.mu.Lock(); delete(r.pending, req.RequestID); r.active--; r.mu.Unlock() }()
		res := runtime.runtime.Explore(ctx, req)
		r.mu.Lock()
		same := r.runtimes[req.SessionID] == runtime && !runtime.suspended
		r.mu.Unlock()
		if ctx.Err() != nil || !same {
			return
		}
		if contextual, ok := sender.(interface {
			SendWorkspaceExplorerResultContext(context.Context, protocol.WorkspaceExplorerResult) error
		}); ok {
			_ = contextual.SendWorkspaceExplorerResultContext(ctx, res)
		} else if bounded, ok := sender.(interface {
			SendWorkspaceExplorerResult(protocol.WorkspaceExplorerResult) error
		}); ok {
			_ = bounded.SendWorkspaceExplorerResult(res)
		} else {
			_ = sender.Send(res)
		}
	}()
}
func (c *WSClient) SendWorkspaceExplorerResult(res protocol.WorkspaceExplorerResult) error {
	return c.SendWorkspaceExplorerResultContext(context.Background(), res)
}
func (m *Manager) BindWorkspaceExplorer(id string, runtime WorkspaceExplorer) {
	// A host daemon must use an in-runtime adapter for sandbox/pod reads.
	// Matching bind-mount inode numbers do not make a host backend acceptable.
	if local, ok := runtime.(*LocalExplorer); ok && local.manifest.Runtime != "host" {
		m.explorer.Bind(id, nil)
		_ = local.Close()
		return
	}
	m.explorer.Bind(id, runtime)
}
func (h *AgentHost) BindWorkspaceExplorer(id string, runtime WorkspaceExplorer) {
	h.explorer.Bind(id, runtime)
}
func (h *AgentHost) HandleWorkspaceExplorer(ctx context.Context, raw []byte) {
	h.explorer.Handle(ctx, h.sender, raw)
}

// WorkspaceExplorerFileReader preserves the existing viewer response while
// ensuring the bytes are opened in the same runtime as explorer traversal.
type WorkspaceExplorerFileReader interface {
	ReadFile(context.Context, protocol.ReadWorkspaceFile) protocol.WorkspaceFileResult
}

func (e *LocalExplorer) ReadFile(ctx context.Context, req protocol.ReadWorkspaceFile) protocol.WorkspaceFileResult {
	res := protocol.WorkspaceFileResult{ErrorCode: "unavailable"}
	clean, allowed := workspaceCleanRequest(req.Path)
	if ctx.Err() == nil && allowed && workspacePublicPath(clean) && req.LaunchGeneration == e.manifest.LaunchGeneration && req.Generation == e.generation && e.valid() && e.memberAvailable(clean) {
		root, relative, member := e.anchorFor(clean)
		if root != nil {
			folder := e.folder
			limits := parseWorkspaceFileLimits()
			limits.expectedRoot = e.info
			limits.pinnedRoot = root
			if member != nil {
				folder = member.folder
				limits.expectedRoot = member.info
			}
			res = readWorkspaceFile(folder, relative, limits)
			res.Path = clean
			if ctx.Err() != nil || !e.valid() || !e.memberAvailable(clean) {
				res = protocol.WorkspaceFileResult{ErrorCode: "unavailable"}
			}
		}
	}
	res.Type = "workspace_file_result"
	res.RequestID = req.RequestID
	res.SessionID = req.SessionID
	res.WorkspaceRuntimeRef = req.WorkspaceRuntimeRef
	return res
}
func (r *WorkspaceExplorerRouter) HandleFile(parent context.Context, sender Sender, req protocol.ReadWorkspaceFile) {
	if req.RequestID == "" || len(req.RequestID) > 100 || req.SessionID == "" || len(req.SessionID) > 100 {
		return
	}
	r.mu.Lock()
	if req.Type == "cancel_workspace_file" {
		if work, ok := r.pending[req.RequestID]; ok && work.session == req.SessionID {
			work.cancel()
		}
		r.mu.Unlock()
		return
	}
	registration := r.runtimes[req.SessionID]
	var reader WorkspaceExplorerFileReader
	if registration != nil && !registration.suspended {
		reader, _ = registration.runtime.(WorkspaceExplorerFileReader)
	}
	if reader == nil {
		r.mu.Unlock()
		res := protocol.WorkspaceFileResult{Type: "workspace_file_result", RequestID: req.RequestID, SessionID: req.SessionID, WorkspaceRuntimeRef: req.WorkspaceRuntimeRef, ErrorCode: "unavailable"}
		queueExplorerRefusal(sender, res)
		return
	}
	if _, ok := r.pending[req.RequestID]; ok {
		r.mu.Unlock()
		return
	}
	select {
	case workspaceFileReads <- struct{}{}:
	default:
		r.mu.Unlock()
		return
	}
	ctx, cancel := context.WithTimeout(parent, workspaceReplyTTL)
	if r.pending == nil {
		r.pending = map[string]explorerWork{}
	}
	done := make(chan struct{})
	r.pending[req.RequestID] = explorerWork{session: req.SessionID, cancel: cancel, done: done}
	r.wait.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.wait.Done()
		defer close(done)
		defer func() { <-workspaceFileReads; cancel(); r.mu.Lock(); delete(r.pending, req.RequestID); r.mu.Unlock() }()
		res := reader.ReadFile(ctx, req)
		r.mu.Lock()
		same := r.runtimes[req.SessionID] == registration && !registration.suspended
		r.mu.Unlock()
		if ctx.Err() != nil || !same {
			return
		}
		if contextual, ok := sender.(interface {
			SendWorkspaceFileResultContext(context.Context, protocol.WorkspaceFileResult, time.Time) error
		}); ok {
			deadline, _ := ctx.Deadline()
			_ = contextual.SendWorkspaceFileResultContext(ctx, res, deadline)
		} else if bounded, ok := sender.(interface {
			SendWorkspaceFileResultBy(protocol.WorkspaceFileResult, time.Time) error
		}); ok {
			_ = bounded.SendWorkspaceFileResultBy(res, time.Now().Add(time.Second))
		} else {
			_ = sender.Send(res)
		}
	}()
}

// Close cancels runtime work and joins readers/Git before a coordinator freezes
// or releases storage. It does not claim containment of agent/shell writers.
func (r *WorkspaceExplorerRouter) Close() {
	r.mu.Lock()
	r.closed = true
	registrations := r.runtimes
	r.runtimes = nil
	for _, work := range r.pending {
		work.cancel()
	}
	r.mu.Unlock()
	r.wait.Wait()
	for _, reg := range registrations {
		if closer, ok := reg.runtime.(io.Closer); ok {
			_ = closer.Close()
		}
	}
}

func (h *AgentHost) CancelWorkspaceExplorerWork() { h.explorer.CancelAll() }

func (m *Manager) UnbindWorkspaceExplorer(id string)   { m.explorer.Unbind(id) }
func (h *AgentHost) UnbindWorkspaceExplorer(id string) { h.explorer.Unbind(id) }

var workspaceExplorerRefusals = make(chan struct{}, 2)

func queueExplorerRefusal(sender Sender, value any) {
	select {
	case workspaceExplorerRefusals <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-workspaceExplorerRefusals }()
		if bounded, ok := sender.(interface {
			sendBoundedWorkspaceReply(any, time.Duration) error
		}); ok {
			_ = bounded.sendBoundedWorkspaceReply(value, time.Second)
		} else if sender != nil {
			_ = sender.Send(value)
		}
	}()
}

func (c *WSClient) SendWorkspaceExplorerResultContext(ctx context.Context, res protocol.WorkspaceExplorerResult) error {
	return c.queuedWorkspaceReply(ctx, res, false, time.Now().Add(2*time.Second))
}
func (c *WSClient) SendWorkspaceFileResultContext(ctx context.Context, res protocol.WorkspaceFileResult, until time.Time) error {
	return c.queuedWorkspaceReply(ctx, res, true, until)
}
func (c *WSClient) queuedWorkspaceReply(ctx context.Context, value any, viewer bool, until time.Time) error {
	until = workspaceOutputDeadline(ctx, until)
	ctx, cancel := context.WithDeadline(ctx, until)
	defer cancel()
	conn := c.currentConn()
	return c.workspaceOutput.send(ctx, viewer, func() error {
		if conn == nil || c.currentConn() != conn {
			return context.Canceled
		}
		remaining := time.Until(until)
		timeout := 2 * time.Second
		if viewer {
			timeout = 10 * time.Second
		}
		return c.sendBoundedReplyContext(ctx, conn, value, min(remaining, timeout), remaining, until)
	})
}
