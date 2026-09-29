package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// TestHandlePostAndGetMessages covers create (bearer, validation) + list. DB-gated.
func TestHandlePostAndGetMessages(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	daemonID := "00000000-0000-0000-0000-0000000000e1"
	sessionID := "00000000-0000-0000-0000-0000000000e2"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "msg-api-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/repos/app", "app", "Msg API", ""); err != nil {
		t.Fatalf("InsertSession: %v", err)
	}

	api := NewAPI(NewHub(), pool, "tok", nil, "")
	// GET /api/messages is browser-gated (it returns real message bodies), so
	// this test needs a core-issued browser token as well as the daemon token
	// the POST path uses.
	mintBrowser := enableBrowserAuth(t, api)

	post := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/messages", bytes.NewBufferString(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		api.HandlePostMessages(rec, req)
		return rec
	}

	// No bearer → 401.
	if rec := post("", `{"session_id":"`+sessionID+`","kind":"ask","body":"x"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("no-bearer = %d, want 401", rec.Code)
	}
	// Bad kind → 400.
	if rec := post("tok", `{"session_id":"`+sessionID+`","kind":"bogus","body":"x"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad-kind = %d, want 400", rec.Code)
	}
	// Unknown session → 404.
	if rec := post("tok", `{"session_id":"00000000-0000-0000-0000-0000000000ff","kind":"ask","body":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown-session = %d, want 404", rec.Code)
	}
	// Valid ask → 201 with id.
	rec := post("tok", `{"session_id":"`+sessionID+`","kind":"ask","body":"green or amber?"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var created map[string]string
	json.Unmarshal(rec.Body.Bytes(), &created)
	if created["id"] == "" {
		t.Fatal("create response missing id")
	}

	// GET lists it.
	greq := httptest.NewRequest("GET", "/api/messages", nil)
	greq.Header.Set("Authorization", "Bearer "+mintBrowser(coreAuthBrowserCap))
	grec := httptest.NewRecorder()
	api.HandleGetMessages(grec, greq)
	if grec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200", grec.Code)
	}
	var list []protocol.MessageInfo
	json.Unmarshal(grec.Body.Bytes(), &list)
	if len(list) != 1 || list[0].ID != created["id"] || list[0].Kind != "ask" || list[0].Status != "open" {
		t.Errorf("list = %+v, want the open ask", list)
	}
}

// TestHandlePostMessageReply covers the POST /api/messages/{id}/reply endpoint.
// DB-gated.
func TestHandlePostMessageReply(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	daemonID := "00000000-0000-0000-0000-0000000000a1"
	sessionID := "00000000-0000-0000-0000-0000000000a2"
	db.UpsertDaemon(ctx, pool, daemonID, "reply-daemon", "local", "/repos")
	db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/repos/app", "app", "Reply Test", "")

	hub := NewHub()
	send := make(chan []byte, 10)
	hub.RegisterBrowser(&BrowserConn{ID: "b1", send: send})
	api := NewAPI(hub, pool, "tok", nil, "")

	doReply := func(id, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/messages/"+id+"/reply", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer tok")
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		api.HandlePostMessageReply(rec, req)
		return rec
	}

	// Create an open ask message and register a waiter before replying.
	askID, _ := db.CreateMessage(ctx, pool, sessionID, "ask", "green or amber?")
	waiterCh := api.waiters.channel(askID)
	waiterDone := make(chan struct{})
	go func() {
		<-waiterCh
		close(waiterDone)
	}()

	// First reply → 200.
	rec := doReply(askID, `{"answer":"amber"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("first reply = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	// Waiter must have been signalled.
	select {
	case <-waiterDone:
	case <-time.After(time.Second):
		t.Error("waiter was not signalled within 1s")
	}

	// DB must show answered with the given answer.
	row, err := db.GetMessage(ctx, pool, askID)
	if err != nil || row == nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if row.Status != "answered" || row.Answer == nil || *row.Answer != "amber" {
		t.Errorf("after reply: status=%q answer=%v, want answered/amber", row.Status, row.Answer)
	}

	// Broadcast must carry message_answered with the updated message.
	select {
	case raw := <-send:
		var got protocol.MessageAnswered
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal broadcast: %v", err)
		}
		if got.Type != "message_answered" || got.Message.ID != askID || got.Message.Status != "answered" {
			t.Errorf("broadcast = %+v, want message_answered id=%s status=answered", got, askID)
		}
	default:
		t.Error("expected message_answered broadcast but got none")
	}

	// Second reply → 409 (already answered).
	if rec2 := doReply(askID, `{"answer":"green"}`); rec2.Code != http.StatusConflict {
		t.Errorf("second reply = %d, want 409", rec2.Code)
	}

	// Reply to an update (created already-answered) → 409.
	updID, _ := db.CreateMessage(ctx, pool, sessionID, "update", "task done")
	if rec3 := doReply(updID, `{"answer":"x"}`); rec3.Code != http.StatusConflict {
		t.Errorf("reply to update = %d, want 409", rec3.Code)
	}
}

// TestHandlePostMessageExpire covers the POST /api/messages/{id}/expire endpoint.
// DB-gated.
func TestHandlePostMessageExpire(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	daemonID := "00000000-0000-0000-0000-0000000000b1"
	sessionID := "00000000-0000-0000-0000-0000000000b2"
	db.UpsertDaemon(ctx, pool, daemonID, "expire-daemon", "local", "/repos")
	db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/repos/app", "app", "Expire Test", "")

	hub := NewHub()
	send := make(chan []byte, 10)
	hub.RegisterBrowser(&BrowserConn{ID: "b1", send: send})
	api := NewAPI(hub, pool, "tok", nil, "")

	doExpire := func(id, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/messages/"+id+"/expire", nil)
		req.SetPathValue("id", id)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		api.HandlePostMessageExpire(rec, req)
		return rec
	}

	askID, _ := db.CreateMessage(ctx, pool, sessionID, "ask", "never answered")

	// No bearer → 401.
	if rec := doExpire(askID, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no-bearer expire = %d, want 401", rec.Code)
	}

	// With bearer → 200, message closed with answer=''.
	rec := doExpire(askID, "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("expire = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	row, err := db.GetMessage(ctx, pool, askID)
	if err != nil || row == nil {
		t.Fatalf("GetMessage after expire: %v", err)
	}
	if row.Status != "answered" || row.Answer == nil || *row.Answer != "" {
		t.Errorf("after expire: status=%q answer=%v, want answered/empty-string", row.Status, row.Answer)
	}

	// Broadcast must carry message_answered.
	select {
	case raw := <-send:
		var got protocol.MessageAnswered
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal broadcast: %v", err)
		}
		if got.Type != "message_answered" || got.Message.ID != askID {
			t.Errorf("broadcast = %+v, want message_answered id=%s", got, askID)
		}
	default:
		t.Error("expected message_answered broadcast but got none")
	}

	// Second expire → 200 (idempotent) with no broadcast.
	// Drain any leftovers first.
	for {
		select {
		case <-send:
		default:
			goto drained
		}
	}
drained:
	if rec2 := doExpire(askID, "tok"); rec2.Code != http.StatusOK {
		t.Errorf("second expire = %d, want 200 (idempotent)", rec2.Code)
	}
	select {
	case raw := <-send:
		t.Errorf("unexpected second broadcast after idempotent expire: %s", raw)
	default:
		// correct — no extra broadcast
	}
}

// TestMessagesBoardToken covers board-token auth for the messaging endpoints. DB-gated.
func TestMessagesBoardToken(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	// Fixtures: daemon + two sessions.
	daemonID := "00000000-0000-0000-0000-0000000000c1"
	sessionID := "00000000-0000-0000-0000-0000000000c2"   // token's session
	otherSessID := "00000000-0000-0000-0000-0000000000c3" // sent in body to prove override
	if err := db.UpsertDaemon(ctx, pool, daemonID, "boardmsg-daemon", "local", "/repos"); err != nil {
		t.Fatalf("UpsertDaemon: %v", err)
	}
	if err := db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/repos/app", "app", "BoardMsg", ""); err != nil {
		t.Fatalf("InsertSession (token session): %v", err)
	}
	if err := db.InsertSession(ctx, pool, otherSessID, daemonID, "running", "/repos/app2", "app2", "Other", ""); err != nil {
		t.Fatalf("InsertSession (other session): %v", err)
	}

	board, err := db.CreateBoard(ctx, pool, "Msg Board", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateBoard: %v", err)
	}

	// Token WITH "message" capability.
	msgToken, _, err := db.MintBoardToken(ctx, pool, board.ID, sessionID, []string{"message"}, time.Hour)
	if err != nil {
		t.Fatalf("MintBoardToken (message): %v", err)
	}
	// Token WITHOUT "message" capability (board only).
	boardOnlyToken, _, err := db.MintBoardToken(ctx, pool, board.ID, sessionID, []string{"board"}, time.Hour)
	if err != nil {
		t.Fatalf("MintBoardToken (board-only): %v", err)
	}

	api := NewAPI(NewHub(), pool, "daemonTok", nil, "")

	postMsg := func(token, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/messages", bytes.NewBufferString(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		api.HandlePostMessages(rec, req)
		return rec
	}

	// 1. Board token WITH message cap → 201, session_id derived from token (not body).
	rec := postMsg(msgToken, `{"session_id":"`+otherSessID+`","kind":"update","body":"progress"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("board-token post = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var created map[string]string
	json.Unmarshal(rec.Body.Bytes(), &created)
	if created["id"] == "" {
		t.Fatal("create response missing id")
	}
	row, err := db.GetMessage(ctx, pool, created["id"])
	if err != nil || row == nil {
		t.Fatalf("GetMessage: %v", err)
	}
	if row.SessionID != sessionID {
		t.Errorf("message session_id = %q, want token's session %q", row.SessionID, sessionID)
	}

	// 2. Board token WITHOUT message cap → 403.
	rec = postMsg(boardOnlyToken, `{"session_id":"`+sessionID+`","kind":"update","body":"x"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("board-only token = %d, want 403", rec.Code)
	}

	// 3. Daemon token still works.
	rec = postMsg("daemonTok", `{"session_id":"`+sessionID+`","kind":"note","body":"daemon direct"}`)
	if rec.Code != http.StatusCreated {
		t.Errorf("daemon token = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}

	// 4. No token → 401.
	rec = postMsg("", `{"session_id":"`+sessionID+`","kind":"update","body":"x"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no token post = %d, want 401", rec.Code)
	}

	// 5. Answer-poll accepts board token with message cap.
	askID, _ := db.CreateMessage(ctx, pool, sessionID, "ask", "board-ask?")
	db.AnswerMessage(ctx, pool, askID, "yes")

	ansReq := httptest.NewRequest("GET", "/api/messages/"+askID+"/answer", nil)
	ansReq.SetPathValue("id", askID)
	ansReq.Header.Set("Authorization", "Bearer "+msgToken)
	ansRec := httptest.NewRecorder()
	api.HandleGetMessageAnswer(ansRec, ansReq)
	if ansRec.Code != http.StatusOK {
		t.Errorf("answer-poll board token = %d, want 200; body=%s", ansRec.Code, ansRec.Body.String())
	}
	var ansResp answerResponse
	json.Unmarshal(ansRec.Body.Bytes(), &ansResp)
	if !ansResp.Answered {
		t.Errorf("answer-poll: answered=false, want true")
	}

	// 6. Answer-poll: board-only token → 403.
	ansReq2 := httptest.NewRequest("GET", "/api/messages/"+askID+"/answer", nil)
	ansReq2.SetPathValue("id", askID)
	ansReq2.Header.Set("Authorization", "Bearer "+boardOnlyToken)
	ansRec2 := httptest.NewRecorder()
	api.HandleGetMessageAnswer(ansRec2, ansReq2)
	if ansRec2.Code != http.StatusForbidden {
		t.Errorf("answer-poll board-only token = %d, want 403", ansRec2.Code)
	}

	// 7. Answer-poll: no token → 401.
	ansReq3 := httptest.NewRequest("GET", "/api/messages/"+askID+"/answer", nil)
	ansReq3.SetPathValue("id", askID)
	ansRec3 := httptest.NewRecorder()
	api.HandleGetMessageAnswer(ansRec3, ansReq3)
	if ansRec3.Code != http.StatusUnauthorized {
		t.Errorf("answer-poll no token = %d, want 401", ansRec3.Code)
	}

	// ── expire endpoint board-token coverage ──────────────────────────────────
	expire := func(id, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/messages/"+id+"/expire", nil)
		req.SetPathValue("id", id)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		api.HandlePostMessageExpire(rec, req)
		return rec
	}

	// 8. Expire: no token → 401.
	expAskID, _ := db.CreateMessage(ctx, pool, sessionID, "ask", "expire-no-token?")
	if rec := expire(expAskID, ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("expire no token = %d, want 401", rec.Code)
	}

	// 9. Expire: board-only token (no message cap) → 403.
	if rec := expire(expAskID, boardOnlyToken); rec.Code != http.StatusForbidden {
		t.Errorf("expire board-only token = %d, want 403", rec.Code)
	}

	// 10. Expire: board token WITH message cap → 200, message closed (answer='').
	if rec := expire(expAskID, msgToken); rec.Code != http.StatusOK {
		t.Fatalf("expire board message-token = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	expRow, err := db.GetMessage(ctx, pool, expAskID)
	if err != nil || expRow == nil {
		t.Fatalf("GetMessage after expire: %v", err)
	}
	if expRow.Status != "answered" || expRow.Answer == nil || *expRow.Answer != "" {
		t.Errorf("after expire: status=%q answer=%v, want answered/empty-string", expRow.Status, expRow.Answer)
	}

	// ── cross-session ownership checks ────────────────────────────────────────
	// Create a message owned by otherSessID, not by sessionID (the token's session).
	foreignMsgID, _ := db.CreateMessage(ctx, pool, otherSessID, "ask", "owned-by-other?")

	// 11. answer-poll on a foreign message → 403.
	foreignAnsReq := httptest.NewRequest("GET", "/api/messages/"+foreignMsgID+"/answer", nil)
	foreignAnsReq.SetPathValue("id", foreignMsgID)
	foreignAnsReq.Header.Set("Authorization", "Bearer "+msgToken)
	foreignAnsRec := httptest.NewRecorder()
	api.HandleGetMessageAnswer(foreignAnsRec, foreignAnsReq)
	if foreignAnsRec.Code != http.StatusForbidden {
		t.Errorf("answer-poll foreign message = %d, want 403; body=%s", foreignAnsRec.Code, foreignAnsRec.Body.String())
	}

	// 12. expire on a foreign message → 403.
	if rec := expire(foreignMsgID, msgToken); rec.Code != http.StatusForbidden {
		t.Errorf("expire foreign message = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}

	// 13. answer-poll on own message works fine (sanity check with answered msg).
	ownMsgID, _ := db.CreateMessage(ctx, pool, sessionID, "ask", "own-ask?")
	db.AnswerMessage(ctx, pool, ownMsgID, "yes")
	ownAnsReq := httptest.NewRequest("GET", "/api/messages/"+ownMsgID+"/answer", nil)
	ownAnsReq.SetPathValue("id", ownMsgID)
	ownAnsReq.Header.Set("Authorization", "Bearer "+msgToken)
	ownAnsRec := httptest.NewRecorder()
	api.HandleGetMessageAnswer(ownAnsRec, ownAnsReq)
	if ownAnsRec.Code != http.StatusOK {
		t.Errorf("answer-poll own message = %d, want 200; body=%s", ownAnsRec.Code, ownAnsRec.Body.String())
	}

	// 14. Daemon token can poll/expire ANY session's messages (no ownership check).
	daemonForeignReq := httptest.NewRequest("GET", "/api/messages/"+foreignMsgID+"/answer", nil)
	daemonForeignReq.SetPathValue("id", foreignMsgID)
	daemonForeignReq.Header.Set("Authorization", "Bearer daemonTok")
	daemonForeignRec := httptest.NewRecorder()
	api.HandleGetMessageAnswer(daemonForeignRec, daemonForeignReq)
	if daemonForeignRec.Code != http.StatusOK {
		t.Errorf("daemon-token answer-poll foreign = %d, want 200 (no ownership check); body=%s", daemonForeignRec.Code, daemonForeignRec.Body.String())
	}
}

// TestFormatNoteReply covers the pure helper that builds the inject text.
// No DB needed — unit-testable in isolation.
func TestFormatNoteReply(t *testing.T) {
	t.Run("basic shape", func(t *testing.T) {
		got := formatNoteReply("my note", "my reply")
		want := `[Re: "my note"] my reply`
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("CR stripped from note", func(t *testing.T) {
		got := formatNoteReply("my\rnote", "reply")
		want := `[Re: "mynote"] reply`
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("LF stripped from note", func(t *testing.T) {
		got := formatNoteReply("my\nnote", "reply")
		want := `[Re: "mynote"] reply`
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("CRLF stripped from reply", func(t *testing.T) {
		got := formatNoteReply("note", "my\r\nreply")
		want := `[Re: "note"] myreply`
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("note truncated at 80 chars", func(t *testing.T) {
		note := strings.Repeat("x", 100)
		got := formatNoteReply(note, "answer")
		notePart := strings.Repeat("x", 80)
		want := `[Re: "` + notePart + `"] answer`
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("empty strings", func(t *testing.T) {
		got := formatNoteReply("", "")
		want := `[Re: ""] `
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("multibyte note truncated at 80 runes not bytes", func(t *testing.T) {
		// Each 'あ' is 3 bytes in UTF-8. 81 runes = 243 bytes. Byte-slicing at 80
		// would land mid-rune and produce invalid UTF-8 or a garbled prefix; rune-
		// aware truncation must yield exactly 80 runes of valid output.
		note := strings.Repeat("あ", 81)
		got := formatNoteReply(note, "answer")
		notePart := strings.Repeat("あ", 80)
		want := `[Re: "` + notePart + `"] answer`
		if got != want {
			t.Errorf("multibyte truncation:\n got  = %q\n want = %q", got, want)
		}
	})
}

// TestHandleGetMessageAnswer covers the ask short-hold poll: immediate when already
// answered, false after a short hold when open, and waking on signal. DB-gated.
func TestHandleGetMessageAnswer(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	daemonID := "00000000-0000-0000-0000-0000000000f1"
	sessionID := "00000000-0000-0000-0000-0000000000f2"
	db.UpsertDaemon(ctx, pool, daemonID, "ans-daemon", "local", "/repos")
	db.InsertSession(ctx, pool, sessionID, daemonID, "running", "/repos/app", "app", "Ans", "")
	api := NewAPI(NewHub(), pool, "tok", nil, "")

	answer := func(id, query string) (*httptest.ResponseRecorder, answerResponse) {
		req := httptest.NewRequest("GET", "/api/messages/"+id+"/answer"+query, nil)
		req.SetPathValue("id", id)
		req.Header.Set("Authorization", "Bearer tok")
		rec := httptest.NewRecorder()
		api.HandleGetMessageAnswer(rec, req)
		var resp answerResponse
		json.Unmarshal(rec.Body.Bytes(), &resp)
		return rec, resp
	}

	// Already answered (an update) → immediate.
	updID, _ := db.CreateMessage(ctx, pool, sessionID, "update", "done")
	if _, resp := answer(updID, ""); !resp.Answered {
		t.Errorf("answered update → %+v, want answered=true", resp)
	}

	// Open ask, short wait, no answer → answered=false after the hold.
	askID, _ := db.CreateMessage(ctx, pool, sessionID, "ask", "?")
	start := time.Now()
	if _, resp := answer(askID, "?wait=1"); resp.Answered {
		t.Errorf("open ask short wait → %+v, want answered=false", resp)
	}
	if time.Since(start) < 800*time.Millisecond {
		t.Errorf("hold returned too fast (%v); should wait ~1s", time.Since(start))
	}

	// Open ask, longer wait, answered+signalled concurrently → wakes with the answer.
	ask2, _ := db.CreateMessage(ctx, pool, sessionID, "ask", "green or amber?")
	go func() {
		time.Sleep(150 * time.Millisecond)
		db.AnswerMessage(ctx, pool, ask2, "amber")
		api.waiters.signal(ask2)
	}()
	st := time.Now()
	if _, resp := answer(ask2, "?wait=10"); !resp.Answered || resp.Answer == nil || *resp.Answer != "amber" {
		t.Errorf("signalled ask → %+v, want answered=true amber", resp)
	}
	if time.Since(st) > 3*time.Second {
		t.Errorf("signal didn't wake promptly (%v)", time.Since(st))
	}
}
