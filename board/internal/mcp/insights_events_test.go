package mcp

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBoardGetTimeEventsRespectsScope(t *testing.T) {
	w := newDiffWorld(t)
	ctx := t.Context()
	a, b := w.ids["A"]["board_id"], w.ids["B"]["board_id"]
	now := time.Now().UTC()
	if _, err := w.pool.Exec(ctx, `INSERT INTO deployments(board_id,env,status,detail,created_at) VALUES($1,'production','ok','canary-private-detail',$3),($2,'private','ok','canary-private-detail',$3)`, a, b, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	args := func(board string) json.RawMessage {
		raw, _ := json.Marshal(map[string]string{"board_id": board, "events_from": now.Add(-7 * 24 * time.Hour).Format(time.RFC3339Nano), "events_to": now.Format(time.RFC3339Nano)})
		return raw
	}
	res, err := w.srv.dispatch(ctx, w.scoped, "blerg_board_get", args(a))
	if err != nil {
		t.Fatal(err)
	}
	text := contentText(res)
	if !strings.Contains(text, "Deployed production") || strings.Contains(text, "canary-private-detail") || strings.Contains(text, "Deployed private") {
		t.Fatalf("events: %s", text)
	}
	if _, err = w.srv.dispatch(ctx, w.scoped, "blerg_board_get", args(b)); err == nil {
		t.Fatal("foreign board events accepted")
	}
	plain, err := w.srv.dispatch(ctx, w.scoped, "blerg_board_get", json.RawMessage(`{"board_id":`+jstr(a)+`}`))
	if err != nil || strings.Contains(contentText(plain), `"events"`) {
		t.Fatal("plain board response changed")
	}
	if _, err = w.srv.dispatch(ctx, w.scoped, "blerg_board_get", json.RawMessage(`{"board_id":`+jstr(a)+`,"events_from":"bad","events_to":"bad"}`)); err == nil {
		t.Fatal("invalid range accepted")
	}
}
