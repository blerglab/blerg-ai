package blackboard

import "context"

// PreflightMilestones reads release evidence before taking the store's write
// locks. The returned service reuses only this bounded evidence during validation.
// Owner-bound decisions call it before their owning answer transaction as well.
func (s Service) PreflightMilestones(ctx context.Context, r Record, changes []Change) (Service, error) {
	if _, ok := s.Board.(milestoneReleaseSnapshot); ok {
		return s, nil
	}
	snapshot := milestoneReleaseSnapshot{}
	needed := false
	for _, c := range changes {
		if c.Operation != "milestone_add" || c.Milestone == nil {
			continue
		}
		needed = true
		board := c.Milestone.BoardID
		if board == "" {
			boards := ReferencedBoards(r)
			if len(boards) == 1 {
				board = boards[0]
			}
		}
		// Local shape/link validation still runs under the write lock. An invalid
		// explicit board must never cause an external read through the owner's proof.
		if board == "" || !boardLinked(r, "", board) {
			continue
		}
		if _, ok := snapshot[board]; ok {
			continue
		}
		releases, err := s.Releases(ctx, board)
		if err != nil {
			return s, err
		}
		snapshot[board] = append([]Release{}, releases...)
	}
	if needed {
		s.Board = snapshot
	}
	return s, nil
}

type milestoneReleaseSnapshot map[string][]Release

func (b milestoneReleaseSnapshot) Call(_ context.Context, tool string, args map[string]any, out any) error {
	board, _ := args["board_id"].(string)
	releases, found := b[board]
	target, ok := out.(*[]Release)
	if tool != "blerg_release_list" || !found || !ok {
		return CheckFailed("milestone_release_snapshot_missing", "board.release_snapshot", "release evidence unavailable")
	}
	*target = append([]Release{}, releases...)
	return nil
}
