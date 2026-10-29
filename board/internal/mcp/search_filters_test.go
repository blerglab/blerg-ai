package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

func TestCardSearchColumn(t *testing.T) {
	w := newDiffWorld(t)
	board := w.ids["A"]["board_id"]
	cols, err := db.ListColumns(t.Context(), w.pool, board)
	if err != nil {
		t.Fatal(err)
	}
	title := "In another column"
	if _, err := db.CreateCard(t.Context(), w.pool, board, db.CardParams{Title: &title, ColumnID: &cols[1].ID}, db.EventMeta{}); err != nil {
		t.Fatal(err)
	}
	for _, column := range []any{strings.ToUpper(cols[1].Name), cols[1].ID, []string{cols[1].ID}, []string{cols[1].Name, cols[1].ID}} {
		args, _ := json.Marshal(map[string]any{"board_id": board, "column": column, "limit": 1, "page": true})
		res, err := w.srv.dispatch(t.Context(), w.scoped, "blerg_card_search", args)
		if err != nil {
			t.Fatal(err)
		}
		var page db.SearchPage
		if err := json.Unmarshal([]byte(contentText(res)), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Cards) != 1 || page.Cards[0].Title != title || page.NextCursor != "" {
			t.Errorf("column %v: %+v", column, page)
		}
	}
	args, _ := json.Marshal(map[string]any{"board_id": board, "column": []string{"unknown"}})
	if _, err := w.srv.dispatch(t.Context(), w.scoped, "blerg_card_search", args); err == nil || !strings.Contains(err.Error(), cols[0].Name) {
		t.Errorf("unknown column: %v", err)
	}
	for _, column := range []any{42, true, []any{"inbox", 42}} {
		args, _ := json.Marshal(map[string]any{"board_id": board, "column": column})
		if _, err := w.srv.dispatch(t.Context(), w.scoped, "blerg_card_search", args); err == nil || !strings.Contains(err.Error(), "column") || strings.Contains(err.Error(), "board_id required") {
			t.Errorf("invalid column %v: %v", column, err)
		}
	}
}

func TestCardSearchColumnSchema(t *testing.T) {
	for _, def := range toolDefs {
		if def["name"] != "blerg_card_search" {
			continue
		}
		raw, err := json.Marshal(def)
		if err != nil {
			t.Fatal(err)
		}
		var tool struct {
			Description string `json:"description"`
			InputSchema struct {
				Properties map[string]struct {
					Type  string `json:"type"`
					Items struct {
						Type string `json:"type"`
					} `json:"items"`
					AnyOf       []any  `json:"anyOf"`
					Description string `json:"description"`
				} `json:"properties"`
			} `json:"inputSchema"`
		}
		if err := json.Unmarshal(raw, &tool); err != nil {
			t.Fatal(err)
		}
		col := tool.InputSchema.Properties["column"]
		// A plain array (no anyOf, which some clients flatten); the server also accepts a string.
		if col.Type != "array" || col.Items.Type != "string" || col.AnyOf != nil || !strings.Contains(col.Description, "case-insensitive") || !strings.Contains(tool.Description, "column") {
			t.Fatalf("column schema: %s", raw)
		}
		return
	}
	t.Fatal("missing card search tool")
}
