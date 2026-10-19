package server

import (
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// Discard accepts either the own-parent delegate credential or the signed-in owner.
// Other users, agent credentials and unrelated children have no authority here.
func (a *API) handleChildDiscard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var parent *db.SessionRow
	var child string
	var actor runnerPrincipal
	if viewer, ok := a.verifyBrowserToken(a.rawBearer(r)); ok {
		if viewer.Kind != string(identity.Human) || viewer.OnBehalfOf != "" || viewer.Sid == "" {
			writeError(w, 403, "forbidden")
			return
		}
		if a.dbPool == nil {
			writeError(w, 503, noDatabaseMsg)
			return
		}
		if !validSessionID(r.PathValue("id")) {
			writeError(w, 404, "session not found")
			return
		}
		var err error
		parent, err = db.GetSession(ctx, a.dbPool, r.PathValue("id"))
		if err != nil {
			writeDatabaseFailure(w, "read parent", "discard", err)
			return
		}
		if !ownsSession(viewer.Sub, parent) {
			writeError(w, 404, "session not found")
			return
		}
		child = r.PathValue("child")
		actor = runnerPrincipal{Kind: string(identity.Human), Sub: viewer.Sub}
	} else {
		var ok bool
		parent, actor, child, ok = a.delegateChild(w, r)
		if !ok {
			return
		}
	}
	if !validSessionID(child) {
		writeError(w, 404, "session not found")
		return
	}
	row, err := db.GetSession(ctx, a.dbPool, child)
	if err != nil {
		writeDatabaseFailure(w, "read child", "discard", err)
		return
	}
	account := derefOrEmpty(parent.SpawningAccountID)
	if row == nil || derefOrEmpty(row.ParentSessionID) != parent.ID || !ownsSession(account, row) {
		writeError(w, 404, "session not found")
		return
	}
	if row.ArchivedAt == nil {
		// The same action fence as message/recovery admission covers stop AND archive.
		// A concurrent input cannot restart a stopped worker between these writes.
		err = withCancelledLimitResume(ctx, a.dbPool, child, func() error {
			// The action fence can wait: authorization must still describe the current parent.
			currentParent, err := db.GetSession(ctx, a.dbPool, parent.ID)
			if err != nil {
				return err
			}
			if !ownsSession(account, currentParent) {
				return db.ErrDelegationNotFound
			}
			if actor.Kind == sessionDelegateKind && (db.TerminallyEnded(currentParent) || isChildSession(currentParent) || !onDesktopDaemon(currentParent)) {
				return db.ErrDelegationEnded
			}
			current, err := db.GetSession(ctx, a.dbPool, child)
			if err != nil {
				return err
			}
			if current == nil || derefOrEmpty(current.ParentSessionID) != parent.ID || !ownsSession(account, current) {
				return db.ErrDelegationNotFound
			}
			if !db.TerminallyEnded(current) {
				if !terminalSessionStatus(current.Status) && a.hub.FindDaemonForSession(child) == nil {
					return db.ErrDelegationNotFound
				}
				if err := a.stopSessionWithDelivery(ctx, child, stopEndByRunner(actor), true); err != nil {
					return err
				}
			}
			at, err := db.ArchiveChildSession(ctx, a.dbPool, child, parent.ID, account)
			if err != nil {
				return err
			}
			row.ArchivedAt = &at
			return nil
		})
		if err != nil {
			writeError(w, 503, "could not stop and archive the child; retry discard")
			return
		}
	}
	at := row.ArchivedAt.UTC().Format(time.RFC3339Nano)
	a.hub.BroadcastJSON(map[string]string{"type": "session_archived", "session_id": child, "parent_session_id": parent.ID, "archived_at": at})
	a.broadcastChildrenChanged(parent.ID, child)
	writeJSON(w, 200, map[string]string{"status": "discarded", "session_id": child, "archived_at": at})
}
