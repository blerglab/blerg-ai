package server

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

type paletteCard struct {
	ID         string  `json:"id"`
	BoardID    string  `json:"board_id"`
	Number     int     `json:"number"`
	Title      string  `json:"title"`
	ColumnID   *string `json:"column_id"`
	ColumnName string  `json:"column_name"`
}

// HandlePaletteCards uses the Board's existing search operation with a read-only
// credential exchanged for the browser's own sign-in, never a session's grant.
func (a *API) HandlePaletteCards(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if err := checkGrantRequester(requesterOf(principal)); err != nil {
		writeAPIError(w, err)
		return
	}
	board := r.PathValue("board")
	if !gatewayServerName.MatchString(board) {
		writeError(w, 400, "Name a board.")
		return
	}
	if principal.Project != "" && principal.Project != board {
		writeError(w, 404, "Board not found.")
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" || !filingText(query, 256, false) {
		writeError(w, 400, "Enter a card number or search text up to 256 characters.")
		return
	}
	caller := a.sessionBoardCaller()
	if caller == nil {
		writeError(w, 503, "Board search is unavailable.")
		return
	}
	proof := mcpgw.Proof{AccountID: principal.Sub, SessionID: principal.Sid}
	w.Header().Set("Cache-Control", "no-store")
	cards := []paletteCard{}
	if number, err := strconv.Atoi(strings.TrimPrefix(query, "#")); err == nil && number > 0 {
		card, apiErr := readLiveBoardCard(r.Context(), caller, proof, board, number, "")
		if apiErr != nil {
			writeAPIError(w, apiErr)
			return
		}
		cards = append(cards, paletteCard{ID: card.ID, BoardID: board, Number: card.Number, Title: card.Title, ColumnID: card.ColumnID, ColumnName: card.ColumnName})
	} else {
		args, _ := json.Marshal(map[string]any{"board_id": board, "query": query, "limit": 100})
		raw, err := caller.CallBoard(r.Context(), mcpgw.BoardCall{Proof: proof, BoardID: board, Tool: "blerg_card_search", Arguments: args, ReadOnly: true})
		if err != nil {
			writeAPIError(w, boardCallError(err))
			return
		}
		if apiErr := boardToolResult(raw, &cards); apiErr != nil {
			writeAPIError(w, apiErr)
			return
		}
		if len(cards) > 100 {
			writeError(w, 502, "Board returned too many search results.")
			return
		}
		// Search returns column IDs. Resolve the labels once per board, rather than
		// reading every matching card (which would also fetch descriptions and trails).
		args, _ = json.Marshal(map[string]string{"board_id": board})
		raw, err = caller.CallBoard(r.Context(), mcpgw.BoardCall{Proof: proof, BoardID: board, Tool: "blerg_column_list", Arguments: args, ReadOnly: true})
		if err != nil {
			writeAPIError(w, boardCallError(err))
			return
		}
		var columns []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if apiErr := boardToolResult(raw, &columns); apiErr != nil {
			writeAPIError(w, apiErr)
			return
		}
		for i := range cards {
			if cards[i].BoardID != board || cards[i].Number <= 0 {
				writeError(w, 502, "Board returned invalid search results.")
				return
			}
			for _, column := range columns {
				if cards[i].ColumnID != nil && *cards[i].ColumnID == column.ID {
					cards[i].ColumnName = column.Name
				}
			}
		}
	}
	if cards == nil {
		cards = []paletteCard{}
	}
	writeJSON(w, 200, map[string]any{"cards": cards, "truncated": len(cards) == 100})
}
