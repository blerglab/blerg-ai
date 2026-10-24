package runner

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/clusterworkspace"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// ClusterServices is implemented by P4/P5 in the owning pod process. Shell
// implementations verify pinned signing keys, live grants and leases before
// admission through Runtime. They own PTY containment, draining and reaping.
// Neither method may dispatch an engine input or use Kubernetes exec.
// Cancel must synchronously fence delivery and cancel outstanding operations;
// Close joins them and closes the backend's held root descriptors.
type ClusterServices interface {
	Explorer(context.Context, json.RawMessage) (json.RawMessage, error)
	Shell(context.Context, json.RawMessage, func(json.RawMessage) error) error
	Cancel()
	Close(context.Context) error
}

type ClusterServicesFactory func(publicRoot, privateRoot string, manifest protocol.WorkspaceManifest, binding protocol.WorkspaceRuntimeBinding, runtime *clusterworkspace.Runtime) (ClusterServices, error)

type clusterRequestLink interface {
	OnMessage(string, func([]byte))
	SendBounded(context.Context, any) error
}

type clusterRequest struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
	protocol.WorkspaceRuntimeRef
}

type clusterRequests struct {
	delivery       context.Context
	cancelDelivery context.CancelFunc
	mu             sync.Mutex
	session        string
	ref            protocol.WorkspaceRuntimeRef
	runtime        *clusterworkspace.Runtime
	services       ClusterServices
	link           clusterRequestLink
	active         map[string]context.CancelFunc
	explorer       chan struct{}
	shells         chan struct{}
	ctx            context.Context
	cancel         context.CancelFunc
	closed         bool
	epoch          uint64
	wg             sync.WaitGroup
}

func newClusterRequests(ctx context.Context, id string, ref protocol.WorkspaceRuntimeRef, runtime *clusterworkspace.Runtime, services ClusterServices, link clusterRequestLink) *clusterRequests {
	ctx, cancel := context.WithCancel(ctx)
	r := &clusterRequests{session: id, ref: ref, runtime: runtime, services: services, link: link, active: map[string]context.CancelFunc{}, explorer: make(chan struct{}, 1), shells: make(chan struct{}, 4), ctx: ctx, cancel: cancel}
	r.delivery, r.cancelDelivery = context.WithCancel(ctx)
	for _, kind := range []string{"workspace_explorer", "cancel_workspace_explorer", "owner_shell", "cancel_owner_shell"} {
		link.OnMessage(kind, r.handle)
	}
	return r
}

func (r *clusterRequests) reply(parent context.Context, q clusterRequest, raw json.RawMessage, code string, epoch uint64) error {
	r.mu.Lock()
	if r.closed || r.epoch != epoch || r.ctx.Err() != nil {
		r.mu.Unlock()
		return context.Canceled
	}
	delivery := r.delivery
	r.mu.Unlock()
	limit := 1 << 20
	kind := "workspace_explorer_result"
	if q.Type == "owner_shell" {
		limit = 32 << 10
		kind = "owner_shell_result"
	}
	var fields map[string]json.RawMessage
	if len(raw) > limit || (len(raw) > 0 && json.Unmarshal(raw, &fields) != nil) {
		code = "unavailable"
	}
	if fields == nil || code != "" {
		fields = map[string]json.RawMessage{}
	}
	set := func(key string, value any) { fields[key], _ = json.Marshal(value) }
	set("type", kind)
	set("request_id", q.RequestID)
	set("session_id", r.session)
	set("launch_generation", r.ref.LaunchGeneration)
	set("generation", r.ref.Generation)
	if code != "" {
		set("ok", false)
		set("error_code", code)
	}
	encoded, err := json.Marshal(fields)
	if err != nil || len(encoded) > limit {
		return errors.New("runtime reply budget exceeded")
	}
	ctx, cancel := context.WithTimeout(delivery, 2*time.Second)
	stop := context.AfterFunc(parent, cancel)
	defer stop()
	defer cancel()
	return r.link.SendBounded(ctx, json.RawMessage(encoded)) //nolint:contextcheck // Inherit link cancellation synchronously; parent cancellation is additionally wired above.
}

func (r *clusterRequests) handle(raw []byte) {
	var q clusterRequest
	if len(raw) > 32<<10 || json.Unmarshal(raw, &q) != nil || q.SessionID != r.session || q.WorkspaceRuntimeRef != r.ref || len(q.RequestID) == 0 || len(q.RequestID) > 128 {
		return
	}
	r.mu.Lock()
	epoch := r.epoch
	if r.closed {
		r.mu.Unlock()
		return
	}
	if q.Type == "cancel_workspace_explorer" || q.Type == "cancel_owner_shell" {
		if cancel := r.active[q.RequestID]; cancel != nil {
			cancel()
		}
		r.mu.Unlock()
		return
	}
	slots := r.explorer
	if q.Type == "owner_shell" {
		slots = r.shells
	} else if q.Type != "workspace_explorer" {
		r.mu.Unlock()
		return
	}
	if r.services == nil || !r.runtime.Prepared() {
		r.mu.Unlock()
		_ = r.reply(r.ctx, q, nil, "unavailable", epoch)
		return
	}
	if _, exists := r.active[q.RequestID]; exists {
		r.mu.Unlock()
		return
	}
	select {
	case slots <- struct{}{}:
	default:
		r.mu.Unlock()
		_ = r.reply(r.ctx, q, nil, "busy", epoch)
		return
	}
	timeout := 2 * time.Second
	if q.Type == "owner_shell" {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.ctx, timeout)
	r.active[q.RequestID] = cancel
	r.wg.Add(1)
	r.mu.Unlock()
	// Own the request bytes; websocket pumps may reuse their input buffer.
	raw = append([]byte(nil), raw...)
	go func() {
		defer r.wg.Done()
		defer func() { cancel(); <-slots; r.mu.Lock(); delete(r.active, q.RequestID); r.mu.Unlock() }()
		emit := func(reply json.RawMessage) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return r.reply(ctx, q, reply, "", epoch)
		}
		var err error
		if q.Type == "owner_shell" {
			err = r.services.Shell(ctx, raw, emit)
		} else {
			var reply json.RawMessage
			reply, err = r.services.Explorer(ctx, raw)
			if err == nil {
				err = emit(reply)
			}
		}
		if err != nil && ctx.Err() == nil {
			_ = r.reply(ctx, q, nil, "unavailable", epoch)
		}
	}()
}

// Disconnect invalidates every in-flight reply before cancellation. Backends
// retain uncertain shell cleanup; transport loss is never a no-writers proof.
func (r *clusterRequests) Disconnect() {
	r.mu.Lock()
	r.epoch++
	r.cancelDelivery()
	r.delivery, r.cancelDelivery = context.WithCancel(r.ctx)
	services := r.services
	for _, cancel := range r.active {
		cancel()
	}
	r.mu.Unlock()
	if services != nil {
		services.Cancel()
	}
}
func (r *clusterRequests) Close(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	r.cancel()
	r.mu.Unlock()
	r.Disconnect()
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	if r.services != nil {
		return r.services.Close(ctx)
	}
	return nil
}
