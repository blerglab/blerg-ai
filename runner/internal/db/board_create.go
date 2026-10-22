package db

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// BoardCreateConnectionID marks a session's proposal of a new board (blerg-runner board create).
// Like a board filing it names no MCP connection: approval creates the board with the approving
// person's authority and gives only the proposing session access to it.
const BoardCreateConnectionID = "00000000-0000-4000-8000-00000000b0f3"

// ErrProposalIdempotency: the idempotency key was used before for a different board proposal.
var ErrProposalIdempotency = errors.New("the idempotency key was used for a different proposal")

func IsBoardCreateProposal(p *Proposal) bool {
	return p != nil && p.ConnectionID == BoardCreateConnectionID && p.ConnectionName == "board-create" && p.Tool == "create_board" && p.ToolHash == "board-create-v1"
}

const boardCreateWhere = ` AND connection_id=$7::uuid AND connection_name='board-create' AND tool='create_board' AND tool_hash='board-create-v1'`

// BeginBoardCreateExecution is the approval's compare-and-set for a proposed board: pending ->
// executing for the owner, only for the exact stored arguments that were reviewed and before the
// deadline. One of any number of concurrent approvals wins.
func BeginBoardCreateExecution(ctx context.Context, pool *pgxpool.Pool, account, id, expected string) (*Proposal, error) {
	return transitionOwnedWhere(ctx, pool, account, id, ProposalPending, ProposalExecuting, `,decided_by=$5,decided_at=now()`,
		` AND arguments::text=$6`+boardCreateWhere+` AND `+proposalDeadline+`>now()`, account, expected, BoardCreateConnectionID)
}

// boardCreateTwin is the session's earlier proposal this one repeats, or nil: the same
// idempotency key (any age; a different draft under the same key is ErrProposalIdempotency), or,
// without a key, the same draft within 15 minutes.
func boardCreateTwin(ctx context.Context, tx pgx.Tx, p Proposal) (*Proposal, error) {
	const kind = ` AND connection_id=$3::uuid AND connection_name='board-create' AND tool='create_board' AND tool_hash='board-create-v1'`
	var row pgx.Row
	if key := idempotencyKeyOf(p.Arguments); key != "" {
		row = tx.QueryRow(ctx, `SELECT `+proposalColumns+` FROM mcp_proposals WHERE account_id=$1 AND session_id=$2::uuid`+kind+`
			AND arguments->>'idempotency_key'=$4 ORDER BY created_at DESC,id DESC LIMIT 1`, p.AccountID, p.SessionID, BoardCreateConnectionID, key)
		twin, err := scanProposal(row)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var same bool
		if err := tx.QueryRow(ctx, `SELECT $1::jsonb = $2::jsonb`, string(twin.Arguments), string(p.Arguments)).Scan(&same); err != nil {
			return nil, err
		}
		if !same {
			return nil, ErrProposalIdempotency
		}
		return twin, nil
	}
	row = tx.QueryRow(ctx, `SELECT `+proposalColumns+` FROM mcp_proposals WHERE account_id=$1 AND session_id=$2::uuid`+kind+`
		AND state IN ('pending','executing','done','unknown') AND created_at > now()-interval '15 minutes'
		AND arguments - 'idempotency_key' = $4::jsonb - 'idempotency_key' ORDER BY created_at DESC,id DESC LIMIT 1`,
		p.AccountID, p.SessionID, BoardCreateConnectionID, string(p.Arguments))
	twin, err := scanProposal(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return twin, err
}

// idempotencyKeyOf is the idempotency_key a board proposal's arguments carry, or "".
func idempotencyKeyOf(raw []byte) string {
	var v struct {
		Key string `json:"idempotency_key"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return ""
	}
	return v.Key
}
