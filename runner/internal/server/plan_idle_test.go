package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
)

func TestPlanIdleDecision(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		ready, running, account int
		want                    string
	}{
		{"idle", 4, 0, 0, "4 ready cards for 0.6.0, 8 free worker slots, no workers running: start work or say why not."},
		{"running", 4, 1, 1, ""}, {"blocked", 0, 0, 0, ""}, {"account full", 4, 0, 8, ""},
		{"account capacity", 2, 0, 6, "2 ready cards for 0.6.0, 2 free worker slots, no workers running: start work or say why not."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := planIdleText(tc.ready, tc.running, tc.account, childStartLimits{parallel: 8}, "0.6.0"); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

// Real session authority and SQL, with the Board transport faked.
type idleBoard struct {
	*planBoard
	cards      []idleBoardCard
	fail       bool
	paged      bool
	calls      int
	noReleases bool
}

func (b *idleBoard) CallBoard(_ context.Context, c mcpgw.BoardCall) (json.RawMessage, error) {
	b.calls++
	if b.fail {
		return nil, errors.New("PRIVATE_CANARY")
	}
	var out any
	switch c.Tool {
	case "blerg_release_list":
		if b.noReleases {
			out = []any{}
			break
		}
		out = []map[string]any{{"id": "release", "name": "0.6.0", "status": "in progress"}}
	case "blerg_column_list":
		out = []map[string]any{{"id": "ready", "name": "Ready", "is_terminal": false}}
	case "blerg_card_search":
		var args struct {
			Release string `json:"release_id"`
			Tag     string `json:"tag"`
			Cursor  string `json:"cursor"`
			Page    bool   `json:"page"`
		}
		if err := json.Unmarshal(c.Arguments, &args); err != nil {
			return nil, err
		}
		if (!b.noReleases && args.Release != "release") || (b.noReleases && args.Tag != "0.6.0") || !args.Page || !c.ReadOnly {
			return nil, fmt.Errorf("invalid search scope")
		}
		cards, next := b.cards, ""
		if b.paged {
			if args.Cursor == "" {
				cards, next = b.cards[:100], "next"
			} else {
				cards = b.cards[100:]
			}
		}
		out = map[string]any{"cards": cards, "next_cursor": next}
	default:
		return nil, fmt.Errorf("unexpected board tool")
	}
	raw, _ := json.Marshal(out)
	return json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": string(raw)}}})
}
func TestPlanIdleTurnDelivery(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelRead)
	col, rel := "ready", "release"
	board := &idleBoard{planBoard: fx.board, cards: []idleBoardCard{{Number: 12, ColumnID: &col, ReleaseID: &rel}, {Number: 13, ColumnID: &col, ReleaseID: &rel}}}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: board})
	fx.api.childLimits = childStartLimits{parallel: 8, hourly: 120}
	for i := 0; i < 2; i++ {
		got := fx.turn("human")
		if !strings.Contains(got, "2 ready cards for 0.6.0, 8 free worker slots") {
			t.Fatalf("turn %d lost idle fact: %s", i, got)
		}
	}
	board.cards[0].StuckAt = &rel
	board.cards[1].Blockers = append(board.cards[1].Blockers, struct {
		Satisfied bool `json:"satisfied"`
	}{false})
	if got := fx.turn("human"); strings.Contains(got, "ready cards for") {
		t.Fatalf("blocked cards warned: %s", got)
	}
}
func TestPlanIdleRunFailureLogsSafeOutcome(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelRead)
	board := &idleBoard{planBoard: fx.board, fail: true}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: board})
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	if got := fx.api.planIdleWork(t.Context(), row, 1); len(got) != 0 {
		t.Fatal(got)
	}
	if strings.Count(logs.String(), `"msg":"plan_idle"`) != 1 || !strings.Contains(logs.String(), `"outcome":"board_call_failed"`) || strings.Contains(logs.String(), "PRIVATE_CANARY") {
		t.Fatal(logs.String())
	}
}

func TestPlanIdleAssignmentsAndRunningChildren(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelRead)
	col, rel := "ready", "release"
	board := &idleBoard{planBoard: fx.board, cards: []idleBoardCard{{Number: 12, ColumnID: &col, ReleaseID: &rel}}}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: board})
	row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := fx.pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// A live linked worker belongs to another parent: Main itself still has no children.
	exec(`INSERT INTO session_card_links VALUES($1,'board-a',12,'pin',now())`, fx.ended)
	if got := fx.api.planIdleWork(t.Context(), row, 1); len(got) != 0 {
		t.Fatalf("assigned card warned: %+v", got)
	}
	exec(`UPDATE sessions SET ended_at=now() WHERE id=$1`, fx.ended)
	exec(`INSERT INTO messages(session_id,kind,body,status) VALUES($1,'ask','Can this proceed?','open')`, fx.ended)
	if got := fx.api.planIdleWork(t.Context(), row, 2); len(got) != 0 {
		t.Fatalf("person-blocked card warned: %+v", got)
	}
	exec(`UPDATE messages SET status='answered',answer='Proceed',answered_at=now() WHERE session_id=$1`, fx.ended)
	if got := fx.api.planIdleWork(t.Context(), row, 3); len(got) != 1 {
		t.Fatalf("available card did not warn: %+v", got)
	}
	seq := fx.event("turn_done", `{"stop_reason":"end_turn"}`)
	fx.api.checkPlanTurn(t.Context(), fx.sess, seq, time.Now(), "end_turn")
	exec(`UPDATE sessions SET parent_session_id=$2,ended_at=NULL WHERE id=$1`, fx.ended, fx.sess)
	if got := fx.api.planIdleWork(t.Context(), row, 4); len(got) != 0 {
		t.Fatalf("running child warned: %+v", got)
	}
	if got := planCarryingInput(t, fx.api, fx.sess, true); strings.Contains(got, "ready cards for") {
		t.Fatalf("stale idle warning delivered: %s", got)
	}
}

func TestPlanIdleCardStates(t *testing.T) {
	col, rel, other := "ready", "release", "other"
	for _, tc := range []struct {
		name   string
		change func(*idleBoardCard)
		want   bool
	}{
		{"ready", func(c *idleBoardCard) {}, true},
		{"other release", func(c *idleBoardCard) { c.ReleaseID = &other }, false},
		{"other column", func(c *idleBoardCard) { c.ColumnID = &other }, false},
		{"archived", func(c *idleBoardCard) { c.ArchivedAt = &other }, false},
		{"stuck", func(c *idleBoardCard) { c.StuckAt = &other }, false},
		{"dependency", func(c *idleBoardCard) {
			c.Blockers = append(c.Blockers, struct {
				Satisfied bool `json:"satisfied"`
			}{false})
		}, false},
		{"landed dependency", func(c *idleBoardCard) {
			c.Blockers = append(c.Blockers, struct {
				Satisfied bool `json:"satisfied"`
			}{true})
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := idleBoardCard{ColumnID: &col, ReleaseID: &rel}
			tc.change(&c)
			if got := idleCardReady(c, map[string]bool{col: true}, rel, ""); got != tc.want {
				t.Fatalf("ready=%t want %t", got, tc.want)
			}
		})
	}
}

func TestPlanIdleCountsEveryBoardPage(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelRead)
	col, rel := "ready", "release"
	board := &idleBoard{planBoard: fx.board, paged: true}
	for i := 1; i <= 101; i++ {
		board.cards = append(board.cards, idleBoardCard{Number: i, ColumnID: &col, ReleaseID: &rel})
	}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: board})
	row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	facts := fx.api.planIdleWork(t.Context(), row, 1)
	if len(facts) != 1 || !strings.HasPrefix(facts[0].text, "101 ready cards for") {
		t.Fatalf("incomplete count: %+v", facts)
	}
}

func TestPlanIdleRequiresBoardRead(t *testing.T) {
	fx := newPlanFx(t, "")
	board := &idleBoard{planBoard: fx.board}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: board})
	row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	id := "board-a"
	row.BoardID = &id
	fx.api.planIdleWork(t.Context(), row, 1)
	if board.calls != 0 {
		t.Fatalf("board read without authority: %d calls", board.calls)
	}
}

func TestPlanIdleMilestoneTagFallback(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelRead)
	col := "ready"
	board := &idleBoard{planBoard: fx.board, noReleases: true, cards: []idleBoardCard{
		{Number: 12, ColumnID: &col, Tags: []string{"0.6.0"}},
		{Number: 13, ColumnID: &col, Tags: []string{"0.6.0"}},
		{Number: 14, ColumnID: &col, Tags: []string{"0.7.0"}},
	}}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: board})
	owner := blackboard.Actor{Kind: "human", ID: topicOwner}
	now := time.Now()
	record := blackboard.Record{ID: blackboard.NewID(), Owner: owner, WorkspaceID: fx.sess, Version: 1, Milestones: []blackboard.Milestone{
		{ID: "old", Name: "0.4.0", Owner: owner, Version: 1, FrozenAt: &now},
		{ID: "shipped", Name: "0.5.0", Owner: owner, Version: 1, ShippedAt: &now},
		{ID: "current", Name: "0.6.0", Owner: owner, Version: 1},
		{ID: "later", Name: "0.7.0", Owner: owner, Version: 1},
	}}
	if err := fx.api.blackboardStore().Create(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	fx.api.childLimits = childStartLimits{parallel: 8, hourly: 120}
	if got := fx.turn("human"); !strings.Contains(got, "2 ready cards for 0.6.0, 8 free worker slots") {
		t.Fatalf("missing tag fallback: %s", got)
	}
	board.noReleases = false
	if got := fx.turn("human"); strings.Contains(got, "ready cards for") {
		t.Fatalf("milestone overrode active Board release: %s", got)
	}
}

func TestPlanIdleNoMilestoneLogsReason(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelRead)
	board := &idleBoard{planBoard: fx.board, noReleases: true}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: board})
	row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	owner := blackboard.Actor{Kind: "human", ID: topicOwner}
	record := blackboard.Record{ID: blackboard.NewID(), Owner: owner, WorkspaceID: fx.ended, Version: 1, Milestones: []blackboard.Milestone{{ID: "other", Name: "0.6.0", Owner: owner, Version: 1}}}
	if err := fx.api.blackboardStore().Create(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	if facts := fx.api.planIdleWork(t.Context(), row, 1); len(facts) != 0 {
		t.Fatal(facts)
	}
	if strings.Count(logs.String(), `"msg":"plan_idle"`) != 1 || !strings.Contains(logs.String(), `"outcome":"no_workspace_blackboard"`) {
		t.Fatal(logs.String())
	}
}
