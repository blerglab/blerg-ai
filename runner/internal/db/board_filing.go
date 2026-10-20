package db

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const BoardFilingConnectionID = "00000000-0000-4000-8000-00000000b0f1"

func IsBoardFilingProposal(p *Proposal) bool {
	return p != nil && p.ConnectionID == BoardFilingConnectionID && p.ConnectionName == "board-file" && p.Tool == "file_ticket" && p.ToolHash == "board-file-v1"
}

func EditBoardFilingProposal(ctx context.Context, pool *pgxpool.Pool, account, id, expected string, args json.RawMessage) (*Proposal, error) {
	if !proposalUUID.MatchString(id) {
		return nil, ErrProposalNotFound
	}
	p, err := scanProposal(pool.QueryRow(ctx, `UPDATE mcp_proposals SET arguments=$4::jsonb WHERE id=$1::uuid AND account_id=$2 AND state='pending' AND arguments::text=$3 AND connection_id=$5::uuid AND connection_name='board-file' AND tool='file_ticket' AND tool_hash='board-file-v1' AND created_at>now()-interval '7 days' RETURNING `+proposalColumns, id, account, expected, string(args), BoardFilingConnectionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, proposalMiss(ctx, pool, account, id)
	}
	return p, err
}
func BeginBoardFilingExecution(ctx context.Context, pool *pgxpool.Pool, account, id, by, expected string) (*Proposal, error) {
	return BeginBoardFilingExecutionByGrant(ctx, pool, account, id, by, expected, "")
}

// BeginBoardFilingExecutionByGrant is BeginBoardFilingExecution for an approval a session
// permission made (grantID; "" for a person's): recorded with the decision, and only while live.
func BeginBoardFilingExecutionByGrant(ctx context.Context, pool *pgxpool.Pool, account, id, by, expected, grantID string) (*Proposal, error) {
	return transitionOwnedWhere(ctx, pool, account, id, ProposalPending, ProposalExecuting, `, decided_by=$5,decided_at=now(),approved_by_grant=NULLIF($8,'')::uuid`, ` AND arguments::text=$6 AND connection_id=$7::uuid AND connection_name='board-file' AND tool='file_ticket' AND tool_hash='board-file-v1' AND created_at>now()-interval '7 days'`+liveGrantGuard("$8"), by, expected, BoardFilingConnectionID, grantID)
}

func SaveBoardFilingProof(ctx context.Context, pool *pgxpool.Pool, sid, kind, value, project string) error {
	if (kind != "session_id" && kind != "token_id") || value == "" {
		return errors.New("invalid board proof")
	}
	_, err := pool.Exec(ctx, `INSERT INTO session_board_filing_proof(session_id,proof_kind,proof_value,project_scope) VALUES($1,$2,$3,$4) ON CONFLICT(session_id) DO NOTHING`, sid, kind, value, project)
	return err
}
func BoardFilingProof(ctx context.Context, pool *pgxpool.Pool, sid string) (kind, value, project string, err error) {
	err = pool.QueryRow(ctx, `SELECT proof_kind,proof_value,project_scope FROM session_board_filing_proof WHERE session_id=$1`, sid).Scan(&kind, &value, &project)
	return
}
