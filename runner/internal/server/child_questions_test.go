package server

import (
	"encoding/json"
	"errors"
	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestChildQuestionsReachOnlyParent(t *testing.T) {
	for _, noWait := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocking", true: "no-wait"}[noWait], func(t *testing.T) {
			f := newDelegFx(t)
			livePushCore(t)
			f.hub.SetSessionOwner(f.parent, f.dc.ID)
			child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent, startedByKind: "session", title: "Question worker", private: true})
			f.hub.SetSessionOwner(child, f.dc.ID) // the connected daemon owns this live child
			f.api.RegisterLiveRoutes(f.mux)
			owner := &BrowserConn{ID: "owner", AccountID: "owner-1", send: make(chan []byte, 32)}
			f.hub.RegisterBrowser(owner)
			body, _ := json.Marshal(map[string]any{"kind": "ask", "body": "Which branch?", "no_wait": noWait})
			rec := privDo(f.api.HandlePostMessages, "POST", "/api/messages", "", token, string(body))
			if rec.Code != http.StatusCreated {
				t.Fatalf("ask: %d %s", rec.Code, rec.Body.String())
			}
			var created struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &created)
			get := func(account, etag string) *httptest.ResponseRecorder {
				r := httptest.NewRequest("GET", "/api/sessions/"+f.parent+"/live", nil)
				r.Header.Set("Authorization", "Bearer "+f.human(account))
				r.Header.Set("If-None-Match", etag)
				w := httptest.NewRecorder()
				f.mux.ServeHTTP(w, r)
				return w
			}
			rec = get("owner-1", "")
			var live struct {
				Counts struct {
					NeedsYou int `json:"needs_you"`
				} `json:"counts"`
				Questions []struct {
					MessageID  string `json:"message_id"`
					SessionID  string `json:"session_id"`
					ChildTitle string `json:"child_title"`
				} `json:"child_asks"`
			}
			if json.Unmarshal(rec.Body.Bytes(), &live) != nil || live.Counts.NeedsYou != 0 || len(live.Questions) != 0 {
				t.Fatalf("parent lost child question: %d %s", rec.Code, rec.Body.String())
			}
			etag := rec.Header().Get("ETag")
			other := get("other", "")
			if json.Unmarshal(other.Body.Bytes(), &live) != nil || live.Counts.NeedsYou != 0 || len(live.Questions) != 0 {
				t.Fatalf("other viewer got child questions: %s", other.Body.String())
			}
			count := 0
			for raw := range awaitAll(owner.send) {
				var m protocol.MessageCreated
				_ = json.Unmarshal(raw, &m)
				if m.Type == "message_created" && m.Message.ID == created.ID {
					count++
					if !m.Quiet || m.Message.Answerable {
						t.Fatalf("owner alert suppressed: %+v", m)
					}
				}
			}
			if count != 1 {
				t.Fatalf("got %d question alerts", count)
			}
			rows, err := db.ListMessagesFor(t.Context(), f.pool, 20, "owner-1")
			if err != nil {
				t.Fatal(err)
			}
			infos := messageInfosFor(t.Context(), f.pool, rows, "owner-1")
			if len(infos) != 1 || infos[0].Answerable {
				t.Fatalf("reconnect lost owner question: %+v", infos)
			}
			drain(f.dc.send)
			code, replyBody, _ := f.do("POST", childRoute(f.parent, child, "/questions/"+created.ID+"/answer"), f.parentTok, map[string]any{"answer": "Use the release branch."}, nil)
			reply := httptest.NewRecorder()
			reply.Code = code
			_ = json.NewEncoder(reply.Body).Encode(replyBody)
			if reply.Code != http.StatusOK {
				t.Fatalf("answer: %d %s", reply.Code, reply.Body.String())
			}

			if noWait {
				raw := awaitDaemonMessage(t, f.dc.send, "agent_user_message")
				var delivered protocol.AgentUserMessage
				if json.Unmarshal(raw, &delivered) != nil || delivered.SessionID != child || delivered.Source != questionAnswerSource || !strings.Contains(delivered.Text, "Use the release branch.") || !strings.Contains(delivered.Text, "[Answered by the parent session.]") {
					t.Fatalf("answer routed incorrectly: %s", raw)
				}
			}
			msg, err := db.GetMessage(t.Context(), f.pool, created.ID)
			if err != nil || msg.Answer == nil || *msg.Answer != "Use the release branch." || msg.SessionID != child {
				t.Fatalf("answer did not reach child: %+v %v", msg, err)
			}
			rec = get("owner-1", etag)
			if rec.Code != http.StatusOK {
				t.Fatalf("parent used stale ETag: %d", rec.Code)
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &live)
			if len(live.Questions) != 0 {
				t.Fatalf("answered child question remains: %s", rec.Body.String())
			}
		})
	}
}

func TestChildQuestionsDoNotPushBeforeEscalation(t *testing.T) {
	for _, noWait := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocking", true: "no-wait"}[noWait], func(t *testing.T) {
			f := newDelegFx(t)
			livePushCore(t)
			f.hub.SetSessionOwner(f.parent, f.dc.ID)
			child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent, startedByKind: "session", private: true})
			t.Setenv("BLERG_RUNNER_VAPID_PUBLIC_KEY", "test")
			t.Setenv("BLERG_RUNNER_VAPID_PRIVATE_KEY", "test")
			for _, owner := range []string{"owner-1", "other", ""} {
				if err := db.UpsertPushSubscription(t.Context(), f.pool, "https://push.example/sub/"+owner, "key", "auth", owner, testSID); err != nil {
					t.Fatal(err)
				}
			}
			saved := sendWebPush
			defer func() { sendWebPush = saved }()
			count := 0
			sendWebPush = func(raw []byte, sub *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
				count++
				if sub.Endpoint != "https://push.example/sub/owner-1" {
					t.Errorf("push leaked to %s", sub.Endpoint)
				}
				var payload map[string]string
				_ = json.Unmarshal(raw, &payload)
				if payload["url"] != "/sessions/"+child || !strings.Contains(payload["body"], "Which branch?") || !strings.Contains(payload["body"], "via \"Main\"") {
					t.Errorf("push lost question: %s", raw)
				}
				return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			body, _ := json.Marshal(map[string]any{"kind": "ask", "body": "Which branch?", "no_wait": noWait})
			rec := privDo(f.api.HandlePostMessages, "POST", "/api/messages", "", token, string(body))
			if rec.Code != 201 || count != 0 {
				t.Fatalf("ask %d, push count %d", rec.Code, count)
			}
			var created struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &created)
			code, _, _ := f.do("POST", childRoute(f.parent, child, "/questions/"+created.ID+"/escalate"), f.parentTok, map[string]string{"summary": "Allow this change?", "recommendation": "I recommend proceeding."}, nil)
			if code != 200 || count != 1 {
				t.Fatalf("escalation %d, pushes %d", code, count)
			}
			if err := f.api.reconcileChildQuestions(t.Context(), time.Now()); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatal("reconciliation repeated push")
			}
			SendPushToAccount(f.pool, "other", "Question", "private question", "/sessions/"+child)
			if count != 1 {
				t.Fatal("private-session backstop allowed a different account")
			}

		})
	}
}

func TestPushSubscriptionRecordsBrowserAccount(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	for _, account := range []string{"owner-1", "other"} {
		rec := privDo(f.api.HandlePostPushSubscribe, "POST", "/api/push/subscribe", "", f.human(account), `{"endpoint":"https://fcm.googleapis.com/sub/browser","keys":{"p256dh":"key","auth":"auth"}}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("subscribe %s: %d %s", account, rec.Code, rec.Body.String())
		}
		subs, err := db.ListPushSubscriptions(t.Context(), f.pool)
		if err != nil || len(subs) != 1 || subs[0].AccountID != account {
			t.Fatalf("subscription account not refreshed: %+v %v", subs, err)
		}
	}
}

func TestQuestionInboxMarksEachQuestionIndependently(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	blocking, err := db.CreateMessage(t.Context(), f.pool, f.parent, "ask", "Wait here?")
	if err != nil {
		t.Fatal(err)
	}
	question, err := db.CreateQuestion(t.Context(), f.pool, f.parent, "Ship it?")
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.ListMessagesBySession(t.Context(), f.pool, f.parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range messageInfosFor(t.Context(), f.pool, rows, "owner-1") {
		if m.ID == question && !m.Answerable {
			t.Fatal("no-wait question lost behind a blocking ask")
		}
		if m.ID == blocking && m.Answerable {
			t.Fatal("ordinary root blocking ask entered the child/no-wait inbox")
		}
	}
}

func TestQuestionInboxPreservesApprovalFlag(t *testing.T) {
	info := messageRowToInfo(db.MessageRow{Kind: "ask", OwnerOnly: true})
	raw, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	_ = json.Unmarshal(raw, &wire)
	if wire["owner_only"] != true {
		t.Fatalf("global inbox cannot distinguish an approval: %s", raw)
	}
}

func TestChildQuestionInitiallyCallsOnlyParent(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent, startedByKind: "session", private: true})
	for _, noWait := range []bool{false, true} {
		body, _ := json.Marshal(map[string]any{"kind": "ask", "body": "Which branch?", "no_wait": noWait})
		rec := privDo(f.api.HandlePostMessages, "POST", "/api/messages", "", token, string(body))
		if rec.Code != 201 {
			t.Fatal(rec.Body.String())
		}
		var created struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &created)
		row, err := db.GetMessage(t.Context(), f.pool, created.ID)
		sess, _ := db.GetSession(t.Context(), f.pool, child)
		if err != nil || row == nil {
			t.Fatal(err)
		}
		if questionCallsOwner(sess, *row) {
			t.Fatal("child question called the person before its parent could answer")
		}
	}
}

func TestChildQuestionParentNoticeIncludesEveryQuestion(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent, startedByKind: "session", title: "Worker"})
	for _, noWait := range []bool{false, true} {
		body, _ := json.Marshal(map[string]any{"kind": "ask", "body": "First line\nSecond line", "no_wait": noWait})
		rec := privDo(f.api.HandlePostMessages, "POST", "/api/messages", "", token, string(body))
		if rec.Code != 201 {
			t.Fatal(rec.Body.String())
		}
		var got struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		note := f.parentNote(f.parent, 5*time.Second)
		if note.Source != childTurnSource || !strings.Contains(note.Text, got.ID) || strings.Contains(note.Text, "Second line") || !strings.Contains(note.Text, "session result "+child) {
			t.Fatalf("notice: %+v", note)
		}
	}
}

func TestChildQuestionAnswerRequiresParentCredential(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	child, childToken := f.session(sessOpts{owner: "owner-1", parent: f.parent, startedByKind: "session"})
	id, err := db.CreateQuestion(t.Context(), f.pool, child, "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	for name, token := range map[string]string{"child": childToken, "human": f.human("owner-1"), "daemon": delegDaemonToken} {
		code, _, _ := f.do("POST", childRoute(f.parent, child, "/questions/"+id+"/answer"), token, map[string]any{"answer": "blue"}, nil)
		if code >= 200 && code < 300 {
			t.Fatalf("%s answered", name)
		}
	}
	direct := privDo(f.api.HandlePostMessageReply, "POST", "/x", id, f.human("owner-1"), `{"answer":"blue"}`)
	if direct.Code != 403 {
		t.Fatalf("person answered before escalation: %d %s", direct.Code, direct.Body.String())
	}
	if answerAskFromChatAs(t.Context(), f.hub, f.pool, child, "blue", newUUID(), id, "owner-1") {
		t.Fatal("person's chat answered before escalation")
	}
	code, _, _ := f.do("POST", childRoute(f.parent, child, "/questions/"+id+"/answer"), f.parentTok, map[string]any{"answer": "blue"}, nil)
	if code != 200 {
		t.Fatalf("parent answer: %d", code)
	}
	row, _ := db.GetMessage(t.Context(), f.pool, id)
	if row.Status != "answered" || derefOrEmpty(row.Answer) != "blue" {
		t.Fatalf("answer: %+v", row)
	}
	if err := f.api.escalateChildQuestion(t.Context(), f.row(child), f.row(f.parent), id); err != nil {
		t.Fatal(err)
	}
	row, _ = db.GetMessage(t.Context(), f.pool, id)
	if row.ChildEscalated {
		t.Fatal("answered question escalated")
	}
	code, _, _ = f.do("POST", childRoute(f.parent, child, "/questions/"+id+"/answer"), f.parentTok, map[string]any{"answer": "red"}, nil)
	if code != 409 {
		t.Fatalf("second answer: %d", code)
	}
}

func TestChildQuestionLifecyclePaths(t *testing.T) {
	for _, path := range []string{"manual", "timeout", "ended", "paused", "unavailable"} {
		t.Run(path, func(t *testing.T) {
			f := newDelegFx(t)
			f.hub.SetSessionOwner(f.parent, f.dc.ID)
			child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent, startedByKind: "session", title: "Worker"})
			id, err := db.CreateQuestion(t.Context(), f.pool, child, "Which branch?")
			if err != nil {
				t.Fatal(err)
			}
			switch path {
			case "manual":
				code, _, _ := f.do("POST", childRoute(f.parent, child, "/questions/"+id+"/escalate"), f.parentTok, map[string]string{"summary": "Allow this change?", "recommendation": "I recommend proceeding."}, nil)
				if code != 200 {
					t.Fatalf("escalate: %d", code)
				}
			case "ended":
				_, err = f.pool.Exec(t.Context(), `UPDATE sessions SET status='ended',ended_at=now() WHERE id=$1`, f.parent)
			case "paused":
				_, err = f.pool.Exec(t.Context(), `UPDATE sessions SET paused_reason='restart' WHERE id=$1`, f.parent)
			case "unavailable":
				f.hub.RemoveSessionOwner(f.parent)
			}
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			if path == "timeout" || path == "unavailable" {
				if path == "unavailable" {
					if err := f.api.reconcileChildQuestions(t.Context(), now); err != nil {
						t.Fatal(err)
					}
					current, _ := db.GetMessage(t.Context(), f.pool, id)
					if current.ChildEscalated {
						t.Fatal("disconnected parent lost its question")
					}
				}
				now = now.Add(childFailureEscalationAfter + time.Second)
			}
			if err = f.api.reconcileChildQuestions(t.Context(), now); err != nil {
				t.Fatal(err)
			}
			row, err := db.GetMessage(t.Context(), f.pool, id)
			if path != "manual" {
				want := "open"
				if path == "ended" {
					want = "withdrawn"
				}
				if err != nil || row.ChildEscalated || row.Status != want || row.Body != "Which branch?" {
					t.Fatalf("ordinary question handed off: %+v %v", row, err)
				}
				return
			}
			if err != nil || !row.ChildEscalated || !strings.Contains(row.Body, `Worker "Worker", via "Main"`) {
				t.Fatalf("escalation: %+v %v", row, err)
			}
			if !questionCallsOwner(f.row(child), *row) {
				t.Fatal("escalation did not reach Needs you")
			}
			f.api.RegisterLiveRoutes(f.mux)
			code, live, _ := f.do("GET", "/api/sessions/"+f.parent+"/live", f.human("owner-1"), nil, nil)
			if code != 200 || len(live["child_asks"].([]any)) != 1 {
				t.Fatalf("escalated parent Needs you: %d %v", code, live)
			}
			if err = f.api.reconcileChildQuestions(t.Context(), now); err != nil {
				t.Fatal(err)
			}
			again, _ := db.GetMessage(t.Context(), f.pool, id)
			if again.Body != row.Body {
				t.Fatal("repeat escalation duplicated question")
			}
		})
	}
}

func TestChildQuestionAnswerRacesEscalation(t *testing.T) {
	f := newDelegFx(t)
	owner := &BrowserConn{ID: "owner", AccountID: "owner-1", send: make(chan []byte, 128)}
	f.hub.RegisterBrowser(owner)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent, startedByKind: "session"})
	f.hub.SetSessionOwner(child, f.dc.ID)
	drain(f.dc.send)
	for range 12 {
		id, err := db.CreateQuestion(t.Context(), f.pool, child, "Which branch?")
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		done := make(chan error, 1)
		childRow, parentRow := f.row(child), f.row(f.parent)
		go func() { <-start; done <- f.api.escalateChildQuestion(t.Context(), childRow, parentRow, id) }()
		close(start)
		code, body, _ := f.do("POST", childRoute(f.parent, child, "/questions/"+id+"/answer"), f.parentTok, map[string]any{"answer": "blue"}, nil)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		row, _ := db.GetMessage(t.Context(), f.pool, id)
		if row.ChildEscalated {
			if code != 409 || row.Status != "open" || row.Answer != nil {
				t.Fatalf("escalation won but parent answered: %d %v %+v", code, body, row)
			}
		} else {
			if code != 200 || row.Status != "answered" || derefOrEmpty(row.Answer) != "blue" {
				t.Fatalf("answer lost: %d %v %+v", code, body, row)
			}
		}
		asks, err := db.ListOpenAsks(t.Context(), f.pool, child)
		wantOpen := 0
		if row.ChildEscalated {
			wantOpen = 1
		}
		if err != nil || len(asks) != wantOpen {
			t.Fatalf("race left wrong open questions: %+v %v; want %d", asks, err, wantOpen)
		}
		if wantOpen == 1 && (asks[0].ID != id || !asks[0].ChildEscalated) {
			t.Fatalf("race left wrong escalated question: %+v", asks)
		}
		lastStatus := ""
		for raw := range awaitAll(owner.send) {
			var m protocol.MessageCreated
			if json.Unmarshal(raw, &m) == nil && m.Message.ID == id {
				lastStatus = m.Message.Status
			}
		}
		if lastStatus != row.Status {
			t.Fatalf("browser status %q, committed status %q", lastStatus, row.Status)
		}

		if row.ChildEscalated {
			rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", id, f.human("owner-1"), `{"answer":"blue from the person"}`)
			if rec.Code != 200 {
				t.Fatalf("person could not resolve the escalation winner: %d %s", rec.Code, rec.Body.String())
			}
		}

		var delivered []byte
		var answer protocol.AgentUserMessage
		for {
			delivered = awaitDaemonMessage(t, f.dc.send, "agent_user_message")
			_ = json.Unmarshal(delivered, &answer)
			if answer.SessionID == child {
				break
			}
		}
		wantSource := "[Answered by the parent session.]"
		if row.ChildEscalated {
			wantSource = "[Answered by the person.]"
		}
		if answer.SessionID != child || answer.Source != questionAnswerSource || !strings.Contains(answer.Text, wantSource) {
			t.Fatalf("race winner answer lost its source: %s", delivered)
		}

	}
}

func TestChildQuestionEscalationSurvivesRestartBeforeNotification(t *testing.T) {
	f := newDelegFx(t)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	id, err := db.CreateQuestion(t.Context(), f.pool, child, "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(t.Context(), `UPDATE messages SET child_escalated=true WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	owner := &BrowserConn{ID: "owner", AccountID: "owner-1", send: make(chan []byte, 32)}
	f.hub.RegisterBrowser(owner)
	if err = f.api.reconcileChildQuestions(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for raw := range awaitAll(owner.send) {
		var m protocol.MessageCreated
		if json.Unmarshal(raw, &m) == nil && m.Type == "message_created" && m.Message.ID == id && m.Message.Answerable {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("pending escalation notifications: %d", seen)
	}
	var notified bool
	if err = f.pool.QueryRow(t.Context(), `SELECT child_person_notified FROM messages WHERE id=$1`, id).Scan(&notified); err != nil || !notified {
		t.Fatalf("notification receipt: %v %v", notified, err)
	}
}

func TestKeptChildQuestionWithdrawsWhenParentEnds(t *testing.T) {
	f := newDelegFx(t)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent, startedByKind: "session"})
	f.hub.SetSessionOwner(child, f.dc.ID)
	if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET keep_running=true WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	id, err := db.CreateQuestion(t.Context(), f.pool, child, "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err = db.EndSessionStatus(t.Context(), f.pool, f.parent, "ended", &now, systemEnd(db.EndReasonProcessExited)); err != nil {
		t.Fatal(err)
	}
	if err = f.api.reconcileChildQuestions(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", id, f.human("owner-1"), `{"answer":"blue"}`)
	if rec.Code != 403 {
		t.Fatalf("ordinary worker question reached owner after parent ended: %d %s", rec.Code, rec.Body.String())
	}
	row, _ := db.GetMessage(t.Context(), f.pool, id)
	if row.Status != "withdrawn" || row.Answer != nil || row.ChildEscalated {
		t.Fatalf("question was not withdrawn without an answer: %+v", row)
	}
}

func TestChildCommandApprovalRequiresEscalationToPerson(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	id, err := db.CreateAsk(t.Context(), f.pool, child, "Allow this command?", true)
	if err != nil {
		t.Fatal(err)
	}
	code, _, _ := f.do("POST", childRoute(f.parent, child, "/questions/"+id+"/answer"), f.parentTok, map[string]any{"answer": "yes"}, nil)
	if code != 403 {
		t.Fatalf("parent answered approval: %d", code)
	}
	code, _, _ = f.do("POST", childRoute(f.parent, child, "/questions/"+id+"/escalate"), f.parentTok, map[string]string{"summary": "Allow this change?", "recommendation": "I recommend proceeding."}, nil)
	if code != 200 {
		t.Fatalf("escalate approval: %d", code)
	}
	rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", id, f.human("owner-1"), `{"answer":"yes"}`)
	if rec.Code != 200 {
		t.Fatalf("person's approval: %d %s", rec.Code, rec.Body.String())
	}
}

func TestChildQuestionWithdrawsWhenParentEndStopsChild(t *testing.T) {
	f := newDelegFx(t)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent, startedByKind: "session"})
	id, err := db.CreateQuestion(t.Context(), f.pool, child, "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err = db.EndSessionStatus(t.Context(), f.pool, f.parent, "ended", &now, systemEnd(db.EndReasonProcessExited)); err != nil {
		t.Fatal(err)
	}
	if err = db.EndSessionStatus(t.Context(), f.pool, child, "ended", &now, systemEnd(db.EndReasonParentEnded)); err != nil {
		t.Fatal(err)
	}
	HandleSessionEnded(t.Context(), f.hub, f.pool, protocol.SessionEnded{Type: "session_ended", SessionID: child})
	if err = f.api.reconcileChildQuestions(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	row, _ := db.GetMessage(t.Context(), f.pool, id)
	if row.Status != "withdrawn" || row.ChildEscalated || row.Answer != nil {
		t.Fatalf("parent end did not withdraw ordinary question: %+v", row)
	}
	rec := privDo(f.api.HandlePostMessageDismiss, "POST", "/x", id, f.human("owner-1"), `{}`)
	if rec.Code != 409 {
		t.Fatalf("ordinary orphaned question reached owner: %d %s", rec.Code, rec.Body.String())
	}
}

func TestParentCannotAnswerEscalatedOrEmptyChildQuestion(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		escalated    bool
		want         int
	}{
		{"escalated", "yes", true, 409}, {"empty", "  ", false, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDelegFx(t)
			child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
			id, err := db.CreateQuestion(t.Context(), f.pool, child, "Which branch?")
			if err != nil {
				t.Fatal(err)
			}
			if tc.escalated {
				if err := f.api.escalateChildQuestion(t.Context(), f.row(child), f.row(f.parent), id); err != nil {
					t.Fatal(err)
				}
			}
			code, body, _ := f.do("POST", childRoute(f.parent, child, "/questions/"+id+"/answer"), f.parentTok, map[string]any{"answer": tc.answer}, nil)
			if code != tc.want {
				t.Fatalf("got %d %v; want %d", code, body, tc.want)
			}
			row, _ := db.GetMessage(t.Context(), f.pool, id)
			if row.Status != "open" || row.Answer != nil {
				t.Fatalf("refused answer changed question: %+v", row)
			}
		})
	}
}

func TestEscalatedChildDismissRequiresOwner(t *testing.T) {
	f := newDelegFx(t)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	id, err := db.CreateMessage(t.Context(), f.pool, child, "ask", "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.api.escalateChildQuestion(t.Context(), f.row(child), f.row(f.parent), id); err != nil {
		t.Fatal(err)
	}
	rec := privDo(f.api.HandlePostMessageDismiss, "POST", "/x", id, f.human("other"), `{}`)
	if rec.Code != 403 {
		t.Fatalf("other dismissed escalated question: %d %s", rec.Code, rec.Body.String())
	}
}

func TestEscalatingClosedChildQuestionConflicts(t *testing.T) {
	f := newDelegFx(t)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	id, err := db.CreateQuestion(t.Context(), f.pool, child, "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.AnswerMessage(t.Context(), f.pool, id, "blue"); err != nil {
		t.Fatal(err)
	}
	code, body, _ := f.do("POST", childRoute(f.parent, child, "/questions/"+id+"/escalate"), f.parentTok, map[string]string{"summary": "Allow this change?", "recommendation": "I recommend proceeding."}, nil)
	if code != 409 {
		t.Fatalf("closed escalation: %d %v", code, body)
	}
}

func TestDaemonClosesPendingChildQuestionWithoutSelfAnswer(t *testing.T) {
	for _, answer := range []string{"", "Already answered in chat"} {
		t.Run(answer, func(t *testing.T) {
			f := newDelegFx(t)
			child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
			id, err := db.CreateQuestion(t.Context(), f.pool, child, "Which branch?")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(map[string]string{"answer": answer})
			rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", id, delegDaemonToken, string(body))
			if answer != "" {
				if rec.Code != 403 {
					t.Fatalf("daemon self-answer = %d %s", rec.Code, rec.Body.String())
				}
				row, _ := db.GetMessage(t.Context(), f.pool, id)
				if row.Status != "open" || row.Answer != nil || row.AnsweredBy != "" {
					t.Fatalf("refused self-answer changed question: %+v", row)
				}
				return
			}
			if rec.Code != 200 {
				t.Fatalf("daemon close: %d %s", rec.Code, rec.Body.String())
			}
			row, _ := db.GetMessage(t.Context(), f.pool, id)
			if row.Status != "answered" || row.Answer == nil || *row.Answer != "" {
				t.Fatalf("close recorded self answer: %+v", row)
			}
		})
	}
}

func TestChildPersonRequiredReachesMainFirst(t *testing.T) {
	for _, tc := range []struct {
		name, body               string
		person, approval, noWait bool
	}{
		{"marker", "Which branch?", true, false, false}, {"deny-list", "May I run git push --force origin main?", false, false, false}, {"approval", "Allow this command?", false, true, false},
		{"marker-no-wait", "Which branch?", true, false, true}, {"deny-list-no-wait", "May I run git push --force origin main?", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDelegFx(t)
			f.hub.SetSessionOwner(f.parent, f.dc.ID)
			child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent})
			body, _ := json.Marshal(map[string]any{"kind": "ask", "body": tc.body, "person_required": tc.person, "owner_only": tc.approval, "no_wait": tc.noWait})
			if tc.approval {
				body, _ = json.Marshal(map[string]any{"session_id": child, "kind": "ask", "body": tc.body, "owner_only": true})
				token = delegDaemonToken
			}
			rec := privDo(f.api.HandlePostMessages, "POST", "/x", "", token, string(body))
			var posted struct{ ID, Recipient string }
			_ = json.Unmarshal(rec.Body.Bytes(), &posted)
			if rec.Code != 201 || posted.Recipient != "parent" {
				t.Fatalf("not sent to Main: %d %s", rec.Code, rec.Body.String())
			}
			code, _, _ := f.do("POST", childRoute(f.parent, child, "/questions/"+posted.ID+"/answer"), f.parentTok, map[string]string{"answer": "yes"}, nil)
			if code != 403 {
				t.Fatalf("parent answered person decision: %d", code)
			}
			message := mustChildMessage(t, f, posted.ID)
			if !message.PersonRequired || message.ChildEscalated || message.EscalatedBy != "" {
				t.Fatalf("requirement bypassed Main: %+v", message)
			}
			if owners := questionOwners(t.Context(), f.pool, []db.MessageRow{*message}); owners[posted.ID] != "" {
				t.Fatalf("pending question reached Needs you: %v", owners)
			}
			if err := f.api.reconcileChildQuestions(t.Context(), time.Now(), posted.ID); err != nil {
				t.Fatal(err)
			}
			if mustChildMessage(t, f, posted.ID).ChildEscalated {
				t.Fatal("reconciliation bypassed Main")
			}
			summary, recommendation := "This changes shared history. Allow it?", "I recommend keeping the existing history."
			code, _, _ = f.do("POST", childRoute(f.parent, child, "/questions/"+posted.ID+"/escalate"), f.parentTok, map[string]string{"summary": summary, "recommendation": recommendation}, nil)
			if code != 200 {
				t.Fatalf("escalation: %d", code)
			}
			message = mustChildMessage(t, f, posted.ID)
			if !message.ChildEscalated || !strings.Contains(message.Body, summary) || !strings.Contains(message.Body, recommendation) || !strings.Contains(message.Body, tc.body) {
				t.Fatalf("missing summary or original: %+v", message)
			}
			if code := replyCode(t, f, posted.ID, f.parentTok); code != 403 {
				t.Fatalf("Main answered after escalation: %d", code)
			}
			if code := replyCode(t, f, posted.ID, f.human("owner-1")); code != 200 {
				t.Fatalf("person could not answer: %d", code)
			}

		})
	}
}

func TestDisconnectedParentKeepsQuestion(t *testing.T) {
	f := newDelegFx(t)
	child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	rec := privDo(f.api.HandlePostMessages, "POST", "/x", "", token, `{"kind":"ask","body":"Which branch?"}`)
	var posted struct{ ID, Recipient string }
	_ = json.Unmarshal(rec.Body.Bytes(), &posted)
	if rec.Code != 201 || posted.Recipient != "parent" {
		t.Fatalf("disconnected parent skipped: %d %s", rec.Code, rec.Body.String())
	}
	row, _ := db.GetMessage(t.Context(), f.pool, posted.ID)
	if row.SessionID != child || row.ChildEscalated {
		t.Fatalf("question left parent: %+v", row)
	}
}

func TestArchivedPendingChildQuestionIsClosed(t *testing.T) {
	f := newDelegFx(t)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	id, err := db.CreateQuestion(t.Context(), f.pool, child, "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(t.Context(), `UPDATE sessions SET archived_at=now() WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	if err = f.api.reconcileChildQuestions(t.Context(), time.Now().Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	row, _ := db.GetMessage(t.Context(), f.pool, id)
	if row.Status == "open" {
		t.Fatalf("archived question remains open: %+v", row)
	}
}

func TestPostingChildQuestionDoesNotSweepOtherQuestions(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	old, err := db.CreateQuestion(t.Context(), f.pool, child, "Earlier question?")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(t.Context(), `UPDATE messages SET created_at=now()-interval '4 minutes' WHERE id=$1`, old); err != nil {
		t.Fatal(err)
	}
	rec := privDo(f.api.HandlePostMessages, "POST", "/x", "", token, `{"kind":"ask","body":"Which branch?"}`)
	if rec.Code != 201 {
		t.Fatal(rec.Body.String())
	}
	row, _ := db.GetMessage(t.Context(), f.pool, old)
	if row.ChildEscalated {
		t.Fatal("posting one question swept unrelated old questions")
	}
}

func TestPendingQuestionDoesNotGiveParentAttention(t *testing.T) {
	f := newDelegFx(t)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	id, err := db.CreateMessage(t.Context(), f.pool, child, "ask", "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	check := func(want int) {
		t.Helper()
		live, err := db.ListVisibleChildLiveRows(t.Context(), f.pool, f.parent, "owner-1")
		if err != nil || len(live) != 1 || live[0].OpenAsks != want {
			t.Fatalf("live attention count: %+v %v; want %d", live, err, want)
		}
		children, err := db.ListChildSessions(t.Context(), f.pool, f.parent)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := db.ReadChildrenDigest(t.Context(), f.pool, f.parent)
		if err != nil {
			t.Fatal(err)
		}
		if len(children) != 1 || children[0].OpenAsks != want || digest.OpenAsks != want {
			t.Fatalf("attention count: %+v %+v; want %d", children, digest, want)
		}
	}
	check(0)
	if err = f.api.escalateChildQuestion(t.Context(), f.row(child), f.row(f.parent), id); err != nil {
		t.Fatal(err)
	}
	check(1)
}

func TestChildQuestionAuditsAnswerAndEscalation(t *testing.T) {
	f := newDelegFx(t)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	id, err := db.CreateQuestion(t.Context(), f.pool, child, "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ := f.do("POST", childRoute(f.parent, child, "/questions/"+id+"/answer"), f.parentTok, map[string]string{"answer": "blue"}, nil)
	if code != 200 {
		t.Fatalf("answer: %d %v", code, body)
	}
	_, response := f.api.answeredNow(t.Context(), httptest.NewRecorder(), id)
	raw, _ := json.Marshal(response)
	var poll map[string]any
	_ = json.Unmarshal(raw, &poll)
	if poll["answered_by"] != "session:"+f.parent {
		t.Fatalf("child cannot identify answer source: %s", raw)
	}
	var by string
	if err = f.pool.QueryRow(t.Context(), `SELECT answered_by FROM messages WHERE id=$1`, id).Scan(&by); err != nil || by != "session:"+f.parent {
		t.Fatalf("answer attribution: %q %v", by, err)
	}
	id, err = db.CreateQuestion(t.Context(), f.pool, child, "Which color?")
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ = f.do("POST", childRoute(f.parent, child, "/questions/"+id+"/escalate"), f.parentTok, map[string]string{"summary": "Allow this change?", "recommendation": "I recommend proceeding."}, nil)
	if code != 200 {
		t.Fatalf("escalation: %d %v", code, body)
	}
	var reason string
	if err = f.pool.QueryRow(t.Context(), `SELECT escalated_by,escalation_reason FROM messages WHERE id=$1`, id).Scan(&by, &reason); err != nil || by != "session:"+f.parent || reason != "parent_requested" {
		t.Fatalf("escalation attribution: %q %q %v", by, reason, err)
	}
}

func TestChildQuestionSweepContinuesAfterOneFailure(t *testing.T) {
	f := newDelegFx(t)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	_, err := db.CreateQuestion(t.Context(), f.pool, child, "Broken question")
	if err != nil {
		t.Fatal(err)
	}
	next, err := db.CreateQuestion(t.Context(), f.pool, child, "Next question")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Exec(t.Context(), `UPDATE messages SET person_required=true WHERE session_id=$1`, child)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Exec(t.Context(), `CREATE FUNCTION fail_one_escalation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF OLD.body='Broken question' THEN RAISE EXCEPTION 'test item unavailable'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_one_escalation BEFORE UPDATE ON messages FOR EACH ROW EXECUTE FUNCTION fail_one_escalation()`)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.api.reconcileChildQuestions(t.Context(), time.Now().Add(4*time.Minute))
	row, _ := db.GetMessage(t.Context(), f.pool, next)
	var notified bool
	if err := f.pool.QueryRow(t.Context(), `SELECT child_parent_notified FROM messages WHERE id=$1`, next).Scan(&notified); err != nil {
		t.Fatal(err)
	}
	if row.ChildEscalated || !notified {
		t.Fatal("one failed item stopped parent notification for the rest of the sweep")
	}
}

func TestExpireWaitsForQuestionEscalationLock(t *testing.T) {
	f := newDelegFx(t)
	child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	id, err := db.CreateQuestion(t.Context(), f.pool, child, "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	err = withLimitResumeActionLock(t.Context(), f.pool, child, func(_ *pgxpool.Conn) error {
		go func() { done <- privDo(f.api.HandlePostMessageExpire, "POST", "/x", id, token, `{}`) }()
		select {
		case rec := <-done:
			t.Errorf("expiry bypassed escalation lock: %d", rec.Code)
			done <- rec
		case <-time.After(100 * time.Millisecond):
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case rec := <-done:
		if rec.Code != 200 {
			t.Fatal(rec.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("expiry did not finish")
	}
}

func TestUpgradeDoesNotEscalatePreviouslyVisibleChildQuestionsAgain(t *testing.T) {
	f := newDelegFx(t)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	id, err := db.CreateQuestion(t.Context(), f.pool, child, "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.pool.Exec(t.Context(), `ALTER TABLE messages DROP COLUMN child_escalated,DROP COLUMN child_parent_notified,DROP COLUMN child_person_notified,DROP COLUMN person_required,DROP COLUMN answered_by,DROP COLUMN escalated_by,DROP COLUMN escalation_reason,DROP COLUMN waiter_ended,DROP COLUMN host_generation; UPDATE messages SET created_at=now()-interval '4 minutes'`)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../db/migrations/101_child_question_escalation.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(t.Context(), string(migration)); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetMessage(t.Context(), f.pool, id)
	if err != nil || !row.ChildEscalated {
		t.Fatalf("previously owner-visible question lost migration state: %+v %v", row, err)
	}
	var notified bool
	if err = f.pool.QueryRow(t.Context(), `SELECT child_person_notified FROM messages WHERE id=$1`, id).Scan(&notified); err != nil || !notified {
		t.Fatalf("upgrade will push question a second time: %t %v", notified, err)
	}
	t.Setenv("BLERG_RUNNER_VAPID_PUBLIC_KEY", "test")
	t.Setenv("BLERG_RUNNER_VAPID_PRIVATE_KEY", "test")
	if err = db.UpsertPushSubscription(t.Context(), f.pool, "https://push.example/upgrade", "key", "auth", "owner-1"); err != nil {
		t.Fatal(err)
	}
	saved := sendWebPush
	defer func() { sendWebPush = saved }()
	pushes := 0
	sendWebPush = func(_ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		pushes++
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	if err = f.api.reconcileChildQuestions(t.Context(), time.Now().Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if pushes != 0 {
		t.Fatalf("upgrade pushed the same question %d more times", pushes)
	}

}

func TestChildPersonRequirementIsStoredWithQuestion(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	_, token := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	_, err := f.pool.Exec(t.Context(), `CREATE FUNCTION require_atomic_person_marker() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.body LIKE '%git push --force%' AND NOT NEW.person_required THEN RAISE EXCEPTION 'missing person requirement at insertion'; END IF; RETURN NEW; END $$; CREATE TRIGGER require_atomic_person_marker BEFORE INSERT ON messages FOR EACH ROW EXECUTE FUNCTION require_atomic_person_marker()`)
	if err != nil {
		t.Fatal(err)
	}
	rec := privDo(f.api.HandlePostMessages, "POST", "/x", "", token, `{"kind":"ask","body":"May I run git push --force origin main?"}`)
	if rec.Code != 201 {
		t.Fatalf("person requirement was not atomic with creation: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDaemonCannotCloseEscalatedChildQuestion(t *testing.T) {
	f := newDelegFx(t)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	id, err := db.CreateQuestion(t.Context(), f.pool, child, "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.api.escalateChildQuestion(t.Context(), f.row(child), f.row(f.parent), id); err != nil {
		t.Fatal(err)
	}
	rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", id, delegDaemonToken, `{"answer":"Already answered in chat"}`)
	if rec.Code != 403 {
		t.Fatalf("daemon close after escalation: %d %s", rec.Code, rec.Body.String())
	}
	row, _ := db.GetMessage(t.Context(), f.pool, id)
	if row.Status != "open" || row.Answer != nil || row.AnsweredBy != "" {
		t.Fatalf("close recorded self answer: %+v", row)
	}
}

func TestChildQuestionAttentionExcludesFailureNotices(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	child := f.childOf(f.parent)
	now := time.Now()
	if err := db.EndSessionStatus(t.Context(), f.pool, child, "error", &now, systemEnd(db.EndReasonProcessFailed)); err != nil {
		t.Fatal(err)
	}
	if err := f.api.reconcileSessionFailures(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := f.pool.QueryRow(t.Context(), `SELECT message_id::text FROM session_failure_alerts WHERE session_id=$1`, child).Scan(&id); err != nil {
		t.Fatal(err)
	}
	message, err := db.GetMessage(t.Context(), f.pool, id)
	if err != nil || message == nil {
		t.Fatalf("failure alert: %+v %v", message, err)
	}
	infos := messageInfosFor(t.Context(), f.pool, []db.MessageRow{*message}, "owner-1")
	if message.Kind != "update" || message.Status != "answered" || message.ChildEscalated || len(infos) != 1 || infos[0].Answerable {
		t.Fatalf("failure alert routing changed: %+v %+v", message, infos)
	}
	children, err := db.ListChildSessions(t.Context(), f.pool, f.parent)
	if err != nil {
		t.Fatal(err)
	}
	live, err := db.ListVisibleChildLiveRows(t.Context(), f.pool, f.parent, "owner-1")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := db.ReadChildrenDigest(t.Context(), f.pool, f.parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || len(live) != 1 || children[0].OpenAsks != 0 || live[0].OpenAsks != 0 || digest.OpenAsks != 0 {
		t.Fatalf("failure alert gained a worker question chip: %+v %+v %+v", children, live, digest)
	}
}

func TestOrphanedChildDecisionsShareEscalationLock(t *testing.T) {
	for _, action := range []string{"reply", "dismiss"} {
		t.Run(action, func(t *testing.T) {
			f := newDelegFx(t)
			child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
			id, err := db.CreateQuestion(t.Context(), f.pool, child, "Which branch?")
			if err != nil {
				t.Fatal(err)
			}
			if err = f.api.escalateChildQuestion(t.Context(), f.row(child), f.row(f.parent), id); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			if err = db.EndSessionStatus(t.Context(), f.pool, child, "ended", &now, systemEnd(db.EndReasonParentEnded)); err != nil {
				t.Fatal(err)
			}
			handler := f.api.HandlePostMessageReply
			body := `{"answer":"blue"}`
			if action == "dismiss" {
				handler = f.api.HandlePostMessageDismiss
				body = `{}`
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			err = withLimitResumeActionLock(t.Context(), f.pool, child, func(_ *pgxpool.Conn) error {
				go func() { done <- privDo(handler, "POST", "/x", id, f.human("owner-1"), body) }()
				select {
				case rec := <-done:
					t.Errorf("orphaned %s bypassed escalation lock: %d", action, rec.Code)
					done <- rec
				case <-time.After(100 * time.Millisecond):
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case rec := <-done:
				if rec.Code != 200 {
					t.Fatalf("orphaned %s: %d %s", action, rec.Code, rec.Body.String())
				}
			case <-time.After(5 * time.Second):
				t.Fatal("decision did not finish")
			}
			if row := f.row(child); row.Status != "ended" || row.EndedAt == nil {
				t.Fatalf("history decision restarted the child: %+v", row)
			}
		})
	}
}

func TestArchivedEscalatedChildQuestionIsWithdrawn(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	id, err := db.CreateQuestion(t.Context(), f.pool, child, "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.api.escalateChildQuestion(t.Context(), f.row(child), f.row(f.parent), id); err != nil {
		t.Fatal(err)
	}
	owner := &BrowserConn{ID: "owner", AccountID: "owner-1", send: make(chan []byte, 32)}
	f.hub.RegisterBrowser(owner)
	if _, err = f.pool.Exec(t.Context(), `UPDATE sessions SET archived_at=now() WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	if err = f.api.reconcileChildQuestions(t.Context(), time.Now().Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	row, err := db.GetMessage(t.Context(), f.pool, id)
	if err != nil || row.Status != "withdrawn" || row.Answer != nil {
		t.Fatalf("archived escalated question stayed open: %+v %v", row, err)
	}
	closed := false
	for raw := range awaitAll(owner.send) {
		var event protocol.MessageAnswered
		_ = json.Unmarshal(raw, &event)
		if event.Message.ID == id {
			if event.Type != "message_answered" || event.Message.Status != "withdrawn" {
				t.Fatalf("archive replayed escalation: %s", raw)
			}
			closed = true
		}
	}
	if !closed {
		t.Fatal("owner did not see the archived question withdrawn")
	}
}

func TestEscalationRechecksChildArchive(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	snapshot := f.row(child)
	id, err := db.CreateQuestion(t.Context(), f.pool, child, "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(t.Context(), `UPDATE sessions SET archived_at=now() WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	owner := &BrowserConn{ID: "owner", AccountID: "owner-1", send: make(chan []byte, 32)}
	f.hub.RegisterBrowser(owner)
	err = f.api.escalateChildQuestion(t.Context(), snapshot, f.row(f.parent), id, "system", "timeout")
	if !errors.Is(err, errChildQuestionClosed) {
		t.Fatalf("stale archive snapshot admitted escalation: %v", err)
	}
	row, err := db.GetMessage(t.Context(), f.pool, id)
	if err != nil || row.Status != "withdrawn" || row.ChildEscalated {
		t.Fatalf("archived question reached the person: %+v %v", row, err)
	}
	for raw := range awaitAll(owner.send) {
		var event protocol.MessageCreated
		_ = json.Unmarshal(raw, &event)
		if event.Type == "message_created" && event.Message.ID == id {
			t.Fatalf("archived question was notified: %s", raw)
		}
	}
}

func TestWorkerQuestionWaitsForMainIndefinitely(t *testing.T) {
	for _, paused := range []bool{false, true} {
		t.Run(map[bool]string{false: "disconnected", true: "paused"}[paused], func(t *testing.T) {
			f := newDelegFx(t)
			child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
			id, err := db.CreateQuestion(t.Context(), f.pool, child, "Private worker question?")
			if err != nil {
				t.Fatal(err)
			}
			if paused {
				_, err = f.pool.Exec(t.Context(), `UPDATE sessions SET paused_reason='restart' WHERE id=$1`, f.parent)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err = f.api.reconcileChildQuestions(t.Context(), time.Now().Add(24*time.Hour)); err != nil {
				t.Fatal(err)
			}
			row, err := db.GetMessage(t.Context(), f.pool, id)
			if err != nil || row.ChildEscalated || row.Status != "open" {
				t.Fatalf("worker question left Main: %+v %v", row, err)
			}
			// Reconnect after the old deadline. The same question still reaches
			// Main through its normal input path, and Main can answer it.
			f.hub.SetSessionOwner(f.parent, f.dc.ID)
			f.dc.SetInputReceipts(true)
			replayPendingSessionInputs(t.Context(), f.hub, f.pool, f.dc, []string{f.parent})
			if err := f.api.reconcileChildQuestions(t.Context(), time.Now().Add(48*time.Hour)); err != nil {
				t.Fatal(err)
			}
			raw := awaitDaemonMessage(t, f.dc.send, "agent_user_message")
			var notice protocol.AgentUserMessage
			if json.Unmarshal(raw, &notice) != nil || notice.SessionID != f.parent || !strings.Contains(notice.Text, id) || strings.Contains(notice.Text, row.Body) {
				t.Fatalf("question not delivered to Main: %s", raw)
			}
			code, _, _ := f.do("POST", childRoute(f.parent, child, "/questions/"+id+"/answer"), f.parentTok, map[string]string{"answer": "Use main"}, nil)
			if code != 200 {
				t.Fatalf("Main lost answer ownership: %d", code)
			}
		})
	}
}

func TestWorkerQuestionMainGoneWithdrawsWithoutLeakingQuestion(t *testing.T) {
	for _, state := range []string{"ended", "error", "deleted"} {
		t.Run(state, func(t *testing.T) {
			f := newDelegFx(t)
			child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent, title: "Build worker", private: true})
			var id string
			if state == "deleted" {
				// No relay has been accepted yet: Main can be deleted before
				// the reconciliation worker first observes this ask.
				var err error
				id, err = db.CreateAsk(t.Context(), f.pool, child, "Private branch selection?", false)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.pool.Exec(t.Context(), `DELETE FROM sessions WHERE id=$1`, f.parent); err != nil {
					t.Fatal(err)
				}
			} else {
				id = postAsk(t, f, token, child, "Private branch selection?", 0, "")
				if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET status=$2 WHERE id=$1`, f.parent, state); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := f.api.reconcileChildQuestions(t.Context(), time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			row := mustChildMessage(t, f, id)
			if row.Status != "withdrawn" || row.ChildEscalated || row.Answer != nil {
				t.Fatalf("wrong outcome: %+v", row)
			}
			poll := privDo(f.api.HandleGetMessageAnswer, "GET", "/api/messages/"+id+"/answer", id, token, "")
			var outcome answerResponse
			if json.Unmarshal(poll.Body.Bytes(), &outcome) != nil || poll.Code != 200 || !outcome.Withdrawn || outcome.Answered {
				t.Fatalf("waiter not released without answer: %d %s", poll.Code, poll.Body.String())
			}
			var count int
			var payload string
			if err := f.pool.QueryRow(t.Context(), `SELECT count(*),min(payload::text) FROM agent_events WHERE session_id=$1 AND client_event_id=$2`, child, messageEventID(id, "main-unavailable")).Scan(&count, &payload); err != nil {
				t.Fatal(err)
			}
			if count != 1 || !strings.Contains(payload, "Build worker") || strings.Contains(payload, "Private branch selection") || !strings.Contains(payload, "No answer was given") {
				t.Fatalf("wrong notice: %d %s", count, payload)
			}
		})
	}
}

func TestFlaggedOwnerlessChildStaysWithMainUntilExplainedEscalation(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	child, token := f.session(sessOpts{parent: f.parent})
	id := postAsk(t, f, token, child, "May I force push?", 0, "")
	row := mustChildMessage(t, f, id)
	if owners := questionOwners(t.Context(), f.pool, []db.MessageRow{*row}); owners[id] != "" {
		t.Fatalf("pending ownerless question reached a person: %v", owners)
	}
	// The request validation uses a child owned by the same account as Main.
	// An ownerless child is intentionally inaccessible through that delegation route.
	child, token = f.session(sessOpts{owner: "owner-1", parent: f.parent})
	id = postAsk(t, f, token, child, "May I force push?", 0, "")
	for _, body := range []map[string]string{nil, {"summary": "Allow it?"}, {"summary": " ", "recommendation": "Keep it."}} {
		code, _, _ := f.do("POST", childRoute(f.parent, child, "/questions/"+id+"/escalate"), f.parentTok, body, nil)
		if code != 400 {
			t.Fatalf("unexplained escalation accepted: %d", code)
		}
	}
	if mustChildMessage(t, f, id).ChildEscalated {
		t.Fatal("invalid explanation escalated the question")
	}
}
