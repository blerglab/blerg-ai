package db

import (
	"context"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
)

// ReadBlackboardCutRecord freezes current durable history under the caller's plan
// transaction. Pending proposals and older frozen bodies are deliberately omitted.
// This archival read is never used by ordinary plan decisions or browser pages.
func ReadBlackboardCutRecord(ctx context.Context, q DBTX, core blackboard.Record) (blackboard.Record, error) {
	out := blackboard.Clone(core)
	out.Proposals = nil
	for i := range out.Milestones {
		out.Milestones[i].Frozen = nil
	}
	var err error
	out.Events, err = readBlackboardRows[blackboard.Event](ctx, q, `SELECT data FROM plan_events WHERE blackboard_id=$1 ORDER BY ordinal`, out.ID)
	if err != nil {
		return out, err
	}
	observations, err := readBlackboardRows[blackboard.Event](ctx, q, `SELECT data FROM blackboard_observations WHERE blackboard_id=$1 ORDER BY scope_id,board_id,card_number`, out.ID)
	if err != nil {
		return out, err
	}
	seen := map[string]bool{}
	for _, event := range out.Events {
		seen[event.ID] = true
	}
	for _, event := range observations {
		if !seen[event.ID] {
			out.Events = append(out.Events, event)
		}
	}
	out.Revisions, err = readBlackboardRows[blackboard.Revision](ctx, q, `SELECT data FROM plan_revisions WHERE blackboard_id=$1 ORDER BY scope_id,revision`, out.ID)
	if err != nil {
		return out, err
	}
	out.Snapshots, err = readBlackboardRows[blackboard.Snapshot](ctx, q, `SELECT data FROM milestone_snapshots WHERE blackboard_id=$1 ORDER BY milestone_id,day`, out.ID)
	return out, err
}
