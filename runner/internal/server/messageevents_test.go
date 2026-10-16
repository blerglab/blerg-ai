package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// `blerg-runner ask|update|note` in an agent session: each message is recorded in the transcript as
// an event of its kind with {body, message_id}; a reply on the ask, or a chat message typed while it
// is open, answers it and is recorded as user_message{source: ask_answer}. The native loop's own
// messages (daemon token) and a terminal session's are not recorded. Requires TEST_DATABASE_URL.
func TestMessagesAreRecordedInTheAgentTranscript(t *testing.T) {
	pool := connectSrvTestDB(t)
	ctx := context.Background()
	setupSrvTestSchema(t, pool)
	if err := db.RunMigrations(ctx, pool); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	daemonID := "00000000-0000-0000-0000-0000000000e1"
	agentSess := "00000000-0000-0000-0000-0000000000e2"
	termSess := "00000000-0000-0000-0000-0000000000e3"
	if err := db.UpsertDaemon(ctx, pool, daemonID, "d", "local", "/repos"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{agentSess, termSess} {
		if err := db.InsertSession(ctx, pool, id, daemonID, "running", "/repos/app", "app", "T", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.SetSessionKind(ctx, pool, agentSess, "agent"); err != nil {
		t.Fatal(err)
	}
	board, err := db.CreateBoard(ctx, pool, "B", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	agentTok, _, err := db.MintBoardToken(ctx, pool, board.ID, agentSess, []string{"message"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	termTok, _, err := db.MintBoardToken(ctx, pool, board.ID, termSess, []string{"message"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	hub := NewHub()
	api := NewAPI(hub, pool, "daemonTok", nil, "")
	sub := make(chan []byte, 64)
	hub.Subscribe(agentSess, "b1", sub)

	post := func(token, sessionID, kind, body string) string {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/messages", bytes.NewBufferString(
			`{"session_id":"`+sessionID+`","kind":"`+kind+`","body":"`+body+`"}`))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		api.HandlePostMessages(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("post %s = %d: %s", kind, rec.Code, rec.Body.String())
		}
		var out map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out["id"]
	}
	reply := func(id, answer string) int {
		req := httptest.NewRequest("POST", "/api/messages/"+id+"/reply", bytes.NewBufferString(`{"answer":"`+answer+`"}`))
		req.Header.Set("Authorization", "Bearer daemonTok")
		req.SetPathValue("id", id)
		rec := httptest.NewRecorder()
		api.HandlePostMessageReply(rec, req)
		return rec.Code
	}
	type ev struct {
		kind    string
		payload map[string]any
	}
	transcript := func(sessionID string) []ev {
		t.Helper()
		rows, err := db.ListAgentEvents(ctx, pool, sessionID, 0, 500)
		if err != nil {
			t.Fatal(err)
		}
		var out []ev
		for _, r := range rows {
			var p map[string]any
			_ = json.Unmarshal([]byte(r.Payload), &p)
			out = append(out, ev{r.Kind, p})
		}
		return out
	}

	upd := post(agentTok, agentSess, "update", "halfway there")
	note := post(agentTok, agentSess, "note", "FYI: tests are slow")
	ask := post(agentTok, agentSess, "ask", "blue or green?")
	post(termTok, termSess, "ask", "terminal ask")
	native := post("daemonTok", agentSess, "ask", "the native loop's ask")

	got := transcript(agentSess)
	if len(got) != 3 {
		t.Fatalf("transcript = %+v, want the three blerg-runner messages only", got)
	}
	for i, want := range []struct{ kind, body, id string }{
		{"update", "halfway there", upd}, {"note", "FYI: tests are slow", note}, {"ask", "blue or green?", ask},
	} {
		if got[i].kind != want.kind || got[i].payload["body"] != want.body || got[i].payload["message_id"] != want.id {
			t.Errorf("event %d = %+v, want %s %q %s", i, got[i], want.kind, want.body, want.id)
		}
	}
	if n := len(transcript(termSess)); n != 0 {
		t.Errorf("a terminal session's transcript got %d events", n)
	}
	fanned := 0
	for len(sub) > 0 {
		var m protocol.AgentEvent
		if json.Unmarshal(<-sub, &m) == nil && m.Type == "agent_event" && m.Seq > 0 {
			fanned++
		}
	}
	if fanned != 3 {
		t.Errorf("fanned out %d events, want 3", fanned)
	}

	// The card's reply answers the ask and is recorded once.
	if code := reply(ask, "blue"); code != http.StatusOK {
		t.Fatalf("reply = %d", code)
	}
	if code := reply(ask, "green"); code != http.StatusConflict {
		t.Fatalf("second reply = %d, want 409", code)
	}
	got = transcript(agentSess)
	if last := got[len(got)-1]; len(got) != 4 || last.kind != "user_message" || last.payload["text"] != "blue" ||
		last.payload["source"] != "ask_answer" || last.payload["message_id"] != ask {
		t.Fatalf("after the reply: %+v", got)
	}

	// The native loop's ask (still open) is the loop's: a chat message is not taken for it...
	if answerAskFromChat(ctx, hub, pool, agentSess, "hello", "cl-0", "") {
		t.Fatal("a chat message answered the native loop's ask")
	}
	// ...but a chat message typed while a blerg-runner ask waits answers it, with its client id
	// (a harness command is never taken for an answer).
	ask2 := post(agentTok, agentSess, "ask", "ship it?")
	if answerAskFromChat(ctx, hub, pool, agentSess, "/model claude-opus-5", "cl-m", "") ||
		answerAskFromChat(ctx, hub, pool, agentSess, " /effort high", "cl-e", "") {
		t.Fatal("a /model or /effort command answered the ask")
	}
	if !answerAskFromChat(ctx, hub, pool, agentSess, "yes", "cl-1", "") {
		t.Fatal("the chat message did not answer the open ask")
	}
	row, _ := db.GetMessage(ctx, pool, ask2)
	if row == nil || row.Status != "answered" || row.Answer == nil || *row.Answer != "yes" {
		t.Fatalf("ask2 = %+v, want answered yes", row)
	}
	got = transcript(agentSess)
	if last := got[len(got)-1]; last.kind != "user_message" || last.payload["text"] != "yes" ||
		last.payload["source"] != "ask_answer" || last.payload["message_id"] != ask2 || last.payload["client_id"] != "cl-1" {
		t.Fatalf("chat answer recorded as %+v", last)
	}
	// The recorded events carry the time they were stored with, live and on reload alike.
	rows, err := db.ListAgentEvents(ctx, pool, agentSess, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	storedTs := map[string]time.Time{}
	for _, r := range rows {
		storedTs[r.ClientEventID] = r.Ts
	}
	for len(sub) > 0 {
		var m protocol.AgentEvent
		if json.Unmarshal(<-sub, &m) != nil || m.Type != "agent_event" {
			continue
		}
		live, err := time.Parse(time.RFC3339Nano, m.Ts)
		if d := live.Sub(storedTs[m.ClientEventID]); err != nil || d > time.Microsecond || d < -time.Microsecond {
			t.Errorf("event %s: live ts %s, stored %v", m.ClientEventID, m.Ts, storedTs[m.ClientEventID])
		}
	}
	if answerAskFromChat(ctx, hub, pool, agentSess, "and another", "cl-2", "") {
		t.Fatal("a chat message with no open ask was taken as an answer")
	}

	// Named explicitly (the card's reply box over the websocket), the native ask is answered too —
	// its own poll delivers it to the loop, which records it — so nothing is recorded here.
	before := len(transcript(agentSess))
	if !answerAskFromChat(ctx, hub, pool, agentSess, "green", "cl-3", native) {
		t.Fatal("the named ask was not answered")
	}
	if row, _ := db.GetMessage(ctx, pool, native); row == nil || row.Status != "answered" {
		t.Fatalf("native ask = %+v, want answered", row)
	}
	if n := len(transcript(agentSess)); n != before {
		t.Fatalf("the native ask's answer was recorded by the server (%d → %d events)", before, n)
	}

	// A message through the runner API (the board's composer, an API caller, MCP send_message)
	// answers an open blerg-runner ask the same way while the session's daemon is connected; a
	// board's automation never does.
	key := runnerPrincipal{Kind: runnerKeyPrincipalKind}
	hub.Register(&DaemonConn{ID: daemonID, Name: "d", send: make(chan []byte, 8)})
	hub.SetSessionOwner(agentSess, daemonID)
	ask3 := post(agentTok, agentSess, "ask", "merge now?")
	_, _ = api.SendMessage(ctx, key, agentSess, "review found two problems", "blerg-board")
	if row, _ := db.GetMessage(ctx, pool, ask3); row == nil || row.Status != "open" {
		t.Fatalf("a blerg-board message answered the ask: %+v", row)
	}
	body, apiErr := api.SendMessage(ctx, key, agentSess, "yes, merge", "human")
	if apiErr != nil || body["answered_ask"] != true {
		t.Fatalf("SendMessage = %v, %v; want the ask answered", body, apiErr)
	}
	if row, _ := db.GetMessage(ctx, pool, ask3); row == nil || row.Status != "answered" || row.Answer == nil || *row.Answer != "yes, merge" {
		t.Fatalf("ask3 = %+v, want answered", row)
	}
	got = transcript(agentSess)
	if last := got[len(got)-1]; last.kind != "user_message" || last.payload["text"] != "yes, merge" ||
		last.payload["source"] != "ask_answer" || last.payload["message_id"] != ask3 {
		t.Fatalf("API answer recorded as %+v", last)
	}
}

func TestMessageEventIDIsStableAndAUUID(t *testing.T) {
	a, b := messageEventID("m1", "ask"), messageEventID("m1", "ask")
	if a != b || a == messageEventID("m1", "answer") || a == messageEventID("m2", "ask") {
		t.Fatalf("ids not stable/distinct: %s %s", a, b)
	}
	if len(a) != 36 || a[14] != '5' {
		t.Fatalf("id %q is not a v5 UUID", a)
	}
}
