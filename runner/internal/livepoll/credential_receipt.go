package livepoll

import (
	"context"
	"encoding/json"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
)

// CredentialReceipt atomically authorizes one credential lookup per attempt
// stage. No HTTP gateway or agent route invokes this; only dedicated Core mTLS.
func (a Attempts) CredentialReceipt(ctx context.Context, r ld.AttemptReceipt) error {
	if a.Pool == nil || !r.Scope.Valid() || (r.Stage != "list" && r.Stage != "call") {
		return refuse("credential_receipt_refused")
	}
	scope, err := json.Marshal(r.Scope)
	if err != nil {
		return refuse("credential_receipt_refused")
	}
	tag, err := a.Pool.Exec(ctx, `UPDATE live_mcp_attempts a SET
 listing_credential_at=CASE WHEN $3='list' THEN clock_timestamp() ELSE listing_credential_at END,
 call_credential_at=CASE WHEN $3='call' THEN clock_timestamp() ELSE call_credential_at END
 FROM live_mcp_binding_state b WHERE a.id::text=$1 AND a.scope=$2::jsonb AND a.scope=b.scope AND a.binding_id=b.binding_id
 AND a.revision=b.revision AND a.fence=b.fence AND b.enabled AND b.lease_until>clock_timestamp() AND b.demand_until>clock_timestamp()
 AND a.expires_at>clock_timestamp() AND a.due_at<=clock_timestamp()
 AND a.binding_id::text=$7 AND a.revision=$8 AND a.owner_id::text=$9 AND a.connection_id::text=$10
 AND EXISTS(SELECT 1 FROM live_mcp_authority_epochs e WHERE e.installation_id::text=$4 AND e.runner_id::text=$5 AND e.epoch=$6)
 AND (($3='list' AND a.outcome='undispatched' AND a.dispatched_at IS NULL AND a.listing_credential_at IS NULL)
 OR ($3='call' AND a.outcome='dispatched' AND a.dispatched_at IS NOT NULL AND a.listing_credential_at IS NOT NULL AND a.call_credential_at IS NULL))`, r.Attempt, scope, r.Stage, r.Scope.Installation, r.Scope.Runner, r.Scope.AuthorityEpoch, r.Scope.Binding, r.Scope.Revision, r.Scope.Owner, r.Scope.Connection)
	if err != nil {
		return refuse("credential_receipt_unavailable")
	}
	if tag.RowsAffected() != 1 {
		return refuse("credential_receipt_refused")
	}
	return nil
}
