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
