package server

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestBoardCodeDoneWarningsRequireRecentMove(t *testing.T) {
	cutoff := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, moved          string
		recorded, body, want bool
	}{
		{"old fast-forward", "2026-01-01T00:00:00Z", false, false, false},
		{"at cutover", "2026-01-02T00:00:00Z", false, false, false},
		{"new missing merge", "2026-01-03T00:00:00Z", false, false, true},
		{"recorded landing", "2026-01-03T00:00:00Z", true, false, false},
		{"merge body names card", "2026-01-03T00:00:00Z", false, true, false},
		{"missing move history", "", false, false, false},
		{"invalid date", "invalid", false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			card := liveBoardCard{Number: 548, ColumnName: "Done", UpdatedAt: "2026-01-04T00:00:00Z"}
			if tc.moved != "" {
				raw, _ := json.Marshal(map[string]any{"trail": []any{map[string]any{"type": "moved", "to_column": "Done", "created_at": tc.moved}}})
				if err := json.Unmarshal(raw, &card); err != nil {
					t.Fatal(err)
				}
			}
			history := []protocol.CodeCommit{{SHA: fixtureSHA, Subject: "Implement BLG-548", Parents: []string{"base"}}}
			if tc.body {
				history = append(history, protocol.CodeCommit{SHA: "integration", Parents: []string{"base", "feature"}, BodyCards: []int{548}})
			}
			d := boardCodeDecision(card, history, cutoff.Add(72*time.Hour), false, boardCodeEvidence{Since: cutoff, RecordedLanding: tc.recorded})
			if (d.Drift == "done_without_main_merge") != tc.want {
				t.Fatalf("drift: %+v", d)
			}
			if strings.Contains(boardCodeFactText(card.Number, d), "Main commit:") {
				t.Fatal("empty commit label")
			}
		})
	}
}

func TestBoardCodeCutoverConfiguration(t *testing.T) {
	enabled := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if got := boardCodeCutover("", enabled); !got.Equal(enabled) {
		t.Fatal(got)
	}
	for _, raw := range []string{"2026-01-02", "2026-01-02T00:00:00Z"} {
		if got := boardCodeCutover(raw, enabled.Add(time.Hour)); !got.Equal(enabled) {
			t.Fatal(got)
		}
	}
	if got := boardCodeCutover("PRIVATE_CANARY", enabled); !got.IsZero() {
		t.Fatal(got)
	}
}

func TestBoardCodeLandingMarksRespectBoardAndKind(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mark   agent.MarkPayload
		boards []string
		want   bool
	}{
		{"deploy text", agent.MarkPayload{Kind: "milestone", Text: "Deployed: lab ENG-548"}, []string{"board-a"}, true},
		{"short card reference", agent.MarkPayload{Kind: "milestone", Text: "Deployed: lab #548"}, []string{"board-a"}, true},
		{"merge text", agent.MarkPayload{Kind: "milestone", Text: "Merged ENG-548: work"}, []string{"board-a"}, true},
		{"structured card", agent.MarkPayload{Kind: "milestone", Text: "Merged", Card: 548, BoardID: "board-a"}, []string{"board-a", "board-b"}, true},
		{"other board", agent.MarkPayload{Kind: "milestone", Text: "Merged ENG-548", BoardID: "board-b"}, []string{"board-a"}, false},
		{"ambiguous board", agent.MarkPayload{Kind: "milestone", Text: "Merged ENG-548"}, []string{"board-a", "board-b"}, false},
		{"ordinary milestone", agent.MarkPayload{Kind: "milestone", Text: "Discussed BLG-548", Card: 548}, []string{"board-a"}, false},
		{"not a milestone", agent.MarkPayload{Kind: "finding", Text: "Merged ENG-548"}, []string{"board-a"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			landed := map[boardCodeCardKey]bool{}
			addBoardCodeLanding(landed, tc.mark, tc.boards, map[string]string{"board-a": "ENG", "board-b": "OTHER"})
			if landed[boardCodeCardKey{"board-a", 548}] != tc.want {
				t.Fatal(landed)
			}
		})
	}
}

func TestBoardCodeReadsRecordedMilestoneLandings(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelFull)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET runtime='daemon' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	fx.event("mark", `{"kind":"milestone","text":"Deployed: lab ENG-548"}`)
	fx.event("mark", `{"kind":"milestone","text":"Merged","card":520,"board_id":"board-a"}`)
	fx.event("mark", `{"kind":"milestone","text":"Merged BLG-541","board_id":"board-b"}`)
	row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	landed, err := fx.api.boardCodeLandings(t.Context(), row, []boardLevelView{{BoardID: "board-a"}})
	if err != nil {
		t.Fatal(err)
	}
	if !landed[boardCodeCardKey{"board-a", 548}] || !landed[boardCodeCardKey{"board-a", 520}] || landed[boardCodeCardKey{"board-a", 541}] {
		t.Fatal(landed)
	}
}

func TestBoardCodeOldQueuedDoneWarningIsDropped(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelFull)
	fx.event("turn_done", `{}`)
	if err := db.AddPlanFact(t.Context(), fx.pool, fx.sess, "code:board-a:548:done_without_main_merge:tip", "Board/code drift: #548 (Done without an active merge on main). Main commit: ."); err != nil {
		t.Fatal(err)
	}
	if got := planCarryingInput(t, fx.api, fx.sess, true); got != "" {
		t.Fatalf("old queued warning delivered: %q", got)
	}
}

func TestBoardCodeQueuedDoneWarningRechecksLandingMark(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelFull)
	if _, err := fx.pool.Exec(t.Context(), `UPDATE sessions SET runtime='daemon' WHERE id=$1`, fx.sess); err != nil {
		t.Fatal(err)
	}
	fx.api.boardCodeSince = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	board := &codeSyncBoard{planBoard: newPlanBoard()}
	if err := json.Unmarshal([]byte(`{"board_id":"board-a","number":548,"column_name":"Done","trail":[{"type":"moved","to_column":"Done","created_at":"2026-01-03T00:00:00Z"}]}`), &board.card); err != nil {
		t.Fatal(err)
	}
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &planBoardList{}, Board: board})
	row, err := db.GetSession(t.Context(), fx.pool, fx.sess)
	if err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("code:board-a:548:done_without_main_merge:tip:%d", fx.api.boardCodeSince.UnixNano())
	if !fx.api.heldBoardCodeFact(t.Context(), row, key) {
		t.Fatal("fresh warning was dropped")
	}
	fx.event("mark", `{"kind":"milestone","text":"Deployed: lab ENG-548"}`)
	if fx.api.heldBoardCodeFact(t.Context(), row, key) {
		t.Fatal("queued warning ignored new deployment evidence")
	}
}
