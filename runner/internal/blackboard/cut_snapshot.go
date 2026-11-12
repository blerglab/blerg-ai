package blackboard

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/blerglab/blerg-ai/contracts/releasecut"
)

// FreezeCutSnapshot combines two explicitly different observations: the current
// runner plan and the board's immutable pre-cut cards. It never reconstructs gaps.
func FreezeCutSnapshot(r Record, mid string, receipt releasecut.Receipt, at time.Time) (SliceView, error) {
	var m Milestone
	found := false
	for _, v := range r.Milestones {
		if v.ID == mid {
			m = v
			found = true
		}
	}
	if !found {
		return SliceView{}, ErrNotFound
	}
	snapshot := Clone(r)
	snapshot.Proposals = nil
	for i := range snapshot.Milestones {
		snapshot.Milestones[i].Frozen = nil
	}
	view := SliceView{Milestone: m, Cards: []SliceCard{}, Counts: map[string]int{}, ObservedAt: at, Complete: true, Frozen: true, RunnerRecord: &snapshot, BoardCut: &receipt}
	view.Milestone.Frozen = nil
	seen := map[string]bool{}
	for _, entry := range receipt.Snapshot {
		var card Card
		if err := json.Unmarshal(entry.Card, &card); err != nil {
			return view, err
		}
		key := fmt.Sprintf("%s:%d", card.BoardID, card.Number)
		seen[key] = true
		member := Item{BoardID: card.BoardID, CardNumber: card.Number, MilestoneID: mid, State: "unplanned"}
		for _, item := range r.Items {
			if item.MilestoneID == mid && item.BoardID == card.BoardID && item.CardNumber == card.Number {
				member = item
				break
			}
		}
		stage := "done"
		baseline := card
		baseline.Version++
		if entry.Terminal {
			stamp := receipt.ReleasedAt.UTC().Format(time.RFC3339Nano)
			baseline.ArchivedAt = &stamp
			baseline.ColumnID = nil
		} else {
			stage = "moved_out"
			next := receipt.NextReleaseID
			baseline.ReleaseID = &next
			member.State = "moved_out"
			member.Reason = "Release cut"
			member.Destination = receipt.NextReleaseName
			if member.Destination == "" {
				member.Destination = receipt.NextReleaseID
			}
		}
		view.Cards = append(view.Cards, SliceCard{Card: card, Membership: member, Stage: stage, Baseline: &baseline, NeedsReason: member.Reason == ""})
	}
	for _, item := range r.Items {
		if item.MilestoneID != mid {
			continue
		}
		if item.BoardID == "" {
			stage := item.Progress
			if stage == "" {
				stage = "next"
			}
			if item.State == "moved_out" {
				stage = "moved_out"
			}
			view.Cards = append(view.Cards, SliceCard{Card: Card{ID: string(item.ID), Title: item.Title}, Membership: item, Stage: stage})
			continue
		}
		if !seen[fmt.Sprintf("%s:%d", item.BoardID, item.CardNumber)] {
			// A missing observation is not a claim that this card was deleted or shipped.
			view.Complete = false
			view.Cards = append(view.Cards, SliceCard{Card: Card{BoardID: item.BoardID, Number: item.CardNumber}, Membership: item, Stage: "moved_out", NeedsReason: true})
		}
	}
	countSlice(&view)
	return view, nil
}
