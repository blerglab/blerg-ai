package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strings"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/runner"
	"github.com/jackc/pgx/v5"
)

// Re-modelling a LIVE session. A worker that finds its card harder than the
// board's default model can handle — or cheaper, once the hard part is done —
// changes its own model instead of being stopped and respawned: same session,
// same context, same event stream, and the card keeps its trail.
//
// The change lands on the session's NEXT turn; the in-flight turn finishes on
// the model it started with. That is the honest semantic for a runner whose
// turns are separate model invocations, and it is why this is a "set the
// model" verb rather than a "restart with" one.

// errModelForbidden / errModelBadRequest carry the HTTP shape of a failure
// out of SetSessionModel, which serves both REST and MCP.
var (
	errModelForbidden  = errors.New("forbidden")
	errModelBadRequest = errors.New("bad request")
)

// modelNameRe: model identifiers are opaque to blerg-board (they are the runner's
// vocabulary, and boards already store them free-form), so this only rejects
// what could not be a name — empty, oversized, or carrying whitespace and
// control characters into the runner's argv.
var modelNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/-]{0,63}$`)

// uuidRe recognises a runner session id — used both to reject ids Postgres
// would choke on and to keep a stray `[rs:…]` in free-text label from passing
// for a session tag.
var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// SessionModelChange is the outcome of a re-model, for the API response and
// the MCP tool result.
type SessionModelChange struct {
	RunnerSessionID string `json:"runner_session_id"`
	Role            string `json:"role"`
	From            string `json:"from"` // empty when the session's model was never recorded
	To              string `json:"to"`
	AppliesTo       string `json:"applies_to"`
}

// SetSessionModel changes the model a running session uses from its next turn
// on. sessionID may be "self", which resolves to the session the calling
// agent token was minted for.
func (a *API) SetSessionModel(ctx context.Context, p auth.Principal, sessionID, model, reason string) (SessionModelChange, error) {
	var out SessionModelChange
	if a.runner == nil {
		return out, fmt.Errorf("%w: no runner configured", runner.ErrUnsupported)
	}
	model = strings.TrimSpace(model)
	if !modelNameRe.MatchString(model) {
		return out, fmt.Errorf("%w: model must be a bare model id, e.g. claude-sonnet-5", errModelBadRequest)
	}
	sessionID, err := resolveSessionID(p, sessionID)
	if err != nil {
		return out, err
	}
	// Session ids are uuids; anything else could not name a row, and asking
	// Postgres would raise a type error rather than "no such session".
	if !uuidRe.MatchString(sessionID) {
		return out, db.ErrNotFound
	}

	var boardID, ext, role, lifecycle, current string
	var cardID *string
	err = a.Pool.QueryRow(ctx, `
		SELECT board_id, card_id, external_session_id, role, lifecycle, model
		FROM runner_sessions WHERE id = $1`, sessionID).
		Scan(&boardID, &cardID, &ext, &role, &lifecycle, &current)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return out, db.ErrNotFound
	case err != nil:
		// A connection blip or a deadline — neither of which means "your
		// session does not exist". Answering 404 to a session asking about
		// ITSELF sends it looking for a problem that isn't there, and 404 is
		// the one shape it can't sensibly retry; this falls through to 502.
		return out, fmt.Errorf("looking up session %s: %w", sessionID, err)
	}
	if err := p.RequireBoard(boardID, "card.write"); err != nil {
		return out, fmt.Errorf("%w: %w", errModelForbidden, err)
	}
	// An agent may re-model ITSELF, not its neighbours: a session's spawn
	// token is tagged `[rs:<id>]`, so ownership is checkable without trusting
	// anything the session says about itself.
	if p.Kind == auth.KindAgent && !tokenOwnsSession(p, sessionID) {
		return out, fmt.Errorf("%w: an agent may only change the model of its own session", errModelForbidden)
	}
	if runner.TerminalLifecycle(lifecycle) {
		return out, fmt.Errorf("%w: session is %s — nothing left to re-model", errModelBadRequest, lifecycle)
	}
	if model == current {
		// Nothing to do, and saying so beats a no-op round trip to the runner.
		return SessionModelChange{RunnerSessionID: sessionID, Role: role, From: current, To: model,
			AppliesTo: "unchanged"}, nil
	}

	if err := a.runner.Driver.SetModel(ctx, ext, model); err != nil {
		return out, err
	}
	// model_set_at stamps OUR write, so a runner-reported change that predates
	// it (an event still sitting in the ingest backlog) can't undo it.
	if _, err := a.Pool.Exec(ctx,
		`UPDATE runner_sessions SET model = $2, model_set_at = now() WHERE id = $1`,
		sessionID, model); err != nil {
		return out, err
	}

	// The trail is the point: a card whose session quietly moved to another
	// model should say so where the human reads it.
	if cardID != nil {
		note := fmt.Sprintf("session model → `%s`", model)
		if current != "" {
			note = fmt.Sprintf("session model `%s` → `%s`", current, model)
		}
		if r := strings.TrimSpace(reason); r != "" {
			note += ": " + r
		}
		note += " (takes effect on the session's next turn)"
		if err := db.AppendComment(ctx, a.Pool, *cardID, note, p.EventMeta()); err != nil {
			// The model DID change; only the record of it failed. Say so in
			// the log rather than losing it silently.
			log.Printf("session %s re-modelled to %s: trail comment failed: %v", sessionID, model, err)
		} else {
			a.Hub.Broadcast(boardID, "card_changed")
		}
	}
	a.Hub.Broadcast(boardID, "runner_changed")
	return SessionModelChange{RunnerSessionID: sessionID, Role: role, From: current, To: model,
		AppliesTo: "next turn"}, nil
}

// resolveSessionID maps "self" (or an empty id) to the runner session the
// caller's token was minted for. Spawn tags every session token's label with
// `[rs:<id>]` — the same tag ingest uses to revoke it.
func resolveSessionID(p auth.Principal, sessionID string) (string, error) {
	if sessionID != "" && sessionID != "self" {
		return sessionID, nil
	}
	if p.Kind != auth.KindAgent || p.Token == nil {
		return "", fmt.Errorf("%w: only a runner session can say \"self\" — pass a runner_session_id", errModelBadRequest)
	}
	if id := sessionIDFromLabel(p.Token.Label); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("%w: this token was not minted for a runner session", errModelBadRequest)
}

// sessionIDFromLabel pulls the `[rs:<id>]` tag out of a token label. Spawn
// APPENDS the tag, and the rest of a label is not all blerg-board's: a standing
// agent's name goes in it verbatim (standing.go), so an agent named
// `x [rs:…]` would otherwise plant a tag ahead of the real one. Read the last
// tag, and only when it holds something shaped like a session id.
func sessionIDFromLabel(label string) string {
	i := strings.LastIndex(label, "[rs:")
	if i < 0 {
		return ""
	}
	id, _, ok := strings.Cut(label[i+len("[rs:"):], "]")
	if !ok || !uuidRe.MatchString(id) {
		return ""
	}
	return id
}

func tokenOwnsSession(p auth.Principal, sessionID string) bool {
	return p.Token != nil && sessionIDFromLabel(p.Token.Label) == sessionID
}

// handleSetSessionModel serves POST /api/runner-sessions/{id}/model, where
// {id} may be the literal "self".
func (a *API) handleSetSessionModel(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if a.runner == nil {
		writeError(w, http.StatusServiceUnavailable, "no runner configured")
		return
	}
	req, err := decode[struct {
		Model  string `json:"model"`
		Reason string `json:"reason"`
	}](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "model required")
		return
	}
	change, err := a.SetSessionModel(r.Context(), p, r.PathValue("id"), req.Model, req.Reason)
	if err != nil {
		writeError(w, sessionModelStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, change)
}

// sessionModelStatus maps a re-model failure onto its HTTP shape. 501 is the
// interesting one: the runner is simply older than the contract, which is a
// deployment fact the caller can act on, not a blerg-board bug.
func sessionModelStatus(err error) int {
	switch {
	case errors.Is(err, errModelBadRequest):
		return http.StatusUnprocessableEntity
	case errors.Is(err, errModelForbidden):
		return http.StatusForbidden
	case errors.Is(err, db.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, runner.ErrUnsupported):
		return http.StatusNotImplemented
	case errors.Is(err, runner.ErrSessionGone):
		return http.StatusGone
	default:
		return http.StatusBadGateway
	}
}
