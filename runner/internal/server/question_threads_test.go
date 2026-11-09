package server

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestQuestionThreadRepliesDoNotAnswer(t *testing.T) {
	f := newPrivFixture(t)
	tok := f.tok(t, "human", privAcctA, coreAuthBrowserCap, "session.start")
	for _, text := range []string{"Which environment?", "And the rollback?", "yes"} {
		rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", f.pubMsg, tok, `{"reply":"`+text+`"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("reply = %d %s", rec.Code, rec.Body.String())
		}
		row, err := db.GetMessage(context.Background(), f.pool, f.pubMsg)
		if err != nil || row.Status != "open" || row.Answer != nil {
			t.Fatalf("reply closed question: %+v %v", row, err)
		}
	}
}

func TestQuestionThreadAgentReplyRevisionAndPrivacy(t *testing.T) {
	f := newPrivFixture(t)
	ctx := t.Context()
	if _, err := f.pool.Exec(ctx, `UPDATE messages SET question_session_posted=true WHERE id=$1`, f.pubMsg); err != nil {
		t.Fatal(err)
	}
	board, err := db.CreateBoard(ctx, f.pool, "thread", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := db.MintBoardToken(ctx, f.pool, board.ID, f.pubSession, []string{"message"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	owner := f.tok(t, "human", privAcctA, coreAuthBrowserCap, "session.start")
	other := f.tok(t, "human", privAcctB, coreAuthBrowserCap, "session.start")
	for _, body := range []string{`{"reply":"Staging first.","client_id":"reply-1"}`, `{"revise":"Deploy to staging?","client_id":"revision-1"}`, `{"reply":"The rollback is automatic.","client_id":"reply-2"}`} {
		rec := privDo(f.api.HandlePostQuestionThread, "POST", "/x", f.pubMsg, token, body)
		if rec.Code != 200 {
			t.Fatalf("agent write: %d %s", rec.Code, rec.Body.String())
		}
	}
	// Retry is idempotent, but conflicting reuse is refused.
	rec := privDo(f.api.HandlePostQuestionThread, "POST", "/x", f.pubMsg, token, `{"reply":"Staging first.","client_id":"reply-1"}`)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	rec = privDo(f.api.HandlePostQuestionThread, "POST", "/x", f.pubMsg, token, `{"reply":"Different","client_id":"reply-1"}`)
	if rec.Code != 409 {
		t.Fatalf("conflict: %d", rec.Code)
	}
	row, _ := db.GetMessage(ctx, f.pool, f.pubMsg)
	if row.Body != "Deploy to staging?" || row.QuestionTurn != "person" || row.Status != "open" {
		t.Fatalf("revised row: %+v", row)
	}
	rec = privDo(f.api.HandleGetQuestionThread, "GET", "/x", f.pubMsg, owner, "")
	var out struct{ Entries []questionThreadEntry }
	if json.Unmarshal(rec.Body.Bytes(), &out) != nil || len(out.Entries) != 3 {
		t.Fatalf("history: %d %s", rec.Code, rec.Body.String())
	}
	for _, tc := range []struct {
		token, id, body string
		code            int
	}{{other, f.pubMsg, `{"reply":"yes"}`, 403}, {owner, f.pubMsg, `{"revise":"changed"}`, 403}, {token, f.privMsg, `{"reply":"foreign"}`, 404}} {
		rec = privDo(f.api.HandlePostQuestionThread, "POST", "/x", tc.id, tc.token, tc.body)
		if rec.Code != tc.code {
			t.Fatalf("privacy: want %d got %d %s", tc.code, rec.Code, rec.Body.String())
		}
	}
	rec = privDo(f.api.HandleGetQuestionThread, "GET", "/x", f.privMsg, other, "")
	if rec.Code != 404 {
		t.Fatalf("foreign read: %d %s", rec.Code, rec.Body.String())
	}
	rec = privDo(f.api.HandlePostMessageReply, "POST", "/x", f.pubMsg, owner, `{"answer":"yes","revision_seq":2}`)
	if rec.Code != 200 {
		t.Fatalf("answer: %d %s", rec.Code, rec.Body.String())
	}
	rec = privDo(f.api.HandlePostQuestionThread, "POST", "/x", f.pubMsg, token, `{"reply":"too late"}`)
	if rec.Code != 409 {
		t.Fatalf("closed write: %d", rec.Code)
	}
}

func TestQuestionThreadPersonReplyIsDurableAndBoundReplyNeverCommits(t *testing.T) {
	f := newPrivFixture(t)
	ctx := t.Context()
	owner := f.tok(t, "human", privAcctA, coreAuthBrowserCap, "session.start")
	// Minimal bound fixture: a reply must not even enter the decision executor.
	_, err := f.pool.Exec(ctx, `INSERT INTO bound_asks(message_id,workspace_id,owner_id,subject,status,expires_at) VALUES($1,$2,$3,'{"kind":"draft"}','open',now()+interval '1 hour')`, f.pubMsg, f.pubSession, privAcctA)
	if err != nil {
		t.Fatal(err)
	}
	rec := privDo(f.api.HandlePostQuestionThread, "POST", "/x", f.pubMsg, owner, `{"reply":"yes","client_id":"person-1"}`)
	if rec.Code != 200 {
		t.Fatalf("reply: %d %s", rec.Code, rec.Body.String())
	}
	row, _ := db.GetMessage(ctx, f.pool, f.pubMsg)
	if row.Status != "open" || row.Answer != nil || row.QuestionTurn != "main" {
		t.Fatalf("reply decided: %+v", row)
	}
	var boundStatus string
	_ = f.pool.QueryRow(ctx, `SELECT status FROM bound_asks WHERE message_id=$1`, f.pubMsg).Scan(&boundStatus)
	if boundStatus != "open" {
		t.Fatalf("bound status %s", boundStatus)
	}
	inputs, err := db.PendingSessionInputs(ctx, f.pool, f.pubSession)
	if err != nil || len(inputs) != 1 || !strings.Contains(inputs[0].Frame, questionThreadSource) {
		t.Fatalf("durable input: %+v %v", inputs, err)
	}
	board, err := db.CreateBoard(ctx, f.pool, "thread", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := db.MintBoardToken(ctx, f.pool, board.ID, f.pubSession, []string{"message"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	rec = privDo(f.api.HandlePostQuestionThread, "POST", "/x", f.pubMsg, token, `{"revise":"A different decision"}`)
	if rec.Code != 409 {
		t.Fatalf("bound revise: %d %s", rec.Code, rec.Body.String())
	}
}

func TestQuestionThreadMutationRollsBackWithDurableInput(t *testing.T) {
	f := newPrivFixture(t)
	ctx := t.Context()
	owner := f.tok(t, "human", privAcctA, coreAuthBrowserCap, "session.start")
	_, err := f.pool.Exec(ctx, `CREATE FUNCTION refuse_thread_input() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected input failure'; END $$; CREATE TRIGGER refuse_thread_input BEFORE INSERT ON session_inputs FOR EACH ROW EXECUTE FUNCTION refuse_thread_input()`)
	if err != nil {
		t.Fatal(err)
	}
	rec := privDo(f.api.HandlePostQuestionThread, "POST", "/x", f.pubMsg, owner, `{"reply":"Which environment?","client_id":"rollback"}`)
	if rec.Code == 200 {
		t.Fatal("reply accepted without durable input")
	}
	var count int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM question_thread_entries WHERE message_id=$1`, f.pubMsg).Scan(&count)
	row, _ := db.GetMessage(ctx, f.pool, f.pubMsg)
	if count != 0 || row.QuestionTurn != "person" || row.Status != "open" {
		t.Fatalf("partial mutation: count %d row %+v", count, row)
	}
}

func TestQuestionThreadSourceIsReserved(t *testing.T) {
	if !reservedMessageSource(questionThreadSource) || answersAsk(questionThreadSource) {
		t.Fatal("thread relays can be forged or close an ask")
	}
}

func TestQuestionThreadReplyDoesNotReleaseCodexWaiter(t *testing.T) {
	f := newPrivFixture(t)
	owner := f.tok(t, "human", privAcctA, coreAuthBrowserCap, "session.start")
	rec := privDo(f.api.HandlePostQuestionThread, "POST", "/x", f.pubMsg, owner, `{"reply":"Which environment?"}`)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	row, _ := db.GetMessage(t.Context(), f.pool, f.pubMsg)
	if row.OwnerOnly || row.PersonRequired || row.NoWait || row.WaiterEnded {
		t.Fatalf("reply changed authority: %+v", row)
	}
	rec = privDo(f.api.HandleGetMessageAnswer, "GET", "/x", f.pubMsg, owner, "")
	if strings.Contains(rec.Body.String(), `"outcome":"discussion"`) || strings.Contains(rec.Body.String(), `"answered":true`) {
		t.Fatal(rec.Body.String())
	}
}

func TestQuestionThreadCannotRewriteACommandApproval(t *testing.T) {
	f := newPrivFixture(t)
	ctx := t.Context()
	_, err := f.pool.Exec(ctx, `UPDATE messages SET owner_only=true,question_session_posted=true WHERE id=$1`, f.pubMsg)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = db.AppendAgentEvent(ctx, f.pool, f.pubSession, messageEventID(f.pubMsg, "ask"), "ask", `{"owner_only":true,"body":"Run protected command?"}`)
	if err != nil {
		t.Fatal(err)
	}
	board, err := db.CreateBoard(ctx, f.pool, "approval", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := db.MintBoardToken(ctx, f.pool, board.ID, f.pubSession, []string{"message"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	owner := f.tok(t, "human", privAcctA, coreAuthBrowserCap, "session.start")
	for _, discussed := range []bool{false, true} {
		if discussed {
			rec := privDo(f.api.HandlePostQuestionThread, "POST", "/x", f.pubMsg, owner, `{"reply":"What does the command do?"}`)
			if rec.Code != 409 {
				t.Fatalf("discussion: %d %s", rec.Code, rec.Body.String())
			}
		}
		rec := privDo(f.api.HandlePostQuestionThread, "POST", "/x", f.pubMsg, token, `{"revise":"Run a harmless command?"}`)
		if rec.Code != 409 {
			t.Fatalf("command approval rewritten (discussed %t): %d %s", discussed, rec.Code, rec.Body.String())
		}
	}
}

func TestQuestionThreadPaginatesLongDiscussions(t *testing.T) {
	f := newPrivFixture(t)
	_, err := f.pool.Exec(t.Context(), `INSERT INTO question_thread_entries(message_id,client_id,author,kind,body) SELECT $1,'reply-'||n,'person','reply','Another clarification' FROM generate_series(1,105) n`, f.pubMsg)
	if err != nil {
		t.Fatal(err)
	}
	owner := f.tok(t, "human", privAcctA, coreAuthBrowserCap, "session.start")
	var page struct {
		Entries []questionThreadEntry
		More    bool
	}
	rec := privDo(f.api.HandleGetQuestionThread, "GET", "/x", f.pubMsg, owner, "")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &page) != nil || len(page.Entries) != 100 || !page.More {
		t.Fatalf("first page: %d %s", rec.Code, rec.Body.String())
	}
	rec = privDo(f.api.HandleGetQuestionThread, "GET", "/x?after="+strconv.FormatInt(page.Entries[99].Seq, 10), f.pubMsg, owner, "")
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &page) != nil || len(page.Entries) != 5 || page.More {
		t.Fatalf("last page: %d %s", rec.Code, rec.Body.String())
	}
}

func TestQuestionThreadStaleRevisionCannotResolve(t *testing.T) {
	f := newPrivFixture(t)
	owner := f.tok(t, "human", privAcctA, coreAuthBrowserCap, "session.start")
	// A revision commits before a stale browser submits any resolving action.
	ctx := t.Context()
	if _, err := f.pool.Exec(ctx, `UPDATE messages SET question_session_posted=true WHERE id=$1`, f.pubMsg); err != nil {
		t.Fatal(err)
	}
	board, err := db.CreateBoard(ctx, f.pool, "revision-race", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := db.MintBoardToken(ctx, f.pool, board.ID, f.pubSession, []string{"message"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	revised := privDo(f.api.HandlePostQuestionThread, "POST", "/x", f.pubMsg, token, `{"revise":"New question?"}`)
	if revised.Code != 200 {
		t.Fatal(revised.Code, revised.Body.String())
	}

	for _, action := range []string{"answer", "approve", "decline", "dismiss"} {
		handler := f.api.HandlePostMessageReply
		if action == "dismiss" {
			handler = f.api.HandlePostMessageDismiss
		}
		rec := privDo(handler, "POST", "/x", f.pubMsg, owner, `{"answer":"yes","revision_seq":0}`)
		if rec.Code != 409 {
			t.Fatalf("stale %s: %d %s", action, rec.Code, rec.Body.String())
		}
	}
	row, _ := db.GetMessage(t.Context(), f.pool, f.pubMsg)
	if row.Status != "open" {
		t.Fatal("stale action committed")
	}
}

func TestQuestionThreadHermesApprovalRefusesReply(t *testing.T) {
	f := newDelegFx(t)
	session, _ := f.session(sessOpts{owner: "owner-1"})
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET engine='hermes' WHERE id=$1`, session); err != nil {
		t.Fatal(err)
	}
	id := postAsk(t, f, delegDaemonToken, session, "Run protected command?", 1, `,"owner_only":true`)
	rec := privDo(f.api.HandlePostQuestionThread, "POST", "/x", id, f.human("owner-1"), `{"reply":"what does this delete?"}`)
	if rec.Code != 409 {
		t.Fatalf("Hermes reply: %d %s", rec.Code, rec.Body.String())
	}
	row := mustChildMessage(t, f, id)
	if row.Status != "open" || row.Answer != nil {
		t.Fatalf("approval resolved: %+v", row)
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM question_thread_entries WHERE message_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("reply persisted: %d %v", count, err)
	}
}

func TestQuestionThreadCodexNativeWaitsForExplicitAnswer(t *testing.T) {
	f := newDelegFx(t)
	session, _ := f.session(sessOpts{owner: "owner-1"})
	n := startNativeAsk(t, f, session, "Deploy to staging?")
	rec := privDo(f.api.HandlePostQuestionThread, "POST", "/x", n.id, f.human("owner-1"), `{"reply":"Which environment?"}`)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if done, _ := f.api.answeredNow(t.Context(), httptest.NewRecorder(), n.id); done {
		t.Fatal("discussion released Codex waiter")
	}
	replyAs(t, f, n.id, f.human("owner-1"), "staging only")
	if got := n.toolResult(t); !strings.Contains(got, "staging only") || strings.Contains(got, "Which environment?") {
		t.Fatal(got)
	}
}

func TestQuestionThreadReplyPreservesOwnerlessAnswer(t *testing.T) {
	f := newDelegFx(t)
	session, _ := f.session(sessOpts{})
	id := postAsk(t, f, delegDaemonToken, session, "Which branch?", 1, "")
	rec := privDo(f.api.HandlePostQuestionThread, "POST", "/x", id, f.human("owner-1"), `{"reply":"Which environment?"}`)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if !answerAskFrom(t.Context(), f.hub, f.pool, session, "main", "", id, "", "human") {
		t.Fatal("ownerless chat no longer answers")
	}
	if row := mustChildMessage(t, f, id); row.Status != "answered" {
		t.Fatalf("%+v", row)
	}
}

func TestQuestionThreadRevisionReclassifiesAndRefusesServerAsks(t *testing.T) {
	f := newDelegFx(t)
	parent, _ := f.session(sessOpts{owner: "owner-1"})
	child, token := f.session(sessOpts{owner: "owner-1", parent: parent})
	id := postAsk(t, f, token, child, "Which branch?", 1, "")
	rec := privDo(f.api.HandlePostQuestionThread, "POST", "/x", id, token, `{"revise":"May I drop the database?"}`)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	row := mustChildMessage(t, f, id)
	if !row.PersonRequired || row.QuestionRevision == 0 {
		t.Fatalf("revision not classified: %+v", row)
	}
	_, urgent, err := quietPushQuestion(t.Context(), f.pool, row.Body, "/sessions/"+child)
	if err != nil || !urgent {
		t.Fatalf("quiet hours urgency: %v %v", urgent, err)
	}
	serverID, err := db.CreateMessage(t.Context(), f.pool, child, "ask", "Cut authorized, not executed")
	if err != nil {
		t.Fatal(err)
	}
	rec = privDo(f.api.HandlePostQuestionThread, "POST", "/x", serverID, token, `{"revise":"Run something else?"}`)
	if rec.Code != 409 {
		t.Fatalf("server ask rewritten: %d %s", rec.Code, rec.Body.String())
	}
}

func TestQuestionThreadSharedViewerLiveEventAndBroadcast(t *testing.T) {
	f := newPrivFixture(t)
	viewer := &BrowserConn{ID: "thread-shared", AccountID: privAcctB, send: make(chan []byte, 32), subs: map[string]context.CancelFunc{}}
	f.hub.RegisterBrowser(viewer)
	live := make(chan []byte, 32)
	f.hub.Subscribe(f.pubSession, viewer.ID, live)
	owner := f.tok(t, "human", privAcctA, coreAuthBrowserCap, "session.start")
	rec := privDo(f.api.HandlePostQuestionThread, "POST", "/x", f.pubMsg, owner, `{"reply":"Public clarification"}`)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	select {
	case frame := <-live:
		if !strings.Contains(string(frame), `"question_thread"`) || !strings.Contains(string(frame), "Public clarification") {
			t.Fatalf("live event: %s", frame)
		}
	default:
		t.Fatal("shared viewer received no live event")
	}
	found := false
	for frame := range awaitAll(viewer.send) {
		var event protocol.MessageCreated
		if json.Unmarshal(frame, &event) == nil && event.Type == "message_created" && event.Message.ID == f.pubMsg {
			found = true
			if event.Message.Answerable || !event.Quiet || event.Message.QuestionTurn != "main" {
				t.Fatalf("shared broadcast grants authority: %+v", event)
			}
		}
	}
	if !found {
		t.Fatal("shared viewer received no question broadcast")
	}
	rec = privDo(f.api.HandlePostQuestionThread, "POST", "/x", f.privMsg, owner, `{"reply":"Private clarification"}`)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for frame := range awaitAll(viewer.send) {
		if strings.Contains(string(frame), f.privMsg) || strings.Contains(string(frame), "Private clarification") {
			t.Fatalf("private broadcast leaked: %s", frame)
		}
	}
	select {
	case frame := <-live:
		t.Fatalf("private live event leaked: %s", frame)
	default:
	}
}

func TestQuestionThreadReplyPreservesChildResolutionRules(t *testing.T) {
	f := newDelegFx(t)
	parent, parentToken := f.session(sessOpts{owner: "owner-1"})
	child, token := f.session(sessOpts{owner: "owner-1", parent: parent})
	id := postAsk(t, f, token, child, "Which branch?", 1, "")
	rec := privDo(f.api.HandlePostQuestionThread, "POST", "/x", id, token, `{"reply":"The staging branch is available."}`)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	rec = privDo(f.api.HandlePostMessageDismiss, "POST", "/x", id, f.human("owner-1"), `{"revision_seq":0}`)
	if rec.Code != 403 {
		t.Fatalf("reply changed child dismissal: %d %s", rec.Code, rec.Body.String())
	}
	code, body, _ := f.do("POST", childRoute(parent, child, "/questions/"+id+"/answer"), parentToken, map[string]any{"answer": "main"}, nil)
	if code != 200 {
		t.Fatalf("parent answer: %d %+v", code, body)
	}
	if row := mustChildMessage(t, f, id); row.Status != "answered" || row.PersonRequired || row.OwnerOnly || row.WaiterEnded {
		t.Fatalf("reply changed parent authority: %+v", row)
	}
}

func TestQuestionThreadCLIReplyYieldsButNativePollDoesNot(t *testing.T) {
	f := newDelegFx(t)
	session, token := f.session(sessOpts{owner: "owner-1"})
	id := postAsk(t, f, token, session, "Which environment?", 1, "")
	rec := privDo(f.api.HandlePostQuestionThread, "POST", "/x", id, f.human("owner-1"), `{"reply":"What does this delete?"}`)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	for _, native := range []struct{ path, token string }{{"/x?wait=1", token}, {"/x?wait=1&waiter=cli", delegDaemonToken}} {
		rec = privDo(f.api.HandleGetMessageAnswer, "GET", native.path, id, native.token, "")
		if rec.Code != 200 || strings.Contains(rec.Body.String(), `"outcome":"discussion"`) {
			t.Fatalf("native poll received discussion: %d %s", rec.Code, rec.Body.String())
		}
	}
	rec = privDo(f.api.HandleGetMessageAnswer, "GET", "/x?wait=1&waiter=cli", id, token, "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"outcome":"discussion"`) || !strings.Contains(rec.Body.String(), "ask --reply "+id) {
		t.Fatalf("CLI did not yield: %d %s", rec.Code, rec.Body.String())
	}
	if done, _ := f.api.answeredNow(t.Context(), httptest.NewRecorder(), id); done {
		t.Fatal("native poll released by discussion")
	}
	if row := mustChildMessage(t, f, id); row.Status != "open" || row.Answer != nil {
		t.Fatal("reply answered")
	}
}

func TestQuestionThreadChatDoesNotResolveAnAsk(t *testing.T) {
	f := newDelegFx(t)
	session, token := f.session(sessOpts{owner: "owner-1"})
	f.dc.SetInputReceipts(true)
	f.hub.SetSessionOwner(session, f.dc.ID)
	id := postAsk(t, f, token, session, "Which environment?", 1, "")
	browserSays(t, f, session, "owner-1", "Please explain the migration.", "composer-discussion", "")
	row := mustChildMessage(t, f, id)
	if row.Status != "open" || row.Answer != nil {
		t.Fatalf("composer answered: %+v", row)
	}
	rec := privDo(f.api.HandleGetMessageAnswer, "GET", "/x?wait=1&waiter=cli", id, token, "")
	if !strings.Contains(rec.Body.String(), `"outcome":"discussion"`) || !strings.Contains(rec.Body.String(), "ask --reply "+id) {
		t.Fatal(rec.Body.String())
	}
	inputs, err := db.PendingSessionInputs(t.Context(), f.pool, session)
	if err != nil || len(inputs) != 1 || !strings.Contains(inputs[0].Frame, "Please explain the migration.") || strings.Contains(inputs[0].Frame, "ask_answer") {
		t.Fatalf("chat not delivered ordinarily: %+v %v", inputs, err)
	}
}

func TestQuestionThreadRevisedQuestionStillClosesWithoutRevision(t *testing.T) {
	for _, action := range []string{"expire", "withdraw", "db-answer", "parent-answer"} {
		t.Run(action, func(t *testing.T) {
			f := newDelegFx(t)
			parent, parentToken := f.session(sessOpts{owner: "owner-1"})
			opts := sessOpts{owner: "owner-1"}
			if action == "parent-answer" {
				opts.parent = parent
			}
			session, token := f.session(opts)
			id := postAsk(t, f, token, session, "Old question?", 1, "")
			rec := privDo(f.api.HandlePostQuestionThread, "POST", "/x", id, token, `{"revise":"Revised question?"}`)
			if rec.Code != 200 {
				t.Fatal(rec.Code, rec.Body.String())
			}
			switch action {
			case "expire", "withdraw":
				body := `{}`
				if action == "withdraw" {
					body = `{"withdraw":true}`
				}
				rec = privDo(f.api.HandlePostMessageExpire, "POST", "/x", id, token, body)
				if rec.Code != 200 {
					t.Fatal(rec.Code, rec.Body.String())
				}
			case "db-answer":
				changed, err := db.AnswerMessage(t.Context(), f.pool, id, "answer")
				if err != nil || !changed {
					t.Fatal(changed, err)
				}
			case "parent-answer":
				code, body, _ := f.do("POST", childRoute(parent, session, "/questions/"+id+"/answer"), parentToken, map[string]any{"answer": "answer"}, nil)
				if code != 200 {
					t.Fatal(code, body)
				}
			}
			if row := mustChildMessage(t, f, id); row.Status == "open" {
				t.Fatalf("%s left revised question open", action)
			}
		})
	}
}

func TestQuestionThreadProvenanceIsInInsert(t *testing.T) {
	f := newDelegFx(t)
	session, token := f.session(sessOpts{owner: "owner-1"})
	_, err := f.pool.Exec(t.Context(), `CREATE FUNCTION check_question_origin() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.body='Atomic provenance?' AND NOT NEW.question_session_posted THEN RAISE EXCEPTION 'missing origin in insert'; END IF; RETURN NEW; END $$; CREATE TRIGGER check_question_origin BEFORE INSERT ON messages FOR EACH ROW EXECUTE FUNCTION check_question_origin()`)
	if err != nil {
		t.Fatal(err)
	}
	postAsk(t, f, token, session, "Atomic provenance?", 1, "")
}

func TestQuestionThreadRevisionRetryRecoversRouting(t *testing.T) {
	f := newDelegFx(t)
	parent, _ := f.session(sessOpts{owner: "owner-1"})
	child, token := f.session(sessOpts{owner: "owner-1", parent: parent})
	id := postAsk(t, f, token, child, "Which branch?", 1, "")
	_, err := f.pool.Exec(t.Context(), `CREATE FUNCTION refuse_revision_route() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected route failure'; END $$; CREATE TRIGGER refuse_revision_route BEFORE UPDATE ON messages FOR EACH ROW WHEN (NEW.child_escalated AND NOT OLD.child_escalated) EXECUTE FUNCTION refuse_revision_route()`)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"revise":"May I drop the database?","client_id":"route-retry"}`
	rec := privDo(f.api.HandlePostQuestionThread, "POST", "/x", id, token, body)
	if rec.Code != 200 {
		t.Fatalf("revision should notify Main without escalating: %d %s", rec.Code, rec.Body.String())
	}
	if _, err = f.pool.Exec(t.Context(), `DROP TRIGGER refuse_revision_route ON messages`); err != nil {
		t.Fatal(err)
	}
	rec = privDo(f.api.HandlePostQuestionThread, "POST", "/x", id, token, body)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if row := mustChildMessage(t, f, id); row.ChildEscalated || !row.PersonRequired {
		t.Fatalf("retry bypassed Main or lost requirement: %+v", row)
	}
}

func TestQuestionThreadCLIWholeCycleDeliversFinalResolution(t *testing.T) {
	for _, choice := range []string{"answer", "approve", "decline", "dismiss", "dead"} {
		t.Run(choice, func(t *testing.T) {
			f := newDelegFx(t)
			session, token := f.session(sessOpts{owner: "owner-1"})
			f.dc.SetInputReceipts(true)
			f.hub.SetSessionOwner(session, f.dc.ID)
			created := make(chan string, 1)
			mux := http.NewServeMux()
			mux.HandleFunc("POST /api/messages", func(w http.ResponseWriter, r *http.Request) {
				rec := httptest.NewRecorder()
				f.api.HandlePostMessages(rec, r)
				var out struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(rec.Body.Bytes(), &out)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(rec.Code)
				_, _ = w.Write(rec.Body.Bytes())
				created <- out.ID
			})
			polled := make(chan struct{}, 1)
			pollEnded := make(chan struct{}, 1)
			mux.HandleFunc("GET /api/messages/{id}/answer", func(w http.ResponseWriter, r *http.Request) {
				select {
				case polled <- struct{}{}:
				default:
				}
				f.api.HandleGetMessageAnswer(w, r)
				select {
				case pollEnded <- struct{}{}:
				default:
				}
			})
			srv := httptest.NewServer(mux)
			defer srv.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "python3", "../../.claude/skills/session-messaging/blerg-runner", "ask", "Deploy to staging?", "--timeout", "10")
			cmd.Env = append(os.Environ(), "BLERG_RUNNER_SERVER_HTTP="+srv.URL, "BLERG_RUNNER_SESSION_ID="+session, "BLERG_RUNNER_SESSION_TOKEN="+token)
			type result struct {
				out []byte
				err error
			}
			done := make(chan result, 1)
			go func() { out, err := cmd.CombinedOutput(); done <- result{out, err} }()
			var id string
			select {
			case id = <-created:
			case <-ctx.Done():
				t.Fatal("CLI did not post ask")
			}

			if choice == "dead" {
				select {
				case <-polled:
				case <-ctx.Done():
					t.Fatal("CLI never polled")
				}
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				<-done
				select {
				case <-pollEnded:
				case <-ctx.Done():
					t.Fatal("CLI poll did not end")
				}
				// Advance the persisted lease instead of waiting for wall time.
				if _, err := f.pool.Exec(t.Context(), `UPDATE messages SET cli_poll_until=now()-interval '1 second' WHERE id=$1`, id); err != nil {
					t.Fatal(err)
				}
				browserSays(t, f, session, "owner-1", "Please explain.", "killed-cli-discussion", "")
				rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", id, f.human("owner-1"), `{"answer":"Use staging","revision_seq":0}`)
				if rec.Code != 200 {
					t.Fatal(rec.Code, rec.Body.String())
				}
				waitUntil(t, "killed CLI answer never reached Main", func() bool {
					inputs, err := db.PendingSessionInputs(t.Context(), f.pool, session)
					if err != nil {
						return false
					}
					for _, input := range inputs {
						var frame protocol.AgentUserMessage
						_ = json.Unmarshal([]byte(input.Frame), &frame)
						if frame.Source == questionAnswerSource && strings.Contains(frame.Text, "Use staging") {
							return true
						}
					}
					return false
				})
				return
			}
			reply := "What does this delete?"
			rec := privDo(f.api.HandlePostQuestionThread, "POST", "/x", id, f.human("owner-1"), `{"reply":"`+reply+`"}`)
			if rec.Code != 200 {
				t.Fatal(rec.Code, rec.Body.String())
			}
			var cli result
			select {
			case cli = <-done:
			case <-ctx.Done():
				t.Fatal("CLI did not yield")
			}
			var exit *exec.ExitError
			if !errors.As(cli.err, &exit) || exit.ExitCode() != 5 {
				t.Fatalf("CLI: %v %s", cli.err, cli.out)
			}
			rec = privDo(f.api.HandlePostQuestionThread, "POST", "/x", id, token, `{"reply":"Only staging data; production stays unchanged."}`)
			if rec.Code != 200 {
				t.Fatal(rec.Code, rec.Body.String())
			}
			if choice == "decline" {
				f.hub.SetQuestionAnswered(nil)
			}
			if choice == "dismiss" {
				rec = privDo(f.api.HandlePostMessageDismiss, "POST", "/x", id, f.human("owner-1"), `{"revision_seq":0}`)
			} else {
				answer := "Use staging"
				if choice == "approve" {
					answer = "yes"
				}
				if choice == "decline" {
					answer = "no"
				}
				rec = privDo(f.api.HandlePostMessageReply, "POST", "/x", id, f.human("owner-1"), `{"answer":"`+answer+`","revision_seq":0}`)
			}
			if rec.Code != 200 {
				t.Fatal(rec.Code, rec.Body.String())
			}
			if choice == "decline" {
				rows, err := db.UndeliveredQuestionAnswers(t.Context(), f.pool, session)
				if err != nil || len(rows) != 1 || rows[0].ID != id {
					t.Fatalf("released CLI answer not recoverable: %+v %v", rows, err)
				}
				f.hub.SetQuestionAnswered(f.api.deliverQuestionAnswer)
				redeliverQuestionAnswers(t.Context(), f.hub, f.pool, session)
			}
			waitUntil(t, "final resolution never reached Main", func() bool {
				inputs, err := db.PendingSessionInputs(t.Context(), f.pool, session)
				if err != nil {
					return false
				}
				for _, input := range inputs {
					var frame protocol.AgentUserMessage
					_ = json.Unmarshal([]byte(input.Frame), &frame)
					if frame.Source == questionAnswerSource && frame.ClientID == questionRelayClientID(*mustChildMessage(t, f, id)) {
						return true
					}
				}
				return false
			})
			if strings.Contains(string(cli.out), reply) || !strings.Contains(string(cli.out), "ask --reply "+id) {
				t.Fatalf("CLI must point to the durable reply, not duplicate it: %s", cli.out)
			}
			yielded, pointer := f.api.answeredNow(t.Context(), httptest.NewRecorder(), id, true)
			if !yielded || pointer == nil || pointer.Outcome != "discussion" || pointer.Answered {
				t.Fatalf("released CLI must not duplicate the durable resolution: %+v", pointer)
			}
			row := mustChildMessage(t, f, id)
			if row.Status != "answered" || (choice == "dismiss" && !row.Dismissed) {
				t.Fatalf("resolution: %+v", row)
			}
		})
	}
}

func TestQuestionThreadCLIPollConsumesOnlyTheHandoffMarker(t *testing.T) {
	f := newDelegFx(t)
	session, token := f.session(sessOpts{owner: "owner-1"})
	id := postAsk(t, f, token, session, "Which environment?", 1, "")
	person := f.human("owner-1")
	for _, text := range []string{"First reply", "Second reply"} {
		rec := privDo(f.api.HandlePostQuestionThread, "POST", "/x", id, person, `{"reply":"`+text+`"}`)
		if rec.Code != 200 {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	rec := privDo(f.api.HandleGetMessageAnswer, "GET", "/x?wait=1&waiter=cli", id, token, "")
	if !strings.Contains(rec.Body.String(), `"outcome":"discussion"`) || strings.Contains(rec.Body.String(), "Second reply") {
		t.Fatal(rec.Body.String())
	}
	var marker string
	if err := f.pool.QueryRow(t.Context(), `SELECT cli_discussion FROM messages WHERE id=$1`, id).Scan(&marker); err != nil || marker != "" {
		t.Fatalf("marker not consumed: %q %v", marker, err)
	}
	done, replay := f.api.answeredNow(t.Context(), httptest.NewRecorder(), id, true)
	if !done || replay == nil || replay.Outcome != "discussion" {
		t.Fatal("a lost handoff response cannot be retried")
	}
	inputs, err := db.PendingSessionInputs(t.Context(), f.pool, session)
	if err != nil || len(inputs) != 2 {
		t.Fatalf("lost replies: %+v %v", inputs, err)
	}
	if !strings.Contains(inputs[0].Frame, "First reply") || !strings.Contains(inputs[1].Frame, "Second reply") {
		t.Fatalf("reply delivery changed: %+v", inputs)
	}
}

func TestQuestionThreadOpaqueInputDoesNotFailDiscussionParsing(t *testing.T) {
	f := newDelegFx(t)
	session, token := f.session(sessOpts{owner: "owner-1"})
	id := postAsk(t, f, token, session, "Which environment?", 1, "")
	for i, frame := range []string{`"opaque legacy frame"`, `["opaque", "frame"]`} {
		client := "opaque-" + strconv.Itoa(i)
		if _, _, err := db.AcceptSessionInput(t.Context(), f.pool, session, client, frame, messageEventID(client, "input-accepted"), `{}`); err != nil {
			t.Fatalf("opaque input rejected: %v", err)
		}
	}
	row := mustChildMessage(t, f, id)
	if row.Status != "open" || row.QuestionTurn != "person" {
		t.Fatalf("opaque input changed question: %+v", row)
	}
}

func TestQuestionThreadDeadCLIWaiterRelaysAnswer(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(strconv.FormatBool(live), func(t *testing.T) {
			f := newDelegFx(t)
			session, token := f.session(sessOpts{owner: "owner-1"})
			f.dc.SetInputReceipts(true)
			f.hub.SetSessionOwner(session, f.dc.ID)
			id := postAsk(t, f, token, session, "Which environment?", 1, "")
			// Model a CLI that stopped polling by expiring its persisted lease.
			lease := "2000-01-01"
			if live {
				lease = "2100-01-01"
			}
			if _, err := f.pool.Exec(t.Context(), `UPDATE messages SET cli_poll_until=$2::timestamptz WHERE id=$1`, id, lease); err != nil {
				t.Fatal(err)
			}
			browserSays(t, f, session, "owner-1", "Please explain.", "dead-cli-discussion", "")
			if !live {
				f.hub.SetQuestionAnswered(nil)
			}
			rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", id, f.human("owner-1"), `{"answer":"Use staging","revision_seq":0}`)
			if rec.Code != 200 {
				t.Fatal(rec.Code, rec.Body.String())
			}
			if live {
				if mustChildMessage(t, f, id).CLIWaitReleased {
					t.Fatal("live waiter released")
				}
				done, answer := f.api.answeredNow(t.Context(), httptest.NewRecorder(), id, true)
				if !done || answer == nil || !answer.Answered {
					t.Fatal("live CLI lost answer")
				}
				var cleared bool
				if err := f.pool.QueryRow(t.Context(), `SELECT cli_poll_until IS NULL FROM messages WHERE id=$1`, id).Scan(&cleared); err != nil || !cleared {
					t.Fatal("consumed answer kept lease", err)
				}
				return
			}
			// Recovery must also work if the answer-time callback was missed.
			f.hub.SetQuestionAnswered(f.api.deliverQuestionAnswer)
			redeliverQuestionAnswers(t.Context(), f.hub, f.pool, session)
			waitUntil(t, "dead CLI answer never reached Main", func() bool {
				inputs, err := db.PendingSessionInputs(t.Context(), f.pool, session)
				if err != nil {
					return false
				}
				for _, input := range inputs {
					var frame protocol.AgentUserMessage
					_ = json.Unmarshal([]byte(input.Frame), &frame)
					if frame.Source == questionAnswerSource && strings.Contains(frame.Text, "Use staging") {
						return true
					}
				}
				return false
			})
		})
	}
}

func TestQuestionThreadOnlyCLIPollsRenewLease(t *testing.T) {
	f := newDelegFx(t)
	session, token := f.session(sessOpts{owner: "owner-1"})
	f.api.answerHold = time.Nanosecond
	for _, waiter := range []string{"native", "cli"} {
		id := postAsk(t, f, token, session, "Which environment?", 1, "")
		rec := privDo(f.api.HandleGetMessageAnswer, "GET", "/x?waiter="+waiter, id, token, "")
		if rec.Code != 200 {
			t.Fatal(rec.Code, rec.Body.String())
		}
		var live bool
		if err := f.pool.QueryRow(t.Context(), `SELECT COALESCE(cli_poll_until > now() AND cli_poll_until <= now()+interval '45 seconds', false) FROM messages WHERE id=$1`, id).Scan(&live); err != nil {
			t.Fatal(err)
		}
		if live != (waiter == "cli") {
			t.Fatalf("%s lease live: %v", waiter, live)
		}
	}
}
