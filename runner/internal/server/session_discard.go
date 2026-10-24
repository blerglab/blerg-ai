package server

import (
	"errors"
	"fmt"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"net/http"
	"time"
)

// Explicit discard is human-owner authority, distinct from ordinary Stop.
// Intent commits before enumeration so in-flight starts and recovery cannot
// create another worker while a partial operation awaits a retry.
func (a *API) handleSessionDiscard(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	viewer, ok := a.verifyBrowserToken(a.rawBearer(r))
	if !ok || viewer.Kind != string(identity.Human) || viewer.OnBehalfOf != "" || viewer.Sid == "" {
		writeError(w, 403, "forbidden")
		return
	}
	if a.dbPool == nil {
		writeError(w, 503, noDatabaseMsg)
		return
	}
	id := r.PathValue("id")
	if !validSessionID(id) {
		writeError(w, 404, "session not found")
		return
	}
	parent, err := db.GetSession(ctx, a.dbPool, id)
	if err != nil {
		writeDatabaseFailure(w, "read session", "discard", err)
		return
	}
	if !ownsSession(viewer.Sub, parent) {
		writeError(w, 404, "session not found")
		return
	}
	actor := runnerPrincipal{Kind: string(identity.Human), Sub: viewer.Sub}
	err = a.withDelegationLock(ctx, viewer.Sub, func() error {
		var at time.Time
		err := withCancelledLimitResume(ctx, a.dbPool, id, func() error {
			var beginErr error
			at, beginErr = db.BeginSessionDiscard(ctx, a.dbPool, id, viewer.Sub)
			return beginErr
		})
		if err != nil {
			return err
		}
		// Neither an old grant nor a pending approval may outlive explicit discard.
		if err = db.RevokeParentDelegationGrantsThrough(ctx, a.dbPool, id, at); err != nil {
			return err
		}
		if err = a.expireParentDelegationsThrough(ctx, id, at); err != nil {
			return err
		}
		ids, err := db.SessionDiscardMembers(ctx, a.dbPool, id, viewer.Sub)
		if err != nil {
			return err
		}
		for _, member := range ids {
			revokeSessionTokens(ctx, a.dbPool, member, "session discard requested")
		}
		var problems []error
		for _, member := range ids {
			err = withCancelledLimitResume(ctx, a.dbPool, member, func() error {
				row, err := db.GetSession(ctx, a.dbPool, member)
				if err != nil {
					return err
				}
				if !ownsSession(viewer.Sub, row) || (member != id && derefOrEmpty(row.ParentSessionID) != id) {
					return db.ErrDelegationNotFound
				}
				if row.ArchivedAt != nil {
					return nil
				}
				// Intent is never termination evidence. Even an ended row whose daemon
				// still claims ownership receives Stop before it may disappear from Live.
				connected := a.hub.FindDaemonForSession(member) != nil
				// A late hello may report an end after intent without delivering Stop.
				// Only terminal evidence predating discard is sufficient offline.
				preEnded := db.TerminallyEnded(row) && row.DiscardRequestedAt != nil && row.EndedAt != nil && !row.EndedAt.After(*row.DiscardRequestedAt)
				if !connected && !preEnded && onDesktopDaemon(row) {
					return fmt.Errorf("discard stop delivery is unavailable")
				}
				if connected || !preEnded {
					if err = a.stopSessionWithDelivery(ctx, member, stopEndByRunner(actor), true); err != nil {
						return err
					}
				}
				if member == id && len(problems) > 0 {
					return fmt.Errorf("children remain unarchived")
				}
				archived, err := db.ArchiveDiscardedSession(ctx, a.dbPool, member, viewer.Sub)
				if err != nil {
					return err
				}
				message := map[string]string{"type": "session_archived", "session_id": member, "archived_at": archived.UTC().Format(time.RFC3339Nano)}
				if member != id {
					message["parent_session_id"] = id
				}
				a.hub.BroadcastJSONPerAccount(func(account string) any { //nolint:contextcheck // broadcasts have independent bounded delivery checks and outlive this request
					if account != viewer.Sub {
						return nil
					}
					return message
				})
				if member != id {
					a.broadcastChildrenChanged(id, member) //nolint:contextcheck // broadcasts have independent bounded delivery checks and outlive this request
				}
				return nil
			})
			if err != nil {
				problems = append(problems, err)
			}
		}
		return errors.Join(problems...)
	})
	if err != nil {
		writeError(w, 503, "discard is pending; could not stop and archive every session; retry discard")
		return
	}
	parent, err = db.GetSession(ctx, a.dbPool, id)
	if err != nil || parent == nil || parent.ArchivedAt == nil {
		writeError(w, 503, "discard is pending; retry discard")
		return
	}
	writeJSON(w, 200, map[string]string{"status": "discarded", "session_id": id, "archived_at": parent.ArchivedAt.UTC().Format(time.RFC3339Nano)})
}
