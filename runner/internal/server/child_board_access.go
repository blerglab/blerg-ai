package server

// Children inherit board access (0.4.4). A session that starts another one passes board levels
// down with the start (`blerg-runner session start --board <name|id>:<level>`, the start's "boards"
// field), never above its own level for that board. No credential is minted for it: the child gets
// a level of its own, a session_proposal_grants row with created_via "parent", and every board call
// of the child goes through the runner (session_board_access.go) like any session's.
//
//   - At the start, each asked board must be one of the parent's boards above propose (by id or by
//     name) and the level at most the parent's level now. The approval card shows them, and the
//     start is checked again when it runs (a decided card, a rule or a grant), so a parent lowered in
//     between cannot pass down what it no longer has.
//   - Live, a child's level for a board is min(its own level, its parent's level now): lowering or
//     revoking the parent lowers every child at its next call, and raising the parent never raises a
//     child above its own level. A child's call acts with its parent's sign-in for that board.
//   - The person may lower a single child from the child's Permissions; raising a child above its
//     parent's level is refused.
//   - The cap is propose once the parent ended for good after the child started (its delegation
//     epoch moved), was discarded, or is gone: a kept-running child never keeps board access past
//     its parent's stop for good, even if the parent is resumed and given access again. The child's
//     own rows end with its own lifetime, as every session permission does.

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

// maxChildBoards bounds the boards one start may pass down.
const maxChildBoards = 8

// childBoard is one board a start passes down. The parent asks with Board (an id, or a name as
// `blerg-runner board access` lists it) and Level; the server resolves Board to the id and fills
// Name, so the stored start, its hash and the approval card all carry the resolved board.
type childBoard struct {
	Board string `json:"board"`
	Name  string `json:"name,omitempty"`
	Level string `json:"level"`
}

const (
	msgChildBoardLevel = `a board passed to a started session is "<board>:<level>", level read, file or full`
	msgChildAboveCap   = "a session started by another session cannot have more board access than the session that started it"
)

// minBoardLevel is the lower of two levels (an unknown level counts as propose).
func minBoardLevel(a, b string) string {
	if db.BoardLevelRank(a) < 0 || db.BoardLevelRank(b) < 0 {
		return db.BoardLevelPropose
	}
	if db.BoardLevelRank(a) <= db.BoardLevelRank(b) {
		return a
	}
	return b
}

// childBoardParent is the parent whose levels cap the child now, or nil when the cap is propose:
// the child has no parent row (deleted), the parent is another account's, was discarded, or ended
// for good after the child started (its delegation epoch moved past the child's start).
func (a *API) childBoardParent(ctx context.Context, child *db.SessionRow) (*db.SessionRow, error) {
	if child == nil || child.ParentSessionID == nil || *child.ParentSessionID == "" {
		return nil, nil
	}
	parent, err := db.GetSession(ctx, a.dbPool, *child.ParentSessionID)
	if err != nil || parent == nil {
		return nil, err
	}
	if !childBoardEpochOK(parent, child) {
		return nil, nil
	}
	return parent, nil
}

// childBoardEpochOK: the parent may still cap (and lend its sign-in to) the child's board calls.
func childBoardEpochOK(parent, child *db.SessionRow) bool {
	switch {
	case parent == nil || child == nil:
		return false
	case derefOrEmpty(parent.SpawningAccountID) == "" || derefOrEmpty(parent.SpawningAccountID) != derefOrEmpty(child.SpawningAccountID):
		return false
	case parent.DiscardRequestedAt != nil || db.TerminallyEnded(parent):
		return false
	case parent.DelegationEndedAt != nil && !parent.DelegationEndedAt.Before(child.StartedAt):
		return false
	}
	return true
}

// applyParentCap turns lv (the child's own level, sessionBoardLevel) into its effective level under
// the parent's level for the board now. parent nil caps at propose.
func (a *API) applyParentCap(ctx context.Context, lv boardLevelOf, parent *db.SessionRow, boardID string) (boardLevelOf, error) {
	lv.Child, lv.Own, lv.Cap = true, lv.Level, db.BoardLevelPropose
	if parent != nil {
		plv, err := a.sessionBoardLevel(ctx, parent.ID, boardID)
		if err != nil {
			lv.Level = db.BoardLevelPropose
			return lv, err
		}
		lv.Cap = plv.Level
		if p, ok := plv.proof(); ok {
			lv.capProof = p
		}
		if plv.State != nil && plv.State.Live {
			lv.capProject = plv.State.Grant.ProjectScope
		}
	}
	lv.Level = minBoardLevel(lv.Own, lv.Cap)
	return lv, nil
}

// effectiveBoardLevel is the board's level for this session now: sessionBoardLevel, capped by the
// parent's level for a child. A database error answers propose with the error.
func (a *API) effectiveBoardLevel(ctx context.Context, row *db.SessionRow, boardID string) (boardLevelOf, error) {
	lv, err := a.sessionBoardLevel(ctx, row.ID, boardID)
	lv.Own = lv.Level
	if err != nil || !isChildSession(row) {
		return lv, err
	}
	parent, err := a.childBoardParent(ctx, row)
	if err != nil {
		lv.Level = db.BoardLevelPropose
		return lv, err
	}
	return a.applyParentCap(ctx, lv, parent, boardID)
}

// boardLevelsFor is sessionBoardLevels for the session row: for a child, every board it holds a
// level of its own for, with its effective level (capped by the parent; possibly propose), its own
// level and the parent's.
func (a *API) boardLevelsFor(ctx context.Context, row *db.SessionRow) ([]boardLevelView, error) {
	boards, err := a.sessionBoardLevels(ctx, row.ID)
	if err != nil || !isChildSession(row) {
		return boards, err
	}
	parent, err := a.childBoardParent(ctx, row)
	if err != nil {
		return nil, err
	}
	for i := range boards {
		lv, err := a.applyParentCap(ctx, boardLevelOf{Level: boards[i].Level}, parent, boards[i].BoardID)
		if err != nil {
			return nil, err
		}
		boards[i].OwnLevel, boards[i].ParentLevel, boards[i].Level = lv.Own, lv.Cap, lv.Level
		boards[i].child, boards[i].capProof = true, lv.capProof
	}
	return boards, nil
}

// resolveChildBoards checks the boards a parent asks to pass down and resolves each to the parent's
// board (id and name): at most maxChildBoards, each once, a level of read, file or full, and never
// above the parent's level for that board now.
func (a *API) resolveChildBoards(ctx context.Context, parent *db.SessionRow, asked []childBoard) ([]childBoard, *APIError) {
	if len(asked) == 0 {
		return nil, nil
	}
	if len(asked) > maxChildBoards {
		return nil, apiErrorf(http.StatusUnprocessableEntity, "at most %d boards can be passed to a started session", maxChildBoards)
	}
	if problem := proposalGrantProblem(parent); problem != "" || isChildSession(parent) {
		return nil, apiErrorf(http.StatusUnprocessableEntity, "this session cannot pass board access to the sessions it starts")
	}
	if a.dbPool == nil {
		return nil, apiErrorf(http.StatusServiceUnavailable, "database unavailable")
	}
	mine, err := a.sessionBoardLevels(ctx, parent.ID)
	if err != nil {
		log.Printf("child boards: parent levels %s: %v", parent.ID, err)
		return nil, apiErrorf(http.StatusServiceUnavailable, "could not read this session's board access; retry")
	}
	out := make([]childBoard, 0, len(asked))
	seen := map[string]bool{}
	for _, b := range asked {
		level := strings.ToLower(strings.TrimSpace(b.Level))
		ref := strings.TrimSpace(b.Board)
		if ref == "" || !filingText(ref, 200, false) || db.BoardLevelRank(level) < 1 {
			return nil, apiErrorf(http.StatusUnprocessableEntity, "%s", msgChildBoardLevel)
		}
		var found []boardLevelView
		for _, m := range mine {
			if m.BoardID == ref && m.Level != db.BoardLevelPropose {
				found = []boardLevelView{m}
				break
			}
		}
		if len(found) == 0 {
			for _, m := range mine {
				if m.BoardName == ref && m.Level != db.BoardLevelPropose {
					found = append(found, m)
				}
			}
		}
		switch {
		case len(found) > 1:
			return nil, apiErrorf(http.StatusUnprocessableEntity, "more than one of your boards is named %s; use its id", quotedName(ref))
		case len(found) == 0:
			return nil, apiErrorf(http.StatusUnprocessableEntity, "board %s is not one this session has access to; `blerg-runner board access` lists yours", quotedName(ref))
		}
		m := found[0]
		if seen[m.BoardID] {
			return nil, apiErrorf(http.StatusUnprocessableEntity, "board %s is passed more than once", quotedName(ref))
		}
		seen[m.BoardID] = true
		if db.BoardLevelRank(level) > db.BoardLevelRank(m.Level) {
			return nil, apiErrorf(http.StatusUnprocessableEntity, "%s: this session has %s on board %s, so it can pass down at most %s",
				msgChildAboveCap, boardLevelLabels[m.Level], quotedName(boardDisplayName(m.BoardName, m.BoardID)), boardLevelLabels[m.Level])
		}
		out = append(out, childBoard{Board: m.BoardID, Name: m.BoardName, Level: level})
	}
	return out, nil
}

// childBoardsProblem checks resolved boards again against the parent's levels now (a start that
// runs later than it was asked: a decided card, a rule or a grant).
func (a *API) childBoardsProblem(ctx context.Context, parent *db.SessionRow, boards []childBoard) *APIError {
	if len(boards) == 0 {
		return nil
	}
	if problem := proposalGrantProblem(parent); problem != "" || isChildSession(parent) {
		return apiErrorf(http.StatusConflict, "this session cannot pass board access to the sessions it starts")
	}
	for _, b := range boards {
		lv, err := a.sessionBoardLevel(ctx, parent.ID, b.Board)
		if err != nil {
			log.Printf("child boards: recheck %s: %v", parent.ID, err)
			return apiErrorf(http.StatusServiceUnavailable, "could not read the parent's board access; retry")
		}
		if db.BoardLevelRank(b.Level) < 1 || db.BoardLevelRank(b.Level) > db.BoardLevelRank(lv.Level) {
			return apiErrorf(http.StatusConflict, "%s: the session that asked now has %s on board %s, below the %s this start passes down; ask again",
				msgChildAboveCap, boardLevelLabels[lv.Level], quotedName(boardDisplayName(b.Name, b.Board)), boardLevelLabels[b.Level])
		}
	}
	return nil
}

func boardDisplayName(name, id string) string {
	if name != "" {
		return name
	}
	return id
}

// seedChildBoards stores the child's own levels after its start. A board the child already has a
// row for (a replayed start) is left as it is, so a level the person lowered since is never raised
// back. The row acts with no sign-in of its own (the parent's is used per call); the parent's proof
// sid is recorded when it has one, for the audit. Best effort: a board not stored stays at propose.
func (a *API) seedChildBoards(ctx context.Context, parent *db.SessionRow, childID string, boards []childBoard) {
	if a.dbPool == nil || parent == nil || childID == "" {
		return
	}
	account := derefOrEmpty(parent.SpawningAccountID)
	for _, b := range boards {
		state, err := db.LatestBoardGrant(ctx, a.dbPool, childID, b.Board)
		if err != nil {
			log.Printf("child boards: read %s board=%s: %v", childID, b.Board, err)
			continue
		}
		if state != nil {
			continue
		}
		g := db.ProposalGrant{SessionID: childID, AccountID: account, Kind: db.ProposalGrantBoard, BoardID: b.Board, BoardName: b.Name,
			Level: b.Level, CreatedVia: "parent"}
		if plv, err := a.sessionBoardLevel(ctx, parent.ID, b.Board); err == nil {
			if p, ok := plv.proof(); ok {
				g.ProofSessionID = p.SessionID
			}
			if plv.State != nil && plv.State.Live {
				g.ProjectScope = plv.State.Grant.ProjectScope
			}
		}
		out, err := db.InsertProposalGrant(ctx, a.dbPool, g)
		if err != nil {
			log.Printf("child boards: store %s board=%s: %v", childID, b.Board, err)
			continue
		}
		log.Printf("board access: session=%s account=%s board=%s level=propose->%s grant=%s via=parent parent=%s", childID, account, b.Board, out.Level, out.ID, parent.ID)
	}
}

// childBoardNote is boardAccessNote for a child whose level changed because its parent's did.
func childBoardNote(board, from, to string) string {
	return boardAccessNote(board, from, to) + " Your board access follows the access of the session that started you."
}

// relayChildrenBoardAccess tells each running child of parent whose effective level for the board
// changed with the parent's (from -> to), as the parent's own note is told. Best effort.
func (a *API) relayChildrenBoardAccess(ctx context.Context, account string, parent *db.SessionRow, changeID, boardID, name, from, to string) {
	if a.dbPool == nil || parent == nil || from == to || isChildSession(parent) {
		return
	}
	kids, err := db.ListChildSessions(ctx, a.dbPool, parent.ID)
	if err != nil {
		log.Printf("board access: children of %s: %v", parent.ID, err)
		return
	}
	for _, k := range kids {
		child, err := db.GetSession(ctx, a.dbPool, k.ID)
		if err != nil || child == nil || terminalSessionStatus(child.Status) || db.TerminallyEnded(child) || !childBoardEpochOK(parent, child) {
			continue
		}
		own, err := a.sessionBoardLevel(ctx, child.ID, boardID)
		if err != nil || own.Level == db.BoardLevelPropose {
			continue
		}
		before, after := minBoardLevel(own.Level, from), minBoardLevel(own.Level, to)
		if before == after {
			continue
		}
		a.relayBoardNote(ctx, account, child, changeID+"-"+child.ID, childBoardNote(name, before, after))
	}
}

// childBoardProof is the sign-in a child's call acts with: its parent's for the board.
func (b boardLevelOf) childBoardProof() (mcpgw.Proof, bool) {
	return b.capProof, b.capProof.Valid() && db.BoardLevelRank(b.Level) > 0
}

// childFilingGrant is the board permission g as it may approve a filing now: g itself for a session
// that was not started by another one; for a child, only when its level under its parent's cap is
// still file or full, acting with the parent's sign-in for the board (nil otherwise: the filing
// waits for the person).
func (a *API) childFilingGrant(ctx context.Context, g *db.ProposalGrant) *db.ProposalGrant {
	row, err := db.GetSession(ctx, a.dbPool, g.SessionID)
	if err != nil || row == nil {
		return nil
	}
	if !isChildSession(row) {
		return g
	}
	lv, err := a.effectiveBoardLevel(ctx, row, g.BoardID)
	if err != nil || db.BoardLevelRank(lv.Level) < db.BoardLevelRank(db.BoardLevelFile) || lv.capProof.SessionID == "" {
		return nil
	}
	out := *g
	out.ProofSessionID, out.ProjectScope = lv.capProof.SessionID, lv.capProject
	return &out
}

// childFilingStillCovered re-checks, just before a filing a session permission approved executes,
// that a started session's level under its parent's cap still allows filing and that it still acts
// with the sign-in the approval uses (the parent's). Any other session: true (its own row is
// checked by the execution guard).
func (a *API) childFilingStillCovered(ctx context.Context, sessionID, boardID, sid string) bool {
	row, err := db.GetSession(ctx, a.dbPool, sessionID)
	if err != nil || row == nil {
		return false
	}
	if !isChildSession(row) {
		return true
	}
	lv, err := a.effectiveBoardLevel(ctx, row, boardID)
	return err == nil && db.BoardLevelRank(lv.Level) >= db.BoardLevelRank(db.BoardLevelFile) && lv.capProof.SessionID != "" && lv.capProof.SessionID == sid
}

// childBoardsHashKey is what a start's idempotency hash covers of its boards: each board's id and
// level, never its display name, so a retry after a board was renamed is the same request.
func childBoardsHashKey(boards []childBoard) []childBoard {
	if len(boards) == 0 {
		return nil
	}
	out := make([]childBoard, len(boards))
	for i, b := range boards {
		out[i] = childBoard{Board: b.Board, Level: b.Level}
	}
	return out
}
