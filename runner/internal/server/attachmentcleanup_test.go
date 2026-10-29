package server

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestAttachmentCleanupServerAuthorization(t *testing.T) {
	fx := newDelegFx(t)
	ended, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, status: "running"})
	discarded, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, status: "running"})
	paused, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, status: "paused"})
	errorID, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, status: "error"})
	foreign, _ := fx.session(sessOpts{owner: "owner-1", parent: fx.parent, status: "running"})
	unknown := newUUID()
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET end_reason=$2 WHERE id=$1`, errorID, db.EndReasonDaemonLost); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET discard_requested_at=now() WHERE id=$1`, fx.parent); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET status='stopped', ended_at=now(), end_reason=$2 WHERE id=$1`, ended, db.EndReasonStoppedByUser); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET archived_at=now() WHERE id=$1`, discarded); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertDaemon(t.Context(), fx.pool, "22222222-2222-4222-8222-222222222222", "other", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET daemon_id='22222222-2222-4222-8222-222222222222', status='ended',ended_at=now(),end_reason=$2 WHERE id=$1`, foreign, db.EndReasonProcessExited); err != nil {
		t.Fatal(err)
	}
	req := protocol.AttachmentCleanupRequest{RequestID: "correlated", SessionIDs: []string{ended, discarded, unknown, fx.parent, paused, errorID, foreign}}
	result := attachmentCleanup(t.Context(), fx.pool, fx.dc.ID, req)
	if result.RequestID != req.RequestID || result.Type != "attachment_cleanup_result" {
		t.Fatal("reply not correlated")
	}
	for _, id := range []string{ended, discarded, unknown} {
		if !slices.Contains(result.Removable, id) {
			t.Fatalf("ended/discarded/unknown session omitted: %s", id)
		}
	}
	for _, id := range []string{fx.parent, paused, errorID, foreign} {
		if slices.Contains(result.Removable, id) {
			t.Fatalf("live/foreign/recoverable session authorized: %s", id)
		}
	}
	// The authenticated daemon frame dispatches the same correlated result.
	raw, _ := json.Marshal(req)
	if !handleDaemonFrame(t.Context(), fx.hub, fx.dc, fx.pool, raw, envelope{Type: "attachment_cleanup"}) {
		t.Fatal("cleanup frame refused")
	}
	select {
	case raw := <-fx.dc.send:
		var wire protocol.AttachmentCleanupResult
		if json.Unmarshal(raw, &wire) != nil || wire.RequestID != req.RequestID || !slices.Equal(wire.Removable, result.Removable) {
			t.Fatalf("invalid cleanup response: %s", raw)
		}
	default:
		t.Fatal("no cleanup reply")
	}
	if got := attachmentCleanup(t.Context(), nil, fx.dc.ID, req); len(got.Removable) != 0 {
		t.Fatal("database failure grants cleanup")
	}
	req.SessionIDs = make([]string, 129)
	if got := attachmentCleanup(t.Context(), fx.pool, fx.dc.ID, req); len(got.Removable) != 0 {
		t.Fatal("unbounded request accepted")
	}
}
