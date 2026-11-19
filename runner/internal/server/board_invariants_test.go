package server

import (
	"context"
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/blackboard"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/mcpgw"
	"strings"
	"testing"
)

func TestBoardReleaseTagInvariant(t *testing.T) {
	releases := []blackboard.Release{{ID: "old", Name: "0.6.0"}, {ID: "new", Name: "0.7.0"}}
	for _, tc := range []struct {
		tags      []string
		want      string
		ambiguous bool
	}{
		{[]string{"0.7.0"}, "new", false},
		{[]string{"0.6.0", "0.7.0"}, "", true},
		{[]string{"board"}, "", false},
		{[]string{"0.6.0"}, "old", false},
	} {
		got, ambiguous := releaseTagTarget(tc.tags, releases)
		if got != tc.want || ambiguous != tc.ambiguous {
			t.Fatalf("tags %v: target=%q ambiguous=%t", tc.tags, got, ambiguous)
		}
	}
}

func TestBoardTerminalInvariant(t *testing.T) {
	for _, tc := range []struct {
		cols []blackboard.Column
		want bool
	}{
		{[]blackboard.Column{{ID: "done", Name: "Done", Terminal: true}}, true},
		{[]blackboard.Column{{ID: "done", Name: "Done"}}, false},
		{[]blackboard.Column{{ID: "done", Name: "Done", Terminal: true}, {ID: "closed", Terminal: true}}, false},
		{nil, false},
	} {
		if got := terminalColumnValid(tc.cols); got != tc.want {
			t.Fatalf("columns %v: valid=%t", tc.cols, got)
		}
	}
}

func TestReleaseTagReconciliationRecordsEvidenceAndGuardsVersion(t *testing.T) {
	old := "old"
	c := liveBoardCard{BoardID: "board", Number: 500, ReleaseID: &old, Tags: []string{"0.7.0"}, Version: 3}
	calls := []string{}
	call := boardCodeCall(func(ctx context.Context, tool string, args map[string]any, out any) error {
		calls = append(calls, tool)
		switch tool {
		case "blerg_card_comment":
			if !strings.Contains(args["text"].(string), "previous release old") {
				t.Fatal("missing reversible evidence")
			}
		case "blerg_card_get":
			fresh := c
			fresh.Version = 4
			*(out.(*liveBoardCard)) = fresh
		case "blerg_card_update":
			if args["if_match"] != 4 || args["release_id"] != "new" {
				t.Fatalf("unguarded write: %v", args)
			}
		default:
			t.Fatalf("unexpected tool %s", tool)
		}
		return nil
	})
	if err := reconcileReleaseTag(t.Context(), call, c, []blackboard.Release{{ID: "new", Name: "0.7.0"}}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "blerg_card_comment,blerg_card_get,blerg_card_update" {
		t.Fatal(calls)
	}
}

type invariantGateway struct{ *planBoard }

func (b *invariantGateway) CallBoard(ctx context.Context, c mcpgw.BoardCall) (json.RawMessage, error) {
	var value any
	switch c.Tool {
	case "blerg_column_list":
		value = []blackboard.Column{{ID: "done", Name: "Done"}}
	case "blerg_release_list":
		value = []blackboard.Release{}
	case "blerg_card_search":
		value = map[string]any{"cards": []liveBoardCard{}}
	default:
		return b.planBoard.CallBoard(ctx, c)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": string(raw)}}})
}

func TestBoardInvariantEventHoldsTerminalDriftWithoutDaemon(t *testing.T) {
	fx := newPlanFx(t, db.BoardLevelRead)
	fx.api.hub.mcpStart.set(&mcpStartConfig{Core: &filingBoardList{}, Board: &invariantGateway{fx.board}})
	fx.api.checkBoardEventInvariants(t.Context(), fx.sess)
	var count int
	err := fx.pool.QueryRow(t.Context(), `SELECT count(*) FROM session_plan_nudges WHERE session_id=$1`, fx.sess).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("terminal-column event drift: got %d facts", count)
	}
}
