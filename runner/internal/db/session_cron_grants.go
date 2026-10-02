package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MCPGrantRef is what the hourly sweeper needs to know about one grant: which connection of whom
// it points at and the proof the gateway would present to core for it.
type MCPGrantRef struct {
	SessionID    string
	ConnectionID string
	AccountID    string
	ProofKind    string
	ProofValue   string
}

// ListMCPGrantRefs lists every grant of a user's connection, for the sweeper's check against core's
// connection lists. Built-in grants (the board) are not core connections and are left out.
func ListMCPGrantRefs(ctx context.Context, conn *pgxpool.Pool) ([]MCPGrantRef, error) {
	rows, err := conn.Query(ctx, `
		SELECT session_id::text, connection_id::text, account_id, proof_kind, proof_value
		  FROM session_mcp_grants WHERE builtin = '' ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MCPGrantRef
	for rows.Next() {
		var g MCPGrantRef
		if err := rows.Scan(&g.SessionID, &g.ConnectionID, &g.AccountID, &g.ProofKind, &g.ProofValue); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// DeleteMCPGrant removes one grant (one connection of one session), reporting whether it existed.
func DeleteMCPGrant(ctx context.Context, conn *pgxpool.Pool, sessionID, connectionID string) (bool, error) {
	tag, err := conn.Exec(ctx, `DELETE FROM session_mcp_grants WHERE session_id = $1 AND connection_id = $2`, sessionID, connectionID)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}
