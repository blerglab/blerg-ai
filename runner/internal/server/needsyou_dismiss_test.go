package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Card #155: the person dismisses what waits on them in Needs you. A review request closes as
// dismissed (not approved, not changes requested) and the agent is told in a Blerg message; only the
// session's owner may, never the session itself. Requires TEST_DATABASE_URL.
func TestReviewRequestDismissedByTheOwner(t *testing.T) {
	fx := newArtFx(t)
	dc := fx.agentDaemon()
	fx.publish(fx.sessTok, fx.sess, "plan.md", []byte("# Plan\n"))
	req := fx.requestReview(fx.sessTok, fx.sess, "plan.md", "Is the plan right?", http.StatusCreated)
	for name, tok := range map[string]string{
		"the session's own token": fx.sessTok,
		"the daemon":              "daemon-tok-1234567890",
		"the owner's agent token": fx.agent(artUser),
		"another person":          fx.human(artOther),
	} {
		if rec := fx.send("POST", "/api/review-requests/"+req.RequestID+"/decide", tok, `{"decision":"dismiss"}`); rec.Code != http.StatusForbidden {
			t.Errorf("%s: dismiss = %d %s, want 403", name, rec.Code, rec.Body.String())
		}
	}
	got := decodeReview(t, fx.send("POST", "/api/review-requests/"+req.RequestID+"/decide", fx.human(artUser), `{"decision":"dismiss"}`), http.StatusOK)
	if got.State != db.ReviewDismissed {
		t.Fatalf("dismissed request = %+v", got)
	}
	texts := sentTexts(dc)
	if len(texts) != 1 {
		t.Fatalf("agent got %d messages, want 1: %q", len(texts), texts)
	}
	for _, want := range []string{"): DISMISSED", `The person dismissed the review request for "plan.md"; they may have answered in the chat instead.`, "neither an approval nor a request for changes"} {
		if !strings.Contains(texts[0], want) {
			t.Fatalf("message %q lacks %q", texts[0], want)
		}
	}
	if strings.Contains(texts[0], "APPROVED") || strings.Contains(texts[0], "Confirm it before you act") {
		t.Fatalf("a dismissal reads like an approval: %q", texts[0])
	}
	// It is no longer pending (Needs you drops it), a second decision is a conflict, and the agent
	// may ask again.
	if pending, _ := fx.api.livePendingReviews(context.Background(), fx.sess); len(pending) != 0 {
		t.Fatalf("still in Needs you: %+v", pending)
	}
	if rec := fx.send("POST", "/api/review-requests/"+req.RequestID+"/decide", fx.human(artUser), `{"decision":"approve"}`); rec.Code != http.StatusConflict {
		t.Fatalf("approving a dismissed request = %d, want 409", rec.Code)
	}
	if again := fx.requestReview(fx.sessTok, fx.sess, "plan.md", "Now?", http.StatusCreated); again.RequestID == req.RequestID || !again.Created {
		t.Fatalf("re-request after a dismissal = %+v", again)
	}
	// The status the CLI reads says dismissed, with the same message.
	status := decodeReview(t, fx.get("GET", "/api/review-requests/"+req.RequestID, fx.sessTok), http.StatusOK)
	if status.State != db.ReviewDismissed || !strings.Contains(status.Message, "DISMISSED") {
		t.Fatalf("status = %+v", status)
	}
}

// A question (ask) the person dismisses from Needs you closes as dismissed, not answered: a question
// the agent did not wait on tells the agent in a Blerg message; the answer the waiting CLI reads says
// dismissed; an approval cannot be dismissed; only a person who may answer it can, never the session.
func TestQuestionDismissedByThePerson(t *testing.T) {
	f := newPrivFixture(t)
	ctx := context.Background()
	board, err := db.CreateBoard(ctx, f.pool, "dismiss", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sessTok, _, err := db.MintBoardToken(ctx, f.pool, board.ID, f.pubSession, []string{"message"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ownerTok := f.tok(t, "human", privAcctA, coreAuthBrowserCap, "session.start")
	otherTok := f.tok(t, "human", privAcctB, coreAuthBrowserCap, "session.start")
	ask := func(extra string) string {
		t.Helper()
		rec := privDo(f.api.HandlePostMessages, "POST", "/api/messages", "", sessTok,
			`{"session_id":"`+f.pubSession+`","kind":"ask","body":"Ship the blue one?"`+extra+`}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("post ask = %d %s", rec.Code, rec.Body.String())
		}
		var out struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.ID
	}

	// A question the agent did not wait on.
	q := ask(`,"no_wait":true`)
	for name, tok := range map[string]string{"another person": otherTok, "the session's own token": sessTok, "the daemon": spawnTokenDaemonTok} {
		if rec := privDo(f.api.HandlePostMessageDismiss, "POST", "/x", q, tok, `{}`); rec.Code == http.StatusOK {
			t.Fatalf("%s dismissed the owner's question", name)
		}
	}
	drain(f.dc.send)
	if rec := privDo(f.api.HandlePostMessageDismiss, "POST", "/x", q, ownerTok, `{}`); rec.Code != http.StatusOK {
		t.Fatalf("owner dismiss = %d %s", rec.Code, rec.Body.String())
	}
	row, _ := db.GetMessage(ctx, f.pool, q)
	if row == nil || row.Status != "answered" || !row.Dismissed || row.Answer == nil || *row.Answer != "" {
		t.Fatalf("dismissed question = %+v", row)
	}
	sent := awaitDaemonMessage(t, f.dc.send, "agent_user_message")
	var msg protocol.AgentUserMessage
	if err := json.Unmarshal(sent, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Source != questionAnswerSource || !strings.Contains(msg.Text, "The person dismissed your question "+q) ||
		!strings.Contains(msg.Text, "they may have answered in the chat instead") || !strings.Contains(msg.Text, "Ship the blue one?") {
		t.Fatalf("agent got %+v", msg)
	}
	if rec := privDo(f.api.HandlePostMessageDismiss, "POST", "/x", q, ownerTok, `{}`); rec.Code != http.StatusConflict {
		t.Fatalf("dismissing twice = %d, want 409", rec.Code)
	}

	// A question the agent waits on: the answer its CLI reads says dismissed; no message is sent.
	w := ask("")
	drain(f.dc.send)
	if rec := privDo(f.api.HandlePostMessageDismiss, "POST", "/x", w, ownerTok, `{}`); rec.Code != http.StatusOK {
		t.Fatalf("dismiss a waited-on ask = %d %s", rec.Code, rec.Body.String())
	}
	rec := privDo(f.api.HandleGetMessageAnswer, "GET", "/x?wait=0", w, sessTok, "")
	var answer answerResponse
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &answer) != nil || !answer.Answered || !answer.Dismissed {
		t.Fatalf("the CLI's answer = %d %s, want answered and dismissed", rec.Code, rec.Body.String())
	}
	select {
	case m := <-f.dc.send:
		if strings.Contains(string(m), "dismissed your question") {
			t.Fatalf("a waited-on ask also sent a message: %s", m)
		}
	case <-time.After(300 * time.Millisecond):
	}

	// An engine's command approval is allowed or denied, never dismissed.
	approval := ask(`,"owner_only":true`)
	if rec := privDo(f.api.HandlePostMessageDismiss, "POST", "/x", approval, ownerTok, `{}`); rec.Code != http.StatusConflict {
		t.Fatalf("dismissing an approval = %d %s, want 409", rec.Code, rec.Body.String())
	}

	// History: the dismissed asks are dismissed (and closed); the answered and open ones are not.
	closed, dismissed, _, err := db.AskStates(ctx, f.pool, f.pubSession)
	if err != nil {
		t.Fatal(err)
	}
	if !dismissed[q] || !dismissed[w] || !closed[q] || dismissed[approval] {
		t.Fatalf("ask states: closed %v dismissed %v", closed, dismissed)
	}
}

// The bell lists the pending review requests of the sessions the person started (they alone decide
// or dismiss them): not another person's, not a settled one, and nothing for a non-person credential.
func TestTheBellListsMyPendingReviewRequests(t *testing.T) {
	fx := newArtFx(t)
	fx.publish(fx.sessTok, fx.sess, "a.md", []byte("a"))
	mine := fx.requestReview(fx.sessTok, fx.sess, "a.md", "ok?", http.StatusCreated)
	fx.publish(fx.sessTok, fx.sess, "b.md", []byte("b"))
	settled := fx.requestReview(fx.sessTok, fx.sess, "b.md", "ok?", http.StatusCreated)
	decodeReview(t, fx.send("POST", "/api/review-requests/"+settled.RequestID+"/decide", fx.human(artUser), `{"decision":"dismiss"}`), http.StatusOK)
	list := func(tok string) (int, []string) {
		rec := fx.get("GET", "/api/review-requests", tok)
		var out struct {
			ReviewRequests []reviewReply `json:"review_requests"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		var ids []string
		for _, r := range out.ReviewRequests {
			ids = append(ids, r.RequestID)
		}
		return rec.Code, ids
	}
	if code, ids := list(fx.human(artUser)); code != http.StatusOK || len(ids) != 1 || ids[0] != mine.RequestID {
		t.Fatalf("owner's bell = %d %v, want only %s", code, ids, mine.RequestID)
	}
	if code, ids := list(fx.human(artOther)); code != http.StatusOK || len(ids) != 0 {
		t.Fatalf("another person's bell = %d %v, want empty", code, ids)
	}
	if code, _ := list(fx.sessTok); code != http.StatusForbidden {
		t.Fatalf("the session's token listing = %d, want 403", code)
	}
}

// Card #155 review I3: a question dismissed while the session's desktop daemon is away is not lost.
// The dismissal is queued in the session's outbox like an answer, under its own id, and one cut off
// before it got there (a restart) is sent on the daemon's next heartbeat, once.
func TestQuestionDismissalWhileTheDaemonIsAwayIsQueued(t *testing.T) {
	f := newPrivFixture(t)
	ctx := context.Background()
	id, err := db.CreateQuestion(ctx, f.pool, f.pubSession, "Ship blue?")
	if err != nil {
		t.Fatal(err)
	}
	f.hub.UnregisterIfCurrent(f.dc)
	ownerTok := f.tok(t, "human", privAcctA, coreAuthBrowserCap, "session.start")
	if rec := privDo(f.api.HandlePostMessageDismiss, "POST", "/x", id, ownerTok, `{}`); rec.Code != http.StatusOK {
		t.Fatalf("dismiss = %d %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var frame string
		err := f.pool.QueryRow(ctx, `SELECT frame::text FROM session_inputs WHERE session_id=$1 AND client_id=$2`, f.pubSession, "question-dismissed-"+id).Scan(&frame)
		if err == nil {
			if !strings.Contains(frame, questionAnswerSource) || !strings.Contains(frame, "The person dismissed your question "+id) {
				t.Fatalf("queued dismissal = %s", frame)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the dismissal was not queued for the daemon: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if rows, err := db.UndeliveredQuestionAnswers(ctx, f.pool, f.pubSession); err != nil || len(rows) != 0 {
		t.Fatalf("a queued dismissal is still listed for redelivery: %v %+v", err, rows)
	}
}

func TestQuestionDismissalLostToARestartIsDeliveredOnce(t *testing.T) {
	f := newIngestFixture(t)
	ctx := f.ctx
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET kind='agent', spawning_account_id='owner-1' WHERE id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	_ = NewAPI(f.hub, f.pool, "fixture-daemon-token", nil, "") // registers the delivery
	f.hub.SetSessionOwner(f.sessionID, f.dc.ID)
	id, err := db.CreateQuestion(ctx, f.pool, f.sessionID, "Ship blue?")
	if err != nil {
		t.Fatal(err)
	}
	// Dismissed, as the handler would, with no delivery after it.
	if changed, err := db.DismissMessage(ctx, f.pool, id, "owner-1"); err != nil || !changed {
		t.Fatal(err)
	}
	drain(f.dc.send)
	replayPendingSessionInputs(ctx, f.hub, f.pool, f.dc, []string{f.sessionID})
	sent := awaitDaemonMessage(t, f.dc.send, "agent_user_message")
	var msg protocol.AgentUserMessage
	if err := json.Unmarshal(sent, &msg); err != nil || msg.Source != questionAnswerSource || msg.ClientID != "question-dismissed-"+id ||
		!strings.Contains(msg.Text, "The person dismissed your question "+id) {
		t.Fatalf("redelivered %s", sent)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		rows, err := db.UndeliveredQuestionAnswers(ctx, f.pool, f.sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still undelivered: %+v", rows)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// One outbox row: the next heartbeat may replay that row until the daemon acknowledges it (the
	// daemon dedupes by its id), but never queues the dismissal again.
	replayPendingSessionInputs(ctx, f.hub, f.pool, f.dc, []string{f.sessionID})
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM session_inputs WHERE session_id=$1 AND client_id LIKE 'question-%'`, f.sessionID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("outbox rows for the question = %d %v, want 1", n, err)
	}
}

// Card #155 review M6: a question the agent waits on, in a session that is not private, may be
// dismissed by anyone who may answer it, and the row says who did.
func TestAnotherPersonMayDismissAWaitedQuestion(t *testing.T) {
	f := newPrivFixture(t)
	ctx := context.Background()
	board, err := db.CreateBoard(ctx, f.pool, "dismiss-other", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sessTok, _, err := db.MintBoardToken(ctx, f.pool, board.ID, f.pubSession, []string{"message"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	rec := privDo(f.api.HandlePostMessages, "POST", "/api/messages", "", sessTok, `{"session_id":"`+f.pubSession+`","kind":"ask","body":"Which name?"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("post ask = %d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	otherTok := f.tok(t, "human", privAcctB, coreAuthBrowserCap, "session.start")
	if rec := privDo(f.api.HandlePostMessageDismiss, "POST", "/x", out.ID, otherTok, `{}`); rec.Code != http.StatusOK {
		t.Fatalf("another person's dismiss = %d %s", rec.Code, rec.Body.String())
	}
	row, err := db.GetMessage(ctx, f.pool, out.ID)
	if err != nil || row == nil || !row.Dismissed || row.DismissedBy != privAcctB {
		t.Fatalf("row = %+v %v, want dismissed by %s", row, err, privAcctB)
	}
	if info := messageRowToInfo(*row); info.DismissedBy != privAcctB {
		t.Fatalf("message info dismissed_by = %q", info.DismissedBy)
	}
}

// Card #155 review I4: the outbox ids of Blerg's relays are its own. A browser frame under
// "question-<id>" is refused, so the owner's later answer is still queued and sent; an API caller
// gets a 422.
func TestReservedRelayClientIDsAreRefused(t *testing.T) {
	f := newPrivFixture(t)
	ctx := context.Background()
	id, err := db.CreateQuestion(ctx, f.pool, f.pubSession, "Ship blue?")
	if err != nil {
		t.Fatal(err)
	}
	for _, reserved := range []string{"question-" + id, "question-dismissed-" + id, "review-x", "proposal-x-approved", "child-turn-a-b-1", "board-access-g"} {
		if !reservedClientID(reserved) {
			t.Errorf("%q is not reserved", reserved)
		}
	}
	if reservedClientID(newUUID()) {
		t.Fatal("a uuid is reserved")
	}
	viewer := &BrowserConn{ID: "crafted", AccountID: privAcctB, send: make(chan []byte, 32)}
	handleBrowserAgentMessage(ctx, f.hub, viewer, f.pool, protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.pubSession, Text: "no", ClientID: "question-" + id})
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM session_inputs WHERE session_id=$1 AND client_id=$2`, f.pubSession, "question-"+id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("the crafted frame was accepted (%d rows, %v)", n, err)
	}
	// The owner's answer is still delivered, under the id it owns.
	owner := &BrowserConn{ID: "owner", AccountID: privAcctA, send: make(chan []byte, 32)}
	handleBrowserAgentMessage(ctx, f.hub, owner, f.pool, protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.pubSession, Text: "yes", ClientID: newUUID(), AnswerMessageID: id})
	deadline := time.Now().Add(5 * time.Second)
	for {
		var frame string
		err := f.pool.QueryRow(ctx, `SELECT frame::text FROM session_inputs WHERE session_id=$1 AND client_id=$2`, f.pubSession, "question-"+id).Scan(&frame)
		if err == nil {
			if !strings.Contains(frame, "Answer to your question "+id) {
				t.Fatalf("queued = %s", frame)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the owner's answer was not queued: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, apiErr := f.api.SendMessage(ctx, runnerPrincipal{Kind: "human", Sub: privAcctA}, f.pubSession, "hi", "", "review-"+id); apiErr == nil || apiErr.Status != http.StatusUnprocessableEntity {
		t.Fatalf("SendMessage with a reserved id = %+v, want 422", apiErr)
	}
}
