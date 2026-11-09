package api

import (
	"context"
	"net/http"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
)

// Receipt, confirm and cancel are exact-subject operations for the authenticated
// Runner. They cannot approve, extend, remap or mint a grant. Confirm is sent only
// from the Runner's committed decision outbox; cancel always wins over confirm.
func (d Deps) handleLiveReconcile(w http.ResponseWriter, r *http.Request) {
	if !d.liveComponent(w, r) {
		return
	}
	var body ld.Decision
	if liveDecode(w, r, &body) != nil || !uuidShape.MatchString(body.AskID) {
		d.liveFail(w, r, 400, "invalid_receipt", "approve")
		return
	}
	runner, err := d.liveRunner(r)
	if err != nil {
		d.liveFail(w, r, 403, "runner_identity_required", "approve")
		return
	}
	tx, err := d.Store.Pool().Begin(r.Context())
	if err != nil {
		d.liveFail(w, r, 503, "store_unavailable", "approve")
		return
	}
	defer func(ctx context.Context) { _ = tx.Rollback(ctx) }(r.Context())
	proposal, err := readLiveProposal(r.Context(), tx, body.AccountID, body.BindingID, body.Revision, true, true)
	if err != nil || proposal.AskID != body.AskID || proposal.Digest != body.Digest || proposal.Configuration.Scope.Installation != runner.Installation || proposal.Configuration.Scope.Runner != runner.Runner {
		d.liveFail(w, r, 404, "receipt_mismatch", "approve")
		return
	}
	switch r.URL.Path {
	case "/internal/live-data/cancel":
		_, err = tx.Exec(r.Context(), `UPDATE live_data_grants SET revoked_at=coalesce(revoked_at,clock_timestamp()) WHERE binding_id=$1 AND revision=$2`, body.BindingID, body.Revision)
		if err == nil {
			_, err = tx.Exec(r.Context(), `UPDATE live_data_proposals SET status='declined' WHERE binding_id=$1 AND revision=$2`, body.BindingID, body.Revision)
		}
		proposal.Status = "declined"
	case "/internal/live-data/confirm":
		if proposal.Status != "approved" {
			d.liveFail(w, r, 409, "decision_revoked", "approve")
			return
		}
		tag, e := tx.Exec(r.Context(), `UPDATE live_data_grants SET runner_committed=true WHERE binding_id=$1 AND revision=$2 AND revoked_at IS NULL`, body.BindingID, body.Revision)
		err = e
		if err == nil && tag.RowsAffected() != 1 {
			d.liveFail(w, r, 409, "decision_revoked", "approve")
			return
		}
	}
	if err != nil {
		d.liveFail(w, r, 503, "reconcile_unavailable", "approve")
		return
	}
	operation := "approve"
	if r.URL.Path == "/internal/live-data/cancel" {
		operation = "decline"
	}
	if r.URL.Path != "/internal/live-data/receipt" {
		_, err = tx.Exec(r.Context(), `INSERT INTO live_data_approval_audit(owner_id,binding_id,revision,digest,ask_id,operation,outcome)VALUES($1,$2,$3,$4,$5,$6,'ok')`, body.AccountID, body.BindingID, body.Revision, body.Digest, body.AskID, operation)
	}
	if err != nil || tx.Commit(r.Context()) != nil {
		d.liveFail(w, r, 503, "reconcile_commit_failed", "approve")
		return
	}
	writeJSON(w, ld.DecisionReceipt{BindingID: body.BindingID, Revision: body.Revision, Digest: body.Digest, AskID: body.AskID, Status: proposal.Status})
}
