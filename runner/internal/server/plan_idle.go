package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

var idleReleaseLabel = regexp.MustCompile(`^[0-9][a-zA-Z0-9._-]{0,79}$`)

func planIdleText(ready, running, account int, limits childStartLimits, release string) string {
	free := min(limits.parallel-running, accountRunningLimit(limits)-account)
	if ready == 0 || running != 0 || free <= 0 {
		return ""
	}
	// Release names are Board data carried inside trusted metadata.
	if !idleReleaseLabel.MatchString(release) {
		release = quotedName(release)
	}
	return fmt.Sprintf("%d ready cards for %s, %d free worker slots, no workers running: start work or say why not.", ready, release, free)
}

type idleBoardCard struct {
	Tags       []string `json:"tags"`
	Number     int      `json:"number"`
	ColumnID   *string  `json:"column_id"`
	ReleaseID  *string  `json:"release_id"`
	ArchivedAt *string  `json:"archived_at"`
	StuckAt    *string  `json:"stuck_at"`
	Blockers   []struct {
		Satisfied bool `json:"satisfied"`
	} `json:"blockers"`
}

func idleCardReady(c idleBoardCard, columns map[string]bool, release, tag string) bool {
	if c.ColumnID == nil || !columns[*c.ColumnID] || c.ArchivedAt != nil || c.StuckAt != nil {
		return false
	}
	if release != "" {
		if c.ReleaseID == nil || *c.ReleaseID != release {
			return false
		}
	} else if tag == "" || !slices.Contains(c.Tags, tag) {
		return false
	}
	for _, b := range c.Blockers {
		if !b.Satisfied {
			return false
		}
	}
	return true
}

// A turn-local fact deliberately repeats on each completed idle turn, never on a timer.
func (a *API) planIdleWork(ctx context.Context, row *db.SessionRow, seq int64) []planFact {
	return a.planIdleWorkMode(ctx, row, seq, true)
}

func (a *API) planIdleWorkMode(ctx context.Context, row *db.SessionRow, seq int64, report bool) (facts []planFact) {
	outcome := "no_ready_work"
	cause := ""
	ready, running, free := 0, 0, 0
	defer func() {
		if report || cause != "" {
			slog.Info("plan_idle", "operation", "plan_idle", "session_id", row.ID, "outcome", outcome, "ready_cards", ready, "running_children", running, "free_slots", free, "cause", cause)
		}
	}()
	fail := func(stage string, err error) {
		outcome = stage
		cause = failurelog.Cause(err)
	}
	if row.ParentSessionID != nil {
		outcome = "child_session"
		return
	}
	if row.CronID != nil {
		outcome = "cron_session"
		return
	}
	if row.SpawningAccountID == nil {
		fail("account_unavailable", nil)
		return
	}
	board := a.planSessionBoard(ctx, row)
	if board == "" {
		outcome = "no_board"
		return
	}
	var err error
	running, err = db.CountRunningChildren(ctx, a.dbPool, row.ID)
	if err != nil {
		fail("children_read_failed", err)
		return
	}
	if running != 0 {
		outcome = "workers_running"
		return
	}
	account, err := db.CountAccountRunningChildren(ctx, a.dbPool, *row.SpawningAccountID)
	if err != nil {
		fail("account_children_read_failed", err)
		return
	}
	limits := a.effectiveChildLimits(ctx)
	free = min(limits.parallel-running, accountRunningLimit(limits)-account)
	if free <= 0 {
		outcome = "no_capacity"
		return
	}
	call := func(tool string, args map[string]any, out any) bool {
		level, err := a.effectiveBoardLevel(ctx, row, board)
		if err != nil {
			fail("board_access_failed", err)
			return false
		}
		proof, ok := level.proof()
		if !ok || proof.AccountID != *row.SpawningAccountID || !mcpgw.BoardLevelAllows(level.Level, tool) {
			outcome = "no_board_access"
			return false
		}
		caller := a.sessionBoardCaller()
		if caller == nil {
			fail("board_unavailable", nil)
			return false
		}
		args["board_id"] = board
		raw, err := json.Marshal(args)
		if err != nil {
			fail("arguments_invalid", err)
			return false
		}
		raw, err = caller.CallBoard(ctx, mcpgw.BoardCall{Proof: proof, BoardID: board, SessionRef: row.ID, Tool: tool, Arguments: raw, ReadOnly: true})
		if err != nil {
			fail("board_call_failed", err)
			return false
		}
		if e := boardToolResult(raw, out); e != nil {
			fail("board_result_failed", e)
			return false
		}
		return true
	}
	var releases []struct{ ID, Name, Status string }
	if !call("blerg_release_list", map[string]any{}, &releases) {
		return
	}
	releaseID, releaseName := "", ""
	for _, r := range releases {
		if r.Status == "in progress" {
			if releaseID != "" {
				outcome = "ambiguous_active_release"
				return
			}
			releaseID, releaseName = r.ID, r.Name
		}
	}
	releaseTag := ""
	if releaseID == "" {
		name, reason, err := a.planIdleMilestone(ctx, row)
		if err != nil {
			fail("milestone_read_failed", err)
			return
		}
		if name == "" {
			outcome = reason
			return
		}
		releaseName, releaseTag = name, name
	}
	var columns []struct {
		ID, Name string
		Terminal bool `json:"is_terminal"`
	}
	if !call("blerg_column_list", map[string]any{}, &columns) {
		return
	}
	readyColumns := map[string]bool{}
	for _, c := range columns {
		if !c.Terminal && strings.EqualFold(strings.TrimSpace(c.Name), "ready") {
			readyColumns[c.ID] = true
		}
	}
	cursor := ""
	for page := 0; page < 100; page++ {
		var result struct {
			Cards []idleBoardCard `json:"cards"`
			Next  string          `json:"next_cursor"`
		}
		args := map[string]any{"page": true, "summary": true, "limit": 100, "cursor": cursor}
		if releaseID != "" {
			args["release_id"] = releaseID
		} else {
			args["tag"] = releaseTag
		}
		if !call("blerg_card_search", args, &result) {
			return
		}
		for _, c := range result.Cards {
			if !idleCardReady(c, readyColumns, releaseID, releaseTag) {
				continue
			}
			var unavailable bool
			err = a.dbPool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM session_card_links l JOIN sessions s ON s.id=l.session_id WHERE l.board_id=$1 AND l.card_number=$2 AND s.spawning_account_id=$3 AND (s.ended_at IS NULL OR EXISTS(SELECT 1 FROM messages m WHERE m.session_id=s.id AND m.kind='ask' AND m.status='open') OR EXISTS(SELECT 1 FROM review_requests r WHERE r.session_id=s.id AND r.state='pending')))`, board, c.Number, *row.SpawningAccountID).Scan(&unavailable)
			if err != nil {
				fail("assignment_read_failed", err)
				return
			}
			if !unavailable {
				ready++
			}
		}
		if result.Next == "" {
			break
		}
		if result.Next == cursor || page == 99 {
			fail("pagination_incomplete", nil)
			return
		}
		cursor = result.Next
	}
	if text := planIdleText(ready, running, account, limits, releaseName); text != "" {
		outcome = "idle_with_ready_work"
		facts = append(facts, planFact{key: fmt.Sprintf("idle:%d", seq), text: text})
	}
	return
}

// Header milestone order is the plan's order (also used by agentMilestone).
// Resolve by the same workspace membership as agentBlackboardID; never guess among blackboards.
func (a *API) planIdleMilestone(ctx context.Context, row *db.SessionRow) (string, string, error) {
	rows, err := a.dbPool.Query(ctx, `SELECT record FROM blackboards WHERE record->>'workspace_id'=$1 OR id IN (SELECT blackboard_id FROM blackboard_links WHERE workspace_id=$1) ORDER BY id LIMIT 2`, row.ID)
	if err != nil {
		return "", "", err
	}
	defer rows.Close()
	var records []blackboard.Record
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return "", "", err
		}
		var record blackboard.Record
		if err := json.Unmarshal(raw, &record); err != nil {
			return "", "", err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return "", "", err
	}
	if len(records) == 0 {
		return "", "no_workspace_blackboard", nil
	}
	if len(records) > 1 {
		return "", "ambiguous_workspace_blackboard", nil
	}
	for _, m := range records[0].Milestones {
		if m.FrozenAt != nil || m.LegacyFrozenAt != nil || m.ShippedAt != nil || strings.EqualFold(m.Stage, "Final") || strings.EqualFold(m.Stage, "Shipped") {
			continue
		}
		if strings.TrimSpace(m.Name) == "" {
			return "", "milestone_name_missing", nil
		}
		return m.Name, "", nil
	}
	return "", "no_open_milestone", nil
}
