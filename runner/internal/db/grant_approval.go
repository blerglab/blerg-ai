package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrProposalGrantCap leaves a burst's excess proposals pending for the person.
var ErrProposalGrantCap = errors.New("session permission reached its hourly approval limit")

// beginGrantExecution serializes the hourly count and the pending-to-executing
// transition on the grant row, across replicas. No remote work holds this lock.
func beginGrantExecution(ctx context.Context, pool *pgxpool.Pool, grantID string, hourlyCap []int, update func(Querier) (*Proposal, error)) (*Proposal, error) {
	if grantID == "" {
		return update(pool)
	}
	if !proposalUUID.MatchString(grantID) {
		return nil, ErrProposalState
	}
	limit := 20
	if len(hourlyCap) > 0 && hourlyCap[0] > 0 {
		limit = hourlyCap[0]
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM session_proposal_grants WHERE id=$1::uuid FOR UPDATE`, grantID).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// The grant is gone: the compare-and-set lost, as when it is revoked.
			return nil, ErrProposalState
		}
		return nil, err
	}
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM mcp_proposals WHERE approved_by_grant=$1::uuid AND decided_at>=now()-interval '1 hour'`, grantID).Scan(&count); err != nil {
		return nil, err
	}
	if count >= limit {
		return nil, ErrProposalGrantCap
	}
	p, err := update(tx)
	if err != nil {
		return nil, err
	}
	return p, tx.Commit(ctx)
}
