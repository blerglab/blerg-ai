package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// `blerg-runner ask --no-wait`: a question the agent does not wait on. It is listed like any ask
// (owner-only, in the transcript with no_wait), a chat message does not answer it and a daemon
// restart does not close it; the owner's answer is recorded as the server's ask_answer and sent to
// the session as a question_answer message naming the question; the session's own token closes it
// without an answer. Requires TEST_DATABASE_URL.
func TestNoWaitQuestionIsAnsweredByTheOwnerAndSentToTheAgent(t *testing.T) {
	f := newPrivFixture(t)
	ctx := context.Background()
	board, err := db.CreateBoard(ctx, f.pool, "questions", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sessTok, _, err := db.MintBoardToken(ctx, f.pool, board.ID, f.pubSession, []string{"message"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ownerTok := f.tok(t, "human", privAcctA, coreAuthBrowserCap, "session.start")
	otherTok := f.tok(t, "human", privAcctB, coreAuthBrowserCap, "session.start")

	ask := func(body string) string {
		t.Helper()
		rec := privDo(f.api.HandlePostMessages, "POST", "/api/messages", "", sessTok,
			`{"session_id":"`+f.pubSession+`","kind":"ask","no_wait":true,"body":"`+body+`"}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("post question = %d %s", rec.Code, rec.Body.String())
		}
		var out struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.ID == "" {
			t.Fatalf("post question reply: %s", rec.Body.String())
		}
		return out.ID
	}
	id := ask("Ship the blue one?")
	row, err := db.GetMessage(ctx, f.pool, id)
	if err != nil || row == nil || !row.NoWait || !row.OwnerOnly || row.Status != "open" {
		t.Fatalf("question stored as %+v %v, want an open owner-only no_wait ask", row, err)
	}
	var raw []byte
	if err := f.pool.QueryRow(ctx, `SELECT payload FROM agent_events WHERE session_id=$1 AND client_event_id=$2`, f.pubSession, messageEventID(id, "ask")).Scan(&raw); err != nil {
		t.Fatalf("question not in the transcript: %v", err)
	}
	var p messagingPayload
	if json.Unmarshal(raw, &p) != nil || !p.NoWait || p.MessageID != id {
		t.Fatalf("transcript ask = %s, want no_wait with the message id", raw)
	}

	// The owner's chat message goes to the agent: it does not answer the question.
	if answerAskFromChatAs(ctx, f.hub, f.pool, f.pubSession, "also fix the footer", newUUID(), "", privAcctA) {
		t.Fatal("a chat message answered a question the agent did not wait on")
	}
	// A daemon restart closes the asks it waited on, not this one.
	closed, err := db.CloseOpenAsksForSession(ctx, f.pool, f.pubSession)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range closed {
		if c.ID == id {
			t.Fatal("a daemon restart closed a question the agent did not wait on")
		}
	}
	for name, tok := range map[string]string{"other person": otherTok, "session token": sessTok} {
		if rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", id, tok, `{"answer":"no"}`); rec.Code == http.StatusOK {
			t.Fatalf("%s answered the owner's question", name)
		}
	}
	drain(f.dc.send)
	if rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", id, ownerTok, `{"answer":"yes, the blue one"}`); rec.Code != http.StatusOK {
		t.Fatalf("owner reply = %d %s", rec.Code, rec.Body.String())
	}
	if err := f.pool.QueryRow(ctx, `SELECT payload FROM agent_events WHERE session_id=$1 AND client_event_id=$2`, f.pubSession, messageEventID(id, "answer")).Scan(&raw); err != nil {
		t.Fatalf("answer not recorded: %v", err)
	}
	var answer askAnswerPayload
	if json.Unmarshal(raw, &answer) != nil || answer.Source != "ask_answer" || answer.MessageID != id || answer.Text != "yes, the blue one" {
		t.Fatalf("recorded answer = %s", raw)
	}
	sent := awaitDaemonMessage(t, f.dc.send, "agent_user_message")
	var msg protocol.AgentUserMessage
	if err := json.Unmarshal(sent, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.SessionID != f.pubSession || msg.Source != questionAnswerSource ||
		!strings.Contains(msg.Text, id) || !strings.Contains(msg.Text, "Ship the blue one?") || !strings.HasSuffix(msg.Text, ": yes, the blue one") {
		t.Fatalf("agent got %+v, want the answer tied to question %s", msg, id)
	}

	// Answered in the chat instead: the agent closes it with its own token, and nothing is sent.
	second := ask("Rename the flag?")
	if rec := privDo(f.api.HandlePostMessageExpire, "POST", "/x", second, sessTok, `{}`); rec.Code != http.StatusOK {
		t.Fatalf("resolve = %d %s", rec.Code, rec.Body.String())
	}
	if row, _ := db.GetMessage(ctx, f.pool, second); row == nil || row.Status != "answered" || row.Answer == nil || *row.Answer != "" {
		t.Fatalf("resolved question = %+v", row)
	}
	select {
	case m := <-f.dc.send:
		if strings.Contains(string(m), questionAnswerSource) {
			t.Fatalf("a resolved question sent an answer: %s", m)
		}
	case <-time.After(300 * time.Millisecond):
	}

	// The owner answering from the question's card (a chat message addressed to it) is delivered too.
	third := ask("Keep the old API?")
	drain(f.dc.send)
	if !answerAskFromChatAs(ctx, f.hub, f.pool, f.pubSession, "keep it", newUUID(), third, privAcctA) {
		t.Fatal("the owner's addressed answer did not answer the question")
	}
	sent = awaitDaemonMessage(t, f.dc.send, "agent_user_message")
	if err := json.Unmarshal(sent, &msg); err != nil || msg.Source != questionAnswerSource || !strings.Contains(msg.Text, third) {
		t.Fatalf("addressed answer reached the agent as %s", sent)
	}
}

func TestNoWaitQuestionNeedsASignedInOwner(t *testing.T) {
	f := newPrivFixture(t)
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET spawning_account_id=NULL,started_by_kind='runner_key' WHERE id=$1`, f.pubSession); err != nil {
		t.Fatal(err)
	}
	rec := privDo(f.api.HandlePostMessages, "POST", "/api/messages", "", spawnTokenDaemonTok,
		`{"session_id":"`+f.pubSession+`","kind":"ask","no_wait":true,"body":"q"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("question without an owner = %d %s, want 409", rec.Code, rec.Body.String())
	}
	if rec := privDo(f.api.HandlePostMessages, "POST", "/api/messages", "", spawnTokenDaemonTok,
		`{"session_id":"`+f.pubSession+`","kind":"note","no_wait":true,"body":"q"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("no_wait on a note = %d, want 400", rec.Code)
	}
}

// No caller writes in the voice the server uses for a question's answer.
func TestQuestionAnswerSourceIsReserved(t *testing.T) {
	if !reservedMessageSource(questionAnswerSource) {
		t.Fatal("question_answer is not reserved")
	}
}

func drain(ch chan []byte) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// awaitDaemonMessage returns the next frame of type typ the daemon is sent within 5 s.
func awaitDaemonMessage(t *testing.T, ch chan []byte, typ string) []byte {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case m := <-ch:
			var head struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(m, &head) == nil && head.Type == typ {
				return m
			}
		case <-deadline:
			t.Fatalf("no %s sent to the daemon", typ)
			return nil
		}
	}
}

// A question only its owner answers notifies its owner only: the owner's socket gets it answerable
// and loud, another viewer of the shared session gets it quiet and not answerable (they see it
// read-only in the session), and the messages list and initial state say the same per viewer.
func TestNoWaitQuestionNotifiesOnlyItsOwner(t *testing.T) {
	f := newPrivFixture(t)
	ctx := context.Background()
	owner := &BrowserConn{ID: "q-owner", AccountID: privAcctA, send: make(chan []byte, 32)}
	other := &BrowserConn{ID: "q-other", AccountID: privAcctB, send: make(chan []byte, 32)}
	f.hub.RegisterBrowser(owner)
	f.hub.RegisterBrowser(other)
	rec := privDo(f.api.HandlePostMessages, "POST", "/api/messages", "", spawnTokenDaemonTok,
		`{"session_id":"`+f.pubSession+`","kind":"ask","no_wait":true,"body":"Ship blue?"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("post = %d %s", rec.Code, rec.Body.String())
	}
	created := func(ch chan []byte) protocol.MessageCreated {
		t.Helper()
		for m := range awaitAll(ch) {
			var mc protocol.MessageCreated
			if json.Unmarshal(m, &mc) == nil && mc.Type == "message_created" {
				return mc
			}
		}
		t.Fatal("no message_created")
		return protocol.MessageCreated{}
	}
	if mc := created(owner.send); !mc.Message.Answerable || mc.Quiet {
		t.Fatalf("owner got %+v, want answerable and not quiet", mc)
	}
	if mc := created(other.send); mc.Message.Answerable || !mc.Quiet {
		t.Fatalf("other viewer got %+v, want quiet and not answerable", mc)
	}
	list := func(tok string) []protocol.MessageInfo {
		t.Helper()
		rec := privDo(f.api.HandleGetMessages, "GET", "/api/messages", "", tok, "")
		var out []protocol.MessageInfo
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
		}
		return out
	}
	answerable := func(infos []protocol.MessageInfo) (seen, mine bool) {
		for _, m := range infos {
			if m.NoWait {
				seen, mine = true, m.Answerable
			}
		}
		return
	}
	if seen, mine := answerable(list(f.tok(t, "human", privAcctA, coreAuthBrowserCap, "session.start"))); !seen || !mine {
		t.Fatal("the owner's list does not hold the question as answerable")
	}
	if seen, mine := answerable(list(f.tok(t, "human", privAcctB, coreAuthBrowserCap, "session.start"))); !seen || mine {
		t.Fatal("another viewer's list must hold the question read-only")
	}
	if seen, mine := answerable(list(f.tok(t, "agent", privAcctA, coreAuthBrowserCap, "session.start"))); !seen || mine {
		t.Fatal("an agent token acting for the owner must not get the question as answerable")
	}
	_ = ctx
}

// awaitAll yields what ch holds now and for 300 ms more.
func awaitAll(ch chan []byte) <-chan []byte {
	out := make(chan []byte, 64)
	go func() {
		defer close(out)
		deadline := time.After(300 * time.Millisecond)
		for {
			select {
			case m := <-ch:
				out <- m
			case <-deadline:
				return
			}
		}
	}()
	return out
}

// An answer recorded but never delivered (the server stopped between the two) is delivered on the
// daemon's next heartbeat, once: the outbox row it leaves keeps it from being sent again.
func TestNoWaitAnswerLostToARestartIsDeliveredOnce(t *testing.T) {
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
	// Answered, as the reply would, with no delivery after it.
	if changed, err := db.AnswerMessage(ctx, f.pool, id, "yes"); err != nil || !changed {
		t.Fatal(err)
	}
	drain(f.dc.send)
	replayPendingSessionInputs(ctx, f.hub, f.pool, f.dc, []string{f.sessionID})
	sent := awaitDaemonMessage(t, f.dc.send, "agent_user_message")
	var msg protocol.AgentUserMessage
	if err := json.Unmarshal(sent, &msg); err != nil || msg.Source != questionAnswerSource || !strings.Contains(msg.Text, id) || msg.ClientID != "question-"+id {
		t.Fatalf("redelivered %s", sent)
	}
	// The delivery left its outbox row: nothing is undelivered now.
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
}

// The owner answering a question while the session's desktop daemon is away: the answer is
// recorded at once (the question closes) and its delivery waits in the outbox, tied to the
// question, instead of reaching the agent later as a bare chat message.
func TestNoWaitAnswerWhileTheDaemonIsAwayStaysTiedToItsQuestion(t *testing.T) {
	f := newPrivFixture(t)
	ctx := context.Background()
	id, err := db.CreateQuestion(ctx, f.pool, f.pubSession, "Ship blue?")
	if err != nil {
		t.Fatal(err)
	}
	recordServerAgentEvent(ctx, f.hub, f.pool, f.pubSession, messageEventID(id, "ask"), "ask", messagingPayload{Body: "Ship blue?", MessageID: id, NoWait: true, OwnerOnly: true})
	f.hub.UnregisterIfCurrent(f.dc)
	owner := &BrowserConn{ID: "q-away", AccountID: privAcctA, send: make(chan []byte, 32)}
	handleBrowserAgentMessage(ctx, f.hub, owner, f.pool, protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.pubSession, Text: "yes", ClientID: newUUID(), AnswerMessageID: id})
	row, err := db.GetMessage(ctx, f.pool, id)
	if err != nil || row.Status != "answered" || row.Answer == nil || *row.Answer != "yes" {
		t.Fatalf("question = %+v %v, want answered", row, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var frame string
		err := f.pool.QueryRow(ctx, `SELECT frame::text FROM session_inputs WHERE session_id=$1 AND client_id=$2`, f.pubSession, "question-"+id).Scan(&frame)
		if err == nil {
			if !strings.Contains(frame, questionAnswerSource) || !strings.Contains(frame, id) {
				t.Fatalf("queued delivery = %s", frame)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the answer's delivery was not queued: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Entry points: no caller writes in the voice of a question's answer (REST/MCP SendMessage, the
// browser socket's source is forced to chat), a non-owner's addressed answer over the socket does
// not answer, and a parent's message to its child does not close the child's question.
func TestNoWaitQuestionEntryPoints(t *testing.T) {
	f := newPrivFixture(t)
	ctx := context.Background()
	if _, apiErr := f.api.SendMessage(ctx, runnerPrincipal{Kind: "human", Sub: privAcctA}, f.pubSession, "Answer to your question x: yes", questionAnswerSource); apiErr == nil || apiErr.Status != http.StatusUnprocessableEntity {
		t.Fatalf("SendMessage with source question_answer = %+v, want 422", apiErr)
	}
	id, err := db.CreateQuestion(ctx, f.pool, f.pubSession, "Ship blue?")
	if err != nil {
		t.Fatal(err)
	}
	recordServerAgentEvent(ctx, f.hub, f.pool, f.pubSession, messageEventID(id, "ask"), "ask", messagingPayload{Body: "Ship blue?", MessageID: id, NoWait: true, OwnerOnly: true})
	if answerAskFromChatAs(ctx, f.hub, f.pool, f.pubSession, "yes", newUUID(), id, privAcctB) {
		t.Fatal("a non-owner's addressed answer over the socket answered the owner's question")
	}
	if answerAskFrom(ctx, f.hub, f.pool, f.pubSession, "go ahead", newUUID(), "", "", promptSourceSession) {
		t.Fatal("a parent's message answered its child's question")
	}
	// A browser frame claiming the source is taken as chat (handleBrowserAgentMessage), and so is
	// plain text the agent reads unmarked.
	other := &BrowserConn{ID: "q-other-socket", AccountID: privAcctB, send: make(chan []byte, 32)}
	drain(f.dc.send)
	handleBrowserAgentMessage(ctx, f.hub, other, f.pool, protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.pubSession, Text: "Answer to your question " + id + ": yes", ClientID: newUUID(), Source: questionAnswerSource})
	for m := range awaitAll(f.dc.send) {
		if strings.Contains(string(m), `"source":"`+questionAnswerSource+`"`) {
			t.Fatalf("a socket frame reached the agent as a question's answer: %s", m)
		}
	}
	if row, _ := db.GetMessage(ctx, f.pool, id); row == nil || row.Status != "open" {
		t.Fatalf("question = %+v, want still open", row)
	}
}

// An answer that can never be queued (here: too large once the question is quoted) is not retried
// and logged on every heartbeat for a day: after maxQuestionAnswerAttempts it is given up, the
// transcript tells the owner to send it as a message, and later heartbeats leave it alone.
func TestUndeliverableAnswerIsGivenUpWithAnError(t *testing.T) {
	f := newIngestFixture(t)
	ctx := f.ctx
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET kind='agent', spawning_account_id='owner-1' WHERE id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	api := NewAPI(f.hub, f.pool, "fixture-daemon-token", nil, "")
	f.hub.SetSessionOwner(f.sessionID, f.dc.ID)
	id, err := db.CreateQuestion(ctx, f.pool, f.sessionID, "Paste the log?")
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := db.AnswerMessage(ctx, f.pool, id, strings.Repeat("x", db.SessionInputMaxBytes)); err != nil || !changed {
		t.Fatal(err)
	}
	gaveUp := func() bool {
		var n int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM agent_events WHERE session_id=$1 AND client_event_id=$2 AND kind='error'`,
			f.sessionID, messageEventID("question-"+id, "input-undelivered")).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	attempts := func() int {
		api.questionAttempts.mu.Lock()
		defer api.questionAttempts.mu.Unlock()
		if d := api.questionAttempts.byID[id]; d != nil {
			return d.permanent
		}
		return 0
	}
	heartbeats := 0
	for !gaveUp() {
		if heartbeats > maxQuestionAnswerAttempts+2 {
			t.Fatalf("still retried after %d heartbeats (attempts %d)", heartbeats, attempts())
		}
		before := attempts()
		replayPendingSessionInputs(ctx, f.hub, f.pool, f.dc, []string{f.sessionID})
		heartbeats++
		deadline := time.Now().Add(5 * time.Second)
		// Wait for this heartbeat's attempt to be counted; the last one is done once it gave up.
		for !gaveUp() && (attempts() <= before || attempts() >= maxQuestionAnswerAttempts) && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if heartbeats != maxQuestionAnswerAttempts {
		t.Fatalf("gave up after %d heartbeats, want %d", heartbeats, maxQuestionAnswerAttempts)
	}
	var raw string
	if err := f.pool.QueryRow(ctx, `SELECT payload::text FROM agent_events WHERE session_id=$1 AND client_event_id=$2`,
		f.sessionID, messageEventID("question-"+id, "input-undelivered")).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, id) || !strings.Contains(raw, "Paste the log?") || !strings.Contains(raw, "too large") ||
		!strings.Contains(raw, "chat message") || strings.Contains(raw, "xxxxxxxx") {
		t.Fatalf("give-up error = %s", raw)
	}
	if rows, err := db.UndeliveredQuestionAnswers(ctx, f.pool, f.sessionID); err != nil || len(rows) != 0 {
		t.Fatalf("given-up answer still listed for redelivery: %v %d", err, len(rows))
	}
	if pending, err := db.HasPendingSessionInputs(ctx, f.pool, f.sessionID); err != nil || pending {
		t.Fatalf("given-up answer left a pending input: %v %v", pending, err)
	}
	drain(f.dc.send)
	replayPendingSessionInputs(ctx, f.hub, f.pool, f.dc, []string{f.sessionID})
	time.Sleep(200 * time.Millisecond)
	if n := attempts(); n != 0 {
		t.Fatalf("a heartbeat after giving up tried again (%d)", n)
	}
}

// An answer delivered live to a host without input receipts leaves no outbox row of its own; it is
// recorded as delivered, so the first heartbeat after that host is upgraded does not send it again.
func TestAnswerDeliveredWithoutReceiptsIsNotRedeliveredAfterUpgrade(t *testing.T) {
	f := newIngestFixture(t)
	ctx := f.ctx
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET kind='agent', spawning_account_id='owner-1' WHERE id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	_ = NewAPI(f.hub, f.pool, "fixture-daemon-token", nil, "")
	f.hub.SetSessionOwner(f.sessionID, f.dc.ID)
	f.dc.SetInputReceipts(false) // an older daemon
	id, err := db.CreateQuestion(ctx, f.pool, f.sessionID, "Ship blue?")
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := db.AnswerMessage(ctx, f.pool, id, "yes"); err != nil || !changed {
		t.Fatal(err)
	}
	row, err := db.GetMessage(ctx, f.pool, id)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	drain(f.dc.send)
	f.hub.QuestionAnswered(*row, "yes", "owner-1")
	sent := awaitDaemonMessage(t, f.dc.send, "agent_user_message")
	var msg protocol.AgentUserMessage
	if err := json.Unmarshal(sent, &msg); err != nil || msg.Source != questionAnswerSource || msg.ClientID != "" {
		t.Fatalf("delivered %s, want the live answer with no client id", sent)
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
			t.Fatal("a live delivery to a host without receipts was not recorded")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pending, err := db.HasPendingSessionInputs(ctx, f.pool, f.sessionID); err != nil || pending {
		t.Fatalf("the recorded delivery is pending: %v %v", pending, err)
	}
	// The host is upgraded: its first heartbeat must not deliver the answer again.
	f.dc.SetInputReceipts(true)
	replayPendingSessionInputs(ctx, f.hub, f.pool, f.dc, []string{f.sessionID})
	for m := range awaitAll(f.dc.send) {
		if strings.Contains(string(m), questionAnswerSource) {
			t.Fatalf("delivered again after the upgrade: %s", m)
		}
	}
}

// Only a permanent failure uses up one of an answer's attempts. A transient one (here a full input
// queue, 429; a busy continuation or a database error are 5xx and alike) is retried on every
// heartbeat however long it lasts, and the answer is delivered once it clears.
func TestTransientAnswerFailuresNeverGiveUp(t *testing.T) {
	f := newIngestFixture(t)
	ctx := f.ctx
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET kind='agent', spawning_account_id='owner-1' WHERE id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	api := NewAPI(f.hub, f.pool, "fixture-daemon-token", nil, "")
	failures := make(chan bool, 64)
	api.questionDeliveryFailed = func(_ string, permanent bool) { failures <- permanent }
	f.hub.SetSessionOwner(f.sessionID, f.dc.ID)
	// A full queue: the answer cannot be queued until something settles.
	for i := range db.SessionInputMaxPending {
		if _, err := acceptSessionInput(ctx, f.hub, f.pool, protocol.AgentUserMessage{Type: "agent_user_message", SessionID: f.sessionID,
			Text: "filler", ClientID: "fill-" + strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	id, err := db.CreateQuestion(ctx, f.pool, f.sessionID, "Ship blue?")
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := db.AnswerMessage(ctx, f.pool, id, "yes"); err != nil || !changed {
		t.Fatal(err)
	}
	// Ten heartbeats (five minutes of a real host's), each failing transiently.
	for i := range 2 * 5 {
		drain(f.dc.send)
		replayPendingSessionInputs(ctx, f.hub, f.pool, f.dc, []string{f.sessionID})
		select {
		case permanent := <-failures:
			if permanent {
				t.Fatalf("heartbeat %d: a full queue was counted as a permanent failure", i+1)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("heartbeat %d: no delivery attempt", i+1)
		}
	}
	var gaveUp int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM agent_events WHERE session_id=$1 AND client_event_id=$2`,
		f.sessionID, messageEventID("question-"+id, "input-undelivered")).Scan(&gaveUp); err != nil {
		t.Fatal(err)
	}
	if rows, err := db.UndeliveredQuestionAnswers(ctx, f.pool, f.sessionID); err != nil || len(rows) != 1 || gaveUp != 0 {
		t.Fatalf("transient failures gave the answer up: undelivered %d (%v), give-up events %d", len(rows), err, gaveUp)
	}
	// The queue clears: the next heartbeat delivers it.
	if _, err := f.pool.Exec(ctx, `UPDATE session_inputs SET settled_at = now() WHERE session_id = $1 AND client_id LIKE 'fill-%'`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	drain(f.dc.send)
	replayPendingSessionInputs(ctx, f.hub, f.pool, f.dc, []string{f.sessionID})
	sent := awaitDaemonMessage(t, f.dc.send, "agent_user_message")
	var msg protocol.AgentUserMessage
	if err := json.Unmarshal(sent, &msg); err != nil || msg.ClientID != "question-"+id {
		t.Fatalf("after the queue cleared: %s", sent)
	}
}

// A missing session is 404 and a database error is 503 (review I1): the shared access check used to
// answer 404 for both, and a question's delivery counted that as a permanent refusal. A malformed id
// is still "not found".
func TestRequireSessionAccessTellsMissingFromUnavailable(t *testing.T) {
	f := newIngestFixture(t)
	api := NewAPI(f.hub, f.pool, "fixture-daemon-token", nil, "")
	owner := runnerPrincipal{Kind: "human", Sub: "owner-1"}
	if _, err := f.pool.Exec(f.ctx, `UPDATE sessions SET spawning_account_id='owner-1' WHERE id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	if _, e := api.requireSessionAccess(f.ctx, owner, f.sessionID); e != nil {
		t.Fatalf("own session: %+v", e)
	}
	for name, id := range map[string]string{"malformed": "not-a-uuid", "unknown": "00000000-0000-4000-8000-00000000dead"} {
		if _, e := api.requireSessionAccess(f.ctx, owner, id); e == nil || e.Status != http.StatusNotFound {
			t.Fatalf("%s session: %+v, want 404", name, e)
		}
	}
	ctx, cancel := context.WithCancel(f.ctx)
	cancel()
	if _, e := api.requireSessionAccess(ctx, owner, f.sessionID); e == nil || e.Status != http.StatusServiceUnavailable || e.RetryAfter == 0 {
		t.Fatalf("database error: %+v, want a retryable 503", e)
	}
}

// Database errors during an answer's delivery, however many, never give the answer up (review I1).
func TestDatabaseErrorsNeverGiveUpAnAnswer(t *testing.T) {
	f := newIngestFixture(t)
	ctx := f.ctx
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET kind='agent', spawning_account_id='owner-1' WHERE id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	api := NewAPI(f.hub, f.pool, "fixture-daemon-token", nil, "")
	id, err := db.CreateQuestion(ctx, f.pool, f.sessionID, "Ship blue?")
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := db.AnswerMessage(ctx, f.pool, id, "yes"); err != nil || !changed {
		t.Fatal(err)
	}
	row, err := db.GetMessage(ctx, f.pool, id)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	down, cancel := context.WithCancel(ctx)
	cancel() // every query of the delivery fails, as with a database that times out
	for i := range 2 * maxQuestionAnswerAttempts {
		apiErr := api.sendRelay(down, runnerPrincipal{Kind: "human", Sub: "owner-1"}, f.sessionID, questionAnswerText(*row, "yes"), questionAnswerSource, questionAnswerClientID(id))
		if apiErr == nil || permanentDeliveryFailure(apiErr) {
			t.Fatalf("delivery %d with the database down: %+v, want a transient failure", i+1, apiErr)
		}
		api.questionAnswerFailed(ctx, *row, apiErr)
	}
	if rows, err := db.UndeliveredQuestionAnswers(ctx, f.pool, f.sessionID); err != nil || len(rows) != 1 {
		t.Fatalf("database errors gave the answer up: %d listed (%v)", len(rows), err)
	}
}

func TestPermanentDeliveryFailureStatuses(t *testing.T) {
	for status, want := range map[int]bool{
		http.StatusNotFound: true, http.StatusGone: true, http.StatusConflict: true, http.StatusUnprocessableEntity: true,
		http.StatusBadRequest: false, http.StatusForbidden: false, http.StatusRequestTimeout: false, http.StatusTooManyRequests: false,
		http.StatusInternalServerError: false, http.StatusServiceUnavailable: false,
	} {
		if got := permanentDeliveryFailure(&APIError{Status: status}); got != want {
			t.Errorf("%d: permanent = %v, want %v", status, got, want)
		}
	}
}

// A question's delivery state goes when its session ends and once it is past the retry window
// (review M2), not only when the answer is delivered or given up.
func TestQuestionDeliveryStateIsForgotten(t *testing.T) {
	f := newIngestFixture(t)
	api := NewAPI(f.hub, f.pool, "fixture-daemon-token", nil, "")
	api.questionAttempts.permanentFailure("q-ended", f.sessionID)
	api.questionAttempts.permanentFailure("q-other", "00000000-0000-4000-8000-0000000000ff")
	HandleSessionEnded(f.ctx, f.hub, f.pool, protocol.SessionEnded{Type: "session_ended", SessionID: f.sessionID})
	api.questionAttempts.mu.Lock()
	_, ended := api.questionAttempts.byID["q-ended"]
	_, other := api.questionAttempts.byID["q-other"]
	api.questionAttempts.mu.Unlock()
	if ended || !other {
		t.Fatalf("after the session ended: its question kept=%v, another session's kept=%v", ended, other)
	}
	now := time.Now()
	api.questionAttempts.now = func() time.Time { return now }
	api.questionAttempts.transientFailure("q-old", "00000000-0000-4000-8000-0000000000ee")
	now = now.Add(questionAnswerRetryWindow + time.Minute)
	api.questionAttempts.transientFailure("q-new", "00000000-0000-4000-8000-0000000000ee")
	if n := api.questionAttempts.len(); n != 1 {
		t.Fatalf("%d entries kept past the retry window, want only the new one", n)
	}
}

// Transient failures that last ten minutes tell the owner once that the answer is still waiting
// (review M3); a restart of the count does not repeat the notice.
func TestLongTransientFailureTellsTheOwnerOnce(t *testing.T) {
	f := newIngestFixture(t)
	ctx := f.ctx
	if _, err := f.pool.Exec(ctx, `UPDATE sessions SET kind='agent', spawning_account_id='owner-1' WHERE id=$1`, f.sessionID); err != nil {
		t.Fatal(err)
	}
	api := NewAPI(f.hub, f.pool, "fixture-daemon-token", nil, "")
	now := time.Now()
	api.questionAttempts.now = func() time.Time { return now }
	id, err := db.CreateQuestion(ctx, f.pool, f.sessionID, "Ship blue?")
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.GetMessage(ctx, f.pool, id)
	if err != nil || row == nil {
		t.Fatal(err)
	}
	notices := func() int {
		var n int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM agent_events WHERE session_id=$1 AND client_event_id=$2`,
			f.sessionID, messageEventID(questionAnswerClientID(id), "input-delayed")).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	busy := apiErrorf(http.StatusTooManyRequests, "%s", "too many pending messages.")
	start := now
	for _, at := range []time.Duration{0, 5 * time.Minute, 9 * time.Minute} {
		now = start.Add(at)
		api.questionAnswerFailed(ctx, *row, busy)
	}
	now = start.Add(9*time.Minute + 59*time.Second)
	api.questionAnswerFailed(ctx, *row, busy)
	if n := notices(); n != 0 {
		t.Fatalf("told the owner after under ten minutes (%d notices)", n)
	}
	now = start.Add(10 * time.Minute)
	api.questionAnswerFailed(ctx, *row, busy)
	now = start.Add(11 * time.Minute)
	api.questionAnswerFailed(ctx, *row, busy)
	if n := notices(); n != 1 {
		t.Fatalf("%d notices after ten minutes of transient failures, want 1", n)
	}
	var raw string
	if err := f.pool.QueryRow(ctx, `SELECT payload::text FROM agent_events WHERE session_id=$1 AND client_event_id=$2`,
		f.sessionID, messageEventID(questionAnswerClientID(id), "input-delayed")).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "still waiting") || !strings.Contains(raw, "Ship blue?") || !strings.Contains(raw, "too many pending messages") {
		t.Fatalf("notice = %s", raw)
	}
	// A restart forgets the count; the notice is not repeated.
	api.questionAttempts = questionAttempts{now: func() time.Time { return now }}
	api.questionAnswerFailed(ctx, *row, busy)
	now = now.Add(15 * time.Minute)
	api.questionAnswerFailed(ctx, *row, busy)
	if n := notices(); n != 1 {
		t.Fatalf("%d notices after a restart, want still 1", n)
	}
}
