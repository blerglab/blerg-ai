package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

func (a *API) requestLocalWorkspaceStop(ctx context.Context, id string, end db.SessionEnd, strict bool) error {
	var complete bool
	err := db.WithRuntimeLaunchFenceRetry(ctx, a.dbPool, id, func(ctx context.Context) error {
		var e error
		complete, e = db.RequestLocalWorkspaceStop(ctx, a.dbPool, id, end)
		return e
	})
	if err != nil {
		return err
	}
	if complete {
		return a.hub.completeLocalWorkspaceFinal(ctx, a.dbPool, id)
	}
	// Rows/tokens already revoke read/admission. Only cleanup RPCs survive.
	if dc := a.hub.FindDaemonForSession(id); dc != nil {
		raw, _ := json.Marshal(protocol.KillSession{Type: "kill_session", SessionID: id})
		if !a.hub.trySendToDaemon(dc, raw) {
			return fmt.Errorf("desktop cleanup queue full; stop retained")
		}
	}
	if strict {
		return fmt.Errorf("desktop cleanup pending; stop retained")
	}
	return nil
}

// Receipt delivery never depends on a later raw notification. The final journal
// is also the durable outbox, retried after reconnect and by reconciliation.
func (h *Hub) completeLocalWorkspaceFinal(ctx context.Context, pool *pgxpool.Pool, id string) error {
	return db.WithSessionNativeHistoryLock(ctx, pool, id, func() error {
		var raw []byte
		var done bool
		if err := pool.QueryRow(ctx, `SELECT final_outcome,effects_done FROM local_workspace_authority WHERE session_id=$1 AND final_digest IS NOT NULL AND NOT aborted`, id).Scan(&raw, &done); err != nil {
			return err
		}
		if done {
			return nil
		}
		var final protocol.WorkspaceFinalization
		if json.Unmarshal(raw, &final) != nil {
			return errWorkspaceUnavailable
		}
		ctx = context.WithValue(ctx, workspaceFinalCommittedKey{}, true)
		if err := handleSessionEndedEffects(ctx, h, pool, final.Message); err != nil {
			return err
		}
		_, err := pool.Exec(ctx, `UPDATE local_workspace_authority SET effects_done=true WHERE session_id=$1 AND final_digest=$2`, id, final.Digest())
		return err
	})
}
func (a *API) reconcileLocalWorkspaceFinals(ctx context.Context) {
	stops, err := a.dbPool.Query(ctx, `SELECT session_id::text FROM local_workspace_authority WHERE NOT aborted AND stop_requested AND final_digest IS NULL LIMIT 128`)
	if err == nil {
		var ids []string
		for stops.Next() {
			var id string
			if stops.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		stops.Close()
		for _, id := range ids {
			if dc := a.hub.FindDaemonForSession(id); dc != nil {
				raw, _ := json.Marshal(protocol.KillSession{Type: "kill_session", SessionID: id})
				a.hub.trySendToDaemon(dc, raw)
			}
		}
	}

	rows, err := a.dbPool.Query(ctx, `SELECT a.session_id::text,a.effects_done FROM local_workspace_authority a JOIN sessions s ON s.id=a.session_id WHERE NOT a.aborted AND a.final_digest IS NOT NULL AND (NOT a.effects_done OR (s.callback_url IS NOT NULL AND s.webhook_delivered_at IS NULL)) LIMIT 128`)
	if err != nil {
		return
	}
	type item struct {
		id   string
		done bool
	}
	var pending []item
	for rows.Next() {
		var i item
		if rows.Scan(&i.id, &i.done) == nil {
			pending = append(pending, i)
		}
	}
	rows.Close()
	for _, i := range pending {
		if i.done {
			a.hub.NotifyCompletion(i.id)
		} else {
			_ = a.hub.completeLocalWorkspaceFinal(ctx, a.dbPool, i.id)
		}
	}
}

// A durable final outbox is acknowledged only after credential cleanup succeeds.
func revokeWorkspaceFinalGrants(ctx context.Context, pool *pgxpool.Pool, id string) error {
	err := mcpgw.DeleteGrantsForSession(ctx, pool, id)
	_, proposalErr := db.RevokeSessionProposalGrants(ctx, pool, id, "session_ended", false)
	revokeBoardTokens(ctx, id)
	return errors.Join(err, proposalErr)
}
