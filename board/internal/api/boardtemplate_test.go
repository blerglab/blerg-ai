package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

const tplToken = "svc-key-123"

func TestCreateBoardFocusTemplate(t *testing.T) {
	srv, pool := testServer(t)
	resp := request(t, srv, "POST", "/api/boards", tplToken, nil,
		map[string]any{"name": "me", "template": "focus-board"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var b db.Board
	decodeBody(t, resp, &b)

	cols, err := db.ListColumns(t.Context(), pool, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Inbox", "Today", "This week", "Waiting on", "Someday", "Proposed", "Done"}
	if len(cols) != len(want) {
		t.Fatalf("columns = %v", cols)
	}
	for i, c := range cols {
		if c.Name != want[i] {
			t.Errorf("column %d = %q, want %q", i, c.Name, want[i])
		}
		if c.IsTerminal != (c.Name == "Done") {
			t.Errorf("column %q is_terminal = %v", c.Name, c.IsTerminal)
		}
	}
	defs, err := db.ParseFieldSchema(b.FieldSchema)
	if err != nil || len(defs) != 3 {
		t.Fatalf("field schema = %s (%v)", b.FieldSchema, err)
	}
	// Admission gate defaults stay at the board defaults.
	if b.GateEnabled || b.GateOnUnavailable != "open" || b.GateOnDispute != "open" {
		t.Errorf("gate defaults changed: %+v", b)
	}

	// A card using the template's fields is accepted; a bad source is not.
	ok := request(t, srv, "POST", "/api/boards/"+b.ID+"/cards", tplToken, nil, map[string]any{
		"column_id": cols[0].ID, "title": "reply to the accountant",
		"fields": map[string]any{"source": "email", "due": "2026-10-01T09:00:00Z", "tracking": "thread 42"},
	})
	if ok.StatusCode != http.StatusCreated {
		t.Errorf("valid card status = %d", ok.StatusCode)
	}
	ok.Body.Close()
	bad := request(t, srv, "POST", "/api/boards/"+b.ID+"/cards", tplToken, nil, map[string]any{
		"column_id": cols[0].ID, "title": "x", "fields": map[string]any{"source": "fax"},
	})
	if bad.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("bad source status = %d, want 422", bad.StatusCode)
	}
	bad.Body.Close()
}

func TestCreateBoardUnknownTemplate(t *testing.T) {
	srv, pool := testServer(t)
	resp := request(t, srv, "POST", "/api/boards", tplToken, nil,
		map[string]any{"name": "me", "template": "nope"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
	boards, err := db.ListBoards(t.Context(), pool)
	if err != nil || len(boards) != 0 {
		t.Errorf("unknown template created boards: %v (%v)", boards, err)
	}
}

func TestCreateBoardTemplateRejectsColumnsAndSchema(t *testing.T) {
	srv, pool := testServer(t)
	field := []any{map[string]any{"key": "x", "type": "text"}}
	for name, body := range map[string]map[string]any{
		"columns": {"name": "me", "template": "focus-board", "columns": []string{"a"}},
		"schema":  {"name": "me", "template": "focus-board", "field_schema": field},
		"both":    {"name": "me", "template": "focus-board", "columns": []string{"a"}, "field_schema": field},
	} {
		resp := request(t, srv, "POST", "/api/boards", tplToken, nil, body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, resp.StatusCode)
		}
		var e struct {
			Error string `json:"error"`
		}
		decodeBody(t, resp, &e)
		if !strings.Contains(e.Error, "template") || !strings.Contains(e.Error, "columns or field_schema") {
			t.Errorf("%s: error %q does not say what to change", name, e.Error)
		}
	}
	boards, err := db.ListBoards(t.Context(), pool)
	if err != nil || len(boards) != 0 {
		t.Errorf("refused creates left boards behind: %v (%v)", boards, err)
	}
}

// An EMPTY columns / field_schema next to a template says nothing (a form that
// always sends field_schema: [] must still be able to pick a template), so it
// is treated as absent and the template's own columns and schema apply.
func TestCreateBoardTemplateAcceptsEmptyColumnsAndSchema(t *testing.T) {
	srv, pool := testServer(t)
	for name, body := range map[string]map[string]any{
		"empty schema":  {"name": "a", "template": "focus-board", "field_schema": []any{}},
		"null schema":   {"name": "b", "template": "focus-board", "field_schema": nil},
		"empty columns": {"name": "c", "template": "focus-board", "columns": []string{}},
		"both empty":    {"name": "d", "template": "focus-board", "columns": []string{}, "field_schema": []any{}},
	} {
		resp := request(t, srv, "POST", "/api/boards", tplToken, nil, body)
		if resp.StatusCode != http.StatusCreated {
			t.Errorf("%s: status = %d, want 201", name, resp.StatusCode)
			resp.Body.Close()
			continue
		}
		var b db.Board
		decodeBody(t, resp, &b)
		cols, err := db.ListColumns(t.Context(), pool, b.ID)
		if err != nil || len(cols) != 7 {
			t.Errorf("%s: columns = %d (%v), want the template's 7", name, len(cols), err)
		}
		if defs, err := db.ParseFieldSchema(b.FieldSchema); err != nil || len(defs) != 3 {
			t.Errorf("%s: schema = %s (%v), want the template's 3 fields", name, b.FieldSchema, err)
		}
	}
}

// A failure partway through applying a template (here the 5th column insert)
// leaves no board, no repos and no columns: the board row, its columns and its
// schema are written by one transaction.
func TestCreateBoardTemplateFailureLeavesNoBoard(t *testing.T) {
	srv, pool := testServer(t)
	ctx := t.Context()
	for _, stmt := range []string{
		`CREATE FUNCTION t37_fail() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN IF NEW.name = 'Someday' THEN RAISE EXCEPTION 'boom'; END IF; RETURN NEW; END $$`,
		`CREATE TRIGGER t37_fail BEFORE INSERT ON board_columns FOR EACH ROW EXECUTE FUNCTION t37_fail()`,
	} {
		if _, err := pool.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS t37_fail ON board_columns`)
		_, _ = pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS t37_fail()`)
	})
	resp := request(t, srv, "POST", "/api/boards", tplToken, nil,
		map[string]any{"name": "half", "template": "focus-board"})
	if resp.StatusCode < 400 {
		t.Fatalf("status = %d, want a failure", resp.StatusCode)
	}
	resp.Body.Close()
	var boards, cols int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM boards`).Scan(&boards); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM board_columns`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if boards != 0 || cols != 0 {
		t.Errorf("failed template create left %d boards and %d columns behind", boards, cols)
	}
}

func TestCreateBoardWithoutTemplateUnchanged(t *testing.T) {
	srv, pool := testServer(t)
	resp := request(t, srv, "POST", "/api/boards", tplToken, nil, map[string]any{"name": "plain"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var b db.Board
	decodeBody(t, resp, &b)
	cols, err := db.ListColumns(t.Context(), pool, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"inbox", "ready", "in progress", "done"}
	if len(cols) != len(want) {
		t.Fatalf("columns = %v", cols)
	}
	for i, c := range cols {
		if c.Name != want[i] || c.IsTerminal {
			t.Errorf("column %d = %+v, want %q non-flagged", i, c, want[i])
		}
	}
	if string(b.FieldSchema) != "[]" {
		t.Errorf("field_schema = %s, want []", b.FieldSchema)
	}

	// Explicit columns and schema still pass through untouched.
	resp = request(t, srv, "POST", "/api/boards", tplToken, nil, map[string]any{
		"name": "custom", "columns": []string{"a", "b"},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("custom status = %d", resp.StatusCode)
	}
	decodeBody(t, resp, &b)
	cols, _ = db.ListColumns(t.Context(), pool, b.ID)
	if len(cols) != 2 || cols[0].Name != "a" {
		t.Errorf("custom columns = %v", cols)
	}
}
