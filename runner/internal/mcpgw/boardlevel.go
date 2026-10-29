package mcpgw

// Board access levels (0.4.4). The person sets one level per board in a session's Permissions:
// propose (the default), read, file (without asking) or full. This file is the gateway's part:
//
//   - The built-in board connection a session was given at launch (board.go) serves at the level of
//     the session's latest board permission row for that board, read on every tools/list and
//     tools/call, so lowering or revoking holds at the agent's next call. A session that never had
//     a row for the board (a cron, a board-started session, or a launch board the person has not
//     changed) keeps full, exactly as before 0.4.4.
//   - CallBoard is the call the runner makes for a session's own board tools (the blerg-runner board
//     commands and the blerg_board session tool, which authenticate with the session's own token):
//     a credential exchanged for that one call and board, used once and revoked, never handed to
//     the agent. A read is made with a card.read-only credential, so a read level cannot write even
//     if a tool were misclassified here.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// boardReadTools are the tools level read allows: they only read. Every other board tool (the fixed
// set and column management) needs level full.
var boardReadTools = map[string]bool{
	"blerg_board_get": true, "blerg_board_list": true, "blerg_board_schema": true,
	"blerg_column_list": true, "blerg_card_search": true, "blerg_card_get": true,
}

// BoardToolLevel is the least level a board tool needs: read for a read tool, full for the rest of
// the fixed set, and "" for a tool outside it (never callable).
func BoardToolLevel(tool string) string {
	switch {
	case !allBoardToolSet[tool]:
		return ""
	case boardReadTools[tool]:
		return db.BoardLevelRead
	default:
		return db.BoardLevelFull
	}
}

// BoardLevelAllows reports whether a board at level may run the tool.
func BoardLevelAllows(level, tool string) bool {
	need := BoardToolLevel(tool)
	return need != "" && db.BoardLevelRank(level) >= db.BoardLevelRank(need)
}

// BoardLevelRefusal is what an agent is told when its board level does not allow a tool.
func BoardLevelRefusal(board, level, tool string) string {
	need := BoardToolLevel(tool)
	if need == "" {
		return "unknown board tool " + tool
	}
	return fmt.Sprintf("this session has %s access to board %s; %s needs %s access. The person can change it while you work: "+
		"call the blerg_board tool with {\"tool\":\"access\"} (or run `blerg-runner board access`) to see your current levels before telling "+
		"the person you lack permission, and use blerg_board for whatever the level allows. If it is still not allowed, ask the person to raise it "+
		"in the session's Permissions (Boards), or propose a card with blerg_ticket_file / `blerg-runner ticket file`, which asks them.", level, board, tool, need)
}

// boardGrantAccess is the level a built-in board grant serves at now (a database error answers
// propose: the call is refused rather than served at a level nobody could read) and the person's sign-in
// behind it, if any: the current permission row's (a level the person set in Permissions, read on
// every call), or for an untouched launch board the launch's own when a person launched it. Column
// management needs that person (nil for a cron, or a token launch nobody raised since).
func (g *Gateway) boardGrantAccess(ctx context.Context, gr *db.MCPGrant) (string, *Proof) {
	if gr.Builtin != BuiltinBoard || g.cfg.DB == nil {
		return db.BoardLevelFull, nil
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	state, err := db.LatestBoardGrant(lctx, g.cfg.DB, gr.SessionID, gr.BuiltinRef)
	switch {
	case err != nil:
		log.Printf("mcpgw: board level: %v", err)
		return db.BoardLevelPropose, nil
	case state == nil:
		if gr.ProofKind == "session_id" {
			p := ProofFromGrant(gr.AccountID, gr.ProofKind, gr.ProofValue)
			return db.BoardLevelFull, &p
		}
		return db.BoardLevelFull, nil
	}
	level := state.Level()
	if state.Live && state.Grant.ProofSessionID != "" && state.Grant.AccountID == gr.AccountID {
		p := Proof{AccountID: state.Grant.AccountID, SessionID: state.Grant.ProofSessionID}
		return level, &p
	}
	return level, nil
}

// BoardCall is one board tool call the runner makes for a session's own board tools.
type BoardCall struct {
	Proof      Proof  // the sign-in of the person who gave the level (or the launch grant's proof)
	BoardID    string // the board the credential is exchanged for; core and the board scope it
	SessionRef string // the runner session, for core's audit line
	Tool       string
	Arguments  json.RawMessage
	// ReadOnly exchanges a card.read-only credential (every read, at any level).
	ReadOnly bool
}

// BoardReadOnlyExchanger is a BoardExchanger that can also mint a card.read-only credential.
type BoardReadOnlyExchanger interface {
	ExchangeReadOnly(ctx context.Context, proof Proof, boardID, sessionRef string) (BoardToken, error)
}

// ErrBoardCallInvalid is a call CallBoard refuses before anything is exchanged.
var ErrBoardCallInvalid = errors.New("invalid board call")

// ErrBoardRevokesFailing is a call CallBoard refuses because too many of the account's earlier
// credentials could not be revoked and are still live.
var ErrBoardRevokesFailing = errors.New("board credentials could not be revoked")

// maxUnrevokedBoardTokens is the default bound on the session board credentials one account may
// have alive after a failed revoke (an administrator can change it in core's settings; the runner
// passes it on with SetUnrevokedBoardTokenLimit). Core allows each account a number of live
// exchange tokens across all its sessions (100 by default); the in-flight cap bounds live tokens
// while revokes work, and this bounds them while revokes fail, whatever the per-minute rate, so a
// revoke outage cannot exhaust the account's budget.
const maxUnrevokedBoardTokens = 20

// SetUnrevokedBoardTokenLimit sets how many of an account's session board credentials may still be
// live after a failed revoke before CallBoard stops minting for it; n < 1 restores the default. It
// applies to the next call.
func (g *Gateway) SetUnrevokedBoardTokenLimit(n int) {
	if n < 1 {
		n = 0
	}
	g.unrevokedLimit.Store(int64(n))
}

// unrevokedBoardTokenLimit is the limit in force.
func (g *Gateway) unrevokedBoardTokenLimit() int {
	if n := g.unrevokedLimit.Load(); n > 0 {
		return int(n)
	}
	return maxUnrevokedBoardTokens
}

// unrevokedBoardTokens remembers, per account, when each credential whose revoke failed expires.
type unrevokedBoardTokens struct {
	mu sync.Mutex
	m  map[string][]time.Time
}

// live is how many of the account's unrevoked credentials have not expired at now (pruning the rest).
func (u *unrevokedBoardTokens) live(account string, now time.Time) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	kept := u.m[account][:0]
	for _, until := range u.m[account] {
		if now.Before(until) {
			kept = append(kept, until)
		}
	}
	if len(kept) == 0 {
		delete(u.m, account)
		return 0
	}
	u.m[account] = kept
	return len(kept)
}

func (u *unrevokedBoardTokens) add(account string, until time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.m == nil {
		u.m = map[string][]time.Time{}
	}
	u.m[account] = append(u.m[account], until)
}

// CallBoard exchanges a credential for the board, makes the one tools/call with it, and revokes it.
// The result is sanitized exactly as an engine's gateway call is. An upstream JSON-RPC error comes
// back as an error result; a failure to reach the board as an error with the gateway's own wording
// (UpstreamMessage).
func (g *Gateway) CallBoard(ctx context.Context, c BoardCall) (json.RawMessage, error) {
	if !g.BoardConfigured() {
		return nil, errBoardNotConfigured
	}
	if !allBoardToolSet[c.Tool] || c.BoardID == "" || !c.Proof.Valid() || c.Proof.AccountID == "" {
		return nil, ErrBoardCallInvalid
	}
	if boardColumnToolSet[c.Tool] && !PersonBehind(c.Proof) {
		return nil, ErrBoardCallInvalid
	}
	if c.ReadOnly && !boardReadTools[c.Tool] {
		return nil, ErrBoardCallInvalid
	}
	if limit := g.unrevokedBoardTokenLimit(); g.unrevoked.live(c.Proof.AccountID, g.now()) >= limit {
		log.Printf("mcpgw: session board call refused: %d unrevoked credentials still live", limit)
		return nil, ErrBoardRevokesFailing
	}
	ctx, cancel := context.WithTimeout(ctx, g.cfg.CallTimeout)
	defer cancel()
	var tok BoardToken
	var err error
	if c.ReadOnly {
		ro, ok := g.cfg.Board.(BoardReadOnlyExchanger)
		if !ok {
			return nil, errBoardAccess // never a write credential for a read
		}
		tok, err = ro.ExchangeReadOnly(ctx, c.Proof, c.BoardID, c.SessionRef)
	} else {
		tok, err = g.cfg.Board.Exchange(ctx, c.Proof, c.BoardID, c.SessionRef)
	}
	if tok.Sub != "" {
		defer func() {
			rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer rcancel()
			if rerr := g.cfg.Board.Revoke(rctx, c.Proof.AccountID, tok.Sub); rerr != nil {
				// It expires within its lifetime (minutes); it never left this process. Until then it
				// counts against the account's unrevoked allowance.
				g.unrevoked.add(c.Proof.AccountID, tok.ExpiresAt)
				log.Printf("mcpgw: revoke session board token: %v", rerr)
			}
		}()
	}
	switch {
	case errors.Is(err, ErrConnectionGone):
		return nil, ErrConnectionGone
	case err != nil:
		log.Printf("mcpgw: session board token exchange failed (%s)", errorClass(err))
		return nil, errBoardAccess
	case tok.Token == "" || tok.Sub == "" || strings.ContainsAny(tok.Token, "\r\n\x00 "):
		return nil, errBoardAccess
	case !g.now().Add(5 * time.Second).Before(tok.ExpiresAt):
		return nil, errBoardAccess
	}
	gr := &db.MCPGrant{
		SessionID: c.SessionRef, AccountID: c.Proof.AccountID, URLSnapshot: g.cfg.BoardMCPURL,
		ProofKind: c.Proof.Kind(), ProofValue: c.Proof.Value(), Builtin: BuiltinBoard, BuiltinRef: c.BoardID,
	}
	cred := Credential{URL: g.cfg.BoardMCPURL, HeaderName: "Authorization", Value: "Bearer " + tok.Token, ExpiresAt: tok.ExpiresAt}
	st := &grantState{g: g, sem: make(chan struct{}, 1), builtin: true, ephemeral: true,
		sessionID: c.SessionRef, account: c.Proof.AccountID, cred: &cred, credUntil: tok.ExpiresAt}
	params := map[string]any{"name": c.Tool}
	if a := strings.TrimSpace(string(c.Arguments)); a != "" && a != "null" {
		params["arguments"] = c.Arguments
	}
	res, rerr, err := st.request(ctx, gr, "tools/call", params)
	if err != nil {
		return nil, err
	}
	if rerr != nil {
		return errorResult("upstream error: " + capRunes(rerr.Message, 300)), nil
	}
	return sanitizeResult(res, g.cfg.MaxResultBytes), nil
}

// UpstreamMessage is the agent-facing wording of a CallBoard failure: the gateway's own text,
// never a raw network error.
func UpstreamMessage(err error) string {
	if errors.Is(err, ErrConnectionGone) {
		return "board access is no longer available: the sign-in that gave it has ended, or the board is no longer yours"
	}
	if errors.Is(err, ErrBoardCallInvalid) {
		return ErrBoardCallInvalid.Error()
	}
	if errors.Is(err, ErrBoardRevokesFailing) {
		return "the board is paused for this account: earlier board credentials could not be revoked; retry in a few minutes"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "the board call timed out"
	}
	return upstreamMessage(err)
}
