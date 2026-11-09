package server

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// Restarting uses a conservative fresh activation boundary unless the operator
// supplies a stable cutover. An invalid setting disables absence-based warnings.
func boardCodeCutoverFromEnv(enabledAt time.Time) time.Time {
	return boardCodeCutover(os.Getenv("BLERG_RUNNER_BOARD_CODE_CUTOVER"), enabledAt)
}

func boardCodeCutover(raw string, enabledAt time.Time) time.Time {
	if raw == "" {
		return enabledAt
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if at, err := time.Parse(layout, raw); err == nil {
			return at
		}
	}
	boardCodeFailure("invalid_cutover")
	return time.Time{}
}

type boardCodeCardKey struct {
	board  string
	number int
}

// Explicit board-scoped marks may come from other sessions of the same owner.
// Unscoped historical marks are accepted only from this Main with one board,
// so a numeric collision on another board cannot supply landing evidence.
func (a *API) boardCodeLandings(ctx context.Context, row *db.SessionRow, boards []boardLevelView) (map[boardCodeCardKey]bool, error) {
	if row.SpawningAccountID == nil {
		return nil, errPlanNoBoardRead
	}
	keys := map[string]string{}
	ids := make([]string, 0, len(boards))
	for _, b := range boards {
		ids = append(ids, b.BoardID)
		keys[b.BoardID] = a.planBoardKey(ctx, row, b.BoardID)
	}
	rows, err := a.dbPool.Query(ctx, `SELECT e.payload FROM agent_events e JOIN sessions s ON s.id=e.session_id WHERE s.spawning_account_id=$2 AND e.kind='mark' AND e.payload->>'kind'='milestone' AND (e.session_id=$1 OR e.payload->>'board_id'=ANY($3::text[]))`, row.ID, *row.SpawningAccountID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	landed := map[boardCodeCardKey]bool{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var mark agent.MarkPayload
		if err := json.Unmarshal(raw, &mark); err != nil {
			return nil, err
		}
		addBoardCodeLanding(landed, mark, ids, keys)
	}
	return landed, rows.Err()
}

func addBoardCodeLanding(landed map[boardCodeCardKey]bool, mark agent.MarkPayload, boards []string, keys map[string]string) {
	if mark.Kind != "milestone" {
		return
	}
	words := strings.Fields(mark.Text)
	if len(words) == 0 {
		return
	}
	switch strings.TrimSuffix(strings.ToLower(words[0]), ":") {
	case "merge", "merged", "deployed":
	default:
		return
	}
	board := mark.BoardID
	if board == "" && len(boards) == 1 {
		board = boards[0]
	}
	allowed := false
	for _, id := range boards {
		if id == board {
			allowed = true
			break
		}
	}
	if !allowed {
		return
	}
	if mark.Card > 0 {
		landed[boardCodeCardKey{board, mark.Card}] = true
	}
	for _, n := range cardRefs(mark.Text, keys[board]) {
		landed[boardCodeCardKey{board, n}] = true
	}
}

// Old absence-based notices had no completion boundary. Drop those queued
// notices on upgrade; recheck fresh ones against current card and mark evidence.
func (a *API) heldBoardCodeFact(ctx context.Context, row *db.SessionRow, key string) bool {
	parts := strings.Split(key, ":")
	if len(parts) < 4 || parts[3] != "done_without_main_merge" {
		return true
	}
	if len(parts) != 6 || parts[5] != strconv.FormatInt(a.boardCodeSince.UnixNano(), 10) {
		return false
	}
	number, err := strconv.Atoi(parts[2])
	if err != nil {
		return false
	}
	boards, err := a.boardLevelsFor(ctx, row)
	if err != nil {
		boardCodeError("held_access_failed", err)
		return false
	}
	landed, err := a.boardCodeLandings(ctx, row, boards)
	if err != nil {
		boardCodeError("held_milestone_read_failed", err)
		return false
	}
	a.invalidatePlanCard(row.ID, parts[1], number)
	card, err := a.planReadCard(ctx, row, parts[1], number)
	if err != nil {
		boardCodeError("held_card_read_failed", err)
		return false
	}
	return boardCodeDecision(*card, nil, time.Now(), false, boardCodeEvidence{Since: a.boardCodeSince, RecordedLanding: landed[boardCodeCardKey{parts[1], number}]}).Drift == "done_without_main_merge"
}
