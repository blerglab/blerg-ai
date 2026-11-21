package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const workerPriorityTimeout = 5 * time.Second

var (
	errWorkerPrioritySendFull   = errors.New("desktop send buffer full")
	errWorkerPriorityDaemonGone = errors.New("desktop disconnected")
	errWorkerPriorityTimeout    = errors.New("desktop reply timed out")
)

type workerPriorityWaiter struct {
	dc *DaemonConn
	ch chan protocol.WorkerPriorityResult // buffered 1; closed when dc disconnects
}

// workerPriorityRequests holds the requests in flight, keyed by request id.
type workerPriorityRequests struct {
	mu      sync.Mutex
	pending map[string]workerPriorityWaiter
}

// requestWorkerPriority sends dc a scheduling settings request and waits for the
// answer, the timeout, ctx, or dc disconnecting — whichever comes first.
func (h *Hub) requestWorkerPriority(ctx context.Context, dc *DaemonConn, enabled *bool) (protocol.WorkerPriorityResult, error) {
	id := newUUID()
	w := workerPriorityWaiter{dc: dc, ch: make(chan protocol.WorkerPriorityResult, 1)}
	r := &h.workerPriorityReqs
	r.mu.Lock()
	if r.pending == nil {
		r.pending = make(map[string]workerPriorityWaiter)
	}
	r.pending[id] = w
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.pending, id)
		r.mu.Unlock()
	}()

	data, err := json.Marshal(protocol.WorkerPriorityRequest{Type: "worker_priority", RequestID: id, Enabled: enabled})
	if err != nil {
		return protocol.WorkerPriorityResult{}, err
	}
	if !h.trySendToDaemon(dc, data) {
		return protocol.WorkerPriorityResult{}, errWorkerPrioritySendFull
	}

	timer := time.NewTimer(workerPriorityTimeout)
	defer timer.Stop()
	select {
	case res, ok := <-w.ch:
		if !ok {
			return protocol.WorkerPriorityResult{}, errWorkerPriorityDaemonGone
		}
		return res, nil
	case <-timer.C:
		return protocol.WorkerPriorityResult{}, errWorkerPriorityTimeout
	case <-ctx.Done():
		return protocol.WorkerPriorityResult{}, ctx.Err()
	}
}

func (h *Hub) deliverWorkerPriorityResult(dc *DaemonConn, res protocol.WorkerPriorityResult) {
	r := &h.workerPriorityReqs
	r.mu.Lock()
	w, ok := r.pending[res.RequestID]
	if ok && w.dc == dc {
		delete(r.pending, res.RequestID)
	}
	r.mu.Unlock()
	if !ok || w.dc != dc {
		return
	}
	w.ch <- res // buffered 1, and each waiter is delivered to at most once
}

// abandonWorkerPriorityRequests fails every request waiting on dc, which has
// just disconnected, so its callers answer now rather than at the timeout.
func (h *Hub) abandonWorkerPriorityRequests(dc *DaemonConn) {
	r := &h.workerPriorityReqs
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, w := range r.pending {
		if w.dc == dc {
			delete(r.pending, id)
			close(w.ch)
		}
	}
}

// HandleWorkerPriority is owner-only; changes are acknowledged only after the
// connected desktop persists them. A read never assumes an older daemon is on.
func (a *API) HandleWorkerPriority(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	dc := a.hub.ownedHostDaemon(r.Context(), principal.Sub, r.PathValue("id"))
	if dc == nil {
		writeError(w, 404, "desktop not connected")
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if r.Method == http.MethodPut {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil || req.Enabled == nil {
			writeError(w, 400, "enabled must be true or false")
			return
		}
	}
	res, err := a.hub.requestWorkerPriority(r.Context(), dc, req.Enabled)
	if err != nil {
		failurelog.Default.Event("worker priority request", slog.LevelError, "component", "runner", "stage", "worker priority request", "cause", failurelog.Cause(err))
		writeError(w, 502, "desktop did not answer; reconnect or update its daemon")
		return
	}
	if a.hub.ownedHostDaemon(r.Context(), principal.Sub, dc.ID) != dc {
		writeError(w, 404, "desktop not connected")
		return
	}
	if !res.OK {
		writeError(w, 422, "desktop could not save worker priority settings")
		return
	}
	writeJSON(w, 200, res)
}
