package server

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

// An engine's command approval (posted owner_only) is answered only by the
// session's signed-in owner: not the daemon token, not a board token of the
// session's board, not an agent token acting for the owner, and not another
// person who can see the session. A plain question keeps its wider repliers.
func TestOwnerOnlyAskAcceptsOnlyTheSignedInOwner(t *testing.T) {
	f := newPrivFixture(t)
	ctx := context.Background()
	board, err := db.CreateBoard(ctx, f.pool, "approvals", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET board_id=$1 WHERE id=$2`, board.ID, f.pubSession); err != nil {
		t.Fatal(err)
	}
	boardTok, _, err := db.MintBoardToken(ctx, f.pool, board.ID, f.pubSession, []string{"board", "message"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ownerTok := f.tok(t, "human", privAcctA, coreAuthBrowserCap, "session.start")
	otherTok := f.tok(t, "human", privAcctB, coreAuthBrowserCap, "session.start")
	agentTok := f.tok(t, "agent", privAcctA, coreAuthBrowserCap, "session.start")

	create := func(ownerOnly bool) string {
		t.Helper()
		body := `{"session_id":"` + f.pubSession + `","kind":"ask","body":"approve?"`
		if ownerOnly {
			body += `,"owner_only":true`
		}
		rec := privDo(f.api.HandlePostMessages, "POST", "/api/messages", "", spawnTokenDaemonTok, body+"}")
		if rec.Code != http.StatusCreated {
			t.Fatalf("create = %d %s", rec.Code, rec.Body.String())
		}
		var out struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out.ID
	}
	approval := create(true)
	row, err := db.GetMessage(ctx, f.pool, approval)
	if err != nil || row == nil || !row.OwnerOnly {
		t.Fatalf("owner_only not stored: %+v %v", row, err)
	}
	for name, tok := range map[string]string{"daemon": spawnTokenDaemonTok, "board": boardTok, "other person": otherTok, "owner's agent token": agentTok} {
		// Each refusal is checked on its own approval, so one leak cannot hide another.
		mine := create(true)
		if rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", mine, tok, `{"answer":"1"}`); rec.Code != http.StatusForbidden {
			t.Errorf("%s answered an owner-only approval: %d %s", name, rec.Code, rec.Body.String())
		}
		if rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", approval, tok, `{"answer":"1"}`); rec.Code != http.StatusForbidden {
			t.Errorf("%s answered an owner-only approval: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if row, _ := db.GetMessage(ctx, f.pool, approval); row == nil || row.Status != "open" {
		t.Fatalf("approval answered by a non-owner: %+v", row)
	}
	if rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", approval, ownerTok, `{"answer":"1"}`); rec.Code != http.StatusOK {
		t.Fatalf("owner reply = %d %s", rec.Code, rec.Body.String())
	}

	// Plain questions are unchanged: the daemon token and another person still answer them.
	for _, tok := range []string{spawnTokenDaemonTok, otherTok} {
		plain := create(false)
		if rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", plain, tok, `{"answer":"blue"}`); rec.Code != http.StatusOK {
			t.Fatalf("plain question reply = %d %s", rec.Code, rec.Body.String())
		}
	}
}

// The chat path (an ask card's reply box over the socket, a message typed while
// a question waits, the runner API) follows the same rule.
func TestOwnerOnlyAskIsNotAnsweredFromAnotherAccountsChat(t *testing.T) {
	f := newPrivFixture(t)
	ctx := context.Background()
	approval, err := db.CreateAsk(ctx, f.pool, f.pubSession, "approve?", true)
	if err != nil {
		t.Fatal(err)
	}
	recordServerAgentEvent(ctx, f.hub, f.pool, f.pubSession, messageEventID(approval, "ask"), "ask", messagingPayload{Body: "approve?", MessageID: approval})
	for _, answerer := range []string{"", privAcctB} {
		if answerAskFromChatAs(ctx, f.hub, f.pool, f.pubSession, "1", newUUID(), approval, answerer) ||
			answerAskFromChatAs(ctx, f.hub, f.pool, f.pubSession, "1", newUUID(), "", answerer) {
			t.Fatalf("answerer %q answered an owner-only approval from chat", answerer)
		}
	}
	if row, _ := db.GetMessage(ctx, f.pool, approval); row == nil || row.Status != "open" {
		t.Fatalf("approval answered by a non-owner: %+v", row)
	}
	if !answerAskFromChatAs(ctx, f.hub, f.pool, f.pubSession, "1", newUUID(), "", privAcctA) {
		t.Fatal("the owner's chat reply did not answer the approval")
	}
	if row, _ := db.GetMessage(ctx, f.pool, approval); row == nil || row.Status != "answered" || row.Answer == nil || *row.Answer != "1" {
		t.Fatalf("approval = %+v, want answered 1", row)
	}
	// A plain question is still answered from chat with no known answerer.
	plain, err := db.CreateMessage(ctx, f.pool, f.pubSession, "ask", "color?")
	if err != nil {
		t.Fatal(err)
	}
	recordServerAgentEvent(ctx, f.hub, f.pool, f.pubSession, messageEventID(plain, "ask"), "ask", messagingPayload{Body: "color?", MessageID: plain})
	if !answerAskFromChat(ctx, f.hub, f.pool, f.pubSession, "blue", newUUID(), "") {
		t.Fatal("plain question no longer answered from chat")
	}
}

// When a Hermes approval ends without an answer (withdrawn, timed out,
// interrupted), the daemon closes its card the way httpMessenger.CloseAsk does:
// POST …/reply with the daemon token and an empty answer, which Hermes reads as
// deny. That close must succeed for an owner-only ask, while the daemon still
// cannot answer one, and the owner's next chat message must then reach the
// agent as an ordinary message instead of answering the dead approval.
func TestOwnerOnlyAskIsClosedByTheDaemonAndFreesTheChat(t *testing.T) {
	f := newPrivFixture(t)
	ctx := context.Background()
	approval, err := db.CreateAsk(ctx, f.pool, f.pubSession, "approve?", true)
	if err != nil {
		t.Fatal(err)
	}
	recordServerAgentEvent(ctx, f.hub, f.pool, f.pubSession, messageEventID(approval, "ask"), "ask", messagingPayload{Body: "approve?", MessageID: approval})
	if rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", approval, spawnTokenDaemonTok, `{"answer":""}`); rec.Code != http.StatusOK {
		t.Fatalf("daemon close of an owner-only approval = %d %s", rec.Code, rec.Body.String())
	}
	row, err := db.GetMessage(ctx, f.pool, approval)
	if err != nil || row == nil || row.Status != "answered" || row.Answer == nil || *row.Answer != "" {
		t.Fatalf("approval after daemon close = %+v %v, want answered with an empty answer", row, err)
	}
	if answerAskFromChatAs(ctx, f.hub, f.pool, f.pubSession, "try a different approach", newUUID(), "", privAcctA) {
		t.Fatal("the owner's chat message was swallowed by a closed approval")
	}
	// Closing is not answering: the daemon still cannot choose for the owner.
	other, err := db.CreateAsk(ctx, f.pool, f.pubSession, "approve?", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, answer := range []string{`"1"`, `" "`, `"once"`} {
		if rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", other, spawnTokenDaemonTok, `{"answer":`+answer+`}`); rec.Code != http.StatusForbidden {
			t.Fatalf("daemon answered an owner-only approval with %s: %d", answer, rec.Code)
		}
	}
}
