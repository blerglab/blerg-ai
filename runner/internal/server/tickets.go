package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5"
)

// ── response / request types ──────────────────────────────────────────────────

// ticketDetailInfo is the full ticket response including repos, tags, and deps.
type ticketDetailInfo struct {
	ticketInfo
	Repos     []string `json:"repos"`
	Tags      []string `json:"tags"`
	DependsOn []string `json:"depends_on"`
	Blocks    []string `json:"blocks"`
}

// postTicketRequest is the body for POST /api/boards/{id}/tickets.
type postTicketRequest struct {
	Title    string   `json:"title"`
	Body     *string  `json:"body"`
	Repos    []string `json:"repos"`
	ColumnID *string  `json:"column_id"`
	Tags     []string `json:"tags"`
	Priority string   `json:"priority"`
	Size     *string  `json:"size"`
	OpID     *string  `json:"op_id"`
}

// listTicketsResponse is returned by GET /api/boards/{id}/tickets.
type listTicketsResponse struct {
	Tickets    []ticketInfo `json:"tickets"`
	NextCursor string       `json:"next_cursor"`
}

// patchTicketRequest is the body for PATCH /api/tickets/{id}.
// All fields are optional; only present (non-nil) fields are applied.
type patchTicketRequest struct {
	Title    *string   `json:"title"`
	Body     *string   `json:"body"`
	Priority *string   `json:"priority"`
	Size     *string   `json:"size"`
	AddTags  []string  `json:"add_tags"`
	RmTags   []string  `json:"rm_tags"`
	AddRepos []string  `json:"add_repos"`
	RmRepos  []string  `json:"rm_repos"`
	Repos    *[]string `json:"repos"`
	ColumnID *string   `json:"column_id"`
	After    *string   `json:"after"`
	Before   *string   `json:"before"`
	OpID     *string   `json:"op_id"`
}

// ticketEventInfo is the JSON view of a db.TicketEvent returned by
// GET /api/tickets/{id}/events.
type ticketEventInfo struct {
	ID           int64           `json:"id"`
	TicketID     string          `json:"ticket_id"`
	Type         string          `json:"type"`
	Actor        string          `json:"actor"`
	FromColumnID *string         `json:"from_column_id"`
	ToColumnID   *string         `json:"to_column_id"`
	Data         json.RawMessage `json:"data"`
	CreatedAt    string          `json:"created_at"`
}

// listEventsResponse is returned by GET /api/tickets/{id}/events.
type listEventsResponse struct {
	Events     []ticketEventInfo `json:"events"`
	NextCursor string            `json:"next_cursor"`
}

// splitTicketRequest is the body for POST /api/tickets/{id}/split.
type splitTicketRequest struct {
	Titles []string `json:"titles"`
	OpID   *string  `json:"op_id"`
}

// addDependencyRequest is the body for POST /api/tickets/{id}/dependencies.
type addDependencyRequest struct {
	DependsOnTicketID string  `json:"depends_on_ticket_id"`
	OpID              *string `json:"op_id"`
}

// ── mappers ───────────────────────────────────────────────────────────────────

func ticketDetailToInfo(d db.TicketDetail) ticketDetailInfo {
	repos := d.Repos
	if repos == nil {
		repos = []string{}
	}
	tags := d.Tags
	if tags == nil {
		tags = []string{}
	}
	dependsOn := d.DependsOn
	if dependsOn == nil {
		dependsOn = []string{}
	}
	blocks := d.Blocks
	if blocks == nil {
		blocks = []string{}
	}
	return ticketDetailInfo{
		ticketInfo: ticketRowToInfo(d.TicketRow),
		Repos:      repos,
		Tags:       tags,
		DependsOn:  dependsOn,
		Blocks:     blocks,
	}
}

// cardInfo returns the enriched card view (tags, repos, blocked) for a ticket,
// for use in broadcast WS events so live clients don't lose those fields after a
// mutation. On lookup failure it falls back to the bare mutation row (which the
// caller already has) and logs — a broadcast must never fail the request.
func (a *API) cardInfo(ctx context.Context, id string, fallback db.TicketRow) ticketInfo {
	card, err := db.GetTicketCard(ctx, a.dbPool, id)
	if err != nil {
		log.Printf("api get ticket card %s for event: %v", id, err)
		return ticketRowToInfo(fallback)
	}
	return ticketRowToInfo(card)
}

// ── error helpers ─────────────────────────────────────────────────────────────

// isTicketValidationErr returns true for errors originating from ticket input
// validation (priority, size, board repos). These map to 400 Bad Request.
func isTicketValidationErr(err error) bool {
	return errors.Is(err, db.ErrInvalidPriority) ||
		errors.Is(err, db.ErrInvalidSize) ||
		errors.Is(err, db.ErrInvalidRepos)
}

// ── auth helper ───────────────────────────────────────────────────────────────

// resolveTicketBoard looks up the board_id for the ticket and requires a
// credential for that board (authBrowserOrBoard with coreCap). Returns
// ok=false if the response was already written (ticket not found, or the
// credential was missing / refused).
func (a *API) resolveTicketBoard(w http.ResponseWriter, r *http.Request, ticketID, coreCap string) (boardID string, actor boardActor, ok bool) {
	ctx := r.Context()
	boardID, err := db.TicketBoardID(ctx, a.dbPool, ticketID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "ticket not found")
			return "", boardActor{}, false
		}
		log.Printf("api resolve ticket board %s: %v", ticketID, err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return "", boardActor{}, false
	}
	actor, authOk := a.authBrowserOrBoard(w, r, boardID, coreCap)
	return boardID, actor, authOk
}

// eventActor names the actor recorded on a ticket event: an Assist session
// (board token) is "assist"; a human or the daemon is "human", as before.
func eventActor(actor boardActor) string {
	if actor.Board != nil {
		return "assist"
	}
	return "human"
}

// ── POST /api/boards/{id}/tickets ─────────────────────────────────────────────

// HandlePostTicket creates a new ticket on a board.
func (a *API) HandlePostTicket(w http.ResponseWriter, r *http.Request) {
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	boardID := r.PathValue("id")
	if boardID == "" {
		writeError(w, http.StatusBadRequest, "board id required")
		return
	}
	actor, ok := a.authBrowserOrBoard(w, r, boardID, "card.write")
	if !ok {
		return
	}

	var req postTicketRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	opID := req.OpID

	ctx := r.Context()
	in := db.TicketInput{
		BoardID:  boardID,
		Title:    strings.TrimSpace(req.Title),
		Body:     req.Body,
		Repos:    req.Repos,
		ColumnID: req.ColumnID,
		Tags:     req.Tags,
		Priority: req.Priority,
		Size:     req.Size,
	}
	if in.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}

	row, err := db.CreateTicket(ctx, a.dbPool, in)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "board or column not found")
			return
		}
		if isTicketValidationErr(err) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		log.Printf("api create ticket board %s: %v", boardID, err)
		writeError(w, http.StatusInternalServerError, "create failed")
		return
	}

	// Append created event (best-effort).
	if evErr := db.AppendTicketEvent(ctx, a.dbPool, db.TicketEvent{
		TicketID: row.ID,
		Type:     "created",
		Actor:    eventActor(actor),
	}); evErr != nil {
		log.Printf("api append created event for ticket %s: %v", row.ID, evErr)
	}

	opIDStr := ""
	if opID != nil {
		opIDStr = *opID
	}
	// Broadcast the enriched card (tags/repos/blocked) so live clients render
	// the new card fully; the HTTP response returns the same enriched view.
	ti := a.cardInfo(ctx, row.ID, row)
	a.hub.BroadcastBoard(boardID, ticketCreatedEvent{
		Type:   "ticket_created",
		Ticket: ti,
		OpID:   opIDStr,
	})

	writeJSON(w, http.StatusCreated, ti)
}

// ── GET /api/boards/{id}/tickets ──────────────────────────────────────────────

// HandleListTickets returns a page of non-archived tickets for a board.
// Query params: column, tag (repeatable), priority, size, ready, limit, cursor.
func (a *API) HandleListTickets(w http.ResponseWriter, r *http.Request) {
	if a.dbPool == nil {
		writeJSON(w, http.StatusOK, listTicketsResponse{Tickets: []ticketInfo{}, NextCursor: ""})
		return
	}
	boardID := r.PathValue("id")
	if boardID == "" {
		writeError(w, http.StatusBadRequest, "board id required")
		return
	}
	if _, ok := a.authBrowserOrBoard(w, r, boardID, "card.read"); !ok {
		return
	}

	q := r.URL.Query()

	f := db.TicketFilter{}

	if col := q.Get("column"); col != "" {
		f.ColumnID = &col
	}
	f.Tags = q["tag"]
	if p := q.Get("priority"); p != "" {
		f.Priority = &p
	}
	if s := q.Get("size"); s != "" {
		f.Size = &s
	}
	if ready := q.Get("ready"); strings.ToLower(ready) == "true" {
		f.Ready = true
	}
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
	rows, next, err := db.ListTickets(ctx, a.dbPool, boardID, f)
	if err != nil {
		log.Printf("api list tickets board %s: %v", boardID, err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	tickets := make([]ticketInfo, 0, len(rows))
	for _, row := range rows {
		tickets = append(tickets, ticketRowToInfo(row))
	}
	writeJSON(w, http.StatusOK, listTicketsResponse{Tickets: tickets, NextCursor: next})
}

// ── GET /api/tickets/{id} ─────────────────────────────────────────────────────

// HandleGetTicket returns full detail for a ticket including repos, tags, deps.
func (a *API) HandleGetTicket(w http.ResponseWriter, r *http.Request) {
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "ticket id required")
		return
	}

	if _, _, ok := a.resolveTicketBoard(w, r, id, "card.read"); !ok {
		return
	}

	ctx := r.Context()
	detail, err := db.GetTicket(ctx, a.dbPool, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "ticket not found")
			return
		}
		log.Printf("api get ticket %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	writeJSON(w, http.StatusOK, ticketDetailToInfo(detail))
}

// ── PATCH /api/tickets/{id} ───────────────────────────────────────────────────

// HandlePatchTicket applies scalar field updates and/or a column move.
// Honours the If-Match header as expectVersion for optimistic concurrency.
func (a *API) HandlePatchTicket(w http.ResponseWriter, r *http.Request) {
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "ticket id required")
		return
	}

	boardID, actor, ok := a.resolveTicketBoard(w, r, id, "card.write")
	if !ok {
		return
	}

	var req patchTicketRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Parse If-Match header as expect version.
	var expectVersion *int
	if ifMatch := r.Header.Get("If-Match"); ifMatch != "" {
		v, err := strconv.Atoi(ifMatch)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid If-Match header")
			return
		}
		expectVersion = &v
	}

	opID := req.OpID

	ctx := r.Context()

	// Validate any repo strings up front: board-membership checks downstream
	// (validateBoardRepos) already constrain these, but a traversal string
	// must never reach the DB layer in the first place (defence in depth).
	if err := validateRepoNames(req.AddRepos); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if req.Repos != nil {
		if err := validateRepoNames(*req.Repos); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
	}

	// Determine if any scalar / additive patch fields are present.
	hasScalar := req.Title != nil || req.Body != nil || req.Priority != nil || req.Size != nil ||
		len(req.AddTags) > 0 || len(req.RmTags) > 0 || len(req.AddRepos) > 0 || len(req.RmRepos) > 0 ||
		req.Repos != nil

	var result db.TicketRow
	var resultIsSet bool

	// ── Scalar / additive update ──────────────────────────────────────────────
	if hasScalar {
		patch := db.TicketPatch{
			Title:    req.Title,
			Body:     req.Body,
			Priority: req.Priority,
			Size:     req.Size,
			AddTags:  req.AddTags,
			RmTags:   req.RmTags,
			AddRepos: req.AddRepos,
			RmRepos:  req.RmRepos,
			Repos:    req.Repos,
		}
		updated, err := db.UpdateTicket(ctx, a.dbPool, id, patch, expectVersion)
		if err != nil {
			if errors.Is(err, db.ErrStaleVersion) {
				writeError(w, http.StatusConflict, "stale version")
				return
			}
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "ticket not found")
				return
			}
			if isTicketValidationErr(err) {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			log.Printf("api update ticket %s: %v", id, err)
			writeError(w, http.StatusInternalServerError, "update failed")
			return
		}
		result = updated
		resultIsSet = true
		// UpdateTicket already honored the If-Match guard and bumped version once.
		// A move that follows in the same request must NOT guard/bump again.
		expectVersion = nil

		// Append updated event (best-effort).
		if evErr := db.AppendTicketEvent(ctx, a.dbPool, db.TicketEvent{
			TicketID: id,
			Type:     "updated",
			Actor:    eventActor(actor),
		}); evErr != nil {
			log.Printf("api append updated event for ticket %s: %v", id, evErr)
		}
		opIDStr := ""
		if opID != nil {
			opIDStr = *opID
		}
		a.hub.BroadcastBoard(boardID, ticketUpdatedEvent{
			Type:   "ticket_updated",
			Ticket: a.cardInfo(ctx, id, result),
			OpID:   opIDStr,
		})
	}

	// ── Column move ───────────────────────────────────────────────────────────
	if req.ColumnID != nil {
		// We need the current column before the move for the event.
		var fromColID *string
		if resultIsSet {
			fromColID = result.ColumnID
		} else {
			cur, err := db.GetTicket(ctx, a.dbPool, id)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					writeError(w, http.StatusNotFound, "ticket not found")
					return
				}
				log.Printf("api patch ticket get-for-move %s: %v", id, err)
				writeError(w, http.StatusInternalServerError, "query failed")
				return
			}
			fromColID = cur.ColumnID
		}

		// expectVersion is non-nil only for a move-only PATCH (it was cleared above
		// if a scalar update already consumed the If-Match guard). This honors
		// If-Match on move-only requests and bumps version exactly once.
		moved, err := db.MoveTicket(ctx, a.dbPool, id, *req.ColumnID, req.After, req.Before, expectVersion)
		if err != nil {
			if errors.Is(err, db.ErrStaleVersion) {
				writeError(w, http.StatusConflict, "stale version")
				return
			}
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "ticket or column not found")
				return
			}
			log.Printf("api move ticket %s: %v", id, err)
			writeError(w, http.StatusInternalServerError, "move failed")
			return
		}
		result = moved
		resultIsSet = true

		// Append moved event (best-effort).
		toColID := req.ColumnID
		if evErr := db.AppendTicketEvent(ctx, a.dbPool, db.TicketEvent{
			TicketID:     id,
			Type:         "moved",
			Actor:        eventActor(actor),
			FromColumnID: fromColID,
			ToColumnID:   toColID,
		}); evErr != nil {
			log.Printf("api append moved event for ticket %s: %v", id, evErr)
		}
		opIDStr := ""
		if opID != nil {
			opIDStr = *opID
		}
		a.hub.BroadcastBoard(boardID, ticketMovedEvent{
			Type:         "ticket_moved",
			Ticket:       a.cardInfo(ctx, id, result),
			FromColumnID: fromColID,
			ToColumnID:   toColID,
			OpID:         opIDStr,
		})
	}

	// ── No-op: return current state ───────────────────────────────────────────
	if !resultIsSet {
		cur, err := db.GetTicket(ctx, a.dbPool, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "ticket not found")
				return
			}
			log.Printf("api patch ticket get-noop %s: %v", id, err)
			writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
		result = cur.TicketRow
	}

	writeJSON(w, http.StatusOK, ticketRowToInfo(result))
}

// ── POST /api/tickets/{id}/split ─────────────────────────────────────────────

// HandleSplitTicket splits a ticket into N children (≥2 titles).
func (a *API) HandleSplitTicket(w http.ResponseWriter, r *http.Request) {
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "ticket id required")
		return
	}

	boardID, _, ok := a.resolveTicketBoard(w, r, id, "card.write")
	if !ok {
		return
	}

	var req splitTicketRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	opID := req.OpID

	ctx := r.Context()
	children, err := db.SplitTicket(ctx, a.dbPool, id, req.Titles)
	if err != nil {
		if errors.Is(err, db.ErrTooFewChildren) {
			writeError(w, http.StatusBadRequest, "split requires at least 2 child titles")
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "ticket not found")
			return
		}
		log.Printf("api split ticket %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "split failed")
		return
	}

	// Children inherit the origin's tags, so build their event payloads from the
	// enriched card view (not the bare insert row) so live clients render them.
	childInfos := make([]ticketInfo, 0, len(children))
	for _, c := range children {
		childInfos = append(childInfos, a.cardInfo(ctx, c.ID, c))
	}
	opIDStr := ""
	if opID != nil {
		opIDStr = *opID
	}

	// Fetch the origin's post-split state (now archived: archived_at set,
	// column_id null) so subscribers holding it in a cache can update it
	// without a reload. GetTicketCard works for archived tickets. Best-effort:
	// a failure here must not fail the request, since the split already succeeded.
	originInfo := ticketInfo{ID: id, BoardID: boardID}
	if origin, gErr := db.GetTicketCard(ctx, a.dbPool, id); gErr != nil {
		log.Printf("api split ticket get-origin %s: %v", id, gErr)
	} else {
		originInfo = ticketRowToInfo(origin)
	}

	a.hub.BroadcastBoard(boardID, ticketSplitEvent{
		Type:         "ticket_split",
		OriginID:     id,
		OriginTicket: originInfo,
		Children:     childInfos,
		OpID:         opIDStr,
	})

	writeJSON(w, http.StatusCreated, childInfos)
}

// ── POST /api/tickets/{id}/archive ───────────────────────────────────────────

// HandleArchiveTicket archives a ticket (sets archived_at, clears column_id).
func (a *API) HandleArchiveTicket(w http.ResponseWriter, r *http.Request) {
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "ticket id required")
		return
	}

	boardID, actor, ok := a.resolveTicketBoard(w, r, id, "card.write")
	if !ok {
		return
	}

	opID := r.URL.Query().Get("op_id")

	ctx := r.Context()
	row, err := db.ArchiveTicket(ctx, a.dbPool, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "ticket not found")
			return
		}
		log.Printf("api archive ticket %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "archive failed")
		return
	}

	// Append archived event (best-effort).
	if evErr := db.AppendTicketEvent(ctx, a.dbPool, db.TicketEvent{
		TicketID: id,
		Type:     "archived",
		Actor:    eventActor(actor),
	}); evErr != nil {
		log.Printf("api append archived event for ticket %s: %v", id, evErr)
	}
	// GetTicketCard works for archived tickets (no archived filter), so the
	// event still carries tags/repos for a client updating its cache.
	ti := a.cardInfo(ctx, id, row)
	a.hub.BroadcastBoard(boardID, ticketArchivedEvent{
		Type:   "ticket_archived",
		Ticket: ti,
		OpID:   opID,
	})

	writeJSON(w, http.StatusOK, ti)
}

// ── POST /api/tickets/{id}/dependencies ──────────────────────────────────────

// HandleAddDependency adds a "ticket depends on dependsOnTicket" edge.
func (a *API) HandleAddDependency(w http.ResponseWriter, r *http.Request) {
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "ticket id required")
		return
	}

	boardID, actor, ok := a.resolveTicketBoard(w, r, id, "card.write")
	if !ok {
		return
	}

	var req addDependencyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.DependsOnTicketID == "" {
		writeError(w, http.StatusBadRequest, "depends_on_ticket_id is required")
		return
	}

	opID := req.OpID

	ctx := r.Context()
	if err := db.AddDependency(ctx, a.dbPool, id, req.DependsOnTicketID, eventActor(actor)); err != nil {
		if errors.Is(err, db.ErrSelfDependency) {
			writeError(w, http.StatusBadRequest, "self dependency not allowed")
			return
		}
		if errors.Is(err, db.ErrDependencyCycle) {
			writeError(w, http.StatusBadRequest, "dependency cycle detected")
			return
		}
		if errors.Is(err, db.ErrCrossBoardDependency) {
			writeError(w, http.StatusBadRequest, "cross-board dependency not allowed")
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "ticket not found")
			return
		}
		log.Printf("api add dependency ticket %s → %s: %v", id, req.DependsOnTicketID, err)
		writeError(w, http.StatusInternalServerError, "add dependency failed")
		return
	}

	opIDStr := ""
	if opID != nil {
		opIDStr = *opID
	}
	a.hub.BroadcastBoard(boardID, ticketDependencyChangedEvent{
		Type:              "ticket_dependency_changed",
		TicketID:          id,
		DependsOnTicketID: req.DependsOnTicketID,
		Action:            "added",
		OpID:              opIDStr,
	})

	// The dependency edge changes the dependent ticket's blocked state, so
	// broadcast a ticket_updated carrying the recomputed card so the client's
	// blocked badge updates live without a reload.
	a.hub.BroadcastBoard(boardID, ticketUpdatedEvent{
		Type:   "ticket_updated",
		Ticket: a.cardInfo(ctx, id, db.TicketRow{ID: id, BoardID: boardID}),
		OpID:   opIDStr,
	})

	w.WriteHeader(http.StatusCreated)
}

// ── DELETE /api/tickets/{id}/dependencies/{depId} ────────────────────────────

// HandleRemoveDependency removes the "ticket depends on depId" edge.
func (a *API) HandleRemoveDependency(w http.ResponseWriter, r *http.Request) {
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	id := r.PathValue("id")
	depID := r.PathValue("depId")
	if id == "" || depID == "" {
		writeError(w, http.StatusBadRequest, "ticket id and dep id required")
		return
	}

	boardID, actor, ok := a.resolveTicketBoard(w, r, id, "card.write")
	if !ok {
		return
	}

	opID := r.URL.Query().Get("op_id")

	ctx := r.Context()
	if err := db.RemoveDependency(ctx, a.dbPool, id, depID, eventActor(actor)); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "ticket not found")
			return
		}
		log.Printf("api remove dependency ticket %s → %s: %v", id, depID, err)
		writeError(w, http.StatusInternalServerError, "remove dependency failed")
		return
	}

	a.hub.BroadcastBoard(boardID, ticketDependencyChangedEvent{
		Type:              "ticket_dependency_changed",
		TicketID:          id,
		DependsOnTicketID: depID,
		Action:            "removed",
		OpID:              opID,
	})

	// Removing the edge may unblock the dependent ticket, so broadcast a
	// ticket_updated with the recomputed card so the client's blocked badge
	// updates live without a reload.
	a.hub.BroadcastBoard(boardID, ticketUpdatedEvent{
		Type:   "ticket_updated",
		Ticket: a.cardInfo(ctx, id, db.TicketRow{ID: id, BoardID: boardID}),
		OpID:   opID,
	})

	w.WriteHeader(http.StatusNoContent)
}

// ── GET /api/tickets/{id}/events ──────────────────────────────────────────────

// HandleGetTicketEvents returns the activity-feed events for a ticket, newest
// first. Query params: limit (default 50, cap 200) + cursor.
func (a *API) HandleGetTicketEvents(w http.ResponseWriter, r *http.Request) {
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "ticket id required")
		return
	}

	if _, _, ok := a.resolveTicketBoard(w, r, id, "card.read"); !ok {
		return
	}

	q := r.URL.Query()
	limit := 50
	if lim := q.Get("limit"); lim != "" {
		n, err := strconv.Atoi(lim)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = n
	}
	if limit > 200 {
		limit = 200
	}
	cursor := q.Get("cursor")

	ctx := r.Context()
	events, nextCursor, err := db.ListTicketEvents(ctx, a.dbPool, id, limit, cursor)
	if err != nil {
		log.Printf("api list ticket events %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	infos := make([]ticketEventInfo, 0, len(events))
	for _, e := range events {
		data := e.Data
		if data == nil {
			data = json.RawMessage("null")
		}
		infos = append(infos, ticketEventInfo{
			ID:           e.ID,
			TicketID:     e.TicketID,
			Type:         e.Type,
			Actor:        e.Actor,
			FromColumnID: e.FromColumnID,
			ToColumnID:   e.ToColumnID,
			Data:         data,
			CreatedAt:    e.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}

	writeJSON(w, http.StatusOK, listEventsResponse{
		Events:     infos,
		NextCursor: nextCursor,
	})
}
