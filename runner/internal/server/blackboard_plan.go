package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

func (a *API) blackboardStore() db.BlackboardStore { return db.BlackboardStore{Pool: a.dbPool} }
func blackboardError(w http.ResponseWriter, err error) {
	failurelog.Default.Event("blackboard_request", slog.LevelWarn, "component", "runner", "cause", blackboardFailureCause(err))
	var upstream *APIError
	if errors.As(err, &upstream) {
		writeAPIError(w, upstream)
		return
	}
	code := http.StatusInternalServerError
	switch {
	case errors.Is(err, blackboard.ErrNotFound):
		code = 404
	case errors.Is(err, blackboard.ErrForbidden):
		code = 403
	case errors.Is(err, blackboard.ErrConflict), errors.Is(err, blackboard.ErrFrozen):
		code = 409
	case errors.Is(err, blackboard.ErrInvalid):
		code = 400
	case errors.Is(err, blackboard.ErrNeedsRelease):
		code = 422
	}
	if code == 500 {
		writeDatabaseFailure(w, "access the blackboard", "blackboard store", err)
		return
	}
	writeError(w, code, err.Error())
}
func decodeBlackboard(w http.ResponseWriter, r *http.Request, out any) bool {
	controller := http.NewResponseController(w)
	deadline := time.Now().Add(5 * time.Second)
	if end, ok := r.Context().Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	if err := controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
		failurelog.Default.Event("blackboard_read_deadline", slog.LevelWarn, "component", "runner", "cause", failurelog.Cause(err))
		writeError(w, 503, "Request reader unavailable.")
		return false
	}
	complete := false
	defer func() {
		if !complete {
			return
		}
		if err := controller.SetReadDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
			failurelog.Default.Event("blackboard_read_deadline_reset", slog.LevelWarn, "component", "runner", "cause", failurelog.Cause(err))
		}
	}()
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	err := d.Decode(out)
	if err == nil {
		var trailing any
		if tail := d.Decode(&trailing); !errors.Is(tail, io.EOF) {
			err = tail
			if err == nil {
				err = blackboard.ErrInvalid
			}
		}
	}
	if err != nil {
		// Do not remove the read bound while net/http closes an unread body.
		// Successful requests reached EOF and can safely reuse the connection.
		r.Close = true
		w.Header().Set("Connection", "close")
		failurelog.Default.Event("blackboard_decode", slog.LevelWarn, "component", "runner", "cause", failurelog.Cause(err))
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() {
			writeError(w, 408, "Blackboard request body timed out.")
			return false
		}
		writeError(w, 400, "Invalid blackboard request.")
		return false
	}
	complete = true
	return true
}

// blackboardAccess rechecks workspace visibility, the workspace grant and Core's
// live board permission even when serving stored history. No cached card text escapes.
func (a *API) blackboardAccess(w http.ResponseWriter, r *http.Request) (blackboard.Record, identity.Principal, bool) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return blackboard.Record{}, p, false
	}
	if p.Kind != "human" || p.OnBehalfOf != "" || p.Sid == "" {
		writeError(w, 403, "Sign in as a person.")
		return blackboard.Record{}, p, false
	}
	if a.dbPool == nil {
		writeError(w, 503, "Database unavailable.")
		return blackboard.Record{}, p, false
	}
	store := a.blackboardStore()
	var record blackboard.Record
	var err error
	record, err = store.LoadPartition(r.Context(), r.PathValue("id"), blackboard.Partition{Kind: blackboard.PartitionCore})
	if err != nil {
		blackboardError(w, err)
		return record, p, false
	}
	workspace := r.URL.Query().Get("session")
	if workspace != "" {
		row, err := db.GetSession(r.Context(), a.dbPool, workspace)
		if err != nil {
			blackboardError(w, err)
			return record, p, false
		}
		if row != nil && row.ParentSessionID != nil {
			workspace, err = sessionWorkspaceRoot(r.Context(), a.dbPool, row)
			if err != nil {
				blackboardError(w, err)
				return record, p, false
			}
		}
	}
	board := ""
	for _, l := range record.Links {
		if l.WorkspaceID == workspace {
			board = l.BoardID
			break
		}
	}
	boards := blackboard.ReferencedBoards(record)
	ownerRead := workspace == "" && p.Sub == record.Owner.ID && len(boards) == 0
	if board == "" && record.WorkspaceID != workspace && !ownerRead {
		writeError(w, 404, "Blackboard not found in this workspace.")
		return record, p, false
	}
	if len(boards) == 0 {
		if p.Sub == record.Owner.ID {
			w.Header().Set("Cache-Control", "no-store")
			return record, p, true
		}
		row, err := db.GetSession(r.Context(), a.dbPool, workspace)
		if err != nil {
			blackboardError(w, err)
			return record, p, false
		}
		if row == nil || !canSeeAccount(p.Sub, row) {
			writeError(w, 404, "Workspace not found.")
			return record, p, false
		}
		w.Header().Set("Cache-Control", "no-store")
		return record, p, true
	}
	for _, board = range boards {
		if p.Project != "" && p.Project != board {
			writeError(w, 404, "Blackboard not found.")
			return record, p, false
		}
		if !a.allowBrowserBoardTool(w, r, p.Sub, board, "blerg_card_search") {
			return record, p, false
		}
		caller := a.sessionBoardCaller()
		if caller == nil {
			writeError(w, 503, "Board unavailable.")
			return record, p, false
		}
		args, _ := json.Marshal(map[string]any{"board_id": board})
		raw, err := caller.CallBoard(r.Context(), mcpgw.BoardCall{Proof: mcpgw.Proof{AccountID: p.Sub, SessionID: p.Sid}, BoardID: board, SessionRef: workspace, Tool: "blerg_board_get", Arguments: args, ReadOnly: true})
		if err != nil {
			writeAPIError(w, blackboardBoardFailure(err))
			return record, p, false
		}
		var check any
		if err := boardToolResult(raw, &check); err != nil {
			blackboardError(w, err)
			return record, p, false
		}
	}
	if r.Method == http.MethodGet && r.URL.Path == "/api/blackboards/"+record.ID {
		a.reconcileBlackboardCuts(r.Context(), record)
		record, err = store.LoadPartition(r.Context(), record.ID, blackboard.Partition{Kind: blackboard.PartitionCore})
		if err != nil {
			blackboardError(w, err)
			return record, p, false
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	return record, p, true
}
func (a *API) HandleBlackboard(w http.ResponseWriter, r *http.Request) {
	record, p, ok := a.blackboardAccess(w, r)
	if !ok {
		return
	}
	offset, limit, ok := blackboardPage(w, r)
	if !ok {
		return
	}
	record, total, pending, err := a.blackboardStore().LoadReadPage(r.Context(), record.ID, offset, limit, p.Sub == record.Owner.ID)
	if err != nil {
		blackboardError(w, err)
		return
	}
	record.Items = blackboardRows(record.Items, offset, limit)
	for i := range record.Milestones {
		record.Milestones[i].Frozen = nil
	}
	record.Goals = blackboardRows(record.Goals, offset, limit)
	record.Thrusts = blackboardRows(record.Thrusts, offset, limit)
	record.Milestones = blackboardRows(record.Milestones, offset, limit)
	record.Links = blackboardRows(record.Links, offset, limit)
	writeJSON(w, 200, map[string]any{"record": record, "pending_proposals": pending, "next_cursor": blackboardCursor(offset, limit, total)})
}
func (a *API) HandleBlackboardPropose(w http.ResponseWriter, r *http.Request) {
	record, p, ok := a.blackboardAccess(w, r)
	if !ok {
		return
	}
	var body struct {
		MilestoneID string              `json:"milestone_id"`
		Changes     []blackboard.Change `json:"changes"`
	}
	if !decodeBlackboard(w, r, &body) {
		return
	}
	id, err := a.blackboardService(r, record, p).Propose(r.Context(), blackboard.Actor{ID: p.Sub, Kind: p.Kind}, body.MilestoneID, body.Changes)
	if err != nil {
		blackboardError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"proposal_id": id, "status": "pending"})
}
func (a *API) HandleBlackboardCommit(w http.ResponseWriter, r *http.Request) {
	record, p, ok := a.blackboardAccess(w, r)
	if !ok {
		return
	}
	_ = record
	_ = p
	writeError(w, http.StatusGone, "Reply to Main's bound question in chat or Needs you to decide this plan.")
}

func (a *API) RegisterBlackboardRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/sessions/{session}/plan", a.HandleBlackboardAgent)
	mux.HandleFunc("POST /api/sessions/{session}/plan", a.HandleBlackboardAgent)
	mux.HandleFunc("POST /api/blackboards/{id}/events", a.HandleBlackboardEvent)
	mux.HandleFunc("GET /api/blackboards/{id}/timeline", a.HandleBlackboardTimeline)
	mux.HandleFunc("GET /api/blackboards/{id}/milestones/{milestone}/timeline", a.HandleBlackboardTimeline)
	mux.HandleFunc("POST /api/blackboards/{id}/plan", a.HandleBlackboardApply)
	mux.HandleFunc("GET /api/blackboards/{id}/slice", a.HandleBlackboardSlice)
	mux.HandleFunc("GET /api/blackboards/{id}/milestones/{milestone}/slice", a.HandleBlackboardSlice)
	mux.HandleFunc("POST /api/blackboards/{id}/milestones/{milestone}/final", a.HandleBlackboardFinal)
	mux.HandleFunc("POST /api/blackboards/{id}/milestones/{milestone}/final/abort", a.HandleBlackboardAbortFinal)
	mux.HandleFunc("GET /api/blackboards", a.HandleListBlackboards)
	mux.HandleFunc("POST /api/blackboards", a.HandleCreateBlackboard)
	mux.HandleFunc("GET /api/blackboards/{id}", a.HandleBlackboard)
	mux.HandleFunc("POST /api/blackboards/{id}/proposals", a.HandleBlackboardPropose)
	mux.HandleFunc("POST /api/blackboards/{id}/commit", a.HandleBlackboardCommit)
	mux.HandleFunc("POST /api/sessions/{session}/plan/draft", a.HandleDraftBlackboardPlan)
	mux.HandleFunc("GET /api/blackboards/draft", a.HandleGetBlackboardDraft)
	mux.HandleFunc("DELETE /api/blackboards/draft", a.HandleDiscardBlackboardDraft)
	mux.HandleFunc("POST /api/blackboards/draft/create", a.HandleCreateBlackboardFromDraft)
	mux.HandleFunc("POST /api/sessions/{session}/plan/request-setup", a.HandleRequestBlackboardSetup)
}

// HandleCreateBlackboard is retired; creation requires an answered draft ask.
func (a *API) HandleCreateBlackboard(w http.ResponseWriter, r *http.Request) {
	a.retiredDraftDecision(w, r)
}

// HandleListBlackboards returns only identities linked to the visible workspace;
// each result still goes through the full live board access check.
func (a *API) HandleListBlackboards(w http.ResponseWriter, r *http.Request) {
	p, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if p.Kind != "human" || p.OnBehalfOf != "" || p.Sid == "" {
		writeError(w, 403, "Sign in as a person.")
		return
	}
	if a.dbPool == nil {
		writeError(w, 503, "Database unavailable.")
		return
	}
	workspace := r.URL.Query().Get("session")
	row, err := db.GetSession(r.Context(), a.dbPool, workspace)
	if err != nil {
		blackboardError(w, err)
		return
	}
	if row == nil || !canSeeAccount(p.Sub, row) {
		writeError(w, 404, "Workspace not found.")
		return
	}
	workspace, err = sessionWorkspaceRoot(r.Context(), a.dbPool, row)
	if err != nil {
		blackboardError(w, err)
		return
	}
	offset, limit, ok := blackboardPage(w, r)
	if !ok {
		return
	}
	rows, err := a.dbPool.Query(r.Context(), `SELECT id FROM blackboards WHERE record->>'workspace_id'=$1 OR id IN (SELECT blackboard_id FROM blackboard_links WHERE workspace_id=$1) ORDER BY id LIMIT $2 OFFSET $3`, workspace, limit+1, offset)
	if err != nil {
		blackboardError(w, err)
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		blackboardError(w, err)
		return
	}
	next := ""
	if len(ids) > limit {
		next = blackboardCursor(offset, limit, offset+len(ids))
		ids = ids[:limit]
	}
	out := []map[string]any{}
	for _, id := range ids {
		r.SetPathValue("id", id)
		record, _, ok := a.blackboardAccess(w, r)
		if !ok {
			return
		}
		for i := range record.Milestones {
			record.Milestones[i].Frozen = nil
		}
		out = append(out, map[string]any{"id": id, "name": record.Name, "milestones": record.Milestones})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"blackboards": out, "next_cursor": next})
}

func (a *API) HandleBlackboardApply(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := a.blackboardAccess(w, r); !ok {
		return
	}
	writeError(w, http.StatusGone, "Discuss plan changes with Main and reply to its bound question.")
}

func blackboardFailureCause(err error) string {
	var check *blackboard.CheckError
	if errors.As(err, &check) {
		return check.Code()
	}
	var bound *boundAskError
	if errors.As(err, &bound) {
		return bound.cause
	}
	var link *blackboard.PlanLinkError
	if errors.As(err, &link) {
		return link.Cause
	}
	return failurelog.Cause(err)
}
