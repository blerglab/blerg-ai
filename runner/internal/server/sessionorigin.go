package server

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

var errSessionOriginUnavailable = errors.New("session origin unavailable")

type sessionOriginWaiter struct {
	dc        *DaemonConn
	sessionID string
	ch        chan protocol.SessionOriginResult
}
type sessionOriginRequests struct {
	mu      sync.Mutex
	pending map[string]sessionOriginWaiter
}

func (h *Hub) requestSessionOrigin(ctx context.Context, dc *DaemonConn, sid string) (protocol.SessionOriginResult, error) {
	id := newUUID()
	w := sessionOriginWaiter{dc: dc, sessionID: sid, ch: make(chan protocol.SessionOriginResult, 1)}
	r := &h.sessionOriginReqs
	r.mu.Lock()
	count := 0
	for _, p := range r.pending {
		if p.sessionID == sid {
			r.mu.Unlock()
			return protocol.SessionOriginResult{}, errSessionOriginUnavailable
		}
		if p.dc.ID == dc.ID {
			count++
		}
	}
	if len(r.pending) >= 16 || count >= 4 {
		r.mu.Unlock()
		return protocol.SessionOriginResult{}, errSessionOriginUnavailable
	}
	if r.pending == nil {
		r.pending = map[string]sessionOriginWaiter{}
	}
	r.pending[id] = w
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.pending, id); r.mu.Unlock() }()
	raw, _ := json.Marshal(protocol.ReadSessionOrigin{Type: "read_session_origin", RequestID: id, SessionID: sid})
	if h.FindDaemonForSession(sid) != dc || !h.trySendToDaemon(dc, raw) {
		return protocol.SessionOriginResult{}, errSessionOriginUnavailable
	}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case result, ok := <-w.ch:
		if !ok || !result.OK || h.GetDaemon(dc.ID) != dc || h.FindDaemonForSession(sid) != dc || (result.Commit != "" && !protocol.ValidSessionOriginCommit(result.Commit)) {
			return protocol.SessionOriginResult{}, errSessionOriginUnavailable
		}
		return result, nil
	case <-timer.C:
		return protocol.SessionOriginResult{}, errSessionOriginUnavailable
	case <-ctx.Done():
		return protocol.SessionOriginResult{}, ctx.Err()
	}
}
func (h *Hub) deliverSessionOriginResult(dc *DaemonConn, res protocol.SessionOriginResult) {
	if h.GetDaemon(dc.ID) != dc || h.FindDaemonForSession(res.SessionID) != dc || (dc.BoundSessionID != "" && dc.BoundSessionID != res.SessionID) {
		return
	}
	r := &h.sessionOriginReqs
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.pending[res.RequestID]
	if !ok || w.dc != dc || w.sessionID != res.SessionID {
		return
	}
	delete(r.pending, res.RequestID)
	w.ch <- res
}
func (h *Hub) abandonSessionOriginRequests(dc *DaemonConn) {
	r := &h.sessionOriginReqs
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, w := range r.pending {
		if w.dc == dc {
			delete(r.pending, id)
			close(w.ch)
		}
	}
}

// readSessionOrigin is internal to authenticated filing: no agent-supplied commit or folder.
func (a *API) readSessionOrigin(ctx context.Context, sid string) (string, error) {
	if a.dbPool == nil || a.hub == nil {
		return "", errSessionOriginUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	row, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil || row == nil || terminalSessionStatus(row.Status) || row.Status == "disconnected" {
		return "", errSessionOriginUnavailable
	}
	dc := a.hub.FindDaemonForSession(sid)
	if dc == nil || dc.ID != row.DaemonID || (dc.BoundSessionID != "" && dc.BoundSessionID != sid) {
		return "", errSessionOriginUnavailable
	}
	result, err := a.hub.requestSessionOrigin(ctx, dc, sid)
	if err != nil {
		return "", err
	}
	unlock := a.hub.lockDaemonWork(dc.ID)
	defer unlock()
	if a.hub.GetDaemon(dc.ID) != dc || a.hub.FindDaemonForSession(sid) != dc {
		return "", errSessionOriginUnavailable
	}
	if dc.RuntimeTokenID != "" {
		if err := db.ValidateRuntimeBinding(ctx, a.dbPool, sid, dc.RuntimeTokenID, false); err != nil {
			return "", errSessionOriginUnavailable
		}
	}
	current, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil || current == nil || current.DaemonID != dc.ID || terminalSessionStatus(current.Status) || current.Status == "disconnected" {
		return "", errSessionOriginUnavailable
	}
	return result.Commit, nil
}
