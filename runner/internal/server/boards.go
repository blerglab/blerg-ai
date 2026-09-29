package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ── response / request types ──────────────────────────────────────────────────

type boardInfo struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Description     *string  `json:"description"`
	Repos           []string `json:"repos"`
	DefaultDaemonID *string  `json:"default_daemon_id"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
}

type columnInfo struct {
	ID         string `json:"id"`
	BoardID    string `json:"board_id"`
	Rank       string `json:"rank"`
	Name       string `json:"name"`
	IsTerminal bool   `json:"is_terminal"`
	CreatedAt  string `json:"created_at"`
}

type ticketInfo struct {
	ID         string   `json:"id"`
	BoardID    string   `json:"board_id"`
	ColumnID   *string  `json:"column_id"`
	Title      string   `json:"title"`
	Body       *string  `json:"body"`
	Priority   string   `json:"priority"`
	Size       *string  `json:"size"`
	Rank       string   `json:"rank"`
	ArchivedAt *string  `json:"archived_at"`
	SessionID  *string  `json:"session_id"`
	Version    int      `json:"version"`
	CreatedAt  string   `json:"created_at"`
	UpdatedAt  string   `json:"updated_at"`
	Tags       []string `json:"tags"`
	Repos      []string `json:"repos"`
	Blocked    bool     `json:"blocked"`
}

// postBoardRequest is the body for POST /api/boards.
type postBoardRequest struct {
	Name            string   `json:"name"`
	Description     *string  `json:"description"`
	Repos           []string `json:"repos"`
	DefaultDaemonID *string  `json:"default_daemon_id"`
	OpID            *string  `json:"op_id"`
}

// postBoardResponse is returned by POST /api/boards; embeds boardInfo and
// includes the seeded columns.
type postBoardResponse struct {
	boardInfo
	Columns []columnInfo `json:"columns"`
}

// getBoardResponse is returned by GET /api/boards/{id}.
type getBoardResponse struct {
	boardInfo
	Columns []columnInfo `json:"columns"`
	Tickets []ticketInfo `json:"tickets"`
}

// boardOrderResponse is returned by GET /api/boards/{id}/order.
type boardOrderResponse struct {
	Tickets []ticketInfo `json:"tickets"`
}

// ── mappers ───────────────────────────────────────────────────────────────────

func boardRowToInfo(r db.BoardRow, repos []string) boardInfo {
	if repos == nil {
		repos = []string{}
	}
	return boardInfo{
		ID:              r.ID,
		Name:            r.Name,
		Description:     r.Description,
		Repos:           repos,
		DefaultDaemonID: r.DefaultDaemonID,
		CreatedAt:       r.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:       r.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// loadAllLiveTickets returns ALL non-archived tickets for a board by paging
// through ListTickets until the cursor is exhausted. ListTickets already
// excludes archived tickets; this avoids the silent truncation a single
// fixed-limit call would cause on boards with many cards.
func loadAllLiveTickets(ctx context.Context, pool *pgxpool.Pool, boardID string) ([]db.TicketRow, error) {
	var all []db.TicketRow
	cursor := ""
	for {
		rows, next, err := db.ListTickets(ctx, pool, boardID, db.TicketFilter{Limit: 200, Cursor: cursor})
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
		if next == "" {
			break
		}
		cursor = next
	}
	return all, nil
}

func columnRowToInfo(r db.ColumnRow) columnInfo {
	return columnInfo{
		ID:         r.ID,
		BoardID:    r.BoardID,
		Rank:       r.Rank,
		Name:       r.Name,
		IsTerminal: r.IsTerminal,
		CreatedAt:  r.CreatedAt.UTC().Format(time.RFC3339),
	}
}

func ticketRowToInfo(r db.TicketRow) ticketInfo {
	tags := r.Tags
	if tags == nil {
		tags = []string{}
	}
	repos := r.Repos
	if repos == nil {
		repos = []string{}
	}
	info := ticketInfo{
		ID:        r.ID,
		BoardID:   r.BoardID,
		ColumnID:  r.ColumnID,
		Title:     r.Title,
		Body:      r.Body,
		Priority:  r.Priority,
		Size:      r.Size,
		Rank:      r.Rank,
		SessionID: r.SessionID,
		Version:   r.Version,
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: r.UpdatedAt.UTC().Format(time.RFC3339),
		Tags:      tags,
		Repos:     repos,
		Blocked:   r.Blocked,
	}
	if r.ArchivedAt != nil {
		s := r.ArchivedAt.UTC().Format(time.RFC3339)
		info.ArchivedAt = &s
	}
	return info
}

// ── GET /api/boards ───────────────────────────────────────────────────────────

// HandleGetBoards returns all boards. Browser-gated (authBrowser), like every
// other browser-facing endpoint — there is no unauthenticated "ingress-only"
// tier any more.
func (a *API) HandleGetBoards(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.authBrowser(w, r); !ok {
		return
	}
	if a.dbPool == nil {
		writeJSON(w, http.StatusOK, []boardInfo{})
		return
	}
	ctx := r.Context()
	rows, err := db.ListBoards(ctx, a.dbPool)
	if err != nil {
		log.Printf("api list boards: %v", err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	result := make([]boardInfo, 0, len(rows))
	for _, row := range rows {
		repos, err := db.ListBoardRepos(ctx, a.dbPool, row.ID)
		if err != nil {
			log.Printf("api list board repos %s: %v", row.ID, err)
			writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
		result = append(result, boardRowToInfo(row, repos))
	}
	writeJSON(w, http.StatusOK, result)
}

// ── POST /api/boards ──────────────────────────────────────────────────────────

// HandlePostBoards creates a new board and seeds its default columns.
// Requires the daemon token or a core token with card.write; a board token
// (scoped to one existing board) can never create boards.
func (a *API) HandlePostBoards(w http.ResponseWriter, r *http.Request) {
	actor, ok := a.authBrowserOrBoard(w, r, "", "card.write")
	if !ok || !requireCoreOrDaemon(w, actor) {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	var req postBoardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if len(req.Repos) == 0 {
		writeError(w, http.StatusBadRequest, "repos is required")
		return
	}
	if err := validateRepoNames(req.Repos); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	opID := ""
	if req.OpID != nil {
		opID = *req.OpID
	}

	ctx := r.Context()
	board, err := db.CreateBoard(ctx, a.dbPool, req.Name, req.Description, req.Repos, req.DefaultDaemonID)
	if err != nil {
		log.Printf("api create board: %v", err)
		writeError(w, http.StatusInternalServerError, "create failed")
		return
	}
	cols, err := db.ListColumns(ctx, a.dbPool, board.ID)
	if err != nil {
		log.Printf("api list columns after create board: %v", err)
		writeError(w, http.StatusInternalServerError, "list columns failed")
		return
	}
	repos, err := db.ListBoardRepos(ctx, a.dbPool, board.ID)
	if err != nil {
		log.Printf("api list board repos after create %s: %v", board.ID, err)
		writeError(w, http.StatusInternalServerError, "list repos failed")
		return
	}
	colInfos := make([]columnInfo, 0, len(cols))
	for _, c := range cols {
		colInfos = append(colInfos, columnRowToInfo(c))
	}
	bi := boardRowToInfo(board, repos)
	a.hub.BroadcastBoard(board.ID, boardCreatedEvent{
		Type:  "board_created",
		Board: bi,
		OpID:  opID,
	})
	writeJSON(w, http.StatusCreated, postBoardResponse{
		boardInfo: bi,
		Columns:   colInfos,
	})
}

// ── DELETE /api/boards/{id} ───────────────────────────────────────────────────

// HandleDeleteBoard deletes a board and all its children (cascade).
// Requires the daemon token or a core token with board.admin; a board token
// can never delete boards. Returns 204 on success, 404 if not found.
func (a *API) HandleDeleteBoard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "board id required")
		return
	}
	actor, ok := a.authBrowserOrBoard(w, r, id, "board.admin")
	if !ok || !requireCoreOrDaemon(w, actor) {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	if err := db.DeleteBoard(r.Context(), a.dbPool, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "board not found")
			return
		}
		log.Printf("api delete board %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── GET /api/boards/{id} ─────────────────────────────────────────────────────

// HandleGetBoard returns a board with its columns and (non-archived) tickets.
// Requires the daemon token, a core token with card.read, or a board token
// scoped to this board carrying the "board" capability.
func (a *API) HandleGetBoard(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "board id required")
		return
	}
	if _, ok := a.authBrowserOrBoard(w, r, id, "card.read"); !ok {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}

	ctx := r.Context()
	board, err := db.GetBoard(ctx, a.dbPool, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "board not found")
			return
		}
		log.Printf("api get board %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	cols, err := db.ListColumns(ctx, a.dbPool, id)
	if err != nil {
		log.Printf("api list columns for board %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	// Fetch ALL live tickets (paging through the cursor) so boards with many
	// cards are not silently truncated. ListTickets already excludes archived.
	tickets, err := loadAllLiveTickets(ctx, a.dbPool, id)
	if err != nil {
		log.Printf("api list tickets for board %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	repos, err := db.ListBoardRepos(ctx, a.dbPool, id)
	if err != nil {
		log.Printf("api list board repos %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	colInfos := make([]columnInfo, 0, len(cols))
	for _, c := range cols {
		colInfos = append(colInfos, columnRowToInfo(c))
	}
	ticketInfos := make([]ticketInfo, 0, len(tickets))
	for _, t := range tickets {
		ticketInfos = append(ticketInfos, ticketRowToInfo(t))
	}

	writeJSON(w, http.StatusOK, getBoardResponse{
		boardInfo: boardRowToInfo(board, repos),
		Columns:   colInfos,
		Tickets:   ticketInfos,
	})
}

// ── GET /api/boards/{id}/order ────────────────────────────────────────────────

// HandleGetBoardOrder returns non-archived tickets in ready-first order: tickets
// with all dependencies satisfied (or no dependencies) appear first, then
// blocked tickets, each group sorted by priority (urgent→low) then rank.
//
// Requires the daemon token, a core token with card.read, or a board token
// scoped to this board carrying the "board" capability.
func (a *API) HandleGetBoardOrder(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "board id required")
		return
	}
	if _, ok := a.authBrowserOrBoard(w, r, id, "card.read"); !ok {
		return
	}
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}

	ctx := r.Context()
	// Verify the board exists before querying tickets.
	if _, err := db.GetBoard(ctx, a.dbPool, id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "board not found")
			return
		}
		log.Printf("api board order get board %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	// Ready-first query: a ticket is "ready" when it has no unsatisfied blocker.
	// An unsatisfied blocker is a non-archived ticket in a non-terminal column.
	// Sort: ready DESC, priority (urgent→low), rank ASC, id ASC (stable).
	rows, err := a.dbPool.Query(ctx, `
		SELECT id, board_id, column_id, title, body, priority, size, rank,
		       archived_at, session_id, version, created_at, updated_at
		  FROM tickets t
		 WHERE t.board_id = $1
		   AND t.archived_at IS NULL
		 ORDER BY
		   (NOT EXISTS (
		       SELECT 1
		         FROM ticket_dependencies td
		         JOIN tickets blocker ON blocker.id = td.depends_on_ticket_id
		         LEFT JOIN columns bc ON bc.id = blocker.column_id
		        WHERE td.ticket_id = t.id
		          AND blocker.archived_at IS NULL
		          AND COALESCE(bc.is_terminal, false) = false
		   )) DESC,
		   CASE t.priority
		       WHEN 'urgent' THEN 0
		       WHEN 'high'   THEN 1
		       WHEN 'medium' THEN 2
		       WHEN 'low'    THEN 3
		       ELSE 4
		   END ASC,
		   t.rank ASC,
		   t.id ASC
	`, id)
	if err != nil {
		log.Printf("api board order query %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}
	defer rows.Close()

	tickets := make([]ticketInfo, 0)
	for rows.Next() {
		var row db.TicketRow
		if err := rows.Scan(
			&row.ID, &row.BoardID, &row.ColumnID, &row.Title, &row.Body,
			&row.Priority, &row.Size, &row.Rank, &row.ArchivedAt, &row.SessionID,
			&row.Version, &row.CreatedAt, &row.UpdatedAt,
		); err != nil {
			log.Printf("api board order scan %s: %v", id, err)
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		tickets = append(tickets, ticketRowToInfo(row))
	}
	if err := rows.Err(); err != nil {
		log.Printf("api board order rows %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	writeJSON(w, http.StatusOK, boardOrderResponse{Tickets: tickets})
}

// ── GET /api/boards/{id}/archive ─────────────────────────────────────────────

// archiveResponse is returned by GET /api/boards/{id}/archive.
type archiveResponse struct {
	Tickets    []ticketInfo `json:"tickets"`
	NextCursor string       `json:"next_cursor"`
}

// HandleGetBoardArchive returns a page of archived tickets for a board, ordered
// newest-archived first. Query params: limit (default 50, cap 200) + cursor.
// Same credential rule as HandleGetBoard (card.read).
func (a *API) HandleGetBoardArchive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "board id required")
		return
	}
	if _, ok := a.authBrowserOrBoard(w, r, id, "card.read"); !ok {
		return
	}
	if a.dbPool == nil {
		writeJSON(w, http.StatusOK, archiveResponse{Tickets: []ticketInfo{}, NextCursor: ""})
		return
	}

	q := r.URL.Query()
	f := db.TicketFilter{ArchivedOnly: true}

	if lim := q.Get("limit"); lim != "" {
		n, err := strconv.Atoi(lim)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		f.Limit = n
	}
	f.Cursor = q.Get("cursor")

	ctx := r.Context()
	rows, next, err := db.ListTickets(ctx, a.dbPool, id, f)
	if err != nil {
		log.Printf("api board archive %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	tickets := make([]ticketInfo, 0, len(rows))
	for _, row := range rows {
		tickets = append(tickets, ticketRowToInfo(row))
	}
	writeJSON(w, http.StatusOK, archiveResponse{Tickets: tickets, NextCursor: next})
}
