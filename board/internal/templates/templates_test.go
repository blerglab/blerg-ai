package templates_test

import (
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
	"github.com/blerglab/blerg-ai/board/internal/templates"
)

func TestFocusBoardShape(t *testing.T) {
	tpl, ok := templates.Get(templates.FocusBoard)
	if !ok {
		t.Fatal("focus-board template missing")
	}
	var names []string
	var terminal []string
	for _, c := range tpl.Columns {
		names = append(names, c.Name)
		if c.Terminal {
			terminal = append(terminal, c.Name)
		}
	}
	want := []string{"Inbox", "Today", "This week", "Waiting on", "Someday", "Proposed", "Done"}
	if len(names) != len(want) {
		t.Fatalf("columns = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("columns = %v, want %v", names, want)
		}
	}
	if len(terminal) != 1 || terminal[0] != "Done" {
		t.Errorf("terminal columns = %v, want [Done]", terminal)
	}
	if tpl.Name == "" || tpl.Description == "" {
		t.Error("template needs a name and description")
	}
}

func TestFocusBoardSchemaValidatesCards(t *testing.T) {
	tpl, _ := templates.Get(templates.FocusBoard)
	defs, err := db.ParseFieldSchema(tpl.FieldSchema)
	if err != nil {
		t.Fatalf("schema does not parse: %v", err)
	}
	byKey := map[string]db.FieldDef{}
	for _, d := range defs {
		byKey[d.Key] = d
	}
	if len(defs) != 3 {
		t.Fatalf("want exactly source, due, tracking; got %d fields", len(defs))
	}
	src := byKey["source"]
	if src.Type != "enum" || !src.Filterable || src.Display == "" || src.Display == "hidden" {
		t.Errorf("source must be a displayed, filterable enum: %+v", src)
	}
	if byKey["due"].Type != "timestamp" || byKey["tracking"].Type != "text" {
		t.Errorf("due/tracking types wrong: %+v %+v", byKey["due"], byKey["tracking"])
	}
	good := map[string]any{"source": "email", "due": "2026-10-01T09:00:00Z", "tracking": "waiting on a reply"}
	if err := db.ValidateFields(defs, good); err != nil {
		t.Errorf("valid fields rejected: %v", err)
	}
	for _, s := range []string{"email", "calendar", "manual"} {
		if err := db.ValidateFields(defs, map[string]any{"source": s}); err != nil {
			t.Errorf("source %q rejected: %v", s, err)
		}
	}
	if err := db.ValidateFields(defs, map[string]any{"source": "fax"}); err == nil {
		t.Error("bad source accepted")
	}
}

func TestUnknownTemplate(t *testing.T) {
	if _, ok := templates.Get("nope"); ok {
		t.Error("unknown id resolved")
	}
	if got := templates.List(); len(got) == 0 || got[0].ID != templates.FocusBoard {
		t.Errorf("List = %+v", got)
	}
}
