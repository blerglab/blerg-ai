package server

import (
	"context"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// A session's interaction mode (migration 038). An agent session runs its engine headless,
// and an engine in that mode assumes nobody is watching: a remark is a work order, and it
// says nothing between tool calls. In Blerg a person usually is reading the chat as it is
// written, but some sessions really are unattended (a board card run, a cron, a tool's job).
// So the mode belongs to the session: the server decides it at start (resolveInteraction),
// records it on the row, and sends it to the engine in the spawn message, where the daemon
// states it in the system prompt.

// interactionEnvVar is the plain (non-secret) environment variable that carries the mode to a
// cluster pod; mirrored from daemon.InteractionEnvVar (the server cannot import the daemon).
const interactionEnvVar = "BLERG_RUNNER_INTERACTION"

// interactionMessage is the 400 a start with an unknown interaction answers.
const interactionMessage = `interaction must be "interactive" or "unattended"`

// resolveInteraction decides a new session's mode from what the caller asked for ("" = nothing)
// and where the start came from:
//
//   - a value that is neither mode is refused (400), whoever sent it;
//   - a cron's session is unattended, whatever was asked;
//   - otherwise a stated mode is honoured;
//   - otherwise a start from the browser (the launch sheet) is interactive, and a start through
//     the agent contract (REST or the MCP tool) is unattended.
func resolveInteraction(asked string, fromBrowser bool, cron bool) (string, *APIError) {
	switch asked {
	case "", protocol.InteractionInteractive, protocol.InteractionUnattended:
	default:
		return "", apiErrorf(http.StatusBadRequest, "%s", interactionMessage)
	}
	switch {
	case cron:
		return protocol.InteractionUnattended, nil
	case asked != "":
		return asked, nil
	case fromBrowser:
		return protocol.InteractionInteractive, nil
	}
	return protocol.InteractionUnattended, nil
}

// effectiveInteraction is the mode of a stored session, never "". A row started before the
// column existed holds "": it is interactive when the person started it, and unattended when
// a cron, a tool with an agent token or the operator key did (startedByOf).
func effectiveInteraction(row *db.SessionRow) string {
	if row == nil {
		return protocol.InteractionInteractive
	}
	switch row.Interaction {
	case protocol.InteractionInteractive, protocol.InteractionUnattended:
		return row.Interaction
	}
	if startedByOf(row) != nil {
		return protocol.InteractionUnattended
	}
	return protocol.InteractionInteractive
}

// recordInteraction writes a new session's resolved mode on its row (best effort, like the
// other start attributes: the spawn message carries the mode either way).
func recordInteraction(ctx context.Context, pool *pgxpool.Pool, sessionID, mode string) {
	if pool == nil {
		return
	}
	if err := db.SetSessionInteraction(ctx, pool, sessionID, mode); err != nil {
		log.Printf("SetSessionInteraction %s: %v", sessionID, err)
	}
}
