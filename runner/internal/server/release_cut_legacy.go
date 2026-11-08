package server

import (
	"context"
	"encoding/json"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// No agent needs an Abort control to release an old transient intent. Preserve
// the exact old snapshot separately before clearing its lock, in one transaction.
func (a *API) clearLegacyCutLocks(ctx context.Context, record blackboard.Record) {
	for _, legacy := range record.Milestones {
		if legacy.CutReceiptID != "" || (legacy.Stage != "Final" && legacy.Stage != "Finalizing") {
			continue
		}
		tx, err := a.dbPool.Begin(ctx)
		if err != nil {
			cutDiagnostic("legacy_cut_clear_unavailable")
			continue
		}
		err = (db.BlackboardStore{Pool: a.dbPool, Tx: tx}).UpdatePartition(ctx, record.ID, blackboard.Partition{Kind: blackboard.PartitionPlan, MilestoneID: legacy.ID}, func(r *blackboard.Record) error {
			for i := range r.Milestones {
				m := &r.Milestones[i]
				if m.ID != legacy.ID || m.CutReceiptID != "" || (m.Stage != "Final" && m.Stage != "Finalizing") {
					continue
				}
				var id int64
				if err := tx.QueryRow(ctx, `INSERT INTO blackboard_cut_legacy_evidence(blackboard_id,milestone_id,snapshot) SELECT blackboard_id,id,data FROM milestones WHERE blackboard_id=$1 AND id=$2 RETURNING id`, r.ID, m.ID).Scan(&id); err != nil {
					return err
				}
				previous := m.Stage
				m.LegacyEvidenceID = id
				m.LegacyFrozenAt = m.FrozenAt
				if previous == "Finalizing" {
					m.FrozenAt = nil
				}
				m.Stage = "Evidence incomplete"
				m.EvidenceIncomplete = true
				m.Version++
				payload, _ := json.Marshal(map[string]any{"previous_stage": previous, "legacy_evidence_id": id, "intent_preserved": true})
				r.Events = append(r.Events, blackboard.Event{ID: blackboard.NewID(), MilestoneID: m.ID, AuthenticatedActor: blackboard.Actor{Kind: "system", ID: "runner"}, RecordedAt: time.Now(), Source: "derived", Kind: "legacy_cut_evidence_incomplete", Payload: payload})
			}
			return nil
		})
		if err == nil {
			err = tx.Commit(ctx)
		}
		if err != nil {
			cutDiagnostic("legacy_cut_clear_unavailable")
		}
		_ = tx.Rollback(ctx)
	}
}
