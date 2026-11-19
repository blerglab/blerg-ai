package server

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/blerglab/blerg-ai/runner/internal/db"

	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

// HandleLiveBoardView shares the live-card credential exchange and sanitized tool transport.
// Pagination is explicit so large boards are never silently truncated.
func (a *API) HandleLiveBoardView(w http.ResponseWriter, r *http.Request) {
	record := &boardViewResponse{ResponseWriter: w, status: http.StatusOK}
	w = record
	defer func() {
		if record.status >= 400 {
			log.Printf("board view: status=%d class=%s", record.status, boardFailureClass(record.status))
		}
	}()
	r.SetPathValue("number", "1")
	proof, board, _, caller, ok := a.boardCardRequest(w, r)
	if !ok {
		return
	}
	if !a.allowBrowserBoardTool(w, r, proof.AccountID, board, "blerg_card_search") {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	call := func(ctx context.Context, tool string, args map[string]any, out any) *APIError {
		args["board_id"] = board
		data, _ := json.Marshal(args)
		raw, err := caller.CallBoard(ctx, mcpgw.BoardCall{Proof: proof, BoardID: board, SessionRef: a.auditRef(r, proof.AccountID), Tool: tool, Arguments: data, ReadOnly: true})
		if err != nil {
			return boardCallError(err)
		}
		return boardToolResult(raw, out)
	}
	var columns []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		Rank     string `json:"rank"`
		Terminal bool   `json:"is_terminal"`
	}
	var releases []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if r.URL.Query().Get("metadata") != "0" {
		if err := call(r.Context(), "blerg_column_list", map[string]any{}, &columns); err != nil {
			writeAPIError(w, err)
			return
		}
		if err := call(r.Context(), "blerg_release_list", map[string]any{}, &releases); err != nil {
			writeAPIError(w, err)
			return
		}
	}
	var page struct {
		Cards []struct {
			paletteCard
			Type       string   `json:"type"`
			Priority   string   `json:"priority"`
			Size       *string  `json:"size"`
			Risk       *string  `json:"risk"`
			Complexity *string  `json:"complexity"`
			StaleAt    *string  `json:"stale_at"`
			StuckAt    *string  `json:"stuck_at"`
			Tags       []string `json:"tags"`
			ReleaseID  *string  `json:"release_id"`
		} `json:"cards"`
		NextCursor string `json:"next_cursor"`
	}
	args := map[string]any{"page": true, "summary": true, "limit": 100, "cursor": r.URL.Query().Get("cursor"), "query": r.URL.Query().Get("q"), "tag": r.URL.Query().Get("tag"), "release_id": r.URL.Query().Get("release")}
	if err := call(r.Context(), "blerg_card_search", args, &page); err != nil {
		writeAPIError(w, err)
		return
	}
	if len(page.Cards) > 100 {
		writeError(w, 502, "The board returned too many cards in one page.")
		return
	}
	for _, c := range page.Cards {
		if c.BoardID != board || c.Number <= 0 {
			writeError(w, 502, "The board returned invalid cards.")
			return
		}
	}
	writeJSON(w, 200, map[string]any{"columns": columns, "releases": releases, "cards": page.Cards, "next_cursor": page.NextCursor})
}

// A native tab is scoped to its workspace as well as the person's board role.
// Re-read grants for tab views, tab card reads and tab moves, including after a downgrade.
// Pinned card reads and Status actions use the person’s board role; session is audit-only there.
func (a *API) allowBrowserBoardTool(w http.ResponseWriter, r *http.Request, account, board, tool string) bool {
	session := r.URL.Query().Get("session")
	if session == "" {
		writeError(w, 403, "A workspace is required for a board tab.")
		return false
	}
	if a.dbPool == nil || !validSessionID(session) {
		writeError(w, 403, "Workspace board access is unavailable.")
		return false
	}
	row, err := db.GetSession(r.Context(), a.dbPool, session)
	if err != nil || !canSeeAccount(account, row) {
		writeError(w, 404, "Workspace not found.")
		return false
	}
	level, err := a.effectiveBoardLevel(r.Context(), row, board)
	if err != nil {
		writeError(w, 503, "Workspace board access could not be read.")
		return false
	}
	if !mcpgw.BoardLevelAllows(level.Level, tool) {
		writeError(w, 403, "This workspace does not have access to that board action.")
		return false
	}
	return true
}

type boardViewResponse struct {
	http.ResponseWriter
	status int
}

func (w *boardViewResponse) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func boardFailureClass(status int) string {
	switch status {
	case 401, 403, 404:
		return "access"
	case 429:
		return "rate_limit"
	case 502, 503, 504:
		return "upstream"
	default:
		return "request"
	}
}
