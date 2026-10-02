package server

// The built-in `board` connection on the server side (AI crons spec 10.2): attaching it to a cron's
// grant, and revoking the board tokens the gateway exchanged for a session when it ends. The gateway
// half (exchange, cache, fixed tool set, trusted client) is mcpgw/board.go.

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

// Environment variables naming the board. BLERG_RUNNER_BOARD_URL is the board's base address (the
// failure card is posted to its REST API, and the MCP endpoint defaults to <base>/mcp);
// BLERG_RUNNER_BOARD_MCP_URL overrides the MCP endpoint alone.
const (
	envBoardURL    = "BLERG_RUNNER_BOARD_URL"
	envBoardMCPURL = "BLERG_RUNNER_BOARD_MCP_URL"
)

// boardBaseURL validates an operator-configured board address: absolute http(s), a host, no
// credentials, no query or fragment. It returns it without a trailing slash, or "".
func boardBaseURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	return strings.TrimRight(u.String(), "/")
}

// BoardURLFromEnv is the board's base address from BLERG_RUNNER_BOARD_URL ("" when unset or invalid).
func BoardURLFromEnv(env func(string) string) string { return boardBaseURL(env(envBoardURL)) }

// BoardMCPURLFromEnv is the board's MCP endpoint: BLERG_RUNNER_BOARD_MCP_URL, else the base address
// plus "/mcp". "" when neither is set (the built-in board connection is then unavailable).
//
// A BLERG_RUNNER_BOARD_MCP_URL that is set but not a usable address is not silently replaced by the
// fallback: the rejection is logged (this is read once, at startup).
func BoardMCPURLFromEnv(env func(string) string) string {
	raw := strings.TrimSpace(env(envBoardMCPURL))
	if u := boardBaseURL(raw); u != "" {
		return u
	}
	if raw != "" {
		log.Printf("board: %s is not a usable address (it must be an absolute http(s) URL with a host, no credentials, query or fragment); ignoring it", envBoardMCPURL)
	}
	if base := BoardURLFromEnv(env); base != "" {
		return base + "/mcp"
	}
	return ""
}

// boardAccess is the gateway's part of the board connection (implemented by *mcpgw.Gateway).
type boardAccess interface {
	BoardConfigured() bool
	BoardMCPURL() string
	RevokeSession(ctx context.Context, sessionID string)
}

// boardAccessError is a failure to give the session its board: the exchange was refused or could not
// be made, or the runner has no board configured. The run fails with its message, visibly; no failure
// card is attempted for it (the same exchange would fail, and the card would need it).
type boardAccessError struct{ err error }

func (e *boardAccessError) Error() string { return e.err.Error() }
func (e *boardAccessError) Unwrap() error { return e.err }

// addBoardGrant adds the built-in board connection to a cron's grant (creating the grant when the
// cron names no MCP connections of its own). It does not exchange a board token: the cron's token
// was just confirmed live at core (checkLive, the status route), the first real exchange happens on
// the session's first use of the board, and a failure there surfaces as a failed tool call. A
// preflight mint-and-revoke would only add a token, and a revocation row, to every run.
func (s *CronService) addBoardGrant(ctx context.Context, c *db.Cron, grant *ResolvedGrant, target grantTarget) (*ResolvedGrant, error) {
	a := s.api
	if grant == nil {
		if apiErr := a.checkGrantTarget(target); apiErr != nil {
			return nil, s.startError(ctx, c, apiErr)
		}
	}
	cfg := a.hub.mcpStart.get()
	if cfg == nil || cfg.Board == nil || !cfg.Board.BoardConfigured() {
		return nil, &boardAccessError{fmt.Errorf("this cron targets a board, but the runner has no board configured (set %s or %s)", envBoardURL, envBoardMCPURL)}
	}
	boardID := derefOrEmpty(c.BoardID)
	if grant == nil {
		grant = &ResolvedGrant{AccountID: c.OwnerAccountID, Proof: mcpgw.Proof{AccountID: c.OwnerAccountID, TokenID: c.TokenID}}
	}
	if len(grant.Connections) >= maxGatewayServers {
		return nil, &boardAccessError{fmt.Errorf("this cron names %d MCP connections, which leaves no room for the board connection", len(grant.Connections))}
	}
	grant.Connections = append(grant.Connections, mcpgw.BoardGrantSpec(boardID, cfg.Board.BoardMCPURL()))
	return grant, nil
}

// ---- revoking at session end -----------------------------------------------------------------

var (
	boardAccessRef  atomic.Pointer[boardAccessHolder]
	boardRevokeWG   sync.WaitGroup
	boardRevokeWait = 30 * time.Second
)

type boardAccessHolder struct{ b boardAccess }

// setBoardAccess installs (or clears, with nil) the gateway the end paths revoke through.
func setBoardAccess(b boardAccess) {
	if b == nil {
		boardAccessRef.Store(nil)
		return
	}
	boardAccessRef.Store(&boardAccessHolder{b})
}

// revokeBoardTokens revokes the board tokens the gateway exchanged for the session, in the
// background: a session's end must not wait on core. Every path that deletes a session's grants
// reaches it through revokeSessionGrants. Tokens it cannot revoke expire within 10 minutes.
func revokeBoardTokens(ctx context.Context, sessionID string) {
	h := boardAccessRef.Load()
	if h == nil {
		return
	}
	bg := context.WithoutCancel(ctx)
	boardRevokeWG.Add(1)
	go func() {
		defer boardRevokeWG.Done()
		rctx, cancel := context.WithTimeout(bg, boardRevokeWait)
		defer cancel()
		h.b.RevokeSession(rctx, sessionID)
	}()
}

// waitBoardRevocations blocks until the background revocations started so far are done (tests, shutdown).
func waitBoardRevocations() { boardRevokeWG.Wait() }
