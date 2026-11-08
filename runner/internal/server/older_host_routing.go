package server

// A daemon older than protocol.DaemonHello.AuthoritativeAnswers runs a native loop that takes
// anything sent while an ask waits as the ask's answer. Routing a worker's question to its parent
// cannot be made safe there, so ordinary worker questions stop waiting with an update notice.
// They never become the person's questions. Every decision reads the session's
// current connection, so a daemon upgraded or downgraded mid-session takes effect on its next hello.

import (
	"context"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// unroutedEscalation is the audit reason of a worker's question that skipped its parent because
// routing is off for the session.
const unroutedEscalation = "routing_off"

const routingOffNote = "Ordinary worker questions cannot reach Main on this daemon and stop without an answer. Updating its daemon enables worker question routing."

// questionRoutingOff reports whether the session's connected host predates authoritative answers.
// With no host connected nothing can reach an old loop, and the next hello decides.
func (a *API) questionRoutingOff(sessionID string) bool {
	dc := a.hub.FindDaemonForSession(sessionID)
	return dc != nil && !dc.CanAuthoritativeAnswers()
}

// noteRoutingOff tells the person once per session: the event id is fixed.
func (a *API) noteRoutingOff(ctx context.Context, sessionID string) {
	recordServerAgentEvent(ctx, a.hub, a.dbPool, sessionID, messageEventID(sessionID, "routing-off"), "update", messagingPayload{Body: routingOffNote})
}

// routeUnroutedChildQuestion fails closed when the old native loop cannot safely route an ask.
func (a *API) routeUnroutedChildQuestion(ctx context.Context, child *db.SessionRow, id string) error {
	a.noteRoutingOff(ctx, child.ID)
	return a.endWorkerQuestion(ctx, child, id, "its daemon must be updated before it can ask Main")
}
