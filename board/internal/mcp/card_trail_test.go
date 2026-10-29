package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/board/internal/db"
)

// blerg_card_get with_trail returns, in the one call another app needs to show the live card, its
// column's name, the board's columns and its latest trail events (a comment with its text), and
// nothing of another board. Without it the card comes back as before.
func TestCardGetWithTrail(t *testing.T) {
	w := newDiffWorld(t)
	ctx := context.Background()
	a := w.ids["A"]
	if err := db.AppendComment(ctx, w.pool, a["card_id"], "Looked at the flaky test", db.EventMeta{Actor: "agent"}); err != nil {
		t.Fatal(err)
	}
	res, err := w.srv.dispatch(ctx, w.scoped, "blerg_card_get", json.RawMessage(`{"card_id":`+jstr(a["card_id"])+`,"with_trail":true}`))
	if err != nil {
		t.Fatal(err)
	}
	text := contentText(res)
	var got struct {
		Title      string `json:"title"`
		ColumnName string `json:"column_name"`
		Columns    []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"columns"`
		Trail []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"trail"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("with_trail reply: %v\n%s", err, text)
	}
	if got.Title != "zebra in A" || got.ColumnName != "Inbox-A" || len(got.Columns) == 0 || got.Columns[len(got.Columns)-1].ID != a["column_id"] {
		t.Fatalf("card = %+v", got)
	}
	last := got.Trail[len(got.Trail)-1]
	if last.Type != "comment" || last.Text != "Looked at the flaky test" {
		t.Fatalf("trail = %+v, want the comment last", got.Trail)
	}
	for _, mark := range w.bMarks {
		if strings.Contains(text, mark) {
			t.Fatalf("board B leaked into the reply: %q", mark)
		}
	}
	plain, err := w.srv.dispatch(ctx, w.scoped, "blerg_card_get", json.RawMessage(`{"card_id":`+jstr(a["card_id"])+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(contentText(plain), `"trail"`) {
		t.Fatal("blerg_card_get without with_trail returned a trail")
	}
}

// contentText is the text of a tool result's first content item.
func contentText(res map[string]any) string {
	items, _ := res["content"].([]map[string]any)
	if len(items) == 0 {
		return ""
	}
	text, _ := items[0]["text"].(string)
	return text
}

func TestKeyReferencesResolveAliasesAndEnforceScope(t *testing.T) {
	w := newDiffWorld(t)
	ctx := t.Context()
	for side, key := range map[string]string{"A": "BLG", "B": "AD"} {
		if _, err := db.UpdateBoard(ctx, w.pool, w.ids[side]["board_id"], db.BoardParams{Key: &key}); err != nil {
			t.Fatal(err)
		}
	}
	renamed := "NEW"
	if _, err := db.UpdateBoard(ctx, w.pool, w.ids["A"]["board_id"], db.BoardParams{Key: &renamed}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"BLG", "NEW"} {
		args, _ := json.Marshal(map[string]any{"reference": key + "-1", "with_trail": true})
		res, err := w.srv.dispatch(ctx, w.scoped, "blerg_card_get", args)
		if err != nil {
			t.Fatal(err)
		}
		var card struct {
			Reference string `json:"reference"`
		}
		if err := json.Unmarshal([]byte(contentText(res)), &card); err != nil || card.Reference != "NEW-1" {
			t.Fatalf("key %s = %v: %v", key, card, err)
		}
	}
	if _, err := w.srv.dispatch(ctx, w.scoped, "blerg_card_get", json.RawMessage(`{"reference":"AD-1"}`)); err == nil {
		t.Fatal("key bypassed board scope")
	}
	if _, err := w.srv.dispatch(ctx, w.scoped, "blerg_card_get", json.RawMessage(`{"reference":"NEW-1","number":2}`)); err == nil {
		t.Fatal("accepted conflicting references")
	}
}

func TestCardSearchPagingKeepsLegacyArrayAndScope(t *testing.T) {
	w := newDiffWorld(t)
	board := w.ids["A"]["board_id"]
	args, _ := json.Marshal(map[string]any{"board_id": board, "limit": 1, "page": true})
	res, err := w.srv.dispatch(t.Context(), w.scoped, "blerg_card_search", args)
	if err != nil {
		t.Fatal(err)
	}
	var page db.SearchPage
	if err := json.Unmarshal([]byte(contentText(res)), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Cards) != 1 {
		t.Fatalf("page = %+v", page)
	}
	if page.Cards[0].BoardID != board {
		t.Fatal("search escaped board scope")
	}
	// Add enough to require a cursor, then traverse the same MCP path again.
	title := "Another card"
	if _, err := db.CreateCard(t.Context(), w.pool, board, db.CardParams{Title: &title}, db.EventMeta{}); err != nil {
		t.Fatal(err)
	}
	res, err = w.srv.dispatch(t.Context(), w.scoped, "blerg_card_search", args)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(contentText(res)), &page); err != nil {
		t.Fatal(err)
	}
	if page.NextCursor == "" {
		t.Fatal("missing next_cursor")
	}
	first := page.Cards[0].ID
	args, _ = json.Marshal(map[string]any{"board_id": board, "limit": 1, "cursor": page.NextCursor})
	res, err = w.srv.dispatch(t.Context(), w.scoped, "blerg_card_search", args)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(contentText(res)), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Cards) != 1 || page.Cards[0].ID == first || page.NextCursor != "" {
		t.Fatalf("last page = %+v", page)
	}
	args, _ = json.Marshal(map[string]any{"board_id": board})
	res, err = w.srv.dispatch(t.Context(), w.scoped, "blerg_card_search", args)
	if err != nil {
		t.Fatal(err)
	}
	var legacy []db.Card
	if err := json.Unmarshal([]byte(contentText(res)), &legacy); err != nil {
		t.Fatal("legacy search changed shape", err)
	}
	args, _ = json.Marshal(map[string]any{"board_id": w.ids["B"]["board_id"], "page": true})
	if _, err = w.srv.dispatch(t.Context(), w.scoped, "blerg_card_search", args); err == nil {
		t.Fatal("paged search bypassed scope")
	}
}
