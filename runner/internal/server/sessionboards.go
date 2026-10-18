package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"

	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

const (
	boardDiscoveryTimeout = 6 * time.Second
	maxSessionBoardsBytes = 1 << 20
	maxSessionBoards      = 1000
)

// SessionBoard describes a board from the standalone Board service. Runner's
// legacy /api/boards tables are intentionally not a source for gateway grants.
type SessionBoard struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type sessionBoardLister interface {
	ListSessionBoards(context.Context, mcpgw.Proof) ([]SessionBoard, error)
}

func (c *HTTPMCPCoreClient) ListSessionBoards(ctx context.Context, proof mcpgw.Proof) ([]SessionBoard, error) {
	if !proof.Valid() || proof.AccountID == "" || c.BaseURL == "" || c.InternalKey == "" {
		return nil, errors.New("board discovery is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, boardDiscoveryTimeout)
	defer cancel()
	raw, _ := json.Marshal(coreListRequest{AccountID: proof.AccountID, TokenID: proof.TokenID, SessionID: proof.SessionID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/internal/boards/list", bytes.NewReader(raw)) //nolint:gosec // BaseURL is the operator-configured Core address, never a browser-supplied destination.
	if err != nil {
		return nil, errors.New("board discovery request could not be created")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", c.InternalKey)
	resp, err := c.httpClient().Do(req) //nolint:gosec // Same fixed Core request; redirects are disabled by httpClient.
	if err != nil {
		return nil, errors.New("board discovery is unavailable")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, coreNotFound(resp)
	}
	if resp.StatusCode == http.StatusForbidden {
		return nil, ErrMCPProofInvalid
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("board discovery is unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSessionBoardsBytes+1))
	if err != nil || len(data) > maxSessionBoardsBytes {
		return nil, errors.New("board discovery response is invalid")
	}
	var out struct {
		Boards []SessionBoard `json:"boards"`
	}
	if json.Unmarshal(data, &out) != nil || out.Boards == nil || len(out.Boards) > maxSessionBoards {
		return nil, errors.New("board discovery response is invalid")
	}
	seen := make(map[string]bool, len(out.Boards))
	for _, b := range out.Boards {
		if !gatewayServerName.MatchString(b.ID) || b.Name == "" || len(b.Name) > 1024 || strings.ContainsFunc(b.Name, unicode.IsControl) || seen[b.ID] {
			return nil, errors.New("board discovery response is invalid")
		}
		seen[b.ID] = true
	}
	return out.Boards, nil
}

func (a *API) boardSelectionConfigured() bool {
	cfg := a.hub.mcpStart.get()
	return cfg != nil && cfg.Board != nil && cfg.Board.BoardConfigured()
}

func (a *API) sessionBoards(ctx context.Context, proof mcpgw.Proof) ([]SessionBoard, *APIError) {
	cfg := a.hub.mcpStart.get()
	if cfg == nil {
		return nil, apiErrorf(http.StatusServiceUnavailable, "board discovery is not configured")
	}
	lister, ok := cfg.Core.(sessionBoardLister)
	if !ok {
		return nil, apiErrorf(http.StatusServiceUnavailable, "board discovery is unavailable; update Core and retry")
	}
	boards, err := lister.ListSessionBoards(ctx, proof)
	if errors.Is(err, ErrMCPCoreOutdated) {
		return nil, apiErrorf(http.StatusServiceUnavailable, "board discovery requires an updated Core; update Core and retry")
	}
	if errors.Is(err, ErrMCPProofInvalid) {
		return nil, apiErrorf(http.StatusUnauthorized, "your sign-in cannot access boards; sign in again and retry")
	}
	if err != nil {
		return nil, apiErrorf(http.StatusServiceUnavailable, "could not load your boards; retry")
	}
	if boards == nil {
		boards = []SessionBoard{}
	}
	return boards, nil
}

func (a *API) HandleSessionBoards(w http.ResponseWriter, r *http.Request) {
	principal, ok := a.authBrowser(w, r)
	if !ok {
		return
	}
	if apiErr := checkGrantRequester(requesterOf(principal)); apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if !a.boardSelectionConfigured() {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "boards": []SessionBoard{}})
		return
	}
	boards, apiErr := a.sessionBoards(r.Context(), mcpgw.Proof{AccountID: principal.Sub, SessionID: principal.Sid})
	if apiErr != nil {
		writeAPIError(w, apiErr)
		return
	}
	if principal.Project != "" {
		scoped := []SessionBoard{}
		for _, board := range boards {
			if board.ID == principal.Project {
				scoped = append(scoped, board)
			}
		}
		boards = scoped
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "boards": boards})
}

func (a *API) resolveLaunchGrant(ctx context.Context, who grantRequester, selection []MCPSelection, boardID string, target grantTarget) (*ResolvedGrant, *APIError) {
	grant, apiErr := a.resolveGrant(ctx, who, selection, target)
	if apiErr != nil || boardID == "" {
		return grant, apiErr
	}
	if apiErr = checkGrantRequester(who); apiErr != nil {
		return nil, apiErr
	}
	if !gatewayServerName.MatchString(boardID) {
		return nil, apiErrorf(http.StatusBadRequest, "board_id is not valid")
	}
	if !a.boardSelectionConfigured() {
		return nil, apiErrorf(http.StatusServiceUnavailable, "the runner has no board configured (set %s or %s)", envBoardURL, envBoardMCPURL)
	}
	if apiErr = a.checkGrantTarget(target); apiErr != nil {
		return nil, apiErr
	}
	proof := mcpgw.Proof{AccountID: who.Account, SessionID: who.Sid}
	boards, apiErr := a.sessionBoards(ctx, proof)
	if apiErr != nil {
		return nil, apiErr
	}
	for _, b := range boards {
		if b.ID == boardID {
			return a.addSessionBoardGrant(grant, boardID, proof)
		}
	}
	return nil, apiErrorf(http.StatusNotFound, "board not found")
}

// Shared with cron starts. All callers have already checked their placement and
// verified proof; the fixed grant never inherits user-selected tool modes.
func (a *API) addSessionBoardGrant(grant *ResolvedGrant, boardID string, proof mcpgw.Proof) (*ResolvedGrant, *APIError) {
	cfg := a.hub.mcpStart.get()
	if cfg == nil || cfg.Board == nil || !cfg.Board.BoardConfigured() {
		return nil, apiErrorf(http.StatusServiceUnavailable, "the runner has no board configured (set %s or %s)", envBoardURL, envBoardMCPURL)
	}
	if grant == nil {
		grant = &ResolvedGrant{AccountID: proof.AccountID, Proof: proof}
	}
	if grant.AccountID != proof.AccountID || grant.Proof != proof {
		return nil, apiErrorf(http.StatusForbidden, "board grant proof does not match the session")
	}
	if len(grant.Connections) >= maxGatewayServers {
		return nil, apiErrorf(http.StatusUnprocessableEntity, "the selected MCP connections leave no room for the board connection")
	}
	for _, connection := range grant.Connections {
		if connection.Name == mcpgw.BuiltinBoard || connection.ConnectionID == mcpgw.BoardConnectionID {
			return nil, apiErrorf(http.StatusUnprocessableEntity, "the board connection is already selected")
		}
	}
	grant.Connections = append(grant.Connections, mcpgw.BoardGrantSpec(boardID, cfg.Board.BoardMCPURL()))
	return grant, nil
}

// These are gateway credentials, not upstream Board credentials. They retain the
// same fixed tool set, session binding, budgets and revocation as engine MCP calls.
func sessionBoardEnv(env map[string]string, boardID string, gateway *protocol.MCPGatewayConfig) map[string]string {
	if boardID == "" || gateway == nil {
		return env
	}
	for _, server := range gateway.Servers {
		if server.Name != mcpgw.BuiltinBoard {
			continue
		}
		out := make(map[string]string, len(env)+3)
		for key, value := range env {
			out[key] = value
		}
		out["BLERG_RUNNER_BOARD_ID"] = boardID
		out["BLERG_RUNNER_BOARD_TOKEN"] = server.Token
		out["BLERG_RUNNER_BOARD_GATEWAY_URL"] = strings.TrimRight(gateway.BaseURL, "/") + "/mcp-gw/board"
		return out
	}
	return env
}
