package db

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
)

const BoardMetadataConnectionID = "00000000-0000-4000-8000-00000000b0f4"

func IsBoardMetadataProposal(p *Proposal) bool {
	return p != nil && p.ConnectionID == BoardMetadataConnectionID && p.ConnectionName == "Board metadata" && p.Tool == "update_board_metadata" && p.ToolHash == "board-metadata-v1"
}
func BeginBoardMetadataExecution(ctx context.Context, pool *pgxpool.Pool, account, id, expected string) (*Proposal, error) {
	return transitionOwnedWhere(ctx, pool, account, id, ProposalPending, ProposalExecuting, `,decided_by=$5,decided_at=now()`,
		` AND arguments::text=$6 AND connection_id=$7::uuid AND connection_name='Board metadata' AND tool='update_board_metadata' AND tool_hash='board-metadata-v1' AND `+proposalDeadline+`>now()`, account, expected, BoardMetadataConnectionID)
}
