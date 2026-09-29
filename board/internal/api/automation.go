package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/blerglab/blerg-ai/board/internal/auth"
	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/runner"
	"github.com/blerglab/blerg-ai/contracts/identity"
)

// Board automation identity: WHO the sessions a board starts run as.
//
// Every session blerg-board starts — Run board's dispatcher, the quiet-worker
// respawn, reviewer and conflict-worker spawns, standing agents, AND the ones
// a human asks for from the UI (card Run/Discuss, board chat) — reaches the
// runner through one call, startOnRunner. Before this, that call carried the
// shared BLERG_RUNNER_KEY, a credential with no account behind it, so a
// cluster session could only use the operator's shared engine credential —
// which a self-service install deliberately does not have, and the session
// died at start with "No Claude credential reached the pod".
//
// Now each board carries an automation token: a blerg-core agent token
// (preset run-sessions) that a board admin minted for THEMSELVES and saved in
// the board's settings, plus the engine to run (claude, codex or hermes). The
// runner resolves the token to its owner (on_behalf_of) and fetches that
// person's own engine credential from core — the path it already takes for any
// agent-token caller. Board never sees the engine credential.
//
// There is no fallback. A board with no token, or a token that is expired or
// revoked, starts nothing and says why; it never quietly reverts to the shared
// key.

// automationAud/automationCap: what an automation token must be — a token the
// runner will accept for a start (runner/internal/server/runner.go's
// coreAuthAudience and coreAuthRunnerCap; core's run-sessions preset).
const (
	automationAud = "blerg-runner"
	automationCap = "session.start"
)

// maxAutomationTokenLen bounds what a PATCH may store: a core agent token is a
// few hundred bytes, and this column is read on every start.
const maxAutomationTokenLen = 8 << 10

// automationHowTo is the one sentence every refusal ends with, so a human
// reading a card comment or a 422 knows exactly what to do.
const automationHowTo = "a board admin must mint a run-sessions agent token for themselves " +
	"(blerg-core Settings → Agent tokens) and save it in this board's settings under Automation"

// automationError is a board-level start-identity problem: nothing about the
// card being started, everything about the board's configuration. The
// dispatcher stops the run on one rather than flagging card after card.
type automationError struct{ msg string }

func (e *automationError) Error() string { return e.msg }

// isAutomationErr reports whether a start failed because of the board's
// automation identity: missing or dead locally, or refused by the runner.
func isAutomationErr(err error) bool {
	var ae *automationError
	return errors.As(err, &ae) ||
		errors.Is(err, runner.ErrStartUnauthorized) ||
		errors.Is(err, runner.ErrNoStartIdentity)
}

// automationErrText is what a human is told about an automation failure.
// ErrStartUnauthorized comes from the runner and is re-worded here; the
// others already say what to do.
func automationErrText(err error) string {
	if errors.Is(err, runner.ErrStartUnauthorized) {
		return "the runner refused this board's automation token (expired, revoked, or not a run-sessions token) — " +
			automationHowTo
	}
	return err.Error()
}

// requiresStartIdentity: the configured driver starts sessions under a
// per-call identity. The production driver (runner.BlergRunner) always does.
func (a *API) requiresStartIdentity() bool {
	is, ok := a.runner.Driver.(runner.IdentityStarter)
	return ok && is.RequiresStartIdentity()
}

// tokenProblem turns a verification failure into words. Never includes the
// token.
func tokenProblem(err error) string {
	switch {
	case errors.Is(err, identity.ErrExpired):
		return "it has expired"
	case errors.Is(err, identity.ErrRevoked):
		return "it has been revoked"
	case errors.Is(err, identity.ErrWrongAudience):
		return "it is not a run-sessions token"
	default:
		return "it is not a valid blerg-core agent token"
	}
}

// startIdentity resolves the token and engine a board's sessions start under,
// refusing (automationError) when the board has none or the one it has is
// dead. For a driver that does not take a per-call identity it returns empty
// values and no error.
func (a *API) startIdentity(ctx context.Context, boardID string) (token, engine string, err error) {
	if !a.requiresStartIdentity() {
		return "", "", nil
	}
	ba, err := db.GetBoardAutomation(ctx, a.Pool, boardID)
	switch {
	case errors.Is(err, db.ErrAutomationUndecryptable):
		return "", "", &automationError{"this board's " + db.ErrAutomationUndecryptable.Error() +
			" in this board's settings under Automation (the encryption key changed or the stored value is damaged): " + automationHowTo}
	case errors.Is(err, db.ErrAutomationKeyMissing):
		return "", "", &automationError{"this board's automation token cannot be used: " + db.SecretKeyEnv +
			" is not set on blerg-board, so it cannot decrypt it — the administrator must set the key the token was saved under"}
	case err != nil:
		return "", "", err
	}
	if ba.Token == "" {
		return "", "", &automationError{"this board has no automation token, so it cannot start sessions: " + automationHowTo}
	}
	// Check it locally first when core is wired: expiry and revocation are
	// known here without a round trip, and "your token expired" is a far
	// better message than the runner's bare 401. A stale revocation list is
	// core's problem, not the token's — let the runner decide that one.
	if a.Auth != nil && a.Auth.Core() != nil {
		if _, verr := a.Auth.VerifyAgentToken(ba.Token, automationAud); verr != nil && !errors.Is(verr, identity.ErrFailClosed) {
			return "", "", &automationError{"this board's automation token can no longer start sessions (" +
				tokenProblem(verr) + "): " + automationHowTo}
		}
	}
	return ba.Token, ba.Engine, nil
}

// modelForEngine drops a Claude model name when the session runs another
// engine. Board models are Claude model ids ("claude-opus-5"); handed to
// codex or hermes as --model they would fail the session outright, where
// sending none lets that engine use its own configured default.
func modelForEngine(engine, model string) string {
	if engine != "" && engine != db.EngineClaude && strings.HasPrefix(model, "claude") {
		return ""
	}
	return model
}

// startOnRunner is the ONE way blerg-board starts a runner session: it
// resolves the board's automation identity and makes the start under it.
func (a *API) startOnRunner(ctx context.Context, boardID string, req runner.StartRequest) (string, error) {
	token, engine, err := a.startIdentity(ctx, boardID)
	if err != nil {
		return "", err
	}
	req.Token, req.Engine = token, engine
	req.Model = modelForEngine(engine, req.Model)
	return a.runner.Driver.Start(ctx, req)
}

// writeStartError answers a failed start for a human-facing endpoint: a
// board-configuration problem is a 422 that says what to fix, anything else is
// the runner's failure (502), as before.
func writeStartError(w http.ResponseWriter, err error) {
	if isAutomationErr(err) {
		writeError(w, http.StatusUnprocessableEntity, automationErrText(err))
		return
	}
	writeError(w, http.StatusBadGateway, "runner start failed: "+err.Error())
}

// checkStartIdentity is the pre-flight a human-facing start runs BEFORE it
// changes anything (claims the card, mints a board token): a board that
// cannot start sessions must not move a card into the work column for a
// session that will never exist. Answers the 422 itself; false = refused.
func (a *API) checkStartIdentity(ctx context.Context, w http.ResponseWriter, boardID string) bool {
	if _, _, err := a.startIdentity(ctx, boardID); err != nil {
		if isAutomationErr(err) {
			writeStartError(w, err)
		} else {
			writeDBError(w, err)
		}
		return false
	}
	return true
}

// verifyAutomationToken validates a board's new automation token from a PATCH
// (or, for "", a clear) and returns the update to write — it writes nothing,
// so the caller can land it in the same statement as the rest of the PATCH.
// It answers the HTTP error itself and returns false on refusal.
//
// Setting one is a human act and only for yourself: the token must be a live
// run-sessions agent token whose owner (on_behalf_of) is the human making the
// request. Board never mints tokens and never accepts one on someone else's
// behalf — the sessions it starts are attributed to, and billed to, the
// token's owner. Clearing is open to any board admin.
func (a *API) verifyAutomationToken(w http.ResponseWriter, p auth.Principal, raw string) (*db.AutomationTokenUpdate, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return &db.AutomationTokenUpdate{}, true
	}
	if !p.IsHuman() || p.HumanAccountID() == "" {
		writeError(w, http.StatusForbidden,
			"only a signed-in person can set a board's automation token, and it must be their own")
		return nil, false
	}
	if !db.AutomationKeyConfigured() {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": db.ErrAutomationKeyMissing.Error(), "field": "automation_token",
		})
		return nil, false
	}
	if len(raw) > maxAutomationTokenLen {
		writeError(w, http.StatusUnprocessableEntity, "automation_token is too long to be an agent token")
		return nil, false
	}
	tok, err := a.Auth.VerifyAgentToken(raw, automationAud)
	switch {
	case errors.Is(err, auth.ErrNoCore):
		writeError(w, http.StatusUnprocessableEntity,
			"automation tokens are blerg-core agent tokens, and this board is not connected to blerg-core (BLERG_CORE_URL)")
		return nil, false
	case err != nil:
		writeError(w, http.StatusUnprocessableEntity, fmt.Sprintf(
			"automation_token rejected: %s. Mint a run-sessions agent token in blerg-core Settings → Agent tokens", tokenProblem(err)))
		return nil, false
	case !tok.Has(automationCap):
		writeError(w, http.StatusUnprocessableEntity,
			"automation_token cannot start sessions (it lacks session.start): mint it with the run-sessions preset")
		return nil, false
	case tok.AccountID != p.HumanAccountID():
		writeError(w, http.StatusUnprocessableEntity,
			"automation_token belongs to a different account: mint one for yourself in blerg-core Settings → Agent tokens")
		return nil, false
	}
	exp := tok.ExpiresAt
	return &db.AutomationTokenUpdate{Token: raw, ExpiresAt: &exp, AccountID: tok.AccountID}, true
}
