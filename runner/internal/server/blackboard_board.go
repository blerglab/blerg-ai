package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/contracts/identity"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

type blackboardBoardFunc func(context.Context, string, map[string]any, any) error

func blackboardBoardFailure(err error) *APIError {
	failurelog.Default.Event("blackboard_board_request", slog.LevelError, "component", "runner", "stage", "board_call", "cause", failurelog.Cause(err))
	return boardCallError(err)
}

func (f blackboardBoardFunc) Call(ctx context.Context, tool string, args map[string]any, out any) error {
	return f(ctx, tool, args, out)
}
func (a *API) blackboardService(r *http.Request, record blackboard.Record, p identity.Principal) blackboard.Service {
	workspace := r.URL.Query().Get("session")
	adapter := blackboardBoardFunc(func(ctx context.Context, tool string, args map[string]any, out any) error {
		if !mcpgw.BoardLevelAllows(db.BoardLevelRead, tool) {
			return blackboard.ErrForbidden
		}
		board, _ := args["board_id"].(string)
		row, err := db.GetSession(ctx, a.dbPool, workspace)
		if err != nil {
			return err
		}
		if row == nil || !canSeeAccount(p.Sub, row) {
			return blackboard.ErrForbidden
		}
		level, err := a.effectiveBoardLevel(ctx, row, board)
		if err != nil {
			return err
		}
		if !mcpgw.BoardLevelAllows(level.Level, tool) {
			return blackboard.ErrForbidden
		}
		caller := a.sessionBoardCaller()
		if caller == nil {
			return blackboard.ErrInvalid
		}
		rawArgs, _ := json.Marshal(args)
		raw, err := caller.CallBoard(ctx, mcpgw.BoardCall{Proof: mcpgw.Proof{AccountID: p.Sub, SessionID: p.Sid}, BoardID: board, SessionRef: workspace, Tool: tool, Arguments: rawArgs, ReadOnly: true})
		if err != nil {
			return blackboardBoardFailure(err)
		}
		if e := boardToolResult(raw, out); e != nil {
			if e.Status == 404 {
				return blackboard.ErrNotFound
			}
			if e.Status == 403 || e.Status == 401 {
				return blackboard.ErrForbidden
			}
			return e
		}
		if tool == "blerg_card_search" || tool == "blerg_card_get" {
			questions, err := a.blackboardQuestions(ctx, workspace, p.Sub, board)
			if err != nil {
				return err
			}
			switch v := out.(type) {
			case *blackboard.CardPage:
				for i := range v.Cards {
					v.Cards[i].NeedsYou = questions[v.Cards[i].Number]
				}
			case *blackboard.Card:
				v.NeedsYou = questions[v.Number]
			}
		}
		return nil
	})
	return blackboard.Service{Store: a.blackboardStore(), BlackboardID: record.ID, Board: adapter, ReadScope: mcpgw.WithBoardReadScope}
}

func (a *API) HandleBlackboardAbortFinal(w http.ResponseWriter, r *http.Request) {
	a.retiredCutDecision(w, r)
}
func (a *API) HandleBlackboardSlice(w http.ResponseWriter, r *http.Request) {
	record, p, ok := a.blackboardAccess(w, r)
	if !ok {
		return
	}
	v, err := a.blackboardService(r, record, p).Slice(r.Context(), r.PathValue("milestone"))
	if err != nil {
		blackboardError(w, err)
		return
	}
	offset, limit, ok := blackboardPage(w, r)
	if !ok {
		return
	}
	v.RunnerRecord = nil
	v.BoardCut = nil
	total := len(v.Cards)
	v.Cards = blackboardRows(v.Cards, offset, limit)
	// Preserve the slice shape used by screens, adding pagination alongside it.
	writeJSON(w, 200, struct {
		blackboard.SliceView
		Next string `json:"next_cursor"`
	}{v, blackboardCursor(offset, limit, total)})
}
func (a *API) HandleBlackboardFinal(w http.ResponseWriter, r *http.Request) {
	a.retiredCutDecision(w, r)
}
func (a *API) retiredCutDecision(w http.ResponseWriter, r *http.Request) {
	p, ok := a.permissionOwner(w, r)
	if !ok {
		return
	}
	if a.dbPool == nil {
		writeError(w, 503, noDatabaseMsg)
		return
	}
	record, err := a.blackboardStore().LoadPartition(r.Context(), r.PathValue("id"), blackboard.Partition{Kind: blackboard.PartitionCore})
	if err != nil {
		blackboardError(w, err)
		return
	}
	if record.Owner.ID != p.Account {
		_ = boundFailure("wrong_owner")
		writeError(w, 403, "Blackboard owner required.")
		return
	}
	for _, board := range blackboard.ReferencedBoards(record) {
		if p.Project != "" && p.Project != board {
			writeError(w, 404, "Blackboard not found.")
			return
		}
	}
	_ = boundFailure("manual_cut_route_retired")
	writeError(w, http.StatusGone, "Ask Main to raise a person-required bound cut question in chat or Needs you. Final follows the board receipt.")
}

// Only the existence of a visible, person-facing open question affects stage;
// question text and private-session existence never leave their owning scope.
// Bound plan approvals decide project scope; historical session card links do
// not turn those approvals into blockers on the cards themselves.
func (a *API) blackboardQuestions(ctx context.Context, workspace, account, board string) (map[int]bool, error) {
	rows, err := a.dbPool.Query(ctx, `WITH RECURSIVE scope AS (
 SELECT id,spawning_account_id,private FROM sessions WHERE id::text=$1
 UNION ALL SELECT s.id,s.spawning_account_id,s.private FROM sessions s JOIN scope p ON s.parent_session_id=p.id
 ) SELECT DISTINCT l.card_number FROM scope s JOIN session_card_links l ON l.session_id=s.id
 JOIN messages m ON m.session_id=s.id
 WHERE l.board_id=$3 AND (NOT s.private OR s.spawning_account_id=$2)
 AND m.kind='ask' AND m.status='open'
 AND (m.person_required OR m.child_escalated OR m.child_person_notified OR (m.owner_only AND NOT m.no_wait))
 AND NOT EXISTS(SELECT 1 FROM bound_asks b WHERE b.message_id=m.id AND b.subject->>'kind'='plan')
 AND NOT EXISTS(SELECT 1 FROM session_failure_alerts f WHERE f.message_id=m.id)`, workspace, account, board)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}
