package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestBoardMergeNaming(t *testing.T) {
	for _, tt := range []struct {
		s    string
		want []int
	}{
		{"Merge BLG-528: board follows code", []int{528}},
		{"Merge BLG-12, BLG-528: shared work", []int{12, 528}},
		{"fix BLG-528 follow-up", nil}, {"Merge cleanup for BLG-528", nil},
		{"Merge BLG-528a", nil}, {"Merge BLG-0", nil}, {"Revert \"Merge BLG-528\"", nil},
	} {
		if got := boardMergeNumbers(tt.s); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q = %v; want %v", tt.s, got, tt.want)
		}
	}
}
func TestBoardCodePolicy(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	merge := protocol.CodeCommit{SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Date: at.Format(time.RFC3339), Subject: "Merge BLG-528", Parents: []string{"p1", "p2"}, Deployed: true}
	card := liveBoardCard{Number: 528, ColumnName: "In progress"}
	card.Columns = append(card.Columns, struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		IsTerminal bool   `json:"is_terminal"`
	}{"done", "Done", true})
	decision := boardCodeDecision(card, []protocol.CodeCommit{merge}, at.Add(5*time.Hour), false)
	if decision.Column != "done" || !decision.Tag || decision.Commit != merge.SHA {
		t.Fatalf("merge: %+v", decision)
	}
	revert := protocol.CodeCommit{SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Reverts: merge.SHA}
	if d := boardCodeDecision(card, []protocol.CodeCommit{revert, merge}, at.Add(time.Hour), false); d.Column != "" || d.Tag || d.Drift == "" {
		t.Fatalf("revert: %+v", d)
	}
	card.Trail = append(card.Trail, struct {
		Type      string `json:"type"`
		Actor     string `json:"actor"`
		ToColumn  string `json:"to_column,omitempty"`
		Text      string `json:"text,omitempty"`
		CreatedAt string `json:"created_at"`
	}{Type: "moved", Actor: "human", CreatedAt: at.Add(time.Minute).Format(time.RFC3339)})
	if d := boardCodeDecision(card, []protocol.CodeCommit{merge}, at.Add(time.Hour), false); d.Column != "" {
		t.Fatalf("manual move overwritten: %+v", d)
	}
	card.Trail = nil
	merge.Parents = nil
	if d := boardCodeDecision(card, []protocol.CodeCommit{merge}, at.Add(5*time.Hour), false); d.Column != "" || d.Tag {
		t.Fatalf("follow-up: %+v", d)
	}
	if d := boardCodeDecision(card, nil, at.Add(5*time.Hour), true); d.Drift != "" {
		t.Fatalf("running worker: %+v", d)
	}
}

func TestBoardCodeWritesUseFreshVersionsAndKeepTags(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	history := []protocol.CodeCommit{{SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Date: at.Format(time.RFC3339), Subject: "Merge BLG-528", Parents: []string{"one", "two"}, Deployed: true}}
	card := liveBoardCard{ID: "card", BoardID: "board", Number: 528, Version: 7, ColumnName: "In progress", Tags: []string{"release"}}
	card.Columns = append(card.Columns, struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		IsTerminal bool   `json:"is_terminal"`
	}{"done", "Done", true})
	var tools []string
	call := func(ctx context.Context, tool string, args map[string]any, out any) error {
		tools = append(tools, tool)
		switch tool {
		case "blerg_card_comment":
			card.Version++
		case "blerg_card_get":
			*out.(*liveBoardCard) = card
		case "blerg_card_move":
			if args["if_match"] != card.Version {
				t.Fatal("stale move version")
			}
			col := "done"
			card.ColumnID = &col
			card.ColumnName = "Done"
			card.Version++
		case "blerg_card_update":
			if args["if_match"] != card.Version {
				t.Fatal("stale tag version")
			}
			card.Tags = args["tags"].([]string)
			card.Version++
		default:
			t.Fatalf("unexpected tool %s", tool)
		}
		return nil
	}
	if _, err := reconcileCodeCard(t.Context(), call, card, history, at.Add(time.Hour), false, false); err != nil || len(tools) != 0 {
		t.Fatal("read grant wrote")
	}
	if _, err := reconcileCodeCard(t.Context(), call, card, history, at.Add(time.Hour), false, true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(card.Tags, []string{"release", "to-test"}) {
		t.Fatalf("tags: %v", card.Tags)
	}
	if !reflect.DeepEqual(tools, []string{"blerg_card_comment", "blerg_card_get", "blerg_card_move", "blerg_card_get", "blerg_card_update"}) {
		t.Fatalf("calls: %v", tools)
	}
	tools = nil
	if _, err := reconcileCodeCard(t.Context(), call, card, history, at.Add(time.Hour), false, true); err != nil || len(tools) != 0 {
		t.Fatal("repeated reconciliation wrote")
	}
}

func TestBoardCodeStopsAfterBoardFailure(t *testing.T) {
	card := liveBoardCard{Number: 528, ColumnName: "Done", Tags: []string{"keep"}}
	history := []protocol.CodeCommit{{SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Date: "2026-01-01T00:00:00Z", Subject: "Merge BLG-528", Parents: []string{"one", "two"}, Deployed: true}}
	calls := 0
	denied := errors.New("fixture denial")
	call := func(context.Context, string, map[string]any, any) error { calls++; return denied }
	if _, err := reconcileCodeCard(t.Context(), call, card, history, time.Now(), false, true); !errors.Is(err, denied) || calls != 1 {
		t.Fatal("write continued after failure")
	}
}

func TestBoardCodeDriftAndRevertOfRevert(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	card := liveBoardCard{Number: 528, ColumnName: "In progress", UpdatedAt: at.Format(time.RFC3339)}
	if d := boardCodeDecision(card, nil, at.Add(boardCodeStaleAfter), false); d.Drift != "stalled_without_worker" {
		t.Fatalf("stalled: %+v", d)
	}
	if d := boardCodeDecision(card, nil, at.Add(boardCodeStaleAfter), true); d.Drift != "" {
		t.Fatalf("live worker: %+v", d)
	}
	follow := protocol.CodeCommit{SHA: fixtureSHA, Date: at.Add(3 * time.Hour).Format(time.RFC3339), Subject: "BLG-528 follow-up"}
	if d := boardCodeDecision(card, []protocol.CodeCommit{follow}, at.Add(boardCodeStaleAfter), false); d.Drift != "" || d.Column != "" {
		t.Fatalf("recent follow-up: %+v", d)
	}
	card.ColumnName = "Done"
	if d := boardCodeDecision(card, nil, at, false); d.Drift != "" {
		t.Fatalf("done drift: %+v", d)
	}
	merge := protocol.CodeCommit{SHA: fixtureSHA, Date: at.Format(time.RFC3339), Subject: "Merge BLG-528", Parents: []string{"one", "two"}}
	revert := protocol.CodeCommit{SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Reverts: merge.SHA}
	restored := protocol.CodeCommit{SHA: "cccccccccccccccccccccccccccccccccccccccc", Reverts: revert.SHA}
	if d := boardCodeDecision(card, []protocol.CodeCommit{restored, revert, merge}, at, false); d.Drift != "" || d.Commit != merge.SHA || d.Tag {
		t.Fatalf("revert of revert: %+v", d)
	}
}

func TestBoardCodeManualMoveBetweenReadAndWrite(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	card := liveBoardCard{Number: 528, ColumnName: "In progress"}
	card.Columns = append(card.Columns, struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		IsTerminal bool   `json:"is_terminal"`
	}{"done", "Done", true})
	history := []protocol.CodeCommit{{SHA: fixtureSHA, Date: at.Format(time.RFC3339), Subject: "Merge BLG-528", Parents: []string{"one", "two"}, Deployed: false}}
	var tools []string
	call := func(_ context.Context, tool string, _ map[string]any, out any) error {
		tools = append(tools, tool)
		if tool == "blerg_card_get" {
			card.Trail = append(card.Trail, struct {
				Type      string `json:"type"`
				Actor     string `json:"actor"`
				ToColumn  string `json:"to_column,omitempty"`
				Text      string `json:"text,omitempty"`
				CreatedAt string `json:"created_at"`
			}{Type: "moved", Actor: "human", CreatedAt: at.Add(time.Minute).Format(time.RFC3339)})
			*out.(*liveBoardCard) = card
		}
		return nil
	}
	if _, err := reconcileCodeCard(t.Context(), call, card, history, at.Add(time.Hour), false, true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tools, []string{"blerg_card_comment", "blerg_card_get"}) {
		t.Fatalf("manual move race wrote: %v", tools)
	}
}

func TestBoardCodeFailurePrivacy(t *testing.T) {
	var logs bytes.Buffer
	previous := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer func() { failurelog.Default = previous }()
	boardCodeError("board_refused", errors.New("PRIVATE_CANARY"))
	if !strings.Contains(logs.String(), "board_refused") || !strings.Contains(logs.String(), "cause") || strings.Contains(logs.String(), "PRIVATE_CANARY") {
		t.Fatalf("unsafe diagnostics: %s", logs.String())
	}
}

func TestBoardCodeRepositoryChangeStopsWrite(t *testing.T) {
	card := liveBoardCard{Number: 528, BoardID: "board", ColumnName: "Done", Repos: []string{"original"}}
	history := []protocol.CodeCommit{{SHA: fixtureSHA, Date: "2026-01-01T00:00:00Z", Subject: "Merge BLG-528", Parents: []string{"one", "two"}, Deployed: true}}
	var tools []string
	call := func(_ context.Context, tool string, _ map[string]any, out any) error {
		tools = append(tools, tool)
		if tool == "blerg_card_get" {
			fresh := card
			fresh.Repos = []string{"different"}
			*out.(*liveBoardCard) = fresh
		}
		return nil
	}
	if _, err := reconcileCodeCard(t.Context(), call, card, history, time.Now(), false, true); !errors.Is(err, errCodeCardScopeChanged) {
		t.Fatalf("scope race: %v", err)
	}
	if !reflect.DeepEqual(tools, []string{"blerg_card_comment", "blerg_card_get"}) {
		t.Fatalf("wrote after repository reassignment: %v", tools)
	}
}

func TestBoardCodeManualDoneStillReceivesDeployTag(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	card := liveBoardCard{Number: 528, ColumnName: "Done"}
	card.Trail = append(card.Trail, struct {
		Type      string `json:"type"`
		Actor     string `json:"actor"`
		ToColumn  string `json:"to_column,omitempty"`
		Text      string `json:"text,omitempty"`
		CreatedAt string `json:"created_at"`
	}{Type: "moved", Actor: "human", CreatedAt: at.Add(time.Minute).Format(time.RFC3339)})
	history := []protocol.CodeCommit{{SHA: fixtureSHA, Date: at.Format(time.RFC3339), Subject: "Merge BLG-528", Parents: []string{"one", "two"}, Deployed: true}}
	d := boardCodeDecision(card, history, at.Add(time.Hour), false)
	if !d.Tag || d.Column != "" || d.Drift != "" {
		t.Fatalf("manual Done should retain its column and gain deploy evidence: %+v", d)
	}
}

func TestBoardCodeHistoryCausePrivacy(t *testing.T) {
	if got := boardCodeHistoryCause("main_unavailable"); got != "history_main_unavailable" {
		t.Fatal(got)
	}
	if got := boardCodeHistoryCause("PRIVATE_CANARY"); got != "history_refused" {
		t.Fatal(got)
	}
}

func TestBoardCodeRevertedIntegrationDoesNotMoveNestedCard(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	history := []protocol.CodeCommit{
		{SHA: "revert", Reverts: "integration", Parents: []string{"integration"}},
		{SHA: "integration", Parents: []string{"base", "nested"}, Date: at.Format(time.RFC3339), Subject: "Integrate fixes"},
		{SHA: "nested", Parents: []string{"base", "feature"}, Date: at.Format(time.RFC3339), Subject: "Merge BLG-528: fix", Deployed: true},
		{SHA: "feature", Parents: []string{"base"}},
		{SHA: "base"},
	}
	card := liveBoardCard{Number: 528, ColumnName: "In progress"}
	d := boardCodeDecision(card, history, at, false)
	if d.Column != "" || d.Tag || d.Commit != "" || d.Drift != "merge_reverted" {
		t.Fatalf("reverted integration kept nested merge active: %+v", d)
	}
	history = append([]protocol.CodeCommit{{SHA: "restore", Reverts: "revert", Parents: []string{"revert"}}}, history...)
	d = boardCodeDecision(card, history, at, false)
	if d.Commit != "" || d.Tag {
		t.Fatalf("restored side-branch merge became completion evidence: %+v", d)
	}

	// A merge already on the first parent survives an unrelated integration revert.
	history = history[1:]
	history[1].Parents = []string{"nested", "side"}
	history = append(history, protocol.CodeCommit{SHA: "side", Parents: []string{"nested"}})
	d = boardCodeDecision(card, history, at, false)
	if d.Commit != "nested" || !d.Tag {
		t.Fatalf("integration revert removed a preexisting main merge: %+v", d)
	}
}

func TestBoardCodeOldFastForwardDoneCardHasNoDrift(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	card := liveBoardCard{Number: 548, ColumnName: "Done", UpdatedAt: at.Format(time.RFC3339)}
	history := []protocol.CodeCommit{{SHA: fixtureSHA, Date: at.Format(time.RFC3339), Subject: "Implement BLG-548", Parents: []string{"base"}, Deployed: true}}
	if d := boardCodeDecision(card, history, at.Add(24*time.Hour), false); d.Drift != "" || d.Column != "" || d.Tag {
		t.Fatalf("historical fast-forward card produced drift: %+v", d)
	}
}

func TestBoardCodePartialMergeEvidence(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, subject := range []string{"Merge BLG-514 chunk 4: increment", "Merge live-data blockers (BLG-514, partial): increment"} {
		t.Run(subject, func(t *testing.T) {
			c := liveBoardCard{Number: 514, ColumnName: "In progress"}
			history := []protocol.CodeCommit{{SHA: "partial", Date: at.Format(time.RFC3339), Subject: subject, Parents: []string{"base", "side"}, Deployed: true, BodyCards: []int{514}}}
			var comment string
			d, err := reconcileCodeCard(t.Context(), func(_ context.Context, tool string, args map[string]any, _ any) error {
				if tool != "blerg_card_comment" {
					t.Fatalf("partial merge wrote %s", tool)
				}
				comment = args["text"].(string)
				return nil
			}, c, history, at, false, true)
			if err != nil || d.Tag || d.Commit != "" || d.Column != "" || !strings.Contains(comment, "preserved") {
				t.Fatalf("partial merge: %+v err=%v comment=%q", d, err, comment)
			}
		})
	}
}

// A partial-merge mention on a card already in Done is history, not drift: the
// card was finished by a later closing merge or by the person.
func TestBoardCodePartialMergeOnDoneCardIsQuiet(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c := liveBoardCard{Number: 479, ColumnName: "Done", Tags: []string{"to-test"}}
	history := []protocol.CodeCommit{{SHA: "partial", Date: at.Format(time.RFC3339), Subject: "Merge cluster workers (BLG-479, partial): increment", Parents: []string{"base", "side"}, Deployed: true, BodyCards: []int{479}}}
	if d := boardCodeDecision(c, history, at.Add(time.Hour), false); d.Drift == "partial_merge_preserved" {
		t.Fatalf("done card reported as partial-merge drift: %+v", d)
	}
}
func TestBoardCodeNestedTitleCannotClose(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	history := []protocol.CodeCommit{
		{SHA: "integration", Parents: []string{"base", "nested"}, Subject: "Merge performance (BLG-431, partial): increment", Date: at.Format(time.RFC3339)},
		{SHA: "nested", Parents: []string{"base", "feature"}, Subject: "Merge BLG-431: fix", Date: at.Format(time.RFC3339), Deployed: true},
		{SHA: "base"},
	}
	d := boardCodeDecision(liveBoardCard{Number: 431, ColumnName: "In progress"}, history, at, false)
	if d.Commit != "" || d.Tag || d.Column != "" {
		t.Fatalf("nested title closed card: %+v", d)
	}
}

func TestBoardCodeBusyRetriesOnce(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		calls, waits := 0, 0
		read := func() (protocol.CommitResult, error) {
			calls++
			if permanent || calls == 1 {
				return protocol.CommitResult{}, errCommitBusy
			}
			return protocol.CommitResult{OK: true}, nil
		}
		result, retried, err := readBoardCodeHistory(t.Context(), read, func(context.Context) error { waits++; return nil })
		if calls != 2 || waits != 1 || !retried || (!permanent && (err != nil || !result.OK)) || (permanent && !errors.Is(err, errCommitBusy)) {
			t.Fatalf("calls=%d waits=%d retried=%t result=%+v err=%v", calls, waits, retried, result, err)
		}
	}
}

func TestBoardCodeBusyRetryBoundaries(t *testing.T) {
	for _, tt := range []struct {
		name         string
		result       protocol.CommitResult
		err          error
		canceled     bool
		calls, waits int
	}{
		{name: "daemon busy", result: protocol.CommitResult{ErrorCode: "busy"}, calls: 2, waits: 1},
		{name: "workspace busy", err: errWorkspaceBusy, calls: 2, waits: 1},
		{name: "canceled backoff", err: errCommitBusy, canceled: true, calls: 1, waits: 1},
		{name: "timeout", err: errCommitUnavailable, calls: 1},
		{name: "safety refusal", result: protocol.CommitResult{ErrorCode: "unavailable"}, calls: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls, waits := 0, 0
			_, _, err := readBoardCodeHistory(t.Context(), func() (protocol.CommitResult, error) { calls++; return tt.result, tt.err }, func(context.Context) error {
				waits++
				if tt.canceled {
					return context.Canceled
				}
				return nil
			})
			if calls != tt.calls || waits != tt.waits || (tt.canceled && !errors.Is(err, context.Canceled)) {
				t.Fatalf("calls=%d waits=%d err=%v", calls, waits, err)
			}
		})
	}
}
