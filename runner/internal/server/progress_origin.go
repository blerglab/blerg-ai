package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"sync"
	"time"
)

var errProgressOriginUnavailable = errors.New("progress origin is unavailable")

type progressOriginWaiter struct {
	dc        *DaemonConn
	sessionID string
	ch        chan protocol.ProgressOriginResult
}
type progressOriginRequests struct {
	mu      sync.Mutex
	pending map[string]progressOriginWaiter
}

func (h *Hub) requestProgressOrigin(ctx context.Context, dc *DaemonConn, sid, callID, threadID string) (protocol.ProgressOriginResult, error) {
	id := newUUID()
	w := progressOriginWaiter{dc: dc, sessionID: sid, ch: make(chan protocol.ProgressOriginResult, 1)}
	r := &h.progressOriginReqs
	r.mu.Lock()
	count := 0
	for _, p := range r.pending {
		if p.dc.ID == dc.ID {
			count++
		}
	}
	if len(r.pending) >= 64 || count >= 16 {
		r.mu.Unlock()
		return protocol.ProgressOriginResult{}, errProgressOriginUnavailable
	}
	if r.pending == nil {
		r.pending = map[string]progressOriginWaiter{}
	}
	r.pending[id] = w
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.pending, id); r.mu.Unlock() }()
	raw, _ := json.Marshal(protocol.ResolveProgressOrigin{Type: "resolve_progress_origin", RequestID: id, SessionID: sid, CallID: callID, NativeThreadID: threadID})
	if h.FindDaemonForSession(sid) != dc || !h.trySendToDaemon(dc, raw) {
		return protocol.ProgressOriginResult{}, errProgressOriginUnavailable
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case res, ok := <-w.ch:
		if !ok || !res.OK || h.GetDaemon(dc.ID) != dc || h.FindDaemonForSession(sid) != dc {
			return protocol.ProgressOriginResult{}, errProgressOriginUnavailable
		}
		if _, valid := protocol.ProgressScope(res.ParentCallID, res.ChildID); !valid {
			return protocol.ProgressOriginResult{}, errProgressOriginUnavailable
		}
		return res, nil
	case <-timer.C:
		return protocol.ProgressOriginResult{}, errProgressOriginUnavailable
	case <-ctx.Done():
		return protocol.ProgressOriginResult{}, ctx.Err()
	}
}
func (h *Hub) deliverProgressOriginResult(dc *DaemonConn, res protocol.ProgressOriginResult) {
	if h.GetDaemon(dc.ID) != dc || h.FindDaemonForSession(res.SessionID) != dc || (dc.BoundSessionID != "" && dc.BoundSessionID != res.SessionID) {
		return
	}
	r := &h.progressOriginReqs
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.pending[res.RequestID]
	if !ok || w.dc != dc || w.sessionID != res.SessionID {
		return
	}
	delete(r.pending, res.RequestID)
	w.ch <- res
}
func (h *Hub) abandonProgressOriginRequests(dc *DaemonConn) {
	r := &h.progressOriginReqs
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, w := range r.pending {
		if w.dc == dc {
			delete(r.pending, id)
			close(w.ch)
		}
	}
}
