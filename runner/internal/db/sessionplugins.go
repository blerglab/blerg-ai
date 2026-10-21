package db

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const SessionPluginConnectionID = "00000000-0000-4000-8000-00000000b0f2"

func IsSessionPluginProposal(p *Proposal) bool {
	return p != nil && p.ConnectionID == SessionPluginConnectionID && p.ConnectionName == "session-plugin" && p.Tool == "request_plugin" && p.ToolHash == "session-plugin-v1"
}

func ListSessionPluginProposals(ctx context.Context, pool *pgxpool.Pool, account, sid string) ([]Proposal, error) {
	rows, err := pool.Query(ctx, `SELECT `+proposalColumns+` FROM mcp_proposals WHERE account_id=$1 AND session_id=$2::uuid AND connection_id=$3::uuid AND connection_name='session-plugin' AND tool='request_plugin' AND tool_hash='session-plugin-v1' ORDER BY CASE WHEN state='pending' THEN 0 WHEN result->>'state'='queued' THEN 1 ELSE 2 END, created_at DESC LIMIT 100`, account, sid, SessionPluginConnectionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Proposal{}
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// The exact reviewed bytes are fenced in the same transition as ordinary approvals.
func BeginSessionPluginExecution(ctx context.Context, pool *pgxpool.Pool, account, id, expected string) (*Proposal, error) {
	return transitionOwnedWhere(ctx, pool, account, id, ProposalPending, ProposalExecuting, `,decided_by=$5,decided_at=now()`, ` AND arguments::text=$6 AND connection_id=$7::uuid AND connection_name='session-plugin' AND tool='request_plugin' AND tool_hash='session-plugin-v1' AND `+proposalDeadline+`>now()`, account, expected, SessionPluginConnectionID)
}

// A queued approval is durable before dispatch. Replies only advance queued -> terminal;
// repeated or late queued acknowledgments cannot overwrite an applied/failed result.
func UpdateSessionPluginResult(ctx context.Context, pool *pgxpool.Pool, account, id, sid, daemon, state, message string) error {
	raw, _ := json.Marshal(map[string]string{"state": state, "message": message})
	_, err := pool.Exec(ctx, `UPDATE mcp_proposals SET result=result||$6::jsonb WHERE id=$1::uuid AND account_id=$2 AND session_id=$3::uuid AND url_snapshot=$4 AND connection_id=$5::uuid AND connection_name='session-plugin' AND tool='request_plugin' AND tool_hash='session-plugin-v1' AND state='done' AND result->>'state'='queued'`, id, account, sid, "session-plugins:"+daemon, SessionPluginConnectionID, string(raw))
	return err
}

func QueuedSessionPlugins(ctx context.Context, pool *pgxpool.Pool, daemon string) ([]Proposal, error) {
	rows, err := pool.Query(ctx, `SELECT `+proposalColumns+` FROM mcp_proposals p WHERE url_snapshot=$1 AND connection_id=$2::uuid AND connection_name='session-plugin' AND tool='request_plugin' AND tool_hash='session-plugin-v1' AND state='done' AND result->>'state'='queued' AND EXISTS(SELECT 1 FROM sessions s WHERE s.id=p.session_id AND s.daemon_id::text=$3 AND s.spawning_account_id=p.account_id AND s.status NOT IN ('ended','stopped','error')) ORDER BY created_at LIMIT 20`, "session-plugins:"+daemon, SessionPluginConnectionID, daemon)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Proposal{}
	for rows.Next() {
		p, e := scanProposal(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func SessionPluginProposalForResult(ctx context.Context, pool *pgxpool.Pool, id string) (*Proposal, error) {
	if !proposalUUID.MatchString(id) {
		return nil, ErrProposalNotFound
	}
	p, err := scanProposal(pool.QueryRow(ctx, `SELECT `+proposalColumns+` FROM mcp_proposals WHERE id=$1::uuid AND connection_id=$2::uuid AND connection_name='session-plugin' AND tool='request_plugin' AND tool_hash='session-plugin-v1'`, id, SessionPluginConnectionID))
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrProposalNotFound
	}
	return p, err
}
