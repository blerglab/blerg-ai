package server

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
)

func TestLegacyCutEvidencePreserved(t *testing.T) {
	for _, mode := range []string{"migration", "read"} {
		for _, stage := range []string{"Final", "Finalizing"} {
			t.Run(mode+"/"+stage, func(t *testing.T) {
				fx := newTopicFx(t)
				at := time.Now().UTC().Truncate(time.Microsecond)
				evidence := json.RawMessage(`{"final_intent":{"request_id":"historical"},"cards":[],"complete":false}`)
				record := blackboard.Record{ID: blackboard.NewID(), Owner: blackboard.Actor{Kind: "human", ID: topicOwner}, WorkspaceID: fx.sess, Version: 1, Milestones: []blackboard.Milestone{{ID: blackboard.NewID(), Name: "Old cut", Stage: stage, FrozenAt: &at, Frozen: evidence, Version: 1}}}
				if err := fx.api.blackboardStore().Create(t.Context(), record); err != nil {
					t.Fatal(err)
				}
				if mode == "migration" {
					migration, err := os.ReadFile("../db/migrations/150_bound_release_cuts.sql")
					if err != nil {
						t.Fatal(err)
					}
					sql := string(migration)
					start := strings.Index(sql, "WITH legacy AS (")
					if start < 0 {
						t.Fatal("legacy migration statement missing")
					}
					sql = sql[start:]
					if _, err = fx.pool.Exec(t.Context(), sql); err != nil {
						t.Fatal(err)
					}
				} else {
					fx.api.reconcileBlackboardCuts(t.Context(), record)
					fx.api.reconcileBlackboardCuts(t.Context(), record)
				}
				got, err := fx.api.blackboardStore().Load(t.Context(), record.ID)
				if err != nil {
					t.Fatal(err)
				}
				m := got.Milestones[0]
				if m.Stage != "Evidence incomplete" || !m.EvidenceIncomplete || m.CutReceiptID != "" || m.ShippedAt != nil || m.LegacyEvidenceID == 0 || m.LegacyFrozenAt == nil || !m.LegacyFrozenAt.Equal(at) || (stage == "Finalizing" && m.FrozenAt != nil) || (stage == "Final" && m.FrozenAt == nil) {
					t.Fatalf("legacy cut: %+v", m)
				}
				var count, events int
				var preserved []byte
				if err = fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM blackboard_cut_legacy_evidence WHERE milestone_id=$1`, m.ID).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if err = fx.pool.QueryRow(t.Context(), `SELECT snapshot->'frozen' FROM blackboard_cut_legacy_evidence WHERE id=$1`, m.LegacyEvidenceID).Scan(&preserved); err != nil {
					t.Fatal(err)
				}
				_ = fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM plan_events WHERE milestone_id=$1 AND kind='legacy_cut_evidence_incomplete' AND source='derived'`, m.ID).Scan(&events)
				var originalValue, preservedValue any
				_ = json.Unmarshal(evidence, &originalValue)
				_ = json.Unmarshal(preserved, &preservedValue)
				originalJSON, _ := json.Marshal(originalValue)
				preservedJSON, _ := json.Marshal(preservedValue)
				if count != 1 || events != 1 || string(originalJSON) != string(preservedJSON) {
					t.Fatalf("lost/duplicated evidence: count=%d events=%d preserved=%s", count, events, preserved)
				}
				if _, err = fx.pool.Exec(t.Context(), `UPDATE blackboard_cut_legacy_evidence SET snapshot='{}' WHERE id=$1`, m.LegacyEvidenceID); err == nil {
					t.Fatal("historical evidence mutable")
				}
				if err = fx.api.blackboardStore().UpdatePartition(t.Context(), record.ID, blackboard.Partition{Kind: blackboard.PartitionPlan, MilestoneID: m.ID}, func(r *blackboard.Record) error { r.Milestones[0].Name = "New name"; return nil }); err != nil {
					t.Fatal(err)
				}
				got, err = fx.api.blackboardStore().Load(t.Context(), record.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(got.Milestones[0].Frozen) == 0 {
					t.Fatal("ordinary plan write erased preserved snapshot")
				}
			})
		}
	}
}
