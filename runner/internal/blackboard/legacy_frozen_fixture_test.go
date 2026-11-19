package blackboard

import (
	"context"
	"encoding/json"
)

// These are historical reader fixtures, not a new approval or cut path. New cuts
// are tested through real runner/board processes. Old snapshots remain readable.
func seedLegacyFrozenView(ctx context.Context, s Service, mid, next string) error {
	view, err := s.slice(ctx, mid, false)
	if err != nil {
		return err
	}
	m := view.Milestone
	if s.Board != nil && m.BoardID != "" && m.ReleaseID != "" {
		var out any
		if err = s.Board.Call(ctx, "blerg_release_close", map[string]any{"board_id": m.BoardID, "release_id": m.ReleaseID, "next_release_id": next}, &out); err != nil {
			return err
		}
		for i := range view.Cards {
			item := &view.Cards[i]
			if item.Membership.BoardID == "" || item.Membership.State == "moved_out" {
				continue
			}
			var live Card
			if err = s.Board.Call(ctx, "blerg_card_get", map[string]any{"board_id": item.Membership.BoardID, "number": item.Card.Number}, &live); err != nil {
				continue
			}
			baseline := item.Card
			baseline.ReleaseID = live.ReleaseID
			baseline.ArchivedAt = live.ArchivedAt
			if item.Stage == "done" {
				baseline.ColumnID = live.ColumnID
			}
			item.Baseline = &baseline
			if item.Card.BoardID == m.BoardID && (live.ReleaseID == nil || *live.ReleaseID != m.ReleaseID) {
				item.Membership.State = "moved_out"
				item.Membership.Reason = "Historical cut"
				item.Stage = "moved_out"
			}
		}
	}
	view.Counts = map[string]int{}
	countSlice(&view)
	return s.cutUpdate(ctx, mid, func(r *Record) error {
		m, err := milestone(r, mid)
		if err != nil {
			return err
		}
		at := s.now()
		m.Frozen, _ = json.Marshal(view)
		m.FrozenAt = &at
		m.Stage = "Evidence incomplete"
		m.EvidenceIncomplete = true
		m.Version++
		return nil
	})
}
func seedClearedLegacyLock(ctx context.Context, s Service, mid string) error {
	return s.cutUpdate(ctx, mid, func(r *Record) error {
		m, err := milestone(r, mid)
		if err != nil {
			return err
		}
		m.LegacyFrozenAt = m.FrozenAt
		m.FrozenAt = nil
		m.Stage = "Evidence incomplete"
		m.EvidenceIncomplete = true
		m.Version++
		return nil
	})
}
