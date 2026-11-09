package server

import (
	"context"
	"encoding/json"
	"errors"

	ld "github.com/blerglab/blerg-ai/contracts/livedata"
	"github.com/blerglab/blerg-ai/runner/internal/livepoll"
)

func liveReceiptMatches(got ld.DecisionReceipt, want ld.Decision, status string) bool {
	return got.BindingID == want.BindingID && got.Revision == want.Revision && got.Digest == want.Digest && got.AskID == want.AskID && got.Status == status
}
func (a *API) liveDecision(ctx context.Context, path string, decision ld.Decision, status string) error {
	var receipt ld.DecisionReceipt
	err := a.liveCore(ctx, path, decision, &receipt)
	if err == nil && liveReceiptMatches(receipt, decision, status) {
		return nil
	}
	var failure *boundAskError
	if errors.As(err, &failure) && failure.cause == "fresh_login_required" {
		return err
	}
	// A lost reply is an unknown outcome. Look up exactly this subject; never
	// synthesize a fresh approval, substitute another ask, or assume success.
	decision.AccessToken = ""
	if lookup := a.liveCore(ctx, "/internal/live-data/receipt", decision, &receipt); lookup != nil {
		return errOrRefusal(err)
	}
	if !liveReceiptMatches(receipt, decision, status) {
		return boundFailure("live_data_decision_refused")
	}
	return nil
}
func errOrRefusal(err error) error {
	if err != nil {
		return err
	}
	return boundFailure("live_data_decision_refused")
}

func (a *API) reconcileLiveDecisions(ctx context.Context, only ...string) error {
	selected := ""
	if len(only) == 1 {
		selected = only[0]
	}
	rows, err := a.dbPool.Query(ctx, `SELECT ask_id::text,body,operation FROM live_mcp_decision_outbox WHERE NOT delivered AND ($1='' OR ask_id::text=$1) ORDER BY CASE WHEN operation='cancel' THEN 0 ELSE 1 END,created_at LIMIT 100`, selected)
	if err != nil {
		return boundFailure("live_outbox_unavailable")
	}
	type item struct {
		id        string
		body      ld.Decision
		operation string
	}
	var items []item
	for rows.Next() {
		var i item
		var raw []byte
		if err = rows.Scan(&i.id, &raw, &i.operation); err != nil {
			rows.Close()
			return boundFailure("live_outbox_unavailable")
		}
		if json.Unmarshal(raw, &i.body) != nil {
			rows.Close()
			return boundFailure("invalid_subject")
		}
		items = append(items, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return boundFailure("live_outbox_unavailable")
	}
	var resultErr error
	for _, i := range items {
		var receipt ld.DecisionReceipt
		if err = a.liveCore(ctx, "/internal/live-data/"+i.operation, i.body, &receipt); err != nil {
			var refusal *boundAskError
			if i.operation != "confirm" || !errors.As(err, &refusal) || refusal.cause != "decision_revoked" {
				resultErr = err
				continue
			}
			// Core's typed refusal is terminal and already logs its safe cause.
			// The operation predicate below preserves a concurrent cancellation.
		} else {
			status := "approved"
			if i.operation == "cancel" {
				status = "declined"
			}
			if !liveReceiptMatches(receipt, i.body, status) {
				resultErr = boundFailure("live_data_decision_refused")
				continue
			}
		}
		if i.operation == "confirm" && receipt.Status == "approved" {
			var raw []byte
			var subject boundSubject
			var workspace string
			if err = a.dbPool.QueryRow(ctx, `SELECT subject,workspace_id::text FROM bound_asks WHERE message_id=$1 AND status='committed'`, i.id).Scan(&raw, &workspace); err != nil || json.Unmarshal(raw, &subject) != nil || subject.LiveBinding == nil {
				resultErr = boundFailure("live_activation_unavailable")
				continue
			}
			if err = (livepoll.Attempts{Pool: a.dbPool}).Activate(ctx, *subject.LiveBinding, workspace); err != nil {
				resultErr = err
				continue
			}
		} else if i.operation == "cancel" || receipt.Status != "approved" {
			if _, err = a.dbPool.Exec(ctx, `UPDATE live_mcp_binding_state SET enabled=false,fence=fence+1,last_payload=NULL,payload_at=NULL,cause='authority_revoked' WHERE binding_id::text=$1 AND revision=$2`, i.body.BindingID, i.body.Revision); err != nil {
				resultErr = boundFailure("live_activation_unavailable")
				continue
			}
		}
		if _, err = a.dbPool.Exec(ctx, `UPDATE live_mcp_decision_outbox SET delivered=true WHERE ask_id=$1 AND operation=$2`, i.id, i.operation); err != nil {
			resultErr = boundFailure("live_outbox_unavailable")
		}
	}
	return resultErr
}
