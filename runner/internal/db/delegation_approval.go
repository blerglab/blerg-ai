package db

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
)

// GetOwnedDelegationGrant resolves an individual revoke without disclosing foreign rows.
func GetOwnedDelegationGrant(ctx context.Context, q Querier, id, account string) (*DelegationGrant, error) {
	g, err := scanDelegationGrant(q.QueryRow(ctx, `SELECT `+delegationGrantCols+` FROM delegation_grants WHERE id=$1::uuid AND account_id=$2`, id, account))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// RevokeChildApprovalTx revokes exactly the selected parent/owner/daemon/runtime, including
// engines no longer advertised. It never revokes desktop rules or another runtime's grants.
func RevokeChildApprovalTx(ctx context.Context, tx pgx.Tx, parent, account, daemon, runtime string) ([]string, error) {
	rows, err := tx.Query(ctx, `UPDATE delegation_grants SET revoked_at=now() WHERE parent_session_id=$1::uuid AND account_id=$2 AND daemon_id=$3::uuid AND runtime=$4 AND revoked_at IS NULL RETURNING id::text`, parent, account, daemon, runtime)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
