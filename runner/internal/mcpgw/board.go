package mcpgw

// The built-in `board` connection (AI crons spec 10.2).
//
// A cron (or any session) that targets a board gets one more gateway connection, named "board", that
// core does not hold. It is an ordinary grant row with three differences, all confined to this file
// and the few lines in gateway.go and upstream.go that branch on GrantRow.Builtin:
//
//   - The credential is a short-lived agent token exchanged at core (POST /internal/tokens/exchange)
//     with the grant's proof and the target board id, cached in memory and exchanged again when fewer
//     than 60 seconds of it remain. Every token issued is remembered (its `sub`) so the session's end,
//     or the deletion of the grant by anything else, revokes it at core.
//   - The upstream is the board's MCP endpoint, which the operator configured
//     (BLERG_RUNNER_BOARD_MCP_URL, or BLERG_RUNNER_BOARD_URL + "/mcp"). It is typically a private
//     in-cluster address, so it is reached by a fixed client that allows exactly that host
//     (boardClient); the policy that holds a user's connection URL is not relaxed for anyone else.
//   - The tool set is fixed in this file. Modes are allow and the tool definitions are the board's
//     own, so they are not hash pinned; nothing outside the set is listed or callable.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/netguard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// BuiltinBoard is the built-in connection's name and the value of a grant row's builtin column. It is
// also the gateway path segment. core reserves the name, so no user connection can collide with it.
const BuiltinBoard = "board"

// BoardConnectionID is the connection_id every board grant carries: a fixed sentinel that is never a
// core connection id, so the primary key (session, connection) stays unique per session.
const BoardConnectionID = "00000000-0000-4000-8000-00000000b0a2"

// boardRenewBefore is how much life a cached exchange token must have left to be used again.
const boardRenewBefore = 60 * time.Second

// boardToolNames is the complete set of board tools a session may use (spec 10.2). Archiving,
// creating or moving columns, and creating or updating boards are deliberately absent.
var boardToolNames = []string{
	"blerg_board_get", "blerg_board_list", "blerg_board_schema",
	"blerg_column_list",
	"blerg_card_search", "blerg_card_get", "blerg_card_create", "blerg_card_update",
	"blerg_card_move", "blerg_card_comment", "blerg_card_link",
}

var boardToolSet = func() map[string]bool {
	m := make(map[string]bool, len(boardToolNames))
	for _, n := range boardToolNames {
		m[n] = true
	}
	return m
}()

// BoardToolNames returns the fixed tool set (a copy).
func BoardToolNames() []string { return append([]string(nil), boardToolNames...) }

// BoardTools is the tools map of a board grant: every tool in mode allow, with no pinned hash (the
// definitions are the board's own).
func BoardTools() map[string]ToolGrant {
	m := make(map[string]ToolGrant, len(boardToolNames))
	for _, n := range boardToolNames {
		m[n] = ToolGrant{Mode: ModeAllow}
	}
	return m
}

// BoardGrantSpec is the grant spec for the built-in board connection targeting boardID, whose MCP
// endpoint is mcpURL (the snapshot the gateway checks the host of).
func BoardGrantSpec(boardID, mcpURL string) GrantSpec {
	return GrantSpec{
		ConnectionID: BoardConnectionID, Name: BuiltinBoard, URL: mcpURL, Tools: BoardTools(),
		Builtin: BuiltinBoard, BuiltinRef: boardID,
	}
}

// BoardToken is an exchanged board token: the bearer string, when it expires and its `sub` (the id
// core revokes it by).
type BoardToken struct {
	Token     string
	Sub       string
	ExpiresAt time.Time
}

// BoardExchanger is core's token exchange for the board (spec 4.5): a liveness proof for a short-lived
// agent token scoped to one board, and the revocation of such a token.
type BoardExchanger interface {
	// Exchange returns a token for boardID. ErrConnectionGone when core answers not found (the proof
	// is not live). sessionRef is the runner's session id, recorded in core's audit line only.
	Exchange(ctx context.Context, proof Proof, boardID, sessionRef string) (BoardToken, error)
	// Revoke revokes an exchanged token by its sub. A token already gone is not an error.
	Revoke(ctx context.Context, accountID, sub string) error
}

var (
	errBoardAccess        = errors.New("the board could not grant access right now")
	errBoardNotConfigured = errors.New("the board is not configured on this runner")
)

// BoardHTTPClient is the fixed client for the operator's board: it allows exactly the host of rawURL,
// plain http and private addresses included, and nothing else; no redirects, no proxy, capped bodies.
// An unparsable URL yields a client that refuses everything.
func BoardHTTPClient(rawURL string, timeout time.Duration, maxBody int64) *http.Client {
	var p netguard.Policy
	if u, err := url.Parse(rawURL); err == nil && u.Hostname() != "" {
		p = netguard.Policy{AllowHTTPHosts: []string{u.Hostname()}, AllowPrivateHosts: []string{u.Hostname()}}
	}
	return p.Client(timeout, maxBody)
}

// ---- the gateway side ------------------------------------------------------------------------

// BoardConfigured reports whether the gateway can serve the board connection: core's exchange and the
// board's MCP address are both set.
func (g *Gateway) BoardConfigured() bool { return g.cfg.Board != nil && g.cfg.BoardMCPURL != "" }

// BoardMCPURL is the board's MCP endpoint a board grant snapshots ("" when not configured).
func (g *Gateway) BoardMCPURL() string { return g.cfg.BoardMCPURL }

// boardCredential is credential() for a board grant: the cached exchange token while it has at least
// boardRenewBefore left, otherwise a fresh exchange.
func (st *grantState) boardCredential(ctx context.Context, gr *db.MCPGrant) (Credential, error) {
	g := st.g
	st.credMu.Lock()
	defer st.credMu.Unlock()
	if st.closed {
		return Credential{}, ErrConnectionGone
	}
	now := g.now()
	if st.cred != nil && now.Add(boardRenewBefore).Before(st.credUntil) {
		return *st.cred, nil
	}
	st.cred = nil
	if !g.BoardConfigured() {
		return Credential{}, errBoardNotConfigured
	}
	want, err := hostKey(gr.URLSnapshot)
	have, err2 := hostKey(g.cfg.BoardMCPURL)
	if err != nil || err2 != nil || want != have {
		return Credential{}, errors.New("the connection's address changed since this session started")
	}
	tok, err := g.cfg.Board.Exchange(ctx, ProofFromGrant(gr.AccountID, gr.ProofKind, gr.ProofValue), gr.BuiltinRef, gr.SessionID)
	switch {
	case errors.Is(err, ErrConnectionGone):
		return Credential{}, err
	case err != nil:
		log.Printf("mcpgw: board token exchange failed (%s)", errorClass(err))
		return Credential{}, errBoardAccess
	}
	if tok.Token == "" || strings.ContainsAny(tok.Token, "\r\n\x00 ") {
		return Credential{}, errBoardAccess
	}
	// Remember the token durably BEFORE using it, so a crash, a restart or another replica can still
	// revoke it; then make sure the grant it was issued for still exists. The grant is deleted
	// before a session's revocation runs, so a token issued after that (a request that got past
	// authentication first) is revoked here: either the revocation sees this row, or this check
	// sees the grant gone.
	if tok.Sub == "" {
		return Credential{}, errBoardAccess // cannot be named, so it could never be revoked
	}
	sub := db.BoardExchangeSub{Sub: tok.Sub, TokenHash: st.tokenHash, SessionID: gr.SessionID, AccountID: gr.AccountID, ExpiresAt: tok.ExpiresAt}
	if err := db.InsertBoardExchangeSub(ctx, g.cfg.DB, sub); err != nil {
		log.Printf("mcpgw: remember board token: %v", err)
		g.revokeSubs(context.WithoutCancel(ctx), []db.BoardExchangeSub{sub})
		return Credential{}, errBoardAccess
	}
	if _, err := db.MCPGrantByTokenHash(ctx, g.cfg.DB, st.tokenHash); errors.Is(err, db.ErrMCPGrantNotFound) {
		st.closed = true
		g.revokeSubs(context.WithoutCancel(ctx), []db.BoardExchangeSub{sub})
		return Credential{}, ErrConnectionGone
	} else if err != nil {
		g.revokeSubs(context.WithoutCancel(ctx), []db.BoardExchangeSub{sub})
		return Credential{}, errBoardAccess
	}
	c := Credential{URL: g.cfg.BoardMCPURL, HeaderName: "Authorization", Value: "Bearer " + tok.Token, ExpiresAt: tok.ExpiresAt}
	st.cred, st.credUntil = &c, tok.ExpiresAt
	return c, nil
}

// grantExists reports whether a grant with the token hash is still stored. An error answers true:
// a state is never closed, and a token never revoked, on a database that could not say.
func (g *Gateway) grantExists(ctx context.Context, tokenHash []byte) bool {
	_, err := db.MCPGrantByTokenHash(ctx, g.cfg.DB, tokenHash)
	if errors.Is(err, db.ErrMCPGrantNotFound) {
		return false
	}
	if err != nil {
		log.Printf("mcpgw: grant lookup: %v", err)
	}
	return true
}

// RevokeSession revokes the board tokens issued for grants of the session that no longer exist and
// stops those grants' state from exchanging another. The caller has deleted the grants first, so no
// new request gets past authentication. A grant that still exists is left alone: a resume issues
// new grants for the same session before the old end's revocation runs, and that revocation must
// not close them.
//
// The tokens come from the durable table (board_exchange_subs), so this also works after a runner
// restart or on another replica. Best effort and logged: a token that cannot be revoked here stays
// in the table for the next sweep, and in any case expires within its 10 minute lifetime.
func (g *Gateway) RevokeSession(ctx context.Context, sessionID string) {
	type cand struct {
		key string
		st  *grantState
	}
	var cands []cand
	g.statesMu.Lock()
	for k, st := range g.states {
		if st.builtin && st.sessionID == sessionID {
			cands = append(cands, cand{k, st})
		}
	}
	g.statesMu.Unlock()
	for _, c := range cands {
		if g.grantExists(ctx, c.st.tokenHash) {
			continue
		}
		g.closeState(c.key, c.st)
	}
	g.revokeOwed(ctx, sessionID)
}

// reapBoardStates revokes the tokens of board grants that were deleted by something other than the
// session's end (the sweeper, the orphan cleanup, a resume that replaced them), including those a
// runner that has since restarted issued, and forgets the rows of tokens that expired.
func (g *Gateway) reapBoardStates(ctx context.Context) {
	type cand struct {
		key string
		st  *grantState
	}
	var cands []cand
	g.statesMu.Lock()
	for k, st := range g.states {
		if st.builtin {
			cands = append(cands, cand{k, st})
		}
	}
	g.statesMu.Unlock()
	for _, c := range cands {
		if !g.grantExists(ctx, c.st.tokenHash) {
			g.closeState(c.key, c.st)
		}
	}
	g.revokeOwed(ctx, "")
	if n, err := db.DeleteExpiredBoardExchangeSubs(ctx, g.cfg.DB, g.now()); err != nil {
		log.Printf("mcpgw: drop expired board tokens: %v", err)
	} else if n > 0 {
		log.Printf("mcpgw: dropped %d expired board token rows", n)
	}
}

// closeState removes the state from the map and marks it closed: it will never exchange again.
func (g *Gateway) closeState(key string, st *grantState) {
	g.statesMu.Lock()
	if g.states[key] == st {
		delete(g.states, key)
	}
	g.statesMu.Unlock()
	st.credMu.Lock()
	st.closed = true
	st.cred = nil
	st.credMu.Unlock()
}

// revokeOwed revokes every unexpired exchange token whose grant is gone (of one session, or of all
// with sessionID ""), in batches, and forgets the ones that were revoked.
func (g *Gateway) revokeOwed(ctx context.Context, sessionID string) {
	for range 20 {
		owed, err := db.ListBoardExchangeSubsOwed(ctx, g.cfg.DB, sessionID, g.now(), 100)
		if err != nil {
			log.Printf("mcpgw: list board tokens owed a revocation: %v", err)
			return
		}
		if len(owed) == 0 {
			return
		}
		if g.revokeSubs(ctx, owed) == 0 {
			return // none could be revoked now (core down): leave them for the next sweep
		}
	}
}

// revokeSubs revokes exchange tokens at core and deletes the rows of those revoked; it returns how
// many were. A token that could not be revoked keeps its row and is retried by the next sweep.
func (g *Gateway) revokeSubs(ctx context.Context, subs []db.BoardExchangeSub) int {
	if g.cfg.Board == nil {
		return 0
	}
	done := 0
	for _, it := range subs {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := g.cfg.Board.Revoke(rctx, it.AccountID, it.Sub)
		cancel()
		if err != nil {
			log.Printf("mcpgw: revoke board token %s: %v", it.Sub, err)
			continue
		}
		if err := db.DeleteBoardExchangeSub(ctx, g.cfg.DB, it.Sub); err != nil {
			log.Printf("mcpgw: forget board token %s: %v", it.Sub, err)
		}
		done++
	}
	return done
}

// ---- the HTTP exchanger ---------------------------------------------------------------------

// HTTPBoardExchanger implements BoardExchanger against core's /internal/tokens/exchange and
// /internal/tokens/exchange/revoke, guarded by the internal key.
type HTTPBoardExchanger struct {
	BaseURL     string
	InternalKey string
	HTTP        *http.Client // nil: a client with a 10 second timeout
}

func (c *HTTPBoardExchanger) post(ctx context.Context, path string, body any) (*http.Response, error) {
	if c.BaseURL == "" || c.InternalKey == "" {
		return nil, errors.New("core is not configured")
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Key", c.InternalKey)
	client := &http.Client{Timeout: 10 * time.Second}
	if c.HTTP != nil {
		cp := *c.HTTP
		client = &cp
	}
	// Never follow a redirect: the request carries the internal key.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("core unreachable: %w", err)
	}
	return resp, nil
}

type exchangeRequest struct {
	AccountID  string `json:"account_id"`
	TokenID    string `json:"token_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	Target     string `json:"target"`
	BoardID    string `json:"board_id"`
	SessionRef string `json:"session_ref,omitempty"`
}

// Exchange implements BoardExchanger.
func (c *HTTPBoardExchanger) Exchange(ctx context.Context, proof Proof, boardID, sessionRef string) (BoardToken, error) {
	if !proof.Valid() {
		return BoardToken{}, errors.New("no liveness proof for the token exchange")
	}
	resp, err := c.post(ctx, "/internal/tokens/exchange", exchangeRequest{
		AccountID: proof.AccountID, TokenID: proof.TokenID, SessionID: proof.SessionID,
		Target: "board", BoardID: boardID, SessionRef: sessionRef,
	})
	if err != nil {
		return BoardToken{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		if !isCoreNotFound(resp) {
			return BoardToken{}, errors.New("core has no token exchange route: is it up to date?")
		}
		return BoardToken{}, ErrConnectionGone
	default:
		return BoardToken{}, fmt.Errorf("core returned %d to the token exchange", resp.StatusCode)
	}
	var out struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return BoardToken{}, fmt.Errorf("decode core response: %w", err)
	}
	exp, err := time.Parse(time.RFC3339, out.ExpiresAt)
	if err != nil || out.Token == "" {
		return BoardToken{}, errors.New("core sent an unusable exchange token")
	}
	return BoardToken{Token: out.Token, Sub: tokenSub(out.Token), ExpiresAt: exp}, nil
}

// Revoke implements BoardExchanger.
func (c *HTTPBoardExchanger) Revoke(ctx context.Context, accountID, sub string) error {
	resp, err := c.post(ctx, "/internal/tokens/exchange/revoke", map[string]string{"account_id": accountID, "token_sub": sub})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent:
		return nil
	case http.StatusNotFound:
		if !isCoreNotFound(resp) {
			return errors.New("core has no token revoke route: is it up to date?")
		}
		return nil // idempotent: core answered "not found" itself, so the token is already gone
	default:
		return fmt.Errorf("core returned %d to the revoke", resp.StatusCode)
	}
}

// tokenSub reads the `sub` claim of a JWT WITHOUT verifying it: the value only names the token to
// revoke at core, which checks the account owns it, and nothing trusts it. "" when unreadable.
func tokenSub(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var c struct {
		Sub string `json:"sub"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return ""
	}
	return c.Sub
}
