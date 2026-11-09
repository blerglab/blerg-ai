package server

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

func releaseTagTarget(tags []string, releases []blackboard.Release) (string, bool) {
	tag, ambiguous := blackboard.ReleaseTag(tags)
	if ambiguous {
		return "", true
	}
	target := ""
	for _, release := range releases {
		if release.Name == tag {
			if target != "" && target != release.ID {
				return "", true
			}
			target = release.ID
		}
	}
	return target, false
}
func terminalColumnValid(columns []blackboard.Column) bool {
	count := 0
	for _, col := range columns {
		if col.Terminal {
			count++
		}
	}
	if count != 1 {
		return false
	}
	for _, col := range columns {
		if strings.EqualFold(strings.TrimSpace(col.Name), "done") && !col.Terminal {
			return false
		}
	}
	return true
}

// Structure checks do not depend on a daemon or Git binding. Read permission
// yields drift; each attempted correction rechecks current write authority.
func (a *API) reconcileBoardInvariants(ctx context.Context, row *db.SessionRow) (facts []planFact) {
	fail := func(stage string, err error) {
		boardCodeError(stage, err)
		facts = append(facts, planFact{key: "invariant:failure:" + stage, text: "Board invariant check failed: " + strings.ReplaceAll(stage, "_", " ") + "; inspect `blerg-runner board access` and runner diagnostics."})
	}
	if row.ParentSessionID != nil || row.SpawningAccountID == nil {
		return facts
	}
	boards, err := a.boardLevelsFor(ctx, row)
	if err != nil {
		fail("invariant_access_failed", err)
		return facts
	}
	caller := a.sessionBoardCaller()
	if caller == nil {
		return facts
	}
	facts = []planFact{}
	records, err := a.invariantPlans(ctx, row)
	if err != nil {
		fail("invariant_plan_read_failed", err)
	}
	detailStart := a.structureDetails.start(row.ID)
	detailCandidates := 0
	defer func() { a.structureDetails.finish(row.ID, detailStart, detailCandidates) }()
	admitDetail := func() bool {
		i := detailCandidates
		detailCandidates++
		return i >= detailStart && i < detailStart+boardDetailLimit
	}
	unplanned := map[string][]int{}
	milestoneNames := map[string]string{}
	for _, board := range boards {
		call := boardCodeCall(func(ctx context.Context, tool string, args map[string]any, out any) error {
			level, err := a.effectiveBoardLevel(ctx, row, board.BoardID)
			if err != nil {
				return err
			}
			proof, ok := level.proof()
			if !ok || proof.AccountID != *row.SpawningAccountID || !mcpgw.BoardLevelAllows(level.Level, tool) {
				return errPlanNoBoardRead
			}
			data, err := json.Marshal(args)
			if err != nil {
				return err
			}
			raw, err := boardReconcileCall(ctx, caller, mcpgw.BoardCall{Proof: proof, SessionRef: row.ID, BoardID: board.BoardID, Tool: tool, Arguments: data, ReadOnly: mcpgw.BoardLevelAllows(db.BoardLevelRead, tool)})
			if err != nil {
				return err
			}
			if e := boardToolResult(raw, out); e != nil {
				return e
			}
			return nil
		})
		var columns []blackboard.Column
		if err := call(ctx, "blerg_column_list", map[string]any{"board_id": board.BoardID}, &columns); err != nil {
			fail("invariant_columns_failed", err)
			continue
		}
		if !terminalColumnValid(columns) {
			facts = append(facts, planFact{key: "invariant:terminal:" + board.BoardID, text: fmt.Sprintf("Board %s breaks invariant 4: mark exactly one Done column terminal; inspect with `blerg-runner board show` and use `blerg-runner column set <id> --terminal` after choosing Done.", board.BoardID)})
		}
		var releases []blackboard.Release
		if err := call(ctx, "blerg_release_list", map[string]any{"board_id": board.BoardID}, &releases); err != nil {
			fail("invariant_releases_failed", err)
			continue
		}
		cursor := ""
		seen := map[string]bool{}
		for page := 0; page < 100; page++ {
			var result struct {
				Cards []liveBoardCard `json:"cards"`
				Next  string          `json:"next_cursor"`
			}
			if err := call(ctx, "blerg_card_search", map[string]any{"board_id": board.BoardID, "page": true, "summary": true, "limit": 100, "cursor": cursor}, &result); err != nil {
				fail("invariant_search_failed", err)
				break
			}
			for _, card := range result.Cards {
				if card.BoardID != board.BoardID || card.Number <= 0 {
					boardCodeFailure("invariant_card_scope")
					continue
				}
				for _, record := range records {
					for _, m := range record.Milestones {
						if m.FrozenAt != nil || m.Stage == "Finalizing" || m.Stage == "Final" {
							continue
						}
						var member *blackboard.Item
						for i := range record.Items {
							it := &record.Items[i]
							if it.MilestoneID == m.ID && it.BoardID == board.BoardID && it.CardNumber == card.Number {
								member = it
								break
							}
						}
						tag, ambiguous := blackboard.ReleaseTag(card.Tags)
						if ambiguous || tag == "" {
							continue
						}
						milestoneTag, _ := blackboard.ReleaseTag([]string{m.Name})
						if member != nil && member.State != "moved_out" && milestoneTag != "" && tag != m.Name && admitDetail() {
							text := fmt.Sprintf("Automatic reconciliation (invariant 3): milestone %s membership observed moved_out because release tag changed from %s to %s. Restore the original tag to reverse this observation.", m.ID, m.Name, tag)
							if err := recordReleaseTagObservation(ctx, call, card, text); err != nil {
								fail("invariant_tag_evidence_failed", err)
								facts = append(facts, planFact{key: fmt.Sprintf("invariant:tag_evidence:%s:%d", board.BoardID, card.Number), text: fmt.Sprintf("#%d breaks invariant 3: release tag changed; inspect with `blerg-runner ticket get %d` and retry after restoring write access.", card.Number, card.Number)})
							}
						}
						if member == nil && tag == m.Name {
							unplanned[m.ID] = append(unplanned[m.ID], card.Number)
							milestoneNames[m.ID] = m.Name
						}
					}
				}
				target, ambiguous := releaseTagTarget(card.Tags, releases)
				if ambiguous {
					facts = append(facts, planFact{key: fmt.Sprintf("invariant:tags:%s:%d", board.BoardID, card.Number), text: fmt.Sprintf("#%d breaks invariant 3: conflicting release tags; resolve with `blerg-runner ticket update %d --rm-tag <unwanted-tag>`.", card.Number, card.Number)})
					continue
				}
				if target == "" || card.ReleaseID == nil || *card.ReleaseID == target {
					continue
				}
				if !admitDetail() {
					continue
				}
				if err := reconcileReleaseTag(ctx, call, card, releases); err != nil {
					fail("invariant_release_fix_failed", err)
					facts = append(facts, planFact{key: fmt.Sprintf("invariant:release:%s:%d", board.BoardID, card.Number), text: fmt.Sprintf("#%d breaks invariant 3: release tag differs from membership; inspect with `blerg-runner ticket get %d` and reconcile its release.", card.Number, card.Number)})
				}
			}
			if result.Next == "" {
				break
			}
			if seen[result.Next] || page == 99 {
				boardCodeFailure("invariant_page_limit")
				break
			}
			seen[result.Next] = true
			cursor = result.Next
		}
	}
	mids := make([]string, 0, len(unplanned))
	for mid := range unplanned {
		mids = append(mids, mid)
	}
	slices.Sort(mids)
	for _, mid := range mids {
		facts = append(facts, unplannedInvariantFact(mid, milestoneNames[mid], unplanned[mid]))
	}

	return facts
}

func reconcileReleaseTag(ctx context.Context, call boardCodeCall, c liveBoardCard, releases []blackboard.Release) error {
	target, ambiguous := releaseTagTarget(c.Tags, releases)
	if ambiguous || target == "" || c.ReleaseID == nil || *c.ReleaseID == target {
		return nil
	}
	text := fmt.Sprintf("Automatic reconciliation attempt (invariant 3): release tag selects %s; previous release %s; card version %d. Release membership can be restored from this evidence.", target, *c.ReleaseID, c.Version)
	if err := call(ctx, "blerg_card_comment", map[string]any{"board_id": c.BoardID, "number": c.Number, "text": text}, nil); err != nil {
		return err
	}
	var fresh liveBoardCard
	if err := call(ctx, "blerg_card_get", map[string]any{"board_id": c.BoardID, "number": c.Number}, &fresh); err != nil {
		return err
	}
	if fresh.BoardID != c.BoardID || fresh.Number != c.Number || !slices.Equal(fresh.Tags, c.Tags) || fresh.ReleaseID == nil || *fresh.ReleaseID != *c.ReleaseID {
		return errCodeCardScopeChanged
	}
	return call(ctx, "blerg_card_update", map[string]any{"board_id": c.BoardID, "number": c.Number, "if_match": fresh.Version, "release_id": target}, nil)
}

func (a *API) invariantPlans(ctx context.Context, row *db.SessionRow) ([]blackboard.Record, error) {
	rows, err := a.dbPool.Query(ctx, `SELECT id FROM blackboards WHERE owner_id=$2 AND (record->>'workspace_id'=$1 OR id IN (SELECT blackboard_id FROM blackboard_links WHERE workspace_id=$1)) ORDER BY id LIMIT 100`, row.ID, *row.SpawningAccountID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	records := []blackboard.Record{}
	for _, id := range ids {
		record, err := a.blackboardStore().LoadPartition(ctx, id, blackboard.Partition{Kind: blackboard.PartitionCore})
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

func recordReleaseTagObservation(ctx context.Context, call boardCodeCall, c liveBoardCard, text string) error {
	var fresh liveBoardCard
	if err := call(ctx, "blerg_card_get", map[string]any{"board_id": c.BoardID, "number": c.Number, "with_trail": true}, &fresh); err != nil {
		return err
	}
	if fresh.BoardID != c.BoardID || fresh.Number != c.Number || !slices.Equal(fresh.Tags, c.Tags) {
		return errCodeCardScopeChanged
	}
	for _, event := range fresh.Trail {
		if event.Type == "comment" && event.Text == text {
			return nil
		}
	}
	return call(ctx, "blerg_card_comment", map[string]any{"board_id": c.BoardID, "number": c.Number, "text": text}, nil)
}

func invariantMutation(tool string) bool {
	switch tool {
	case "blerg_card_create", "blerg_card_update", "blerg_card_move", "blerg_card_archive", "blerg_column_update", "blerg_column_create", "blerg_column_delete":
		return true
	}
	return false
}

func (a *API) checkBoardEventInvariants(parent context.Context, sid string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), planCheckTimeout)
	defer cancel()
	row, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil {
		boardCodeError("invariant_event_session_failed", err)
		return
	}
	if row == nil {
		return
	}
	root, err := sessionWorkspaceRoot(ctx, a.dbPool, row)
	if err != nil {
		boardCodeError("invariant_event_root_failed", err)
		return
	}
	if root != row.ID {
		row, err = db.GetSession(ctx, a.dbPool, root)
		if err != nil {
			boardCodeError("invariant_event_main_failed", err)
			return
		}
		if row == nil {
			return
		}
	}
	ctx = boardReconcileContext(ctx)
	facts := a.reconcileBoardInvariants(ctx, row)
	facts = append(facts, a.reconcileBoardCode(ctx, row, time.Now())...)
	for _, fact := range facts {
		if err := db.AddPlanFact(ctx, a.dbPool, row.ID, fact.key, fact.text); err != nil {
			boardCodeError("invariant_event_fact_failed", err)
		}
	}
}

func unplannedInvariantFact(mid, name string, numbers []int) planFact {
	numbers = slices.Clone(numbers)
	slices.Sort(numbers)
	numbers = slices.Compact(numbers)
	keys := []string{}
	for _, n := range numbers[:min(5, len(numbers))] {
		keys = append(keys, "#"+strconv.Itoa(n))
	}
	return planFact{key: "invariant:unplanned:" + mid, text: fmt.Sprintf("%d unplanned cards for milestone %s (invariant 3): %s; review with `blerg-runner ticket list --tag %s`.", len(numbers), name, strings.Join(keys, ", "), name)}
}

// parent is the board tool's request context; the check outlives the request, so cancellation is dropped.
func (a *API) scheduleBoardEventInvariants(parent context.Context, sid string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), planCheckTimeout)
	defer cancel()
	row, err := db.GetSession(ctx, a.dbPool, sid)
	if err != nil {
		boardCodeError("invariant_schedule_session_failed", err)
		return
	}
	if row == nil {
		return
	}
	root, err := sessionWorkspaceRoot(ctx, a.dbPool, row)
	if err != nil {
		boardCodeError("invariant_schedule_root_failed", err)
		return
	}
	a.invariantEvents.enqueue(root, func(delay time.Duration, f func()) { time.AfterFunc(delay, f) }, func() { a.checkBoardEventInvariants(context.WithoutCancel(parent), root) })
}
