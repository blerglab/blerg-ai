package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/coremint"
	"github.com/blerglab/blerg-ai/board/internal/db"
)

// "Run as me": one click in Board settings → Automation (or the pre-checked box when a board is
// created, or the call to action beside Run on a card) makes the signed-in person the board's
// automation identity. The board asks blerg-core to mint a run-sessions agent token for them and
// stores it exactly as a pasted token: sealed under BLERG_BOARD_SECRET_KEY, write-only, never in a
// response, a URL or a log.
//
// The person's proof is the board-audience access token their browser sent on this request
// (auth.BearerToken). Core verifies it as its own token, checks that its browser session (sid) is
// live and the account matches, and answers server-to-server. The board holds only
// BLERG_CORE_BOARD_MINT_KEY, which opens core's two board-automation routes and nothing else: it
// never sees the person's core cookie or refresh token and cannot fetch engine credentials.
//
// A token Blerg minted is recorded (automation_token_minted_id) and revoked in core when it is
// replaced (another Run as me, a pasted token), removed, or its board is deleted. A pasted token
// is the person's own and is never revoked by the board.

// AutomationMinter is core's board-automation mint (coremint.Client in production).
type AutomationMinter interface {
	Mint(ctx context.Context, accountID, boardID, boardName, accessToken string) (coremint.Minted, error)
	Revoke(ctx context.Context, accountID, tokenID, boardID string) error
}

// msgRunAsMeUnavailable is the answer when this install cannot mint: pasting still works.
const msgRunAsMeUnavailable = "Run as me is not set up on this install (blerg-board and blerg-core need the same " +
	"BLERG_CORE_BOARD_MINT_KEY): paste a run-sessions agent token from blerg-core Settings → Agent tokens instead"

// msgRunAsMeSwitchedOff is the answer when an operator switched new mints off on core.
const msgRunAsMeSwitchedOff = "Run as me is turned off on this install: paste a run-sessions agent token from " +
	"blerg-core Settings → Agent tokens instead"

// runAsMeAvailable reports whether this board server can mint at all.
func (a *API) runAsMeAvailable() bool {
	return a.Minter != nil && db.AutomationKeyConfigured() && a.Auth != nil && a.Auth.Core() != nil
}

// canRunAsMe reports whether p could make themselves boardID's automation identity right now.
func (a *API) canRunAsMe(p auth.Principal, boardID string) bool {
	return a.runAsMeAvailable() && p.IsHuman() && p.HumanAccountID() != "" && p.RequireAdmin(boardID) == nil
}

// runAsMeError is a refusal with the status to answer and words a person can act on.
type runAsMeError struct {
	status int
	msg    string
}

func (e *runAsMeError) Error() string { return e.msg }

// revokeMinted revokes a token Blerg minted for boardID, best effort and past the request's
// cancellation: a failure is logged (ids only) and the token stays listed in the owner's
// Settings, where they can revoke it themselves.
func (a *API) revokeMinted(ctx context.Context, boardID string, m *db.MintedAutomation) {
	if m == nil || a.Minter == nil {
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := a.Minter.Revoke(cctx, m.AccountID, m.TokenID, boardID); err != nil {
		log.Printf("run as me: could not revoke minted token %s of board %s: %v", m.TokenID, boardID, err)
	}
}

// runAsMe mints a run-sessions token for p through core and stores it as board's automation
// token, revoking the minted token it replaces. r is the request whose bearer is p's proof.
func (a *API) runAsMe(r *http.Request, p auth.Principal, board db.Board) error {
	ctx := r.Context()
	account := p.HumanAccountID()
	if !p.IsHuman() || account == "" {
		return &runAsMeError{http.StatusForbidden, "only a signed-in person can run a board's sessions as themselves"}
	}
	if err := p.RequireAdmin(board.ID); err != nil {
		return &runAsMeError{http.StatusForbidden, "only a board admin can set who this board's sessions run as"}
	}
	if !db.AutomationKeyConfigured() {
		return &runAsMeError{http.StatusUnprocessableEntity, db.ErrAutomationKeyMissing.Error()}
	}
	if a.Minter == nil || a.Auth == nil || a.Auth.Core() == nil {
		return &runAsMeError{http.StatusServiceUnavailable, msgRunAsMeUnavailable}
	}
	proof := auth.BearerToken(r)
	if proof == "" {
		return &runAsMeError{http.StatusForbidden, "only a signed-in person can run a board's sessions as themselves"}
	}

	minted, err := a.Minter.Mint(ctx, account, board.ID, board.Name, proof)
	switch {
	case errors.Is(err, coremint.ErrRefused):
		return &runAsMeError{http.StatusForbidden, "blerg-core did not accept your sign-in as live: sign in again and retry"}
	case errors.Is(err, coremint.ErrRateLimited):
		return &runAsMeError{http.StatusTooManyRequests, err.Error()}
	case errors.Is(err, coremint.ErrTooManyTokens), errors.Is(err, coremint.ErrForbidden):
		return &runAsMeError{http.StatusUnprocessableEntity, err.Error()}
	case errors.Is(err, coremint.ErrSwitchedOff):
		log.Printf("run as me: board %s: %v", board.ID, err)
		return &runAsMeError{http.StatusServiceUnavailable, msgRunAsMeSwitchedOff}
	case errors.Is(err, coremint.ErrDisabled), errors.Is(err, coremint.ErrUnauthorized), errors.Is(err, coremint.ErrNotConfigured):
		log.Printf("run as me: board %s: %v", board.ID, err)
		return &runAsMeError{http.StatusServiceUnavailable, msgRunAsMeUnavailable}
	case err != nil:
		log.Printf("run as me: board %s: %v", board.ID, err)
		return &runAsMeError{http.StatusBadGateway, "blerg-core could not mint the token; try again"}
	}

	// Trust, but verify: what core handed back must be exactly what a pasted token must be —
	// a live run-sessions token of this person — before it is stored.
	tok, verr := a.Auth.VerifyAgentToken(minted.Token, automationAud)
	if verr != nil || !tok.Has(automationCap) || tok.AccountID != account || tok.TokenID != minted.ID {
		a.revokeMinted(ctx, board.ID, &db.MintedAutomation{AccountID: account, TokenID: minted.ID})
		log.Printf("run as me: board %s: core returned a token that is not this person's run-sessions token", board.ID)
		return &runAsMeError{http.StatusBadGateway, "blerg-core returned an unusable token; nothing was saved"}
	}
	exp := tok.ExpiresAt
	replaced, err := db.ReplaceBoardAutomationToken(ctx, a.Pool, board.ID, db.AutomationTokenUpdate{
		Token: minted.Token, ExpiresAt: &exp, AccountID: account, MintedID: minted.ID,
	})
	if err != nil {
		// Never leave a live token behind that nothing holds.
		a.revokeMinted(ctx, board.ID, &db.MintedAutomation{AccountID: account, TokenID: minted.ID})
		return err
	}
	a.revokeMinted(ctx, board.ID, replaced)
	log.Printf("run as me: board %s now runs as account %s (token %s)", board.ID, account, minted.ID)
	return nil
}

// writeRunAsMeError answers a failed Run as me.
func writeRunAsMeError(w http.ResponseWriter, err error) {
	var re *runAsMeError
	if errors.As(err, &re) {
		writeError(w, re.status, re.msg)
		return
	}
	writeDBError(w, err)
}

// handleRunAsMe is POST /api/boards/{id}/automation/run-as-me: the signed-in board admin becomes
// the board's automation identity. Answers the board (never the token).
func (a *API) handleRunAsMe(w http.ResponseWriter, r *http.Request, p auth.Principal) {
	boardID := r.PathValue("id")
	if err := p.RequireAdmin(boardID); err != nil {
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	board, err := db.GetBoard(r.Context(), a.Pool, boardID)
	if err != nil {
		writeDBError(w, err)
		return
	}
	if err := a.runAsMe(r, p, board); err != nil {
		writeRunAsMeError(w, err)
		return
	}
	b, err := db.GetBoard(r.Context(), a.Pool, boardID)
	if err != nil {
		writeDBError(w, err)
		return
	}
	a.Hub.Broadcast(boardID, "board_changed")
	writeJSON(w, http.StatusOK, b)
}
