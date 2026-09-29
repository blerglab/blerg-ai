package server

// Session end attribution (migration 018): why a session ended and who ended
// it. The vocabulary and the write-once rules live in db/session_end.go; this
// file turns the principals the handlers already have into an attribution,
// and a row's attribution back into what the APIs report.
//
// Every path that ends a session records a reason exactly once:
//
//   - DELETE /api/sessions/{id} (a signed-in browser user) → stopped_by_user,
//     recorded when the stop is requested, so the exit it causes keeps it.
//   - POST /api/runner/sessions/{id}/stop and the MCP stop_session tool →
//     stopped_by_user or stopped_by_agent, by the caller's principal kind.
//   - auto_stop → auto_stopped, in the same statement as its claim.
//   - The daemon reporting the process gone with no stop asked for →
//     process_exited (exit 0) or process_failed (non-zero).
//   - Start failures → start_failed; error_reason carries the detail.
//   - The reconciler → job_finished / job_failed / job_disappeared /
//     not_resumed / daemon_lost / daemon_unreported.
//
// interrupt_session only cancels the in-flight turn; it never ends a session,
// so it records nothing.

import (
	"context"
	"log"

	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

// stopEndByPrincipal attributes a stop to a verified core principal. The
// account recorded is the one the actor IS (a human's sub) or acts FOR (an
// agent token's on_behalf_of) — the id the UI compares with its own to say
// "you". An agent token's own id is not kept: it identifies a credential, not
// a person, and the runner has no label to show for it.
func stopEndByPrincipal(kind, sub, onBehalfOf string) db.SessionEnd {
	account := onBehalfOf
	if kind == db.EndedByHuman {
		account = sub
	}
	return db.SessionEnd{Reason: db.EndReasonForActor(kind), ByKind: kind, ByAccount: account}
}

// stopEndByBrowser attributes a stop requested through the browser API.
func stopEndByBrowser(p identity.Principal) db.SessionEnd {
	return stopEndByPrincipal(p.Kind, p.Sub, p.OnBehalfOf)
}

// stopEndByRunner attributes a stop requested through the runner contract.
// The static runner key is a broker, not a person: it has no account.
func stopEndByRunner(p runnerPrincipal) db.SessionEnd {
	if p.Kind == runnerKeyPrincipalKind {
		return db.SessionEnd{Reason: db.EndReasonStoppedByAgent, ByKind: db.EndedByRunnerKey}
	}
	return stopEndByPrincipal(p.Kind, p.Sub, p.OnBehalfOf)
}

// systemEnd is an attribution with no actor: the session ended by itself or
// by the runner's own housekeeping.
func systemEnd(reason string) db.SessionEnd { return db.SessionEnd{Reason: reason} }

// sessionEnd is a row's end attribution as the server holds it. The account
// never leaves the server: it is only ever compared with a viewer's own
// account, and what goes out is the answer (EndedBy.Self), not the id.
type sessionEnd struct {
	Reason  string
	Kind    string
	Account string
}

// rowEnd reads a row's attribution. Only an ended session has an ending to
// report: a stop recorded when it was requested sits on a row that is still
// live until the daemon's exit lands, and must not be read as the session
// being over.
func rowEnd(row *db.SessionRow) sessionEnd {
	if row == nil || row.EndReason == nil || !terminalSessionStatus(row.Status) {
		return sessionEnd{}
	}
	return sessionEnd{
		Reason:  *row.EndReason,
		Kind:    derefOrEmpty(row.EndedByKind),
		Account: derefOrEmpty(row.EndedByAccount),
	}
}

// forViewer renders the attribution for one viewer: the reason, the kind of
// actor, and whether that actor is — or acts for — the viewer's own account.
// viewer "" (the runner contract, the webhook, a broadcast to a socket with no
// identity) never gets Self.
func (e sessionEnd) forViewer(viewer string) (string, *protocol.EndedBy) {
	if e.Reason == "" {
		return "", nil
	}
	if e.Kind == "" {
		return e.Reason, nil
	}
	return e.Reason, &protocol.EndedBy{
		Kind: e.Kind,
		Self: viewer != "" && e.Account != "" && e.Account == viewer,
	}
}

// endAttribution is rowEnd(row).forViewer(viewer).
func endAttribution(row *db.SessionRow, viewer string) (string, *protocol.EndedBy) {
	return rowEnd(row).forViewer(viewer)
}

// sessionEndFor reads a session's attribution for a browser broadcast. Only a
// terminal status carries one; anything else reports none, so a live
// session's broadcast can never be read as an ending.
func sessionEndFor(ctx context.Context, pool *pgxpool.Pool, sessionID, status string) sessionEnd {
	if pool == nil || !terminalSessionStatus(status) {
		return sessionEnd{}
	}
	row, err := db.GetSession(ctx, pool, sessionID)
	if err != nil {
		log.Printf("session end fields %s: %v", sessionID, err)
		return sessionEnd{}
	}
	return rowEnd(row)
}

// broadcastWithEnd sends a status message to every browser with the end
// attribution filled in for that browser's own account (EndedBy.Self). With no
// attribution it is an ordinary broadcast. build must be pure: it runs under
// the hub's lock.
func broadcastWithEnd(h *Hub, end sessionEnd, build func(reason string, by *protocol.EndedBy) any) {
	if end.Kind == "" {
		h.BroadcastJSON(build(end.forViewer("")))
		return
	}
	h.BroadcastJSONPerAccount(func(account string) any {
		return build(end.forViewer(account))
	})
}
