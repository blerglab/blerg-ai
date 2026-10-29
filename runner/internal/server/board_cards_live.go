package server

// The live Blerg Board card behind a pinned ticket.
//
// An agent pins a board card (`blerg-runner pin` with source "blerg-board", its board_id and key
// "#<number>"); what it pinned is its own copy, which goes stale as soon as the card moves. The
// session panel shows the card itself instead, read now with the signed-in person's own board
// permissions: GET /api/board-cards/{board}/{number} exchanges a read-only board credential for that
// person (as the session board tools do for an agent, mcpgw.CallBoard) and calls blerg_card_get
// with_trail, so the card's column, tags, origin, repos and trail come back in one call. POST
// /api/board-cards/{board}/{number}/move moves it, with a credential the person's own role allows
// (core and the board refuse a move they may not make). Nothing is stored; the agent never sees
// these calls. The card's page in the Board app is boardCardURL, inside Blerg.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

// boardCardURL is the Board app's page of card number on board, or "" when the runner does not
// know the Board's address (the app then derives it, lib/boardLink.ts).
func (a *API) boardCardURL(ctx context.Context, board string, number int) string {
	base := a.siteBoard.get(ctx, a.coreURL)
	if base == "" || board == "" || number <= 0 {
		return ""
	}
	return base + "/boards/" + url.PathEscape(board) + "/cards/" + strconv.Itoa(number)
}

// liveBoardCard is the card as the panel shows it: the fields it draws, never the whole record.
type liveBoardCard struct {
	Reference  string          `json:"reference,omitempty"`
	ID         string          `json:"id"`
	BoardID    string          `json:"board_id"`
	Number     int             `json:"number"`
	Title      string          `json:"title"`
	Body       *string         `json:"body"`
	Type       string          `json:"type"`
	Priority   string          `json:"priority"`
	Size       *string         `json:"size"`
	ColumnID   *string         `json:"column_id"`
	ColumnName string          `json:"column_name"`
	Tags       []string        `json:"tags"`
	Repos      []string        `json:"repos"`
	ArchivedAt *string         `json:"archived_at"`
	Sensitive  bool            `json:"sensitive"`
	Origin     json.RawMessage `json:"origin,omitempty"`
	Version    int             `json:"version"`
	UpdatedAt  string          `json:"updated_at"`
	Columns    []struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		IsTerminal bool   `json:"is_terminal"`
	} `json:"columns"`
	Trail []struct {
		Type      string `json:"type"`
		Actor     string `json:"actor"`
		ToColumn  string `json:"to_column,omitempty"`
		Text      string `json:"text,omitempty"`
		CreatedAt string `json:"created_at"`
	} `json:"trail"`
}

// boardToolResult reads a board tool's sanitized MCP result into out; an error result is an
// apiError with its text (the board's own refusal, e.g. "card not found").
func boardToolResult(raw json.RawMessage, out any) *APIError {
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if json.Unmarshal(raw, &res) != nil {
		return apiErrorf(http.StatusBadGateway, "the board returned an unreadable result")
	}
	var text strings.Builder
	for _, c := range res.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	if res.IsError {
		msg := capText(strings.TrimSpace(text.String()), 300)
		status := http.StatusBadGateway
		if strings.Contains(strings.ToLower(msg), "not found") {
			status = http.StatusNotFound
		} else if strings.Contains(strings.ToLower(msg), "forbidden") || strings.Contains(strings.ToLower(msg), "permission") {
			status = http.StatusForbidden
		}
		return apiErrorf(status, "the board refused: %s", msg)
	}
	if out != nil && json.Unmarshal([]byte(text.String()), out) != nil {
		return apiErrorf(http.StatusBadGateway, "the board returned an unreadable card")
	}
	return nil
}

// boardCardRequest is the person and card of a live card call, checked: a person signed in as
// themselves (their sign-in is the proof the credential is exchanged with), a board id and a
// card number, and, for a sign-in limited to one board, that board.
func (a *API) boardCardRequest(w http.ResponseWriter, r *http.Request) (mcpgw.Proof, string, int, sessionBoardCaller, bool) {
	principal, ok := a.authBrowser(w, r)
	if !ok {
		return mcpgw.Proof{}, "", 0, nil, false
	}
	if apiErr := checkGrantRequester(requesterOf(principal)); apiErr != nil {
		writeAPIError(w, apiErr)
		return mcpgw.Proof{}, "", 0, nil, false
	}
	board := r.PathValue("board")
	number, err := strconv.Atoi(r.PathValue("number"))
	if !gatewayServerName.MatchString(board) || err != nil || number <= 0 {
		writeError(w, http.StatusBadRequest, "name a board id and a card number")
		return mcpgw.Proof{}, "", 0, nil, false
	}
	proof := mcpgw.Proof{AccountID: principal.Sub, SessionID: principal.Sid}
	if boardKeyReference.MatchString(board) {
		resolved, err := a.resolveLiveBoardKey(r.Context(), proof, principal.Project, board)
		if err != nil {
			writeAPIError(w, err)
			return mcpgw.Proof{}, "", 0, nil, false
		}
		board = resolved
	}
	if principal.Project != "" && principal.Project != board {
		writeError(w, http.StatusNotFound, "card not found")
		return mcpgw.Proof{}, "", 0, nil, false
	}
	caller := a.sessionBoardCaller()
	if caller == nil {
		writeError(w, http.StatusServiceUnavailable, "this runner has no board configured")
		return mcpgw.Proof{}, "", 0, nil, false
	}
	return mcpgw.Proof{AccountID: principal.Sub, SessionID: principal.Sid}, board, number, caller, true
}

// auditRef is the runner session a live card call is made from (core's audit line), when the
// request names one the caller can see; any other is left out, so a caller cannot make the audit
// line name a session that is not theirs to see.
func (a *API) auditRef(r *http.Request, account string) string {
	s := r.URL.Query().Get("session")
	if a.dbPool == nil || !validSessionID(s) {
		return ""
	}
	row, err := db.GetSession(r.Context(), a.dbPool, s)
	if err != nil || !canSeeAccount(account, row) {
		return ""
	}
	return s
}

// readLiveBoardCard is blerg_card_get with_trail for the person.
func readLiveBoardCard(ctx context.Context, caller sessionBoardCaller, proof mcpgw.Proof, board string, number int, ref string) (*liveBoardCard, *APIError) {
	args, _ := json.Marshal(map[string]any{"board_id": board, "number": number, "with_trail": true})
	raw, err := caller.CallBoard(ctx, mcpgw.BoardCall{Proof: proof, BoardID: board, SessionRef: ref, Tool: "blerg_card_get", Arguments: args, ReadOnly: true})
	if err != nil {
		return nil, boardCallError(err)
	}
	var card liveBoardCard
	if apiErr := boardToolResult(raw, &card); apiErr != nil {
		return nil, apiErr
	}
	if card.BoardID != board || card.Number != number {
		return nil, apiErrorf(http.StatusNotFound, "card not found")
	}
	return &card, nil
}

// boardCallError is a failed exchange or call, in the gateway's words.
func boardCallError(err error) *APIError {
	switch {
	case errors.Is(err, mcpgw.ErrConnectionGone):
		return apiErrorf(http.StatusForbidden, "%s", "you cannot read this board with your sign-in")
	case errors.Is(err, mcpgw.ErrBoardRevokesFailing):
		return apiErrorf(http.StatusServiceUnavailable, "%s", mcpgw.UpstreamMessage(err))
	}
	msg := mcpgw.UpstreamMessage(err)
	if strings.Contains(msg, "could not grant access") {
		// Core or the board refused the person a credential: no role on this board, or it is
		// unavailable. Either way the card cannot be shown with this sign-in now.
		return apiErrorf(http.StatusForbidden, "%s", "the board did not give your sign-in access to this card")
	}
	return apiErrorf(http.StatusBadGateway, "%s", msg)
}

// HandleGetLiveBoardCard is GET /api/board-cards/{board}/{number}[?session=<id>]: the card now, as
// the signed-in person may read it, with its Board page.
func (a *API) HandleGetLiveBoardCard(w http.ResponseWriter, r *http.Request) {
	proof, board, number, caller, ok := a.boardCardRequest(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	card, apiErr := readLiveBoardCard(r.Context(), caller, proof, board, number, a.auditRef(r, proof.AccountID))
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"card": card, "url": a.boardCardURL(r.Context(), board, number)})
}

// HandleMoveLiveBoardCard is POST /api/board-cards/{board}/{number}/move {"column_id": …}: the
// person moves the card, as their own role on the board allows. One credential exchange: the reply
// is {"moved": true, "column_id": …}, and the app updates the card it holds.
func (a *API) HandleMoveLiveBoardCard(w http.ResponseWriter, r *http.Request) {
	proof, board, number, caller, ok := a.boardCardRequest(w, r)
	if !ok {
		return
	}
	var req struct {
		ColumnID string `json:"column_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || !gatewayServerName.MatchString(req.ColumnID) {
		writeError(w, http.StatusBadRequest, `the body is {"column_id": "<column id>"}`)
		return
	}
	ctx := r.Context()
	args, _ := json.Marshal(map[string]any{"board_id": board, "number": number, "column_id": req.ColumnID})
	raw, err := caller.CallBoard(ctx, mcpgw.BoardCall{Proof: proof, BoardID: board, SessionRef: a.auditRef(r, proof.AccountID), Tool: "blerg_card_move", Arguments: args})
	if err != nil {
		writeAPIError(w, boardCallError(err))
		return
	}
	if apiErr := boardToolResult(raw, nil); apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	log.Printf("board card: account=%s board=%s card=%d moved from the session panel", proof.AccountID, board, number)
	writeJSON(w, http.StatusOK, map[string]any{"moved": true, "column_id": req.ColumnID})
}

// HandleSessionBoardCardURL is GET /api/sessions/{id}/board-card-url?board=<id>&number=<n> for the
// session itself (its own token): the Board page of a card, so `blerg-runner ticket get|create|file`
// can print the address an agent pins (the runner sets it again when the card is pinned). It reads
// no card: the address says nothing the agent does not already know.
func (a *API) HandleSessionBoardCardURL(w http.ResponseWriter, r *http.Request) {
	tokenSID, ok := a.authMessaging(w, r)
	if !ok {
		return
	}
	if tokenSID == "" || tokenSID != r.PathValue("id") {
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}
	board := r.URL.Query().Get("board")
	number, err := strconv.Atoi(strings.TrimPrefix(r.URL.Query().Get("number"), "#"))
	if !gatewayServerName.MatchString(board) || err != nil || number <= 0 {
		writeError(w, http.StatusBadRequest, "name a board id and a card number")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"board_id": board, "number": number, "url": a.boardCardURL(r.Context(), board, number)})
}

var boardKeyReference = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,4}$`)

// Discovery includes keys and aliases; never probe each board for a prose candidate.
func (a *API) resolveLiveBoardKey(ctx context.Context, proof mcpgw.Proof, project, key string) (string, *APIError) {
	boards, err := a.filingBoards(ctx, proof, project)
	if err != nil {
		return "", apiErrorf(http.StatusServiceUnavailable, "could not resolve the board key")
	}
	for _, b := range boards {
		if b.Key == key || slices.Contains(b.KeyAliases, key) {
			return b.ID, nil
		}
	}
	return "", apiErrorf(http.StatusNotFound, "card not found")
}
