package identity

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// LockLiveHumanApprovalTx serializes approval with refresh/logout/password reset.
// Call only after verifying the person's own access-token signature/audience/kind.
func (s *Service) LockLiveHumanApprovalTx(ctx context.Context, tx pgx.Tx, account, sid string) error {
	if !agentTokenIDShape.MatchString(account) || !agentTokenIDShape.MatchString(sid) {
		return ErrSessionRevoked
	}
	if err := s.lockAccountSessions(ctx, tx, account); err != nil {
		return sessionBusy(err)
	}
	var live bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM human_sessions hs JOIN accounts a ON a.id=hs.account_id WHERE hs.chain_id=$1 AND hs.account_id=$2 AND hs.revoked_at IS NULL AND hs.expires_at>clock_timestamp() AND a.disabled_at IS NULL AND NOT a.must_change_password)`, sid, account).Scan(&live)
	if err != nil {
		return err
	}
	if !live {
		return ErrSessionRevoked
	}
	return nil
}
