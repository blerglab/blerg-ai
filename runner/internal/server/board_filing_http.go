package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/boardfiling"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

type boardFilingUpstream interface {
	SnapshotURL() string
	Prepare(context.Context, mcpgw.Proof, string, boardfiling.Draft) (boardfiling.Draft, error)
	Search(context.Context, mcpgw.Proof, string, string, string) (json.RawMessage, error)
	Create(context.Context, mcpgw.Proof, string, string, boardfiling.Draft) (json.RawMessage, error)
}
type httpBoardFiling struct {
	base            string
	core            *HTTPMCPCoreClient
	version, commit string
}

func (c *httpBoardFiling) SnapshotURL() string { return c.base }
func (a *API) filingUpstream() (boardFilingUpstream, error) {
	if a.boardFiling != nil {
		return a.boardFiling, nil
	}
	base := BoardURLFromEnv(os.Getenv)
	if base == "" || a.coreURL == "" || a.coreInternalKey == "" {
		return nil, apiErrorf(503, "Board filing is not configured on this runner.")
	}
	a.hub.mu.RLock()
	version, commit := a.hub.serverRelease, a.hub.serverCommit
	a.hub.mu.RUnlock()
	if version == "" {
		version = componentVersionString()
	}
	return &httpBoardFiling{base: base, core: &HTTPMCPCoreClient{BaseURL: a.coreURL, InternalKey: a.coreInternalKey}, version: version, commit: commit}, nil
}

// Remote errors never include a response body, credential, address or draft.
func (c *httpBoardFiling) boardRequest(ctx context.Context, method, path, token string, body any) (json.RawMessage, error) {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(data)) //nolint:gosec // Board base is operator-configured; board/path segments are validated, never a caller-selected destination.
	if err != nil {
		return nil, apiErrorf(503, "Board request is unavailable.")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := mcpgw.BoardHTTPClient(c.base, 10*time.Second, 1<<20).Do(req) //nolint:gosec // G704: operator-configured Board URL; BoardHTTPClient enforces exact-host netguard and forbids redirects.
	if err != nil {
		return nil, mcpgw.ErrOutcomeUnknown
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return nil, apiErrorf(resp.StatusCode, "Board refused the draft; check the target, repositories and required fields.")
		}
		return nil, mcpgw.ErrOutcomeUnknown
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 || !json.Valid(raw) {
		return nil, mcpgw.ErrOutcomeUnknown
	}
	return raw, nil
}
func (c *httpBoardFiling) read(ctx context.Context, proof mcpgw.Proof, board, path string) (json.RawMessage, error) {
	ex := &mcpgw.HTTPBoardExchanger{BaseURL: c.core.BaseURL, InternalKey: c.core.InternalKey, HTTP: c.core.httpClient()}
	tok, err := ex.ExchangeReadOnly(ctx, proof, board, "")
	if err != nil {
		return nil, apiErrorf(503, "Board access could not be verified.")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = ex.Revoke(cleanup, proof.AccountID, tok.Sub)
	}()
	return c.boardRequest(ctx, "GET", path, tok.Token, nil)
}
func (c *httpBoardFiling) Prepare(ctx context.Context, proof mcpgw.Proof, board string, draft boardfiling.Draft) (boardfiling.Draft, error) {
	raw, err := c.read(ctx, proof, board, "/api/boards/"+board)
	if err != nil {
		return draft, err
	}
	var b struct {
		ID             string   `json:"id"`
		Repos          []string `json:"repos"`
		RequireRepo    bool     `json:"require_repo"`
		IncludeVersion bool     `json:"include_component_version"`
	}
	if json.Unmarshal(raw, &b) != nil || b.ID != board {
		return draft, apiErrorf(503, "Board returned an invalid target.")
	}
	if len(draft.Repos) == 0 && b.RequireRepo && len(b.Repos) == 1 {
		draft.Repos = append([]string(nil), b.Repos...)
	}
	if len(draft.Repos) == 0 && b.RequireRepo {
		return draft, apiErrorf(422, "This board requires a target repository; choose one explicitly before filing.")
	}
	if b.IncludeVersion {
		draft.Origin.ComponentVersion, draft.Origin.ComponentCommit = c.version, c.commit
	} else {
		draft.Origin.ComponentVersion, draft.Origin.ComponentCommit = "", ""
	}
	return draft, nil
}
func (c *httpBoardFiling) Search(ctx context.Context, proof mcpgw.Proof, board, q, cursor string) (json.RawMessage, error) {
	raw, err := c.read(ctx, proof, board, "/api/boards/"+board+"/cards/search?q="+url.QueryEscape(q)+"&limit=100&page=true&cursor="+url.QueryEscape(cursor))
	if err != nil {
		return nil, err
	}
	// Discovery returns only public duplicate summaries; never card bodies or other metadata.
	type summary struct {
		Reference string `json:"reference,omitempty"`
		ID        string `json:"id"`
		BoardID   string `json:"board_id"`
		Number    int    `json:"number"`
		Key       string `json:"key"`
		Title     string `json:"title"`
		Type      string `json:"type"`
		Sensitive bool   `json:"sensitive"`
	}
	var page struct {
		Cards      []summary `json:"cards"`
		NextCursor string    `json:"next_cursor"`
	}
	truncated := false
	if len(raw) > 0 && raw[0] == '[' {
		// Compatibility with an older Board; it cannot supply a continuation cursor.
		if json.Unmarshal(raw, &page.Cards) != nil {
			return nil, apiErrorf(503, "Board returned invalid search results.")
		}
		truncated = len(page.Cards) == 100
	} else if json.Unmarshal(raw, &page) != nil {
		return nil, apiErrorf(503, "Board returned invalid search results.")
	}
	truncated = truncated || page.NextCursor != ""
	cards := page.Cards
	if len(cards) > 100 || len(page.NextCursor) > 4096 {
		return nil, apiErrorf(503, "Board returned invalid search results.")
	}
	if cards == nil {
		cards = []summary{}
	}
	for i := range cards {
		// Bind references to the selected board, regardless of upstream metadata.
		cards[i].BoardID = board
	}
	return json.Marshal(map[string]any{"cards": cards, "truncated": truncated, "next_cursor": page.NextCursor})
}
func (c *httpBoardFiling) Create(ctx context.Context, proof mcpgw.Proof, intent, board string, draft boardfiling.Draft) (json.RawMessage, error) {
	if proof.AccountID == "" || proof.SessionID == "" || proof.TokenID != "" {
		return nil, apiErrorf(403, "A signed-in person must approve this draft.")
	}
	body := map[string]string{"account_id": proof.AccountID, "session_id": proof.SessionID, "board_id": board, "intent": intent, "digest": boardfiling.Digest(board, draft)}
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.core.BaseURL, "/")+"/internal/tokens/exchange/board-create", bytes.NewReader(raw))
	if err != nil {
		return nil, apiErrorf(503, "Board approval is unavailable.")
	}
	req.Header.Set("X-Internal-Key", c.core.InternalKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.core.httpClient().Do(req)
	if err != nil {
		return nil, apiErrorf(503, "Board approval could not verify access.")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return nil, apiErrorf(503, "Board approval could not verify access; sign in again or retry.")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (16<<10)+1))
	var tok struct {
		Token string `json:"token"`
	}
	if err != nil || len(data) > 16<<10 || json.Unmarshal(data, &tok) != nil || tok.Token == "" || len(tok.Token) > 8<<10 || strings.ContainsAny(tok.Token, "\r\n \t") {
		return nil, errors.New("board approval credential unavailable")
	}
	receiptRaw, err := c.boardRequest(ctx, "POST", "/api/boards/"+board+"/file-card", tok.Token, draft)
	if err != nil {
		return nil, err
	}
	var receipt struct {
		ID      string `json:"id"`
		BoardID string `json:"board_id"`
		Number  int    `json:"number"`
	}
	if json.Unmarshal(receiptRaw, &receipt) != nil || !validSessionID(receipt.ID) || receipt.BoardID != board || receipt.Number < 1 {
		return nil, mcpgw.ErrOutcomeUnknown
	}
	return json.Marshal(receipt)
}

// Describe reads a created card's public summary with the approver's read access: its title,
// type and priority, and its column's name. Never the body.
func (c *httpBoardFiling) Describe(ctx context.Context, proof mcpgw.Proof, board, cardID string) (filedCard, error) {
	if !validSessionID(cardID) {
		return filedCard{}, errors.New("invalid card id")
	}
	ex := &mcpgw.HTTPBoardExchanger{BaseURL: c.core.BaseURL, InternalKey: c.core.InternalKey, HTTP: c.core.httpClient()}
	tok, err := ex.ExchangeReadOnly(ctx, proof, board, "")
	if err != nil {
		return filedCard{}, apiErrorf(503, "Board access could not be verified.")
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		defer cancel()
		_ = ex.Revoke(cleanup, proof.AccountID, tok.Sub)
	}()
	raw, err := c.boardRequest(ctx, "GET", "/api/cards/"+cardID, tok.Token, nil)
	if err != nil {
		return filedCard{}, err
	}
	var card struct {
		BoardID  string  `json:"board_id"`
		Number   int     `json:"number"`
		Title    string  `json:"title"`
		Type     string  `json:"type"`
		Priority string  `json:"priority"`
		ColumnID *string `json:"column_id"`
	}
	if json.Unmarshal(raw, &card) != nil || card.BoardID != board {
		return filedCard{}, errors.New("board returned an invalid card")
	}
	out := filedCard{ID: cardID, BoardID: board, Number: card.Number, Title: card.Title, Type: card.Type, Priority: card.Priority}
	if card.ColumnID != nil {
		if cols, err := c.boardRequest(ctx, "GET", "/api/boards/"+board+"/columns", tok.Token, nil); err == nil {
			var list []struct{ ID, Name string }
			if json.Unmarshal(cols, &list) == nil {
				for _, col := range list {
					if col.ID == *card.ColumnID {
						out.Column = col.Name
					}
				}
			}
		}
	}
	return out, nil
}
