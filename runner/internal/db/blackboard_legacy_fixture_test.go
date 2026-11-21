package db_test

import (
	"context"
	"encoding/json"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
)

// Historical snapshot fixtures keep DB isolation/budget tests independent from
// retired consent controls. Actual cut authority is exercised in server processes.
func seedDBLegacyCut(ctx context.Context, s blackboard.Service, mid string, clearItems bool) error {
	store := s.Store.(blackboard.PartitionStore)
	return store.UpdatePartition(ctx, s.BlackboardID, blackboard.Partition{Kind: blackboard.PartitionPlan, MilestoneID: mid}, func(r *blackboard.Record) error {
		for i := range r.Milestones {
			m := &r.Milestones[i]
			if m.ID != mid {
				continue
			}
			m.Stage = "Evidence incomplete"
			m.EvidenceIncomplete = true
			m.Version++
			if clearItems {
				m.FrozenAt = nil
			} else {
				at := time.Now()
				m.FrozenAt = &at
				m.Frozen = json.RawMessage(`{"cards":[],"complete":false}`)
			}
		}
		return nil
	})
}
