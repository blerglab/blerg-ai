package server

import (
	"context"
	"log"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/jackc/pgx/v5/pgxpool"
)

type filingProjectKey struct{}

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
func (a *API) filingProof(ctx context.Context, row *db.SessionRow) (mcpgw.Proof, string, error) {
	if row == nil || row.SpawningAccountID == nil || *row.SpawningAccountID == "" {
		return mcpgw.Proof{}, "", apiErrorf(409, "This session has no account for Board access.")
	}
	kind, value, project, err := db.BoardFilingProof(ctx, a.dbPool, row.ID)
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
