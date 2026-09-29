package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

var snapshotNow = time.Date(2026, 8, 6, 9, 12, 31, 0, time.UTC)

// snapshotFixture: a small board with an inbox and a terminal done column.
func snapshotFixture(inbox, done int) boardSnapshotInput {
	deploy := "https://blerg-board.example"
	board := db.Board{
		ID: "b-1", Name: "test-board", Repos: []string{"blerg-board", "zarnk"},
		GitBase: "https://github.com/blerglab", DeployURL: &deploy, GateEnabled: true,
		FieldSchema: json.RawMessage(`[{"key": "area", "type": "string"}]`),
	}
	cols := []db.Column{
		{ID: "c-inbox", BoardID: "b-1", Name: "inbox"},
		{ID: "c-review", BoardID: "b-1", Name: "review"},
		{ID: "c-done", BoardID: "b-1", Name: "done", IsTerminal: true},
	}
	var cards []db.Card
	n := 0
	add := func(colID string, count int) {
		for i := 0; i < count; i++ {
			n++
			col := colID
			cards = append(cards, db.Card{
				ID: fmt.Sprintf("card-%d", n), BoardID: "b-1", Number: n,
				ColumnID: &col, Type: "bug", Title: fmt.Sprintf("card number %d", n),
				Version:   7,
				UpdatedAt: fmt.Sprintf("2026-08-%02dT00:00:00Z", (n%27)+1),
			})
		}
	}
	add("c-inbox", inbox)
	add("c-done", done)
	return boardSnapshotInput{Board: board, Columns: cols, Cards: cards,
		Run: "running (3 done, 0 stuck this run; 2 ready, 1 in flight now)", Now: snapshotNow}
}

func TestRenderBoardSnapshotCoversBoardState(t *testing.T) {
	out := renderBoardSnapshot(snapshotFixture(3, 2))
	for _, want := range []string{
		"2026-08-06T09:12:31Z", // snapshot timestamp, named in the block header
		"board id: b-1",
		"repos: blerg-board, zarnk",
		"git_base: https://github.com/blerglab",
		"deploy_url: https://blerg-board.example",
		"admission gate: enabled",
		"board run: running (3 done",
		`field_schema: [{"key":"area","type":"string"}]`,
		"### Columns (rank order)",
		"1. inbox",
		"2. review",
		"3. done — terminal",
		"### Cards (5 live, one line each)",
		"**inbox** (3)",
		"- #1 [bug] card number 1 (id card-1)",
		"**review** (0)",
		"**done** (2)",
		"- #4 [bug] card number 4 (id card-4)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("snapshot missing %q:\n%s", want, out)
		}
	}
}

// The brief must tell the session the snapshot ages and that mutations need a
// fresh read — without that line the snapshot teaches it to trust stale data.
func TestRenderBoardSnapshotWarnsAboutStaleness(t *testing.T) {
	out := renderBoardSnapshot(snapshotFixture(1, 1))
	if !strings.Contains(out, "RE-READ anything you are about to change") {
		t.Errorf("snapshot does not instruct a re-read before mutation:\n%s", out)
	}
	if !strings.Contains(out, "point-in-time copy") {
		t.Errorf("snapshot does not label itself a point-in-time copy:\n%s", out)
	}
}

// REST resolves cards by uuid only (GET /api/cards/40 is not a route), so an
// index of bare numbers would send the session back to the cards endpoint for
// every mutation — the exact round-trip the snapshot removes. Every listed
// card carries its id, and the block says so.
func TestRenderBoardSnapshotCarriesCardIDs(t *testing.T) {
	in := snapshotFixture(3, 2)
	out := renderBoardSnapshot(in)
	if !strings.Contains(out, "every REST path under /api/cards/ takes the card's UUID") &&
		!strings.Contains(out, "REST path under /api/cards/ takes the card's UUID") {
		t.Errorf("snapshot does not explain number-vs-uuid:\n%s", out)
	}
	for _, c := range in.Cards {
		if !strings.Contains(out, fmt.Sprintf("- #%d [bug] card number %d (id %s)", c.Number, c.Number, c.ID)) {
			t.Errorf("card #%d listed without its id:\n%s", c.Number, out)
		}
	}
	// a card with no id (nothing to resolve with) must not render "(id )"
	if got := idSuffix(""); got != "" {
		t.Errorf("idSuffix(\"\") = %q, want empty", got)
	}
}

// A stale version passed as if_match is a spurious 409, so no version may
// appear anywhere in the block.
func TestRenderBoardSnapshotOmitsVersionsAndBodies(t *testing.T) {
	in := snapshotFixture(2, 1)
	for i := range in.Cards {
		body := "a body that must not be inlined into the prompt"
		in.Cards[i].Body = &body
	}
	out := renderBoardSnapshot(in)
	if strings.Contains(strings.ToLower(out), "version 7") || strings.Contains(out, "if_match: 7") {
		t.Errorf("snapshot leaks a card version:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "- #") && strings.Contains(strings.ToLower(line), "version") {
			t.Errorf("card index line carries a version: %q", line)
		}
	}
	if strings.Contains(out, "must not be inlined") {
		t.Errorf("snapshot inlined card bodies:\n%s", out)
	}
}

func TestRenderBoardSnapshotElidesLargeTerminalColumn(t *testing.T) {
	out := renderBoardSnapshot(snapshotFixture(2, 412+snapshotTerminalCap))
	if !strings.Contains(out, "**done** (432, most recently updated shown)") {
		t.Errorf("done header does not report the true count:\n%s", out)
	}
	if !strings.Contains(out, "… 412 more cards in done, not shown") {
		t.Errorf("elided cards not counted explicitly:\n%s", out)
	}
	if n := countIndexLines(out, "**done**"); n != snapshotTerminalCap {
		t.Errorf("done listed %d cards, want the cap of %d", n, snapshotTerminalCap)
	}
	// most-recent-N, not first-N: the fixture stamps updated_at cyclically, so
	// #26 (2026-08-27, the newest stamp) must be listed and #4 (2026-08-05,
	// which comes first in board order) must not be
	if !strings.Contains(out, "- #26 ") {
		t.Errorf("terminal column dropped its most recently updated card:\n%s", out)
	}
	if strings.Contains(out, "- #4 ") {
		t.Errorf("terminal column listed an old card, so it truncated by board order:\n%s", out)
	}
}

func TestRenderBoardSnapshotElidesLargeLiveColumn(t *testing.T) {
	out := renderBoardSnapshot(snapshotFixture(snapshotColumnCap+9, 0))
	if !strings.Contains(out, "… 9 more cards in inbox, not shown") {
		t.Errorf("live column elision not counted:\n%s", out)
	}
	if n := countIndexLines(out, "**inbox**"); n != snapshotColumnCap {
		t.Errorf("inbox listed %d cards, want the cap of %d", n, snapshotColumnCap)
	}
}

// Cards with no column (or pointing at a column the caller did not hand us)
// still have to appear — dropping them would make the index quietly wrong.
func TestRenderBoardSnapshotListsUnfiledCards(t *testing.T) {
	in := snapshotFixture(1, 0)
	orphan := "c-gone"
	in.Cards = append(in.Cards,
		db.Card{Number: 90, Title: "no column at all", Type: "chore"},
		db.Card{Number: 91, Title: "column not in list", Type: "chore", ColumnID: &orphan})
	out := renderBoardSnapshot(in)
	if !strings.Contains(out, "**(no column)** (2)") ||
		!strings.Contains(out, "- #90 [chore] no column at all") ||
		!strings.Contains(out, "- #91 [chore] column not in list") {
		t.Errorf("unfiled cards missing from the index:\n%s", out)
	}
}

func TestRenderBoardSnapshotEmptyBoardAndZeroValue(t *testing.T) {
	if got := renderBoardSnapshot(boardSnapshotInput{Now: snapshotNow}); got != "" {
		t.Errorf("zero-value input should render nothing, got:\n%s", got)
	}
	in := snapshotFixture(0, 0)
	in.Board.Repos, in.Board.GitBase, in.Board.DeployURL = nil, "", nil
	in.Board.FieldSchema, in.Board.GateEnabled = json.RawMessage(`[]`), false
	out := renderBoardSnapshot(in)
	for _, want := range []string{
		"repos: (none)", "git_base: (server default)", "deploy_url: (none)",
		"admission gate: disabled", "field_schema: (no custom fields declared)",
		"No live cards on this board.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("empty-board snapshot missing %q:\n%s", want, out)
		}
	}
}

// With no pool there is nothing to read, and a spawn must still succeed —
// the brief just goes out without a snapshot.
func TestBoardSnapshotWithoutPoolDegrades(t *testing.T) {
	a := New(nil, nil, nil)
	if got := a.boardSnapshot(context.Background(), db.Board{ID: "b-1"}); got != "" {
		t.Errorf("boardSnapshot with no pool = %q, want empty", got)
	}
}

func TestSnapshotTitleStaysOneLine(t *testing.T) {
	long := strings.Repeat("x", snapshotTitleMax+40)
	got := snapshotTitle(long)
	if strings.Count(got, "\n") != 0 || len([]rune(got)) != snapshotTitleMax+1 {
		t.Errorf("long title not truncated: %d runes", len([]rune(got)))
	}
	if got := snapshotTitle("wrapped\ntitle  with   gaps"); got != "wrapped title with gaps" {
		t.Errorf("title whitespace not collapsed: %q", got)
	}
}

// countIndexLines counts the "- #N ..." lines in the group whose header is
// the given string, up to the next blank-line-separated header.
func countIndexLines(out, header string) int {
	idx := strings.Index(out, header)
	if idx < 0 {
		return -1
	}
	n := 0
	for _, line := range strings.Split(out[idx:], "\n")[1:] {
		if strings.HasPrefix(line, "- #") {
			n++
			continue
		}
		if strings.HasPrefix(line, "**") {
			break
		}
	}
	return n
}
