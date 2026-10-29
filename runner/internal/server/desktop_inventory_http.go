package server

import (
	"bytes"
	"context"
	"net/http"
)

// Build the bounded inventory before the final fence. No runtime RPC or
// unbounded response writer runs while ownership authority is held.
type inventoryResponse struct {
	header http.Header
	status int
	bytes.Buffer
}

func (w *inventoryResponse) Header() http.Header    { return w.header }
func (w *inventoryResponse) WriteHeader(status int) { w.status = status }
func (h *Hub) deliverDesktopInventory(ctx context.Context, owner string, daemons []*DaemonConn, w http.ResponseWriter, build func(http.ResponseWriter)) {
	snapshot := &inventoryResponse{header: make(http.Header), status: http.StatusOK}
	build(snapshot)
	started := false
	var snapshotErr error
	err := h.deliverDesktopSnapshot(ctx, owner, daemons, func() {
		for key, values := range snapshot.header {
			w.Header()[key] = values
		}
		started, snapshotErr = writeFencedResponse(ctx, w, snapshot.status, snapshot.Bytes())
	})
	if err == nil {
		err = snapshotErr
	}
	if err != nil && !started {
		for key := range snapshot.header {
			w.Header().Del(key)
		}
		discoveryProblem(w)
	}
}
