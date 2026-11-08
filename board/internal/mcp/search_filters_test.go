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

func TestCardSearchOmitsCommitSummary(t *testing.T) {
	w := newDiffWorld(t)
	board := w.ids["A"]["board_id"]
	for _, paged := range []bool{false, true} {
		args, _ := json.Marshal(map[string]any{"board_id": board, "page": paged})
		res, err := w.srv.dispatch(t.Context(), w.scoped, "blerg_card_search", args)
		if err != nil {
			t.Fatal(err)
		}
		text := contentText(res)
		if strings.Contains(text, "commit_summary") {
			t.Fatalf("search includes heavy summaries: %.300s", text)
		}
	}
}

func TestCardListOmitsCommitSummaryButGetKeepsIt(t *testing.T) {
	w := newDiffWorld(t)
	cards, err := db.ListCards(t.Context(), w.pool, w.ids["A"]["board_id"], false)
	if err != nil || len(cards) == 0 {
		t.Fatalf("list: %v, %d cards", err, len(cards))
	}
	for _, card := range cards {
		raw, _ := json.Marshal(card)
		if strings.Contains(string(raw), "commit_summary") {
			t.Fatal("list includes commit summary")
		}
		full, err := db.GetCard(t.Context(), w.pool, card.ID)
		if err != nil || len(full.CommitSummary) == 0 {
			t.Fatalf("get lost summary: %v", err)
		}
	}
}

func TestCardSearchSummaryOmitsBodies(t *testing.T) {
	w := newDiffWorld(t)
	board := w.ids["A"]["board_id"]
	body := strings.Repeat("long card body ", 40)
	if _, err := w.pool.Exec(t.Context(), `UPDATE cards SET body=$1 WHERE board_id=$2`, body, board); err != nil {
		t.Fatal(err)
	}
	for _, paged := range []bool{false, true} {
		for _, summary := range []bool{false, true} {
			args, _ := json.Marshal(map[string]any{"board_id": board, "page": paged, "summary": summary})
			res, err := w.srv.dispatch(t.Context(), w.scoped, "blerg_card_search", args)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(contentText(res), "long card body"); got == summary {
				t.Fatalf("paged=%v summary=%v carried body=%v", paged, summary, got)
			}
		}
	}
}

func TestCardSearchNumbers(t *testing.T) {
	w := newDiffWorld(t)
	board := w.ids["A"]["board_id"]
	title := "Another card"
	if _, err := db.CreateCard(t.Context(), w.pool, board, db.CardParams{Title: &title}, db.EventMeta{}); err != nil {
		t.Fatal(err)
	}
	cards, err := db.SearchCards(t.Context(), w.pool, db.SearchParams{BoardID: board, Limit: 100})
	if err != nil || len(cards) < 2 {
		t.Fatalf("fixture: %v %d", err, len(cards))
	}
	args, _ := json.Marshal(map[string]any{"board_id": board, "numbers": []int{cards[0].Number}, "page": true, "include_archived": true})
	res, err := w.srv.dispatch(t.Context(), w.scoped, "blerg_card_search", args)
	if err != nil {
		t.Fatal(err)
	}
	var page db.SearchPage
	if err = json.Unmarshal([]byte(contentText(res)), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Cards) != 1 || page.Cards[0].Number != cards[0].Number {
		t.Fatalf("numbers ignored: %+v", page)
	}
	for _, numbers := range []any{[]int{}, []int{0}, []int{-1}, make([]int, 101), "1"} {
		args, _ = json.Marshal(map[string]any{"board_id": board, "numbers": numbers})
		if _, err = w.srv.dispatch(t.Context(), w.scoped, "blerg_card_search", args); err == nil {
			t.Errorf("accepted %v", numbers)
		}
	}
}
