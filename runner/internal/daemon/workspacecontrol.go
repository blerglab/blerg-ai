package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/gorilla/websocket"
)

type workspaceControlWait struct {
	connection *websocket.Conn
	session    string
	reply      chan protocol.LocalWorkspaceControlResult
}

// WorkspaceAuthority is the concrete authenticated outbound adapter. A receipt
// comes only from the correlated server response, never successful submission.
type WorkspaceAuthority struct {
	client   *WSClient
	mu       sync.Mutex
	pending  map[string]workspaceControlWait
	launches map[string]protocol.SupervisorLaunch
}

func NewWorkspaceAuthority(client *WSClient) *WorkspaceAuthority {
	a := &WorkspaceAuthority{client: client, pending: map[string]workspaceControlWait{}, launches: map[string]protocol.SupervisorLaunch{}}
	client.OnMessage("local_workspace_control_result", func(raw []byte) {
		var reply protocol.LocalWorkspaceControlResult
		if len(raw) > 1<<20 || json.Unmarshal(raw, &reply) != nil {
			return
		}
		a.mu.Lock()
		wait, ok := a.pending[reply.RequestID]
		if ok && wait.session == reply.SessionID && wait.connection == client.currentConn() {
			delete(a.pending, reply.RequestID)
			wait.reply <- reply
		}
		a.mu.Unlock()
	})
	return a
}
func (a *WorkspaceAuthority) call(ctx context.Context, req protocol.LocalWorkspaceControl) (protocol.LocalWorkspaceControlResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req.Type = "local_workspace_control"
	req.RequestID = ccUUID()
	conn := a.client.currentConn()
	if conn == nil {
		return protocol.LocalWorkspaceControlResult{}, errors.New("workspace authority disconnected")
	}
	wait := workspaceControlWait{connection: conn, session: req.SessionID, reply: make(chan protocol.LocalWorkspaceControlResult, 1)}
	a.mu.Lock()
	if len(a.pending) >= 64 {
		a.mu.Unlock()
		return protocol.LocalWorkspaceControlResult{}, errors.New("workspace authority busy")
	}
	a.pending[req.RequestID] = wait
	a.mu.Unlock()
	defer func() { a.mu.Lock(); delete(a.pending, req.RequestID); a.mu.Unlock() }()
	if err := a.client.sendBoundedReplyContext(ctx, conn, req, time.Second, time.Second, time.Time{}); err != nil {
		return protocol.LocalWorkspaceControlResult{}, err
	}
	select {
	case reply := <-wait.reply:
		if !reply.OK || a.client.currentConn() != conn {
			return reply, errors.New("workspace authority refused")
		}
		return reply, nil
	case <-ctx.Done():
		return protocol.LocalWorkspaceControlResult{}, ctx.Err()
	}
}
func (a *WorkspaceAuthority) Launch(ctx context.Context, id string, m protocol.WorkspaceManifest, token string) (protocol.SupervisorLaunch, error) {
	r, e := a.call(ctx, protocol.LocalWorkspaceControl{SessionID: id, Operation: "launch", Manifest: &m, Token: token})
	if e == nil && (r.Launch.Validate() != nil || r.Launch.SessionID != id || r.Launch.LaunchGeneration != m.LaunchGeneration || r.Launch.Runtime != m.Runtime) {
		e = errors.New("invalid workspace launch response")
	}
	if e == nil {
		a.remember(r.Launch)
	}
	return r.Launch, e
}
func (a *WorkspaceAuthority) remember(l protocol.SupervisorLaunch) {
	a.mu.Lock()
	a.launches[l.SessionID] = l
	a.mu.Unlock()
}
func (a *WorkspaceAuthority) Bind(ctx context.Context, l protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding) error {
	_, e := a.call(ctx, protocol.LocalWorkspaceControl{SessionID: l.SessionID, Operation: "bind", Launch: l, Binding: b})
	return e
}
func (a *WorkspaceAuthority) Current(ctx context.Context, l protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding) error {
	_, e := a.call(ctx, protocol.LocalWorkspaceControl{SessionID: l.SessionID, Operation: "current", Launch: l, Binding: b})
	return e
}
func (a *WorkspaceAuthority) Abort(ctx context.Context, id string, m protocol.WorkspaceManifest, token string, l protocol.SupervisorLaunch) error {
	_, e := a.call(ctx, protocol.LocalWorkspaceControl{SessionID: id, Operation: "abort", Manifest: &m, Token: token, Launch: l})
	return e
}
func (a *WorkspaceAuthority) Recover(ctx context.Context, l protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding) (protocol.SupervisorLaunch, error) {
	r, e := a.call(ctx, protocol.LocalWorkspaceControl{SessionID: l.SessionID, Operation: "recover", Launch: l, Binding: b})
	if e == nil && (r.Launch.Validate() != nil || r.Launch.SessionID != l.SessionID || r.Launch.LaunchGeneration != l.LaunchGeneration || r.Launch.RuntimeGeneration != l.RuntimeGeneration || r.Launch.Runtime != l.Runtime) {
		e = errors.New("invalid workspace recovery response")
	}
	if e == nil {
		a.remember(r.Launch)
	}
	return r.Launch, e
}
func (a *WorkspaceAuthority) TrustedLaunch(ctx context.Context, id string) (protocol.WorkspaceManifest, error) {
	r, e := a.call(ctx, protocol.LocalWorkspaceControl{SessionID: id, Operation: "manifest"})
	if e == nil && (r.Manifest == nil || r.Manifest.Validate() != nil) {
		e = errors.New("invalid workspace selection response")
	}
	if e != nil {
		return protocol.WorkspaceManifest{}, e
	}
	return *r.Manifest, nil
}
func (a *WorkspaceAuthority) Results(ctx context.Context, id string, b protocol.WorkspaceRuntimeBinding, results []protocol.WorkspaceMemberResult) error {
	a.mu.Lock()
	l := a.launches[id]
	a.mu.Unlock()
	_, e := a.call(ctx, protocol.LocalWorkspaceControl{SessionID: id, Operation: "results", Launch: l, Binding: b, Results: results})
	return e
}
func (a *WorkspaceAuthority) CommitFinal(ctx context.Context, l protocol.SupervisorLaunch, f WorkspaceFinalization) (WorkspaceFinalReceipt, error) {
	r, e := a.call(ctx, protocol.LocalWorkspaceControl{SessionID: l.SessionID, Operation: "final", Launch: l, Binding: f.Binding, Final: &f})
	if e == nil && r.Receipt.Digest != f.Digest() {
		e = errors.New("workspace completion receipt mismatch")
	}
	return r.Receipt, e
}

// Cleanup methods cannot admit readers, shells or fresh runtimes on the server.
func (a *WorkspaceAuthority) CleanupCurrent(ctx context.Context, l protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding) error {
	_, e := a.call(ctx, protocol.LocalWorkspaceControl{SessionID: l.SessionID, Operation: "cleanup_current", Launch: l, Binding: b})
	return e
}
func (a *WorkspaceAuthority) CleanupRecover(ctx context.Context, l protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding) (protocol.SupervisorLaunch, error) {
	r, e := a.call(ctx, protocol.LocalWorkspaceControl{SessionID: l.SessionID, Operation: "cleanup_recover", Launch: l, Binding: b})
	if e == nil && (r.Launch.Validate() != nil || r.Launch.SessionID != l.SessionID || r.Launch.LaunchGeneration != l.LaunchGeneration || r.Launch.RuntimeGeneration != l.RuntimeGeneration || r.Launch.Runtime != l.Runtime) {
		e = errors.New("cleanup incarnation changed")
	}
	if e == nil {
		a.remember(r.Launch)
	}
	return r.Launch, e
}
func (a *WorkspaceAuthority) CleanupResults(ctx context.Context, l protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding, results []protocol.WorkspaceMemberResult) error {
	_, e := a.call(ctx, protocol.LocalWorkspaceControl{SessionID: l.SessionID, Operation: "cleanup_results", Launch: l, Binding: b, Results: results})
	return e
}

type workspaceCleanupKey struct{}

func cleanupContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, workspaceCleanupKey{}, true)
}

type workspaceCleanupAuthority interface {
	CleanupCurrent(context.Context, protocol.SupervisorLaunch, protocol.WorkspaceRuntimeBinding) error
	CleanupRecover(context.Context, protocol.SupervisorLaunch, protocol.WorkspaceRuntimeBinding) (protocol.SupervisorLaunch, error)
	CleanupResults(context.Context, protocol.SupervisorLaunch, protocol.WorkspaceRuntimeBinding, []protocol.WorkspaceMemberResult) error
}

func workspaceCurrentAuthority(ctx context.Context, a WorkspaceRuntimeAuthority, l protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding) error {
	if cleanup, _ := ctx.Value(workspaceCleanupKey{}).(bool); cleanup {
		if authority, ok := a.(workspaceCleanupAuthority); ok {
			return authority.CleanupCurrent(ctx, l, b)
		}
	}
	return a.Current(ctx, l, b)
}
func workspaceRecoverCleanup(ctx context.Context, a WorkspaceRuntimeAuthority, l protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding) (protocol.SupervisorLaunch, error) {
	if authority, ok := a.(workspaceCleanupAuthority); ok {
		return authority.CleanupRecover(ctx, l, b)
	}
	return a.Recover(ctx, l, b)
}

// Caller owns the session lock. Cleanup can reconnect only this immutable
// incarnation; it never reauthorizes a file read or shell admission.
func (s *LocalWorkspaceService) currentWorkspace(ctx context.Context, w *LocalWorkspace) error {
	if w.state.Binding == nil {
		return errors.New("workspace binding unavailable")
	}
	l, b := w.state.SupervisorLaunch, *w.state.Binding
	cleanup, _ := ctx.Value(workspaceCleanupKey{}).(bool)
	a, ok := s.Authority.(workspaceCleanupAuthority)
	if !cleanup || !ok {
		return s.Authority.Current(ctx, l, b)
	}
	if err := a.CleanupCurrent(ctx, l, b); err == nil {
		return nil
	}
	resumed, err := a.CleanupRecover(ctx, l, b)
	if err != nil {
		return err
	}
	if resumed.SessionID != l.SessionID || resumed.LaunchGeneration != l.LaunchGeneration || resumed.RuntimeGeneration != l.RuntimeGeneration || resumed.Runtime != l.Runtime {
		return errors.New("cleanup incarnation changed")
	}
	if registry, ok := s.Supervisor.(*workspaceSupervisors); ok {
		if d, ok := w.runtime.(*DockerWorkspaceContainer); ok && d.coordinator != nil {
			if err = d.coordinator.rebindStopping(ctx, resumed); err != nil {
				return err
			}
			d.SupervisorLaunch = resumed
		} else if w.state.Host {
			c, e := registry.peer(l.SessionID)
			if e != nil {
				return e
			}
			if err = c.rebind(ctx, resumed); err != nil {
				return err
			}
		}
	}
	w.state.SupervisorLaunch = resumed
	if err = w.save(); err != nil {
		return err
	}
	return a.CleanupCurrent(ctx, resumed, b)
}

type workspacePreparationCleanupAuthority interface {
	CleanupLaunch(context.Context, string, protocol.WorkspaceManifest, string) (protocol.SupervisorLaunch, error)
	CleanupBind(context.Context, protocol.SupervisorLaunch, protocol.WorkspaceRuntimeBinding) error
}

func (a *WorkspaceAuthority) CleanupLaunch(ctx context.Context, id string, m protocol.WorkspaceManifest, token string) (protocol.SupervisorLaunch, error) {
	r, e := a.call(ctx, protocol.LocalWorkspaceControl{SessionID: id, Operation: "cleanup_launch", Manifest: &m, Token: token})
	if e == nil && (r.Launch.Validate() != nil || r.Launch.SessionID != id || r.Launch.LaunchGeneration != m.LaunchGeneration || r.Launch.Runtime != m.Runtime) {
		e = errors.New("cleanup launch changed")
	}
	if e == nil {
		a.remember(r.Launch)
	}
	return r.Launch, e
}
func (a *WorkspaceAuthority) CleanupBind(ctx context.Context, l protocol.SupervisorLaunch, b protocol.WorkspaceRuntimeBinding) error {
	_, e := a.call(ctx, protocol.LocalWorkspaceControl{SessionID: l.SessionID, Operation: "cleanup_bind", Launch: l, Binding: b})
	return e
}
