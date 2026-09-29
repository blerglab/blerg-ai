package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5"
)

type assistRequest struct {
	TicketID string `json:"ticket_id"`
	OpID     string `json:"op_id"`
}

// HandlePostAssist spawns a board-scoped Assist Claude session.
//
// Route: POST /api/boards/{id}/assist. Spawning a session is session.start:
// the daemon token, a core token with session.start, or a board token scoped
// to this board (an Assist session may spawn a sibling for its own board).
//
// The ordering problem: board_tokens.session_id and tickets.session_id are both
// FKs → sessions. The session row MUST exist before minting the token or binding
// a ticket. Normal sessions are created async by session_started from the daemon,
// but we need the row now. Solution: CreateAssistSession pre-creates it with
// assist=true; the later session_started is a no-op (ON CONFLICT DO NOTHING).
func (a *API) HandlePostAssist(w http.ResponseWriter, r *http.Request) {
	boardID := r.PathValue("id")
	if boardID == "" {
		writeError(w, http.StatusBadRequest, "board id required")
		return
	}
	if _, ok := a.authBrowserOrBoard(w, r, boardID, "session.start"); !ok {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}

	var req assistRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	ctx := r.Context()

	// 1. Load board → 404 if missing.
	board, err := db.GetBoard(ctx, a.dbPool, boardID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "board not found")
			return
		}
		log.Printf("HandlePostAssist GetBoard %s: %v", boardID, err)
		writeError(w, http.StatusInternalServerError, "board lookup failed")
		return
	}

	// 2. Load board repos (≥1 expected).
	boardRepos, err := db.ListBoardRepos(ctx, a.dbPool, boardID)
	if err != nil {
		log.Printf("HandlePostAssist ListBoardRepos %s: %v", boardID, err)
		writeError(w, http.StatusInternalServerError, "repos lookup failed")
		return
	}
	if len(boardRepos) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "board has no repos")
		return
	}

	// 3. Select daemon: default_daemon_id → any connected daemon → 503.
	var daemon *DaemonConn
	if board.DefaultDaemonID != nil {
		daemon = a.hub.GetDaemon(*board.DefaultDaemonID)
	}
	if daemon == nil {
		daemon = a.hub.AnyDaemon()
	}
	if daemon == nil {
		writeError(w, http.StatusServiceUnavailable, "no daemon available for this board's repos")
		return
	}

	// Determine working repo and optional ticket context.
	repo := boardRepos[0]
	var ticketIDPtr *string
	var ticketTitle string

	if req.TicketID != "" {
		ticket, err := db.GetTicket(ctx, a.dbPool, req.TicketID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "ticket not found")
				return
			}
			log.Printf("HandlePostAssist GetTicket %s: %v", req.TicketID, err)
			writeError(w, http.StatusInternalServerError, "ticket lookup failed")
			return
		}
		if len(ticket.Repos) > 0 {
			repo = ticket.Repos[0]
		}
		tid := ticket.ID
		ticketIDPtr = &tid
		ticketTitle = ticket.Title
	}

	// 4. Pre-create session row before minting token or binding ticket.
	sessionID := newUUID()
	title := "Assist: " + board.Name
	if err := db.CreateAssistSession(ctx, a.dbPool, sessionID, daemon.ID, boardID, ticketIDPtr, repo, title); err != nil {
		log.Printf("HandlePostAssist CreateAssistSession %s: %v", sessionID, err)
		writeError(w, http.StatusInternalServerError, "session creation failed")
		return
	}

	// 5. Mint board token with {board, message} capabilities, 24h TTL.
	rawToken, _, err := db.MintBoardToken(ctx, a.dbPool, boardID, sessionID, []string{"board", "message"}, 24*time.Hour)
	if err != nil {
		log.Printf("HandlePostAssist MintBoardToken %s: %v", sessionID, err)
		writeError(w, http.StatusInternalServerError, "token mint failed")
		return
	}

	// 6. Bind ticket if ticket_id was given.
	if ticketIDPtr != nil {
		if err := db.SetTicketSession(ctx, a.dbPool, *ticketIDPtr, sessionID); err != nil {
			log.Printf("HandlePostAssist SetTicketSession %s→%s: %v", *ticketIDPtr, sessionID, err)
			writeError(w, http.StatusInternalServerError, "ticket bind failed")
			return
		}
	}

	// 7. Send SpawnSession to daemon.
	spawnMsg := protocol.SpawnSession{
		Type:          "spawn_session",
		SessionID:     sessionID,
		Repo:          repo,
		Title:         title,
		Cols:          80,
		Rows:          24,
		Assist:        true,
		BoardID:       boardID,
		TicketID:      req.TicketID,
		BoardToken:    rawToken,
		InitialPrompt: buildAssistPrompt(board.Name, req.TicketID, ticketTitle),
	}
	data, err := json.Marshal(spawnMsg) //nolint:gosec // the spawn message must carry the session token to the daemon over the authenticated websocket; never logged
	if err != nil {
		writeError(w, http.StatusInternalServerError, "marshal error")
		return
	}
	select {
	case daemon.send <- data:
	default:
		// The session row, board token, and ticket binding are already committed.
		// Roll them back (best-effort) so a failed spawn does not leave a stuck
		// "starting" session, a live 24h token, or a ticket shown as actively-worked.
		_ = db.RevokeBoardTokensForSession(ctx, a.dbPool, sessionID)
		_ = db.ClearTicketSessionBySession(ctx, a.dbPool, sessionID)
		now := time.Now()
		_ = db.EndSessionStatus(ctx, a.dbPool, sessionID, "ended", &now, systemEnd(db.EndReasonStartFailed))
		// Assist sessions are started without a callback, so this is a no-op
		// today; it is here so "every path that ends a session notifies" holds
		// as a rule rather than as a list someone has to remember to extend.
		a.notifyCompletion(sessionID) //nolint:contextcheck // webhook delivery outlives its caller by design: retries run for minutes (see notifyCompletion)
		writeError(w, http.StatusServiceUnavailable, "daemon send buffer full")
		return
	}

	// 8. 202 {session_id}.
	writeJSON(w, http.StatusAccepted, map[string]string{"session_id": sessionID})
}

// buildAssistPrompt constructs the initial prompt for the Assist session,
// pointing it at the managing-tickets skill and the board-show command.
func buildAssistPrompt(boardName, ticketID, ticketTitle string) string {
	focus := ""
	if ticketID != "" && ticketTitle != "" {
		focus = fmt.Sprintf("\n\nYour focus for this session is ticket: %q.", ticketTitle)
	} else if ticketID != "" {
		focus = fmt.Sprintf("\n\nYour focus for this session is ticket ID: %q.", ticketID)
	}
	return fmt.Sprintf(`You are the Assist agent for the %q board.

Your environment has BLERG_RUNNER_BOARD_ID and BLERG_RUNNER_BOARD_TOKEN set. Start by running:

  blerg-runner board show

to orient yourself on the current board state.%s

Use the managing-tickets skill to help organize this board. You can create, refine, split, tag, prioritize, and reorder tickets — collaborate actively with the user to keep the board healthy and the work moving.`, boardName, focus)
}
