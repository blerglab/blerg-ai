package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// The parent's answer to its child's question is recorded as the parent's (answered_by
// "session"), from the parent's own token, so the child's chat never labels it "You".
func TestParentAnswerToChildAskIsTheParents(t *testing.T) {
	fx := newDelegFx(t)
	ctx := context.Background()
	child := fx.childOf(fx.parent)
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	ask, err := db.CreateMessage(ctx, fx.pool, child, "ask", "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	row, _ := db.GetMessage(ctx, fx.pool, ask)
	fx.api.recordMessageEvent(ctx, *fx.row(child), *row)

	code, out, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/children/"+child+"/message", fx.parentTok, map[string]any{"text": "main"}, nil)
	if code != http.StatusAccepted || out["answered_ask"] != true {
		t.Fatalf("%d %v", code, out)
	}
	var raw string
	if err := fx.pool.QueryRow(ctx, `SELECT payload::text FROM agent_events WHERE session_id=$1 AND kind='user_message' ORDER BY seq DESC LIMIT 1`, child).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	_ = json.Unmarshal([]byte(raw), &p)
	if p["source"] != "ask_answer" || p["answered_by"] != "session" || p["text"] != "main" {
		t.Fatalf("the parent's answer was recorded as %s", raw)
	}

	// The person's own answer stays theirs.
	ask2, _ := db.CreateMessage(ctx, fx.pool, child, "ask", "Ship it?")
	row2, _ := db.GetMessage(ctx, fx.pool, ask2)
	fx.api.recordMessageEvent(ctx, *fx.row(child), *row2)
	if _, apiErr := fx.api.SendMessage(ctx, runnerPrincipal{Kind: "human", Sub: "owner-1"}, child, "yes", "human"); apiErr != nil {
		t.Fatal(apiErr.Message)
	}
	if err := fx.pool.QueryRow(ctx, `SELECT payload::text FROM agent_events WHERE session_id=$1 AND kind='user_message' ORDER BY seq DESC LIMIT 1`, child).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	p = map[string]any{}
	_ = json.Unmarshal([]byte(raw), &p)
	if p["source"] != "ask_answer" || p["answered_by"] != nil {
		t.Fatalf("the person's answer was recorded as %s", raw)
	}
}

// Only a parent's own token sends as "session": any other caller claiming it is refused.
func TestSessionSourceIsTheServers(t *testing.T) {
	fx := newDelegFx(t)
	child := fx.childOf(fx.parent)
	_, apiErr := fx.api.SendMessage(context.Background(), runnerPrincipal{Kind: "human", Sub: "owner-1"}, child, "do it", "session")
	if apiErr == nil || apiErr.Status != http.StatusUnprocessableEntity {
		t.Fatalf("a caller claimed to be the parent: %+v", apiErr)
	}
}
