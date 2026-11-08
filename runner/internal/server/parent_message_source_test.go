package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
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

	code, out, _ := fx.do("POST", "/api/sessions/"+fx.parent+"/children/"+child+"/questions/"+ask+"/answer", fx.parentTok, map[string]any{"answer": "main"}, nil)
	if code != http.StatusOK {
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

	var parentBy string
	if err := fx.pool.QueryRow(ctx, `SELECT answered_by FROM messages WHERE id=$1`, ask).Scan(&parentBy); err != nil || parentBy != "session:"+fx.parent {
		t.Fatalf("explicit parent answer audit: %q %v", parentBy, err)
	}
	// After escalation, the person's own answer stays theirs.
	ask2, _ := db.CreateMessage(ctx, fx.pool, child, "ask", "Ship it?")
	row2, _ := db.GetMessage(ctx, fx.pool, ask2)
	fx.api.recordMessageEvent(ctx, *fx.row(child), *row2)
	if err := fx.api.escalateChildQuestion(ctx, fx.row(child), fx.row(fx.parent), ask2); err != nil {
		t.Fatal(err)
	}
	replyAs(t, fx, ask2, fx.human("owner-1"), "yes")
	if err := fx.pool.QueryRow(ctx, `SELECT payload::text FROM agent_events WHERE session_id=$1 AND kind='user_message' ORDER BY seq DESC LIMIT 1`, child).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	p = map[string]any{}
	_ = json.Unmarshal([]byte(raw), &p)
	var personBy string
	if err := fx.pool.QueryRow(ctx, `SELECT answered_by FROM messages WHERE id=$1`, ask2).Scan(&personBy); err != nil || personBy != "person:owner-1" {
		t.Fatalf("explicit person answer audit: %q %v", personBy, err)
	}
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

func TestParentMessageCannotAnswerEscalatedQuestion(t *testing.T) {
	fx := newDelegFx(t)
	child := fx.childOf(fx.parent)
	fx.hub.SetSessionOwner(child, fx.dc.ID)
	id, err := db.CreateMessage(t.Context(), fx.pool, child, "ask", "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	row, _ := db.GetMessage(t.Context(), fx.pool, id)
	fx.api.recordMessageEvent(t.Context(), *fx.row(child), *row)
	if err = fx.api.escalateChildQuestion(t.Context(), fx.row(child), fx.row(fx.parent), id); err != nil {
		t.Fatal(err)
	}
	code, out, _ := fx.do("POST", childRoute(fx.parent, child, "/message"), fx.parentTok, map[string]any{"text": "main"}, nil)
	if code != http.StatusConflict || out["answered_ask"] == true || !strings.Contains(asString(out["error"]), "waiting on the person") {
		t.Fatalf("person-only blocked ask did not refuse parent input clearly: %d %v", code, out)
	}
	row, _ = db.GetMessage(t.Context(), fx.pool, id)
	if row.Status != "open" || row.Answer != nil || row.AnsweredBy != "" {
		t.Fatal("parent message changed escalated question")
	}
}
