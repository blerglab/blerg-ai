package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

func TestCreateBoardTerminalColumns(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{
		Columns:         []string{"Open", "Finished"},
		TerminalColumns: []string{"Finished"},
	})
	cols, err := db.ListColumns(ctx, pool, b.ID)
	if err != nil || len(cols) != 2 {
		t.Fatalf("columns = %v (%v)", cols, err)
	}
	if cols[0].IsTerminal || !cols[1].IsTerminal {
		t.Errorf("is_terminal = %v, %v; want false, true", cols[0].IsTerminal, cols[1].IsTerminal)
	}
}

// A failure while inserting a later column must roll the whole board back:
// board row, repos and earlier columns included.
func TestCreateBoardIsTransactional(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE FUNCTION t44_fail() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN IF NEW.name = 'Boom' THEN RAISE EXCEPTION 'boom'; END IF; RETURN NEW; END $$`,
		`CREATE TRIGGER t44_fail BEFORE INSERT ON board_columns FOR EACH ROW EXECUTE FUNCTION t44_fail()`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DROP TRIGGER IF EXISTS t44_fail ON board_columns`)
		_, _ = pool.Exec(ctx, `DROP FUNCTION IF EXISTS t44_fail()`)
	})
	_, err := db.CreateBoard(ctx, pool, db.BoardParams{
		Name: "half", Columns: []string{"Inbox", "Boom", "Done"},
	})
	if err == nil {
		t.Fatal("expected the column insert to fail")
	}
	var boards, cols int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM boards`).Scan(&boards); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM board_columns`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if boards != 0 || cols != 0 {
		t.Errorf("failed create left %d boards and %d columns behind", boards, cols)
	}
}

func TestMoveColumnReordersWithinBoard(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})

	cols, err := db.ListColumns(ctx, pool, b.ID)
	if err != nil || len(cols) != 4 {
		t.Fatalf("expected 4 default columns, got %d (%v)", len(cols), err)
	}
	// Defaults: inbox, ready, in progress, done.
	inbox, ready, inProgress, done := cols[0], cols[1], cols[2], cols[3]

	// Move "done" to sit before "in progress": inbox, ready, done, in progress.
	if _, err := db.MoveColumn(ctx, pool, done.ID, &inProgress.ID); err != nil {
		t.Fatalf("MoveColumn: %v", err)
	}
	got, err := db.ListColumns(ctx, pool, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{inbox.Name, ready.Name, done.Name, inProgress.Name}
	for i, c := range got {
		if c.Name != wantOrder[i] {
			t.Errorf("position %d = %q, want %q (full order: %v)", i, c.Name, wantOrder[i], names(got))
		}
	}

	// Move "inbox" to the end (no before_id).
	if _, err := db.MoveColumn(ctx, pool, inbox.ID, nil); err != nil {
		t.Fatalf("MoveColumn to end: %v", err)
	}
	got, err = db.ListColumns(ctx, pool, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got[len(got)-1].Name != "inbox" {
		t.Errorf("last column = %q, want inbox (full order: %v)", got[len(got)-1].Name, names(got))
	}
}

func TestMoveColumnUnknownIDReturnsNotFound(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	if _, err := db.MoveColumn(ctx, pool, "00000000-0000-0000-0000-000000000000", nil); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("MoveColumn(missing): got %v, want ErrNotFound", err)
	}
}

func TestGetColumn(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	cols, _ := db.ListColumns(ctx, pool, b.ID)

	c, err := db.GetColumn(ctx, pool, cols[0].ID)
	if err != nil {
		t.Fatalf("GetColumn: %v", err)
	}
	if c.BoardID != b.ID || c.Name != cols[0].Name {
		t.Errorf("GetColumn = %+v, want board %s name %s", c, b.ID, cols[0].Name)
	}

	if _, err := db.GetColumn(ctx, pool, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("GetColumn(missing): got %v, want ErrNotFound", err)
	}
}

func names(cols []db.Column) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Name
	}
	return out
}

// The board settings panel in the web UI (web/src/BoardSettings.tsx) reads the
// board's model config straight off GET /api/boards/{id}. Dropping or renaming
// one of these json tags blanks a row in that panel with no build error on
// either side, so pin the wire names here.
func TestBoardJSONCarriesModelConfig(t *testing.T) {
	raw, err := json.Marshal(db.Board{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"model", "reviewer_model", "discuss_model", "chat_model", "git_base", "repos"} {
		if _, ok := got[key]; !ok {
			t.Errorf("board JSON is missing %q — the board settings panel reads it", key)
		}
	}
}

// ── ci_policy ───────────────────────────────────────────────────────────────

// The default is the behaviour that predates the column: a board nobody has
// configured must still insist on a green check. Anything else would silently
// loosen every existing board on migration.
func TestBoardCIPolicyDefaultsToRequired(t *testing.T) {
	pool := testPool(t)
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})
	if b.CIPolicy != db.CIRequired {
		t.Errorf("new board ci_policy = %q, want %q", b.CIPolicy, db.CIRequired)
	}
}

func TestBoardCIPolicyRoundTrips(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})

	policy := db.CIIfPresent
	updated, err := db.UpdateBoard(ctx, pool, b.ID, db.BoardParams{CIPolicy: &policy})
	if err != nil {
		t.Fatalf("UpdateBoard: %v", err)
	}
	if updated.CIPolicy != db.CIIfPresent {
		t.Errorf("after update, ci_policy = %q, want %q", updated.CIPolicy, db.CIIfPresent)
	}
	// and it survives a re-read, i.e. it is the column and not just the
	// RETURNING row
	got, err := db.GetBoard(ctx, pool, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CIPolicy != db.CIIfPresent {
		t.Errorf("re-read ci_policy = %q, want %q", got.CIPolicy, db.CIIfPresent)
	}
}

// An unknown value must come back as a FieldError naming the field, so the API
// answers 422 with something an agent can act on rather than a 500 out of the
// column's CHECK constraint.
func TestBoardCIPolicyRejectsUnknownValue(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}})

	for _, bad := range []string{"none", "ignored", "REQUIRED", "yes"} {
		policy := bad
		_, err := db.UpdateBoard(ctx, pool, b.ID, db.BoardParams{CIPolicy: &policy})
		var fe *db.FieldError
		if !errors.As(err, &fe) {
			t.Errorf("UpdateBoard(ci_policy=%q): err = %v, want a FieldError", bad, err)
			continue
		}
		if fe.Key != "ci_policy" {
			t.Errorf("FieldError.Key = %q, want ci_policy", fe.Key)
		}
	}
	// the rejected writes left the board alone
	got, err := db.GetBoard(ctx, pool, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CIPolicy != db.CIRequired {
		t.Errorf("ci_policy = %q after rejected writes, want it unchanged at %q", got.CIPolicy, db.CIRequired)
	}
}

// CreateBoard takes the same value, so a board can be born with it rather than
// needing a second call.
func TestCreateBoardAcceptsCIPolicy(t *testing.T) {
	pool := testPool(t)
	policy := db.CIIfPresent
	b := mkBoard(t, pool, db.BoardParams{Repos: []string{"blerg-board"}, CIPolicy: &policy})
	if b.CIPolicy != db.CIIfPresent {
		t.Errorf("created board ci_policy = %q, want %q", b.CIPolicy, db.CIIfPresent)
	}
}
