package server

import (
	"context"
	"log"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/jackc/pgx/v5/pgxpool"
)

type filingProjectKey struct{}

const (
	msgChildFilingNoParent  = "This session files with the sign-in of the session that started it, and that session has ended for good, was discarded or is gone; ask the person to file it."
	msgChildFilingNoSignIn  = "This session files with the sign-in of the session that started it, and that session has no board sign-in; ask the person to file it."
	msgChildFilingSignedOut = "This session files with the sign-in of the session that started it, and that sign-in has ended; the person has to sign in again and renew it."
)

func withFilingProject(ctx context.Context, project string) context.Context {
	return context.WithValue(ctx, filingProjectKey{}, project)
}
func recordBoardFilingProof(ctx context.Context, pool *pgxpool.Pool, sid string) {
	p := coreProofFrom(ctx)
	kind, value := "session_id", p.SessionID
	if p.TokenID != "" {
		kind, value = "token_id", p.TokenID
	}
	if value == "" {
		return
	}
	project, _ := ctx.Value(filingProjectKey{}).(string)
	if err := db.SaveBoardFilingProof(ctx, pool, sid, kind, value, project); err != nil {
		log.Printf("session %s: board discovery proof unavailable", sid)
	}
}

// filingProof is the sign-in a session's board discovery and filing act with: the one recorded at
// its start (or when its owner opened it). A session started by another one (a child) has none of
// its own that counts: it files with its parent's, as its other board calls act with the parent's
// sign-in (child_board_access.go). The parent must still be able to lend it (same owner, not
// discarded, not stopped for good since the child started); otherwise the filing is refused. What a
// child's filing may do without asking is capped separately, at approval and again right before it
// executes (childFilingGrant, childFilingStillCovered).
func (a *API) filingProof(ctx context.Context, row *db.SessionRow) (mcpgw.Proof, string, error) {
	if row == nil || row.SpawningAccountID == nil || *row.SpawningAccountID == "" {
		return mcpgw.Proof{}, "", apiErrorf(409, "This session has no account for Board access.")
	}
	source := row
	if isChildSession(row) {
		parent, err := a.childBoardParent(ctx, row)
		if err != nil {
			return mcpgw.Proof{}, "", apiErrorf(503, "Could not read the session that started this one; retry.")
		}
		if parent == nil {
			return mcpgw.Proof{}, "", apiErrorf(409, "%s", msgChildFilingNoParent)
		}
		source = parent
	}
	kind, value, project, err := db.BoardFilingProof(ctx, a.dbPool, source.ID)
	if err != nil && source != row {
		return mcpgw.Proof{}, "", apiErrorf(409, "%s", msgChildFilingNoSignIn)
	}
	if err != nil {
		return mcpgw.Proof{}, "", apiErrorf(409, "This session has no retained Board discovery proof; start a new session.")
	}
	p := mcpgw.Proof{AccountID: *row.SpawningAccountID}
	if kind == "token_id" {
		p.TokenID = value
	} else {
		p.SessionID = value
	}
	return p, project, nil
}
