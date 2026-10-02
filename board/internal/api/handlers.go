package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/templates"
)

// ── auth ─────────────────────────────────────────────────────────────────────

func (a *API) handleMe(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	out := map[string]any{"kind": p.Kind, "is_admin": p.IsAdmin(), "is_human": p.IsHuman()}
	// A person's own blerg-core account id, so the UI can tell them a
	// board's automation token is theirs.
	if id := p.HumanAccountID(); id != "" {
		out["account_id"] = id
	}
	if p.Token != nil {
		out["token_label"] = p.Token.Label
		out["board_id"] = p.Token.BoardID
		out["capabilities"] = p.Token.Capabilities
	}
	writeJSON(w, http.StatusOK, out)
}

// ── boards ───────────────────────────────────────────────────────────────────

// requireReadScope is the capability gate for the two instance-wide read
// routes (list boards, global search): a board-scoped agent token needs
// card.read on its own board (the handler then narrows results to it);
// everything else needs unscoped card.read via RequireGlobalRead. Without
// this, any verified core token — including a password.change-only one (R7)
// — could enumerate every board and search every card.
func requireReadScope(p auth.Principal) error {
	if p.Token != nil && p.Token.BoardID != nil {
		return p.RequireBoard(*p.Token.BoardID, "card.read")
	}
	return p.RequireGlobalRead()
}

func (a *API) handleListBoards(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if err := requireReadScope(p); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	boards, err := db.ListBoards(r.Context(), a.Pool)
	if err != nil {
		writeDBError(w, err)
		return
	}
	// Board-scoped tokens (any kind) see only their board.
	if p.Token != nil && p.Token.BoardID != nil {
		filtered := boards[:0]
		for _, b := range boards {
			if b.ID == *p.Token.BoardID {
				filtered = append(filtered, b)
			}
		}
		boards = filtered
	}
	writeJSON(w, http.StatusOK, boards)
}

func (a *API) handleCreateBoard(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if err := p.RequireAdmin(""); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	body, err := decode[struct {
		db.BoardParams
		Template string `json:"template"`
	}](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	params := body.BoardParams
	if body.Template != "" {
		if err := applyTemplate(&params, body.Template); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	b, err := db.CreateBoard(r.Context(), a.Pool, params)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, b)
}

// applyTemplate copies a registered template's columns and field schema into
// params. The board row, columns and schema are then written by the one
// db.CreateBoard transaction. A template owns the columns and the schema, so
// a request that also names either is ambiguous and refused.
func applyTemplate(params *db.BoardParams, id string) error {
	tpl, ok := templates.Get(id)
	if !ok {
		return fmt.Errorf("unknown template %q", id)
	}
	// An empty columns list or an empty field_schema ([] or null) says
	// nothing, so it is treated as absent: a form that always sends them can
	// still pick a template. A non-empty one is ambiguous and refused.
	if len(params.Columns) > 0 || !emptyFieldSchema(params.FieldSchema) {
		return errors.New("template cannot be combined with columns or field_schema: send either the template or your own columns and field_schema")
	}
	params.Columns, params.TerminalColumns = nil, nil
	for _, c := range tpl.Columns {
		params.Columns = append(params.Columns, c.Name)
		if c.Terminal {
			params.TerminalColumns = append(params.TerminalColumns, c.Name)
		}
	}
	params.FieldSchema = tpl.FieldSchema
	return nil
}

// emptyFieldSchema reports whether a field_schema value is absent, null or an
// empty array.
func emptyFieldSchema(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || string(t) == "null" || string(t) == "[]"
}

func (a *API) handleGetBoard(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if err := p.RequireBoard(r.PathValue("id"), "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	b, err := db.GetBoard(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b)
}

func (a *API) handleGetSchema(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if err := p.RequireBoard(r.PathValue("id"), "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	b, err := db.GetBoard(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b.FieldSchema)
}

func (a *API) handleUpdateBoard(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	boardID := r.PathValue("id")
	if err := p.RequireAdmin(boardID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	// automation_token rides in the same PATCH but is not a BoardParams
	// field: it is write-only and verified against the caller before it is
	// stored (setAutomationToken). Absent leaves it alone; "" clears it.
	body, err := decode[struct {
		db.BoardParams
		AutomationToken *string `json:"automation_token"`
	}](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.AutomationEngine != nil && !db.ValidAutomationEngine(*body.AutomationEngine) {
		// Refuse the whole PATCH before a token is stored alongside a bad engine.
		writeError(w, http.StatusUnprocessableEntity, "automation_engine: must be \"claude\", \"codex\" or \"hermes\"")
		return
	}
	var auto *db.AutomationTokenUpdate
	if body.AutomationToken != nil {
		var ok bool
		if auto, ok = a.verifyAutomationToken(w, p, *body.AutomationToken); !ok {
			return
		}
	}
	// One write: the token lands with the rest of the PATCH or not at all.
	b, err := db.UpdateBoardWithAutomation(r.Context(), a.Pool, boardID, body.BoardParams, auto)
	if err != nil {
		writeDBError(w, err)
		return
	}
	a.Hub.Broadcast(boardID, "board_changed")
	writeJSON(w, http.StatusOK, b)
}

func (a *API) handleDeleteBoard(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	// Human-only AND admin: the native service key is not a human, and a
	// core member is not an admin.
	if !p.IsHuman() || p.RequireAdmin(r.PathValue("id")) != nil {
		writeError(w, http.StatusForbidden, "board deletion requires a human with board.admin")
		return
	}
	if err := db.DeleteBoard(r.Context(), a.Pool, r.PathValue("id")); err != nil {
		writeDBError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── columns ──────────────────────────────────────────────────────────────────

func (a *API) handleListColumns(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if err := p.RequireBoard(r.PathValue("id"), "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	cols, err := db.ListColumns(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cols)
}

func (a *API) handleCreateColumn(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	boardID := r.PathValue("id")
	if err := p.RequireBoard(boardID, "column.write"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	req, err := decode[struct {
		Name       string `json:"name"`
		IsTerminal bool   `json:"is_terminal"`
	}](r)
	if err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}
	col, err := db.CreateColumn(r.Context(), a.Pool, boardID, req.Name, req.IsTerminal)
	if err != nil {
		writeDBError(w, err)
		return
	}
	a.Hub.Broadcast(boardID, "board_changed")
	writeJSON(w, http.StatusCreated, col)
}

func (a *API) handleRenameColumn(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	col, err := db.GetColumn(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(col.BoardID, "column.write"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	req, err := decode[struct {
		Name string `json:"name"`
	}](r)
	if err != nil || req.Name == "" {
		writeError(w, http.StatusBadRequest, "name required")
		return
	}
	if err := db.RenameColumn(r.Context(), a.Pool, col.ID, req.Name); err != nil {
		writeDBError(w, err)
		return
	}
	a.Hub.Broadcast(col.BoardID, "board_changed")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) handleDeleteColumn(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	col, err := db.GetColumn(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(col.BoardID, "column.write"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	if err := db.DeleteColumn(r.Context(), a.Pool, col.ID); err != nil {
		writeDBError(w, err)
		return
	}
	a.Hub.Broadcast(col.BoardID, "board_changed")
	w.WriteHeader(http.StatusNoContent)
}

// handleMoveColumn repositions a column among its board's siblings, placing
// it before before_id, or at the end if before_id is empty/omitted.
func (a *API) handleMoveColumn(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	col, err := db.GetColumn(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(col.BoardID, "column.write"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	req, err := decode[struct {
		BeforeID *string `json:"before_id"`
	}](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	moved, err := db.MoveColumn(r.Context(), a.Pool, col.ID, req.BeforeID)
	if err != nil {
		writeDBError(w, err)
		return
	}
	a.Hub.Broadcast(col.BoardID, "board_changed")
	writeJSON(w, http.StatusOK, moved)
}

// ── cards ────────────────────────────────────────────────────────────────────

// createCardRequest is CardParams plus the gate's dispute arguments.
type createCardRequest struct {
	db.CardParams
	DisputeOf string `json:"dispute_of,omitempty"`
	Rebuttal  string `json:"rebuttal,omitempty"`
}

func (a *API) handleCreateCard(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	boardID := r.PathValue("id")
	if err := p.RequireBoard(boardID, "card.write"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	req, err := decode[createCardRequest](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if err := a.resolveArtifactLinks(&req.CardParams); err != nil {
		writeDBError(w, err)
		return
	}
	board, err := db.GetBoard(r.Context(), a.Pool, boardID)
	if err != nil {
		writeDBError(w, err)
		return
	}
	payload, _ := json.Marshal(req.CardParams)
	out, handled := a.gateCheck(w, r, p, board, nil, "create", payload, req.DisputeOf, req.Rebuttal)
	if handled {
		return
	}
	res, err := db.CreateCard(r.Context(), a.Pool, boardID, req.CardParams, meta(p, out.ReviewID))
	if err != nil {
		writeDBError(w, err)
		return
	}
	a.stampGate(r.Context(), res.Card.ID, out)
	a.Hub.Broadcast(boardID, "card_changed")
	status := http.StatusCreated
	if res.Refreshed {
		status = http.StatusOK // dedup refresh, not a new card
	}
	writeJSON(w, status, res.Card)
}

func meta(p auth.Principal, reviewID *string) db.EventMeta {
	m := p.EventMeta()
	m.ReviewID = reviewID
	return m
}

func (a *API) handleGetCard(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	card, err := db.GetCard(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(card.BoardID, "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, card)
}

func (a *API) handleListCards(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	boardID := r.PathValue("id")
	if err := p.RequireBoard(boardID, "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	archived := r.URL.Query().Get("archived") == "1"
	cards, err := db.ListCards(r.Context(), a.Pool, boardID, archived)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cards)
}

func (a *API) handleSearch(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	boardID := r.PathValue("id")
	if err := p.RequireBoard(boardID, "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	params := db.SearchParams{
		BoardID: boardID, Query: q.Get("q"), Type: q.Get("type"),
		Tag: q.Get("tag"), Priority: q.Get("priority"),
		IncludeArchived: q.Get("include_archived") == "1", Limit: limit,
		Fields: map[string]string{},
	}
	for key, vals := range q {
		if k, ok := cutPrefix(key, "field."); ok && len(vals) > 0 {
			params.Fields[k] = vals[0]
		}
	}
	cards, err := db.SearchCards(r.Context(), a.Pool, params)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cards)
}

func cutPrefix(s, prefix string) (string, bool) {
	if len(s) > len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return "", false
}

func (a *API) handleUpdateCard(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	card, err := db.GetCard(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(card.BoardID, "card.write"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	req, err := decode[createCardRequest](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.IfMatch == nil {
		req.IfMatch = ifMatch(r)
	}
	if err := a.resolveArtifactLinks(&req.CardParams); err != nil {
		writeDBError(w, err)
		return
	}
	board, err := db.GetBoard(r.Context(), a.Pool, card.BoardID)
	if err != nil {
		writeDBError(w, err)
		return
	}
	payload, _ := json.Marshal(req.CardParams)
	out, handled := a.gateCheck(w, r, p, board, &card, "update", payload, req.DisputeOf, req.Rebuttal)
	if handled {
		return
	}
	updated, err := db.UpdateCard(r.Context(), a.Pool, card.ID, req.CardParams, meta(p, out.ReviewID))
	if err != nil {
		writeDBError(w, err)
		return
	}
	a.stampGate(r.Context(), card.ID, out)
	a.Hub.Broadcast(card.BoardID, "card_changed")
	writeJSON(w, http.StatusOK, updated)
}

func (a *API) handleMoveCard(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	card, err := db.GetCard(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(card.BoardID, "card.write"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	req, err := decode[struct {
		ColumnID     string  `json:"column_id"`
		BeforeCardID *string `json:"before_card_id"`
		IfMatch      *int    `json:"if_match"`
		DisputeOf    string  `json:"dispute_of"`
		Rebuttal     string  `json:"rebuttal"`
	}](r)
	if err != nil || req.ColumnID == "" {
		writeError(w, http.StatusBadRequest, "column_id required")
		return
	}
	if req.IfMatch == nil {
		req.IfMatch = ifMatch(r)
	}
	board, err := db.GetBoard(r.Context(), a.Pool, card.BoardID)
	if err != nil {
		writeDBError(w, err)
		return
	}
	payload, _ := json.Marshal(map[string]any{"card": card.Number, "to_column": req.ColumnID})
	out, handled := a.gateCheck(w, r, p, board, &card, "move", payload, req.DisputeOf, req.Rebuttal)
	if handled {
		return
	}
	moved, err := db.MoveCard(r.Context(), a.Pool, card.ID, req.ColumnID, req.BeforeCardID, req.IfMatch, meta(p, out.ReviewID))
	if err != nil {
		writeDBError(w, err)
		return
	}
	a.stampGate(r.Context(), card.ID, out)
	a.Hub.Broadcast(card.BoardID, "card_changed")
	go a.afterMove(context.WithoutCancel(r.Context()), board, moved) // detached: the review-flow reaction (spawn reviewer / relay findings) must outlive the request
	writeJSON(w, http.StatusOK, moved)
}

func (a *API) handleArchiveCard(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	card, err := db.GetCard(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(card.BoardID, "card.write"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	board, err := db.GetBoard(r.Context(), a.Pool, card.BoardID)
	if err != nil {
		writeDBError(w, err)
		return
	}
	payload, _ := json.Marshal(map[string]any{"card": card.Number, "archive": true})
	out, handled := a.gateCheck(w, r, p, board, &card, "archive", payload, "", "")
	if handled {
		return
	}
	archived, err := db.ArchiveCard(r.Context(), a.Pool, card.ID, ifMatch(r), meta(p, out.ReviewID))
	if err != nil {
		writeDBError(w, err)
		return
	}
	a.Hub.Broadcast(card.BoardID, "card_changed")
	writeJSON(w, http.StatusOK, archived)
}

func (a *API) handleDeleteCard(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	card, err := db.GetCard(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(card.BoardID, "card.write"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	board, err := db.GetBoard(r.Context(), a.Pool, card.BoardID)
	if err != nil {
		writeDBError(w, err)
		return
	}
	payload, _ := json.Marshal(map[string]any{"card": card.Number, "delete": true})
	_, handled := a.gateCheck(w, r, p, board, &card, "delete", payload, "", "")
	if handled {
		return
	}
	if err := db.DeleteCard(r.Context(), a.Pool, card.ID); err != nil {
		writeDBError(w, err)
		return
	}
	a.Hub.Broadcast(card.BoardID, "card_changed")
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleAddDep(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	req, err := decode[struct {
		DependsOn string `json:"depends_on"`
	}](r)
	if err != nil || req.DependsOn == "" {
		writeError(w, http.StatusBadRequest, "depends_on required")
		return
	}
	card, err := db.GetCard(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(card.BoardID, "card.write"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	// The blocker is named in the body: for a project-scoped token another
	// board's card answers exactly like a missing one (AddDependency would
	// otherwise tell the two apart).
	if !a.InScope(r.Context(), p, OwnerCard, req.DependsOn) {
		notFoundForScope(w)
		return
	}
	if err := db.AddDependency(r.Context(), a.Pool, card.ID, req.DependsOn, p.EventMeta()); err != nil {
		writeDBError(w, err)
		return
	}
	a.Hub.Broadcast(card.BoardID, "card_changed")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *API) handleRemoveDep(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	card, err := db.GetCard(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(card.BoardID, "card.write"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	if err := db.RemoveDependency(r.Context(), a.Pool, card.ID, r.PathValue("depID"), p.EventMeta()); err != nil {
		writeDBError(w, err)
		return
	}
	a.Hub.Broadcast(card.BoardID, "card_changed")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleCardComment appends an engineer's-log comment to the card trail.
func (a *API) handleCardComment(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	card, err := db.GetCard(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(card.BoardID, "card.write"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	req, err := decode[struct {
		Text string `json:"text"`
	}](r)
	if err != nil || req.Text == "" {
		writeError(w, http.StatusBadRequest, "text required")
		return
	}
	if err := db.AppendComment(r.Context(), a.Pool, card.ID, req.Text, p.EventMeta()); err != nil {
		writeDBError(w, err)
		return
	}
	a.Hub.Broadcast(card.BoardID, "card_changed")
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

func (a *API) handleCardEvents(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	card, err := db.GetCard(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(card.BoardID, "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := db.ListCardEvents(r.Context(), a.Pool, card.ID, after, limit)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

// ── tokens ───────────────────────────────────────────────────────────────────

func (a *API) handleListTokens(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if err := p.RequireAdmin(""); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	tokens, err := db.ListTokens(r.Context(), a.Pool)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

// handleMintToken: minting is instance-wide admin work (RequireAdmin("") —
// a blerg-core platform "admin" carries board.admin, a plain "member" does
// not, and a board-scoped token never qualifies), and a caller can never
// mint a token more privileged than itself (the clamp below).
func (a *API) handleMintToken(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	// Gate 1: only an instance-wide admin may mint.
	if err := p.RequireAdmin(""); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	if p.Token == nil {
		// The native key always resolves with its registered row (Resolve);
		// nothing else reaches here token-less. Refuse rather than mint an
		// empty-capability token.
		writeError(w, http.StatusForbidden, "minting principal carries no capability set")
		return
	}
	req, err := decode[struct {
		BoardID *string  `json:"board_id"`
		Label   string   `json:"label"`
		Caps    []string `json:"capabilities"`
		TTLHrs  int      `json:"ttl_hours"`
	}](r)
	if err != nil || req.Label == "" {
		writeError(w, http.StatusBadRequest, "label required")
		return
	}
	// Gate 2: a caller can never mint a token more privileged than itself.
	//
	// The requested capability set is clamped to the caller's own. An
	// EXPLICIT request for a capability the caller doesn't hold is an error
	// (silently handing back a weaker token than asked for would be a
	// confusing lie); the DEFAULT set below is intersected instead, since it
	// is this handler's suggestion rather than the caller's demand.
	if len(req.Caps) == 0 {
		defaults := []string{"card.read", "card.write", "column.write"}
		// Unscoped session tokens may create boards — board setup is agent
		// work (see /onboard). Board-scoped tokens stay narrow.
		if req.BoardID == nil {
			defaults = append(defaults, "board.admin")
		}
		req.Caps = nil
		for _, c := range defaults {
			if p.Token.HasCap(c) {
				req.Caps = append(req.Caps, c)
			}
		}
	} else {
		for _, c := range req.Caps {
			if !p.Token.HasCap(c) {
				writeError(w, http.StatusForbidden,
					"cannot mint capability "+c+": the minting token does not hold it")
				return
			}
		}
	}
	if len(req.Caps) == 0 {
		writeError(w, http.StatusForbidden, "minting token holds none of the requested capabilities")
		return
	}
	ttl := 24 * time.Hour
	if req.TTLHrs > 0 {
		ttl = time.Duration(req.TTLHrs) * time.Hour
	}
	tok, raw, err := db.MintToken(r.Context(), a.Pool, req.BoardID, "agent", req.Label, req.Caps, ttl)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"token": tok, "secret": raw})
}

func (a *API) handleRefreshToken(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	// Refresh issues a successor tokens ROW from the caller's own row. A
	// core-issued agent principal has no row (its Token is synthesized), so
	// it can't be refreshed — and must not materialise a native token here.
	if p.Kind != auth.KindAgent || p.Token == nil || p.FromCore {
		writeError(w, http.StatusBadRequest, "refresh with the board agent token being refreshed")
		return
	}
	tok, raw, err := db.RefreshToken(r.Context(), a.Pool, *p.Token, 24*time.Hour)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"token": tok, "secret": raw})
}

// handleRevokeToken: instance-wide admin work, and never upward — a caller
// may revoke only tokens whose every capability it holds itself (the native
// key holds everything). Otherwise a board.admin could knock out the env
// service key's row, or a gate.bypass token it could never have minted.
func (a *API) handleRevokeToken(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if err := p.RequireAdmin(""); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	target, err := db.GetToken(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if !p.IsNativeService() {
		for _, c := range target.Capabilities {
			if p.Token == nil || !p.Token.HasCap(c) {
				writeError(w, http.StatusForbidden, "cannot revoke a token more privileged than the caller")
				return
			}
		}
	}
	if err := db.RevokeToken(r.Context(), a.Pool, target.ID); err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ── reviews (gate log + held queue) ─────────────────────────────────────────

func (a *API) handleListReviews(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	boardID := r.URL.Query().Get("board")
	var authErr error
	if boardID != "" {
		authErr = p.RequireBoard(boardID, "card.read")
	} else {
		authErr = p.RequireGlobalRead()
	}
	if authErr != nil {
		writeError(w, http.StatusForbidden, authErr.Error())
		return
	}
	if r.URL.Query().Get("state") == "held" {
		out, err := db.ListHeldReviews(r.Context(), a.Pool, boardID)
		if err != nil {
			writeDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	out, err := db.ListReviews(r.Context(), a.Pool, boardID, limit)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleGetReview(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	// Fetch, then authorize on the review's board: like every other
	// id-addressed read here (cards, columns), a token scoped elsewhere gets
	// 403 for an existing id and 404 for an unknown one. Review ids are
	// unguessable uuids, so that distinction is accepted.
	rev, err := db.GetReview(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := p.RequireBoard(rev.BoardID, "card.read"); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rev)
}

// handleResolveReview is the human's held-queue action: approve applies the
// stored payload; reject just resolves. A human WITH board.admin — a core
// member is human but not a curator.
func (a *API) handleResolveReview(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if !p.IsHuman() || !p.IsAdmin() {
		writeError(w, http.StatusForbidden, "held-review resolution requires a human with board.admin")
		return
	}
	req, err := decode[struct {
		Decision string `json:"decision"` // approve|reject
		Reason   string `json:"reason"`
	}](r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	rev, err := db.GetReview(r.Context(), a.Pool, r.PathValue("id"))
	if err != nil {
		writeDBError(w, err)
		return
	}
	switch req.Decision {
	case "approve":
		var cardID, note string
		if rev.Operation == "create" {
			cardID, err = a.applyHeldCreate(r.Context(), rev)
		} else {
			cardID, note, err = a.applyHeldNonCreate(r.Context(), rev)
		}
		if err != nil {
			switch {
			case errors.Is(err, errTargetGone):
				writeError(w, http.StatusConflict, "target card no longer exists; it was deleted since this review was held")
			case errors.Is(err, errTargetArchived):
				writeError(w, http.StatusConflict, "target card was archived since this review was held; perform the change directly")
			default:
				writeDBError(w, err)
			}
			return
		}
		// A held delete removes the card the review would otherwise point at,
		// so leave card_id unset rather than reference a row that's gone.
		var cardIDForReview *string
		if rev.Operation != "delete" {
			cardIDForReview = &cardID
		}
		if _, err := db.ResolveHeldReview(r.Context(), a.Pool, rev.ID, "accept", "human", cardIDForReview); err != nil {
			writeDBError(w, err)
			return
		}
		a.Hub.Broadcast(rev.BoardID, "card_changed")
		resp := map[string]any{"ok": true, "card_id": cardID}
		if note != "" {
			resp["note"] = note
		}
		writeJSON(w, http.StatusOK, resp)
	case "reject":
		if _, err := db.ResolveHeldReview(r.Context(), a.Pool, rev.ID, "deny", "human", nil); err != nil {
			writeDBError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		writeError(w, http.StatusBadRequest, "decision must be approve or reject")
	}
}

// handleGlobalSearch: cross-board card search for the UI ("did we file /
// land X?"). Agents stay inside their board scope; humans search everything,
// archived included (landed work is often archived or done).
func (a *API) handleGlobalSearch(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	if err := requireReadScope(p); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	q := r.URL.Query().Get("q")
	if q == "" {
		writeJSON(w, http.StatusOK, []db.Card{})
		return
	}
	params := db.SearchParams{
		Query:           q,
		BoardID:         r.URL.Query().Get("board"),
		Limit:           30,
		IncludeArchived: true,
	}
	if p.Token != nil && p.Token.BoardID != nil {
		params.BoardID = *p.Token.BoardID
	}
	cards, err := db.SearchCards(r.Context(), a.Pool, params)
	if err != nil {
		writeDBError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cards)
}
