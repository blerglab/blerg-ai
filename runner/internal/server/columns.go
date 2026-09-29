package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/jackc/pgx/v5"
)

// ── POST /api/boards/{id}/columns ─────────────────────────────────────────────

// postColumnRequest is the body for POST /api/boards/{id}/columns.
type postColumnRequest struct {
	Name       string  `json:"name"`
	After      *string `json:"after"`
	IsTerminal bool    `json:"terminal"`
	OpID       *string `json:"op_id"`
}

// HandlePostColumn creates a new column on a board.
// POST /api/boards/{id}/columns
func (a *API) HandlePostColumn(w http.ResponseWriter, r *http.Request) {
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	boardID := r.PathValue("id")
	if boardID == "" {
		writeError(w, http.StatusBadRequest, "board id required")
		return
	}
	if _, ok := a.authBrowserOrBoard(w, r, boardID, "card.write"); !ok {
		return
	}

	var req postColumnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	opID := ""
	if req.OpID != nil {
		opID = *req.OpID
	}

	ctx := r.Context()
	col, err := db.AddColumn(ctx, a.dbPool, boardID, req.Name, req.After, req.IsTerminal)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "board not found")
			return
		}
		log.Printf("api post column board %s: %v", boardID, err)
		writeError(w, http.StatusInternalServerError, "create failed")
		return
	}
	ci := columnRowToInfo(col)
	a.hub.BroadcastBoard(boardID, columnChangedEvent{
		Type:   "column_changed",
		Column: ci,
		OpID:   opID,
	})
	writeJSON(w, http.StatusCreated, ci)
}

// ── PATCH /api/columns/{id} ───────────────────────────────────────────────────

// patchColumnRequest is the body for PATCH /api/columns/{id}.
// All fields are optional; only present (non-nil) fields are applied.
type patchColumnRequest struct {
	Name       *string `json:"name"`
	After      *string `json:"after"`
	Before     *string `json:"before"`
	IsTerminal *bool   `json:"is_terminal"`
	OpID       *string `json:"op_id"`
}

// HandlePatchColumn applies one or more mutations (rename / reorder / terminal)
// to a column and returns the updated column.
// PATCH /api/columns/{id}
func (a *API) HandlePatchColumn(w http.ResponseWriter, r *http.Request) {
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "column id required")
		return
	}

	ctx := r.Context()

	// Resolve the board so we can auth-check it.
	boardID, err := db.ColumnBoardID(ctx, a.dbPool, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "column not found")
			return
		}
		log.Printf("api patch column resolve board %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	if _, ok := a.authBrowserOrBoard(w, r, boardID, "card.write"); !ok {
		return
	}

	var req patchColumnRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	opID := ""
	if req.OpID != nil {
		opID = *req.OpID
	}

	var col db.ColumnRow
	var updated bool

	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		col, err = db.RenameColumn(ctx, a.dbPool, id, name)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "column not found")
				return
			}
			log.Printf("api patch column rename %s: %v", id, err)
			writeError(w, http.StatusInternalServerError, "rename failed")
			return
		}
		updated = true
	}

	if req.After != nil || req.Before != nil {
		col, err = db.MoveColumn(ctx, a.dbPool, id, req.After, req.Before)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "column not found")
				return
			}
			log.Printf("api patch column move %s: %v", id, err)
			writeError(w, http.StatusInternalServerError, "move failed")
			return
		}
		updated = true
	}

	if req.IsTerminal != nil {
		col, err = db.SetColumnTerminal(ctx, a.dbPool, id, *req.IsTerminal)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "column not found")
				return
			}
			log.Printf("api patch column set terminal %s: %v", id, err)
			writeError(w, http.StatusInternalServerError, "set terminal failed")
			return
		}
		updated = true
	}

	// No-op body: return current state.
	if !updated {
		col, err = db.GetColumn(ctx, a.dbPool, id)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				writeError(w, http.StatusNotFound, "column not found")
				return
			}
			log.Printf("api patch column get %s: %v", id, err)
			writeError(w, http.StatusInternalServerError, "query failed")
			return
		}
	}

	ci := columnRowToInfo(col)
	if updated {
		a.hub.BroadcastBoard(boardID, columnChangedEvent{
			Type:   "column_changed",
			Column: ci,
			OpID:   opID,
		})
	}
	writeJSON(w, http.StatusOK, ci)
}

// ── DELETE /api/columns/{id} ─────────────────────────────────────────────────

// HandleDeleteColumn removes a column.
// Returns 409 if the column still holds live tickets; 204 on success.
// DELETE /api/columns/{id}
func (a *API) HandleDeleteColumn(w http.ResponseWriter, r *http.Request) {
	if a.dbPool == nil {
		writeError(w, http.StatusServiceUnavailable, "database not available")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "column id required")
		return
	}

	ctx := r.Context()

	boardID, err := db.ColumnBoardID(ctx, a.dbPool, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "column not found")
			return
		}
		log.Printf("api delete column resolve board %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "query failed")
		return
	}

	if _, ok := a.authBrowserOrBoard(w, r, boardID, "card.write"); !ok {
		return
	}

	opID := r.URL.Query().Get("op_id")

	if err := db.DeleteColumn(ctx, a.dbPool, id); err != nil {
		if errors.Is(err, db.ErrColumnHasLiveTickets) {
			writeError(w, http.StatusConflict, "column has live tickets")
			return
		}
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "column not found")
			return
		}
		log.Printf("api delete column %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}

	a.hub.BroadcastBoard(boardID, columnRemovedEvent{
		Type:     "column_removed",
		ColumnID: id,
		BoardID:  boardID,
		OpID:     opID,
	})
	w.WriteHeader(http.StatusNoContent)
}
