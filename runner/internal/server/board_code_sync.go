package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/checklist"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func boardCodeFailure(cause string) {
	failurelog.Default.Event("board_code:"+cause, slog.LevelWarn, "component", "runner", "operation", "board_code", "cause", cause)
}

var errCodeCardScopeChanged = errors.New("card scope changed during reconciliation")

type boardCodeCall func(context.Context, string, map[string]any, any) error

// Reconciliation uses the same version guards and board authority as ordinary
// session tools. A read grant can flag drift but never becomes write authority.
func reconcileCodeCard(ctx context.Context, call boardCodeCall, c liveBoardCard, history []protocol.CodeCommit, now time.Time, running, write bool, evidence ...boardCodeEvidence) (boardCodeAction, error) {
	d := boardCodeDecision(c, history, now, running, evidence...)
	if write && d.Drift == "partial_merge_preserved" {
		text := fmt.Sprintf("Automatic reconciliation: column and tags preserved for BLG-%d; partial or nonstandard merge titles are activity, not completion evidence. Completion requires a first-parent Merge BLG-N: title (or bare Merge BLG-N).", c.Number)
		// The trail prevents repeating the same explanation every Main turn.
		for _, event := range c.Trail {
			if event.Type == "comment" && event.Text == text {
				return d, nil
			}
		}
		return d, call(ctx, "blerg_card_comment", map[string]any{"board_id": c.BoardID, "number": c.Number, "text": text}, nil)
	}
	if !write || d.Column == "" && !d.Tag {
		return d, nil
	}
	args := func() map[string]any {
		return map[string]any{"board_id": c.BoardID, "number": c.Number, "if_match": c.Version}
	}
	// Record the cause before attempting the version-guarded write, so even a
	// lost response cannot leave an unexplained automatic move on the card.
	text := fmt.Sprintf("Automatic reconciliation attempt: main merge %s; move to done=%t, deployed to-test=%t. Writes use card version %d.", d.Commit, d.Column != "", d.Tag, c.Version)
	if err := call(ctx, "blerg_card_comment", map[string]any{"board_id": c.BoardID, "number": c.Number, "text": text}, nil); err != nil {
		return d, err
	}
	// Comments may bump the card's version. Always reread before a write and
	// re-evaluate manual moves that raced the comment.
	var fresh liveBoardCard
	if err := call(ctx, "blerg_card_get", map[string]any{"board_id": c.BoardID, "number": c.Number, "with_trail": true}, &fresh); err != nil {
		return d, err
	}
	if fresh.Number != c.Number || fresh.BoardID != c.BoardID || !slices.Equal(fresh.Repos, c.Repos) {
		return d, errCodeCardScopeChanged
	}
	d = boardCodeDecision(fresh, history, now, running, evidence...)
	c = fresh
	if d.Column != "" {
		a := args()
		a["column_id"] = d.Column
		if err := call(ctx, "blerg_card_move", a, nil); err != nil {
			return d, err
		}
		before := c
		if err := call(ctx, "blerg_card_get", map[string]any{"board_id": c.BoardID, "number": c.Number, "with_trail": true}, &c); err != nil {
			return d, err
		}
		if before.Number != c.Number || before.BoardID != c.BoardID || !slices.Equal(before.Repos, c.Repos) {
			return d, errCodeCardScopeChanged
		}
		d = boardCodeDecision(c, history, now, running, evidence...)
	}
	if d.Tag {
		a := args()
		a["tags"] = append(slices.Clone(c.Tags), "to-test")
		if err := call(ctx, "blerg_card_update", a, nil); err != nil {
			return d, err
		}
	}
	return d, nil
}

// Called at Main turn end; no model, background turn, fetch or checkout. Each
// repo's main is observed through the existing live workspace Git binding.
func (a *API) reconcileBoardCode(ctx context.Context, row *db.SessionRow, now time.Time) (facts []planFact) {
	started := time.Now()
	outcome := "no_changes"
	reads, checked, writes, unmatched := 0, 0, 0, 0
	busyRetries := 0
	defer func() {
		// Other repositories on the same board are normal. Flag a name
		// mismatch only when none of the cards matched a successfully read repo.
		if outcome == "no_changes" && reads > 0 && checked == 0 && unmatched > 0 {
			outcome = "repository_name_unmatched"
		}
		slog.Info("board_code", "operation", "board_code", "session_id", row.ID, "outcome", outcome, "repositories", reads, "cards", checked, "writes", writes, "unmatched_repositories", unmatched, "elapsed_ms", time.Since(started).Milliseconds(), "busy_retries", busyRetries)
		if outcome != "no_changes" && outcome != "reconciled" && outcome != "no_board_access" && outcome != "runtime_not_configured" && row.ParentSessionID == nil {
			facts = append(facts, planFact{key: "code:run:" + outcome, text: "A board/code reconciliation run reported: " + strings.ReplaceAll(outcome, "_", " ") + ". See runner diagnostics."})
		}
	}()
	fail := func(cause string) {
		if outcome == "no_changes" || outcome == "reconciled" {
			outcome = cause
		}
		boardCodeFailure(cause)
	}
	failError := func(stage string, err error) {
		if outcome == "no_changes" || outcome == "reconciled" {
			outcome = stage
		}
		boardCodeError(stage, err)
	}
	if row.ParentSessionID != nil {
		outcome = "child_session"
		return nil
	}
	if row.SpawningAccountID == nil {
		fail("account_unavailable")
		return nil
	}
	if a.hub == nil {
		fail("hub_unavailable")
		return nil
	}
	// Rows without a runtime cannot execute a turn or own a Git binding.
	// Report the skipped gate, but do not inject infrastructure drift into chat.
	if row.Runtime == nil || *row.Runtime == "" {
		outcome = "runtime_not_configured"
		return nil
	}
	caller := a.sessionBoardCaller()
	if caller == nil {
		fail("board_unavailable")
		return nil
	}
	boards, err := a.boardLevelsFor(ctx, row)
	if err != nil {
		failError("board_access_failed", err)
		return nil
	}
	if len(boards) == 0 {
		outcome = "no_board_access"
		return nil
	}
	landed, landingErr := a.boardCodeLandings(ctx, row, boards)
	landingIncomplete := landingErr != nil
	if landingErr != nil {
		failError("milestone_read_failed", landingErr)
	}
	// Legacy scalar sessions have no launch manifest. Use only their recorded
	// repository name and the existing daemon-owned legacy Git binding; never
	// downgrade a broken v2 binding to an unqualified read.
	ws, err := db.GetSessionReaderWorkspace(ctx, a.dbPool, row.ID)
	if err != nil || ws == nil {
		failError("workspace_unavailable", err)
		return nil
	}
	legacy := ws.Manifest == nil && ws.Kind == "repo" && row.Repo != ""
	var manifest protocol.WorkspaceManifest
	var generation int64
	if legacy {
		manifest.Members = []protocol.WorkspaceMember{{Name: row.Repo}}
	} else {
		manifest, generation, err = loadExplorerBinding(ctx, a.dbPool, row.ID)
		if err != nil {
			failError("workspace_unavailable", err)
			return nil
		}
	}
	dc := a.hub.FindDaemonForSession(row.ID)
	if dc == nil || (derefOrEmpty(row.Runtime) != "cluster" && a.hub.ownedHostDaemon(ctx, *row.SpawningAccountID, dc.ID) != dc) {
		fail("daemon_unavailable")
		return nil
	}
	// Existing milestone marks supply deploy evidence through an explicit prefix,
	// not a language-model classification or an arbitrary mentioned commit.
	var deploy string
	var raw []byte
	err = a.dbPool.QueryRow(ctx, `SELECT payload FROM agent_events WHERE session_id=$1 AND kind='mark' AND payload->>'kind'='milestone' AND payload->>'text' LIKE 'Deployed:%' AND jsonb_array_length(COALESCE(payload->'commits','[]'::jsonb))=1 ORDER BY seq DESC LIMIT 1`, row.ID).Scan(&raw)
	if err == nil {
		var mark agent.MarkPayload
		if json.Unmarshal(raw, &mark) == nil && len(mark.Commits) == 1 && protocol.ValidSessionOriginCommit(mark.Commits[0].Sha) {
			deploy = mark.Commits[0].Sha
		} else {
			fail("invalid_deploy_mark")
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		failError("deploy_read_failed", err)
	}
	var firstDeploy time.Time
	err = a.dbPool.QueryRow(ctx, `SELECT ts FROM agent_events WHERE session_id=$1 AND kind='mark' AND payload->>'kind'='milestone' AND payload->>'text' LIKE 'Deployed:%' AND jsonb_array_length(COALESCE(payload->'commits','[]'::jsonb))=1 AND payload->'commits'->0->>'sha' ~ '^[0-9a-f]{40}([0-9a-f]{24})?$' ORDER BY seq LIMIT 1`, row.ID).Scan(&firstDeploy)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		failError("deploy_cutover_read_failed", err)
	}
	undeployed := []int{}
	histories := map[string][]protocol.CodeCommit{}
	activities := map[string][]protocol.CodeActivity{}
	activityComplete := map[string]bool{}
	tips := []string{}
	for _, member := range manifest.Members {
		if ctx.Err() != nil {
			fail("check_canceled")
			return facts
		}
		ref := protocol.WorkspaceGitRef{MemberID: member.ID, WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: manifest.LaunchGeneration, Generation: generation}}
		if legacy {
			ref = protocol.WorkspaceGitRef{}
		}
		req := protocol.ReadCommit{SessionID: row.ID, BoardHistory: true, BoardActivitySince: now.Add(-boardCodeStaleAfter).UTC().Format(time.RFC3339), WorkspaceGitRef: ref}
		// Each repository independently resolves the recorded deploy SHA.
		req.SHA = deploy
		read := func() (protocol.CommitResult, error) {
			return a.hub.requestCommitRead(ctx, dc, *row.SpawningAccountID, req, commitLookupTimeout)
		}
		result, retried, err := readBoardCodeHistory(ctx, read, boardCodeBusyBackoff)
		if retried {
			busyRetries++
			boardCodeFailure("daemon_busy_retried")
		}
		if err == nil && !result.OK && result.ErrorCode == "deploy_unavailable" {
			// This deploy belongs to another repository. Main reconciliation still runs.
			req.SHA = ""
			result, retried, err = readBoardCodeHistory(ctx, read, boardCodeBusyBackoff)
			if retried {
				busyRetries++
				boardCodeFailure("daemon_busy_retried")
			}
		}
		if err != nil {
			failError("history_read_failed", err)
			continue
		}
		if !result.OK {
			fail(boardCodeHistoryCause(result.ErrorCode))
			continue
		}
		if result.CodeHistory == nil {
			fail("history_missing")
			continue
		}
		if !a.hub.currentWorkspaceGit(ctx, row.ID, result.WorkspaceGitRef) {
			fail("history_binding_changed")
			continue
		}
		reads++
		histories[member.Name] = result.CodeHistory.Commits
		activities[member.Name] = result.CodeHistory.Activity
		activityComplete[member.Name] = result.CodeHistory.ActivityComplete
		if !result.CodeHistory.ActivityComplete {
			boardCodeFailure("activity_history_unavailable")
			facts = append(facts, planFact{key: "invariant:activity_history_unavailable", text: "Stalled-card checks need branch activity evidence; update the workstation daemon before relying on invariant 2."})
		}
		tips = append(tips, result.CodeHistory.Tip)
	}
	checklistBoard := a.planSessionBoard(ctx, row)
	checklistKey := a.planBoardKey(ctx, row, checklistBoard)
	checklistActive := map[int]bool{}
	checklistUnknown := false
	currentChecklist, err := db.LatestChecklist(ctx, a.dbPool, row.ID, 1<<62)
	if err != nil {
		failError("checklist_read_failed", err)
		checklistUnknown = true
	} else if currentChecklist != nil {
		parsed, ok := checklist.ReadWithSeed([]byte(currentChecklist.Payload), currentChecklist.ClientEventID)
		if !ok {
			fail("checklist_invalid")
			checklistUnknown = true
		} else {
			for _, item := range parsed.Items {
				if !item.Done {
					for _, n := range cardRefs(item.Text, checklistKey) {
						checklistActive[n] = true
					}
				}
			}
		}
	}
	detailStart := a.boardDetails.start(row.ID)
	candidates := 0
	defer func() { a.boardDetails.finish(row.ID, detailStart, candidates) }()
	for _, board := range boards {
		level, err := a.effectiveBoardLevel(ctx, row, board.BoardID)
		if err != nil {
			failError("access_check_failed", err)
			continue
		}
		proof, ok := level.proof()
		if !ok || proof.AccountID != *row.SpawningAccountID || !mcpgw.BoardLevelAllows(level.Level, "blerg_card_get") {
			fail("access_denied")
			continue
		}
		call := boardCodeCall(func(ctx context.Context, tool string, args map[string]any, out any) error {
			current, err := a.effectiveBoardLevel(ctx, row, board.BoardID)
			if err != nil {
				return err
			}
			currentProof, valid := current.proof()
			if !valid || currentProof.AccountID != *row.SpawningAccountID || !mcpgw.BoardLevelAllows(current.Level, tool) {
				return errPlanNoBoardRead
			}
			data, err := json.Marshal(args)
			if err != nil {
				return err
			}
			raw, err := boardReconcileCall(ctx, caller, mcpgw.BoardCall{Proof: currentProof, BoardID: board.BoardID, SessionRef: row.ID, Tool: tool, Arguments: data, ReadOnly: mcpgw.BoardLevelAllows(db.BoardLevelRead, tool)})
			if err != nil {
				return err
			}
			if e := boardToolResult(raw, out); e != nil {
				return e
			}
			if tool == "blerg_card_move" || tool == "blerg_card_update" {
				writes++
				if outcome == "no_changes" {
					outcome = "reconciled"
				}
			}
			return nil
		})
		var columns []blackboard.Column
		if err := call(ctx, "blerg_column_list", map[string]any{"board_id": board.BoardID}, &columns); err != nil {
			failError("columns_read_failed", err)
			continue
		}
		cursor := ""
		for page := 0; page < 100; page++ {
			if ctx.Err() != nil {
				fail("check_canceled")
				return facts
			}
			var cards struct {
				Cards []liveBoardCard `json:"cards"`
				Next  string          `json:"next_cursor"`
			}
			if err := call(ctx, "blerg_card_search", map[string]any{"board_id": board.BoardID, "page": true, "summary": true, "limit": 100, "cursor": cursor}, &cards); err != nil {
				failError("card_search_failed", err)
				break
			}
			for _, summary := range cards.Cards {
				if ctx.Err() != nil {
					fail("check_canceled")
					return facts
				}
				// Repo names and BLG prefix must both match; numeric collisions on other
				// boards and unrelated repositories cannot drive an automatic move.
				if !strings.HasPrefix(summary.Reference, "BLG-") {
					continue
				}
				var history []protocol.CodeCommit
				complete := true
				for _, repo := range summary.Repos {
					commits, ok := histories[repo]
					if !ok {
						complete = false
						break
					}
					history = append(history, commits...)
				}
				if !complete || len(history) == 0 {
					unmatched++
					continue
				}
				checked++
				card := summary
				card.Columns = nil
				for _, col := range columns {
					card.Columns = append(card.Columns, struct {
						ID         string `json:"id"`
						Name       string `json:"name"`
						IsTerminal bool   `json:"is_terminal"`
					}{col.ID, col.Name, col.Terminal})
					if card.ColumnID != nil && *card.ColumnID == col.ID {
						card.ColumnName = col.Name
					}
				}
				running, err := a.boardCardLiveWorker(ctx, board.BoardID, card.Number, *row.SpawningAccountID, row.ID, now)
				if err != nil {
					failError("worker_read_failed", err)
					continue
				}
				proof := boardCodeEvidence{Since: a.boardCodeSince, RecordedLanding: landingIncomplete || landed[boardCodeCardKey{board.BoardID, card.Number}], ChecklistActive: board.BoardID == checklistBoard && checklistActive[card.Number], ActivityUnavailable: checklistUnknown}
				for _, repo := range summary.Repos {
					proof.Activity = append(proof.Activity, activities[repo]...)
					if !activityComplete[repo] {
						proof.ActivityUnavailable = true
					}
				}
				d := boardCodeDecision(card, history, now, running, proof)
				if boardCodeNeedsTrail(d) {
					candidate := candidates
					candidates++
					if candidate < detailStart || candidate >= detailStart+boardDetailLimit {
						continue
					}
					if err := call(ctx, "blerg_card_get", map[string]any{"board_id": board.BoardID, "number": summary.Number, "with_trail": true}, &card); err != nil {
						failError("card_read_failed", err)
						continue
					}
					d, err = reconcileCodeCard(ctx, call, card, history, now, running, mcpgw.BoardLevelAllows(level.Level, "blerg_card_move"), proof)
					if err != nil {
						failError("reconcile_failed", err)
					}
				}

				if d.Undeployed && toTestAfterDeploy(card, firstDeploy) {
					undeployed = append(undeployed, card.Number)
				}
				if d.Drift != "" {
					key := fmt.Sprintf("code:%s:%d:%s:%s", board.BoardID, card.Number, d.Drift, strings.Join(tips, ","))
					if d.Drift == "done_without_main_merge" {
						key += fmt.Sprintf(":%d", a.boardCodeSince.UnixNano())
					}
					facts = append(facts, planFact{key: key, text: boardCodeFactText(card.Number, d)})
				}
			}
			if cards.Next == "" {
				break
			}
			if cards.Next == cursor {
				fail("pagination_stalled")
				break
			}
			if page == 99 {
				fail("card_page_limit")
			}
			cursor = cards.Next
		}
	}
	if len(undeployed) > 0 {
		facts = append(facts, undeployedInvariantFact(undeployed))
	}

	return facts
}

func boardCodeError(stage string, err error) {
	cause := boardCodeErrorCause(err)
	failurelog.Default.Event("board_code:"+stage+":"+cause, slog.LevelWarn, "component", "runner", "operation", "board_code", "stage", stage, "cause", cause)
}

func boardCodeDriftText(cause string) string {
	switch cause {
	case "partial_merge_preserved":
		return "a partial or nonstandard merge was preserved without closing the card"
	case "merged_not_done":
		return "merged on main but outside Done"
	case "done_without_main_merge":
		return "Done without an active merge on main"
	case "merge_reverted":
		return "the merge was reverted"
	case "stalled_without_worker":
		return "in progress without a live worker, an open Main checklist item or a commit in four hours (invariant 2)"
	case "manual_move_preserved":
		return "a later manual move was preserved"
	case "history_incomplete":
		return "card history is incomplete; the column was preserved"
	case "ambiguous_done_column":
		return "the board has more than one terminal column"
	case "invalid_merge_date":
		return "the merge date is invalid"
	default:
		return "reconciliation needs attention"
	}
}

// Daemon replies are untrusted: retain known diagnostic codes, never raw text.
func boardCodeHistoryCause(code string) string {
	switch code {
	case "binding_unavailable", "repository_unavailable", "repository_untrusted", "main_unavailable", "invalid_deploy", "deploy_unavailable", "deploy_history_failed", "deploy_history_limit", "history_failed", "invalid_history", "history_limit", "read_canceled", "history_frame_limit", "invalid_activity_since", "activity_history_failed", "invalid_activity_history", "activity_subject_limit", "activity_history_limit", "busy", "unavailable":
		return "history_" + code
	default:
		return "history_refused"
	}
}

func boardCodeFactText(number int, d boardCodeAction) string {
	if d.Drift == "stalled_without_worker" {
		return fmt.Sprintf("#%d breaks invariant 2: no live worker, open Main checklist item or commit in four hours; resume or explain with `blerg-runner checklist add 'Resume #%d'`.", number, number)
	}
	text := fmt.Sprintf("Board/code drift: #%d (%s).", number, boardCodeDriftText(d.Drift))
	if d.Commit != "" {
		text += " Main commit: " + d.Commit + "."
	}
	return text + fmt.Sprintf(" Inspect with `blerg-runner ticket get %d`; preserve the card until the evidence is resolved.", number)
}

func boardCodeErrorCause(err error) string {
	var lookup *commitLookupFailure
	if errors.As(err, &lookup) {
		return lookup.code
	}
	switch {
	case errors.Is(err, errCommitBusy):
		return "commit_lookup_busy"
	case errors.Is(err, errCommitUnavailable):
		return "commit_lookup_unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return failurelog.Cause(err)
	}
}

func toTestAfterDeploy(c liveBoardCard, first time.Time) bool {
	if first.IsZero() || c.ToTestAddedAt == nil {
		return false
	}
	at, err := time.Parse(time.RFC3339, *c.ToTestAddedAt)
	return err == nil && !at.Before(first)
}
func undeployedInvariantFact(numbers []int) planFact {
	numbers = slices.Clone(numbers)
	slices.Sort(numbers)
	numbers = slices.Compact(numbers)
	keys := []string{}
	for _, n := range numbers[:min(5, len(numbers))] {
		keys = append(keys, fmt.Sprintf("#%d", n))
	}
	return planFact{key: "invariant:to_test_without_deploy", text: fmt.Sprintf("%d to-test cards lack deployed active main-merge evidence (invariant 5): %s; review with `blerg-runner ticket list --tag to-test`.", len(numbers), strings.Join(keys, ", "))}
}
