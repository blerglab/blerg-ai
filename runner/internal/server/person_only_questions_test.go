package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func expireAs(t *testing.T, f *delegFx, id, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/messages/"+id+"/expire", strings.NewReader(`{}`))
	req.SetPathValue("id", id)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	f.api.HandlePostMessageExpire(w, req)
	return w
}

func replyCode(t *testing.T, f *delegFx, id, token string) int {
	t.Helper()
	return privDo(func(w http.ResponseWriter, r *http.Request) {
		r.SetPathValue("id", id)
		f.api.HandlePostMessageReply(w, r)
	}, "POST", "/api/messages/"+id+"/reply", "", token, `{"answer":"yes"}`).Code
}

// ask --person and a command approval in a top-level session, the way each is created.
func topLevelPersonQuestions(t *testing.T, f *delegFx, session, token string) map[string]string {
	t.Helper()
	return map[string]string{
		"ask --person": postAsk(t, f, token, session, "Ship it?", 1, `,"person_required":true`),
		"approval":     postAsk(t, f, delegDaemonToken, session, "Allow the command?", 1, `,"owner_only":true`),
	}
}

func TestTopLevelPersonRequirementSurvivesCreation(t *testing.T) {
	f := newDelegFx(t)
	session, token := f.session(sessOpts{owner: "owner-1"})
	for name, id := range topLevelPersonQuestions(t, f, session, token) {
		if row := mustChildMessage(t, f, id); !row.PersonRequired {
			t.Errorf("%s lost its requirement: %+v", name, row)
		}
	}
	plain := postAsk(t, f, token, session, "May I force push?", 1, "")
	if row := mustChildMessage(t, f, plain); row.PersonRequired {
		t.Errorf("a plain top-level ask became person-only: %+v", row)
	}
}

func TestTopLevelPersonQuestionRefusesEverySessionSideClose(t *testing.T) {
	f := newDelegFx(t)
	session, token := f.session(sessOpts{owner: "owner-1"})
	for name, id := range topLevelPersonQuestions(t, f, session, token) {
		if w := expireAs(t, f, id, token); w.Code != 403 {
			t.Errorf("%s: session expire: %d %s", name, w.Code, w.Body.String())
		}
		row := mustChildMessage(t, f, id)
		if row.Status != "open" || !row.WaiterEnded {
			t.Errorf("%s: not held: %+v", name, row)
		}
		// Already held: a second close, and the agent's own --resolve, still change nothing.
		if w := expireAs(t, f, id, delegDaemonToken); w.Code != 403 {
			t.Errorf("%s: daemon expire: %d", name, w.Code)
		}
		if row := mustChildMessage(t, f, id); row.Status != "open" {
			t.Errorf("%s: closed: %+v", name, row)
		}
	}
}

func TestTopLevelInterruptCloseKeepsPersonQuestion(t *testing.T) {
	f := newDelegFx(t)
	session, token := f.session(sessOpts{owner: "owner-1"})
	for name, id := range topLevelPersonQuestions(t, f, session, token) {
		// The daemon's empty /reply is the host's interruption close.
		if code := privDo(func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("id", id)
			f.api.HandlePostMessageReply(w, r)
		}, "POST", "/api/messages/"+id+"/reply", "", delegDaemonToken, `{"answer":""}`).Code; code != 403 {
			t.Errorf("%s: close: %d", name, code)
		}
		if row := mustChildMessage(t, f, id); row.Status != "open" {
			t.Errorf("%s closed: %+v", name, row)
		}
	}
}

func TestTopLevelPersonQuestionRefusesSessionSideAnswers(t *testing.T) {
	f := newDelegFx(t)
	session, token := f.session(sessOpts{owner: "owner-1"})
	f.hub.SetSessionOwner(session, f.dc.ID)
	for name, id := range topLevelPersonQuestions(t, f, session, token) {
		if code := replyCode(t, f, id, delegDaemonToken); code != 403 {
			t.Errorf("%s: daemon reply: %d", name, code)
		}
		if code := replyCode(t, f, id, token); code == 200 {
			t.Errorf("%s: session token answered", name)
		}
		if code := replyCode(t, f, id, f.human("someone-else")); code == 200 {
			t.Errorf("%s: another person answered", name)
		}
		if answerAskFrom(t.Context(), f.hub, f.pool, session, "yes", "", id, "", "blerg-board") {
			t.Errorf("%s: unsigned input answered", name)
		}
		if row := mustChildMessage(t, f, id); row.Status != "open" {
			t.Errorf("%s answered: %+v", name, row)
		}
	}
}

// Chat from the person still answers; a person-only question is theirs to answer.
func TestTopLevelPersonQuestionAnsweredByItsPerson(t *testing.T) {
	f := newDelegFx(t)
	session, token := f.session(sessOpts{owner: "owner-1"})
	f.hub.SetSessionOwner(session, f.dc.ID)
	id := postAsk(t, f, token, session, "Ship it?", 1, `,"person_required":true`)
	replyAs(t, f, id, f.human("owner-1"), "yes")
	if row := mustChildMessage(t, f, id); row.Status != "answered" || row.AnsweredBy != "person:owner-1" {
		t.Fatalf("row: %+v", row)
	}
}

func TestPlainTopLevelAskChatStaysOrdinaryAndSessionCanClose(t *testing.T) {
	f := newDelegFx(t)
	session, token := f.session(sessOpts{owner: "owner-1"})
	f.hub.SetSessionOwner(session, f.dc.ID)
	closed := postAsk(t, f, token, session, "Which branch?", 1, "")
	if w := expireAs(t, f, closed, token); w.Code != 200 {
		t.Fatalf("expire: %d", w.Code)
	}
	chat := postAsk(t, f, token, session, "Which tag?", 1, "")
	out, problem := f.api.SendMessage(t.Context(), runnerPrincipal{Kind: "human", Sub: "owner-1"}, session, "v1", "human")
	if problem != nil || out["answered_ask"] == true {
		t.Fatalf("chat: %v %v", out, problem)
	}
	if row := mustChildMessage(t, f, chat); row.Status != "open" {
		t.Fatalf("row: %+v", row)
	}
}

func TestTopLevelPersonQuestionSurvivesRecoveryAndPause(t *testing.T) {
	f := newDelegFx(t)
	session, token := f.session(sessOpts{owner: "owner-1"})
	questions := topLevelPersonQuestions(t, f, session, token)
	plain := postAsk(t, f, token, session, "Which branch?", 1, "")
	if w := postRecover(t, f, delegDaemonToken, session, 2); w.Code != 200 {
		t.Fatalf("recover: %d", w.Code)
	}
	for name, id := range questions {
		if row := mustChildMessage(t, f, id); row.Status != "open" || !row.WaiterEnded {
			t.Errorf("%s: %+v", name, row)
		}
	}
	if row := mustChildMessage(t, f, plain); row.Status != "withdrawn" {
		t.Errorf("plain: %+v", row)
	}

	other, otherToken := f.session(sessOpts{owner: "owner-1"})
	paused := topLevelPersonQuestions(t, f, other, otherToken)
	// An approval from before this change has no requirement recorded.
	old := postAsk(t, f, delegDaemonToken, other, "Allow it?", 1, `,"owner_only":true`)
	if _, err := f.pool.Exec(t.Context(), `UPDATE messages SET person_required=false WHERE id=$1`, old); err != nil {
		t.Fatal(err)
	}
	paused["older approval"] = old
	if _, err := db.CloseOpenAsksForSession(t.Context(), f.pool, other); err != nil {
		t.Fatal(err)
	}
	for name, id := range paused {
		if row := mustChildMessage(t, f, id); row.Status != "open" || !row.WaiterEnded {
			t.Errorf("%s: %+v", name, row)
		}
	}
	// Held, it stays the person's: the agent's own --resolve cannot close it either.
	if w := expireAs(t, f, old, otherToken); w.Code != 403 {
		t.Errorf("resolve of a held approval: %d", w.Code)
	}
}

func TestOwnerlessTopLevelPersonQuestion(t *testing.T) {
	f := newDelegFx(t)
	session, token := f.session(sessOpts{})
	f.hub.SetSessionOwner(session, f.dc.ID)
	id := postAsk(t, f, token, session, "Ship it?", 1, `,"person_required":true`)
	row := mustChildMessage(t, f, id)
	if !row.PersonRequired {
		t.Fatalf("row: %+v", row)
	}
	// The runner key's own session cannot answer or close it.
	if code := replyCode(t, f, id, delegDaemonToken); code != 403 {
		t.Fatalf("daemon reply: %d", code)
	}
	if out, problem := f.api.SendMessage(t.Context(), runnerPrincipal{Kind: "runner_key"}, session, "yes", "human"); problem == nil && out["answered_ask"] == true {
		t.Fatalf("runner key answered: %v", out)
	}
	if w := expireAs(t, f, id, token); w.Code != 403 {
		t.Fatalf("expire: %d", w.Code)
	}
	// Every signed-in person sees it in Needs you, and one of them resolves it.
	if owners := questionOwners(t.Context(), f.pool, []db.MessageRow{*mustChildMessage(t, f, id)}); owners[id] != anySignedInPerson {
		t.Fatalf("owners: %v", owners)
	}
	if code := replyCode(t, f, id, f.human("anyone")); code != 200 {
		t.Fatalf("signed-in person: %d", code)
	}
}

// The plain ask of a session a runner key started keeps its chat answer (0.5.4 and on).
func TestOwnerlessPlainAskStillAcceptsExplicitAnswer(t *testing.T) {
	f := newDelegFx(t)
	session, token := f.session(sessOpts{})
	f.hub.SetSessionOwner(session, f.dc.ID)
	id := postAsk(t, f, token, session, "Which branch?", 1, "")
	if !answerAskFrom(t.Context(), f.hub, f.pool, session, "main", "", id, "", "human") {
		t.Fatal("chat did not answer")
	}
	if row := mustChildMessage(t, f, id); row.Status != "answered" {
		t.Fatalf("row: %+v", row)
	}
}

// A late answer: the waiter left after the commit, so the session's outbox carries it, once.
func TestLateAnswerReachesTheSessionThroughItsOutbox(t *testing.T) {
	for _, engine := range []string{"codex", "hermes"} {
		t.Run(engine, func(t *testing.T) {
			f := newDelegFx(t)
			session, token := f.session(sessOpts{owner: "owner-1"})
			if _, err := f.pool.Exec(t.Context(), `UPDATE sessions SET engine=$2 WHERE id=$1`, session, engine); err != nil {
				t.Fatal(err)
			}
			f.dc.SetInputReceipts(true)
			f.hub.SetSessionOwner(session, f.dc.ID)
			id := postAsk(t, f, token, session, "Which branch?", 1, "")
			replyAs(t, f, id, f.human("owner-1"), "main")
			drain(f.dc.send)
			late := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest("POST", "/api/messages/"+id+"/expire", strings.NewReader(`{"waiter_ended":true}`))
				req.SetPathValue("id", id)
				req.Header.Set("Authorization", "Bearer "+delegDaemonToken)
				w := httptest.NewRecorder()
				f.api.HandlePostMessageExpire(w, req)
				return w
			}
			if w := late(); w.Code != 200 {
				t.Fatalf("close: %d %s", w.Code, w.Body.String())
			}
			if w := late(); w.Code != 200 {
				t.Fatalf("repeat: %d", w.Code)
			}
			redeliverQuestionAnswers(t.Context(), f.hub, f.pool, session)
			clientID := questionAnswerClientID(id)
			waitUntil(t, "the answer never reached the outbox", func() bool {
				var n int
				err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM session_inputs WHERE session_id=$1 AND client_id=$2`, session, clientID).Scan(&n)
				return err == nil && n == 1
			})
			row := mustChildMessage(t, f, id)
			if row.Status != "answered" || derefOrEmpty(row.Answer) != "main" {
				t.Fatalf("the answer changed: %+v", row)
			}
			// A heartbeat finds nothing more to send.
			if rows, err := db.UndeliveredQuestionAnswers(t.Context(), f.pool, session); err != nil || len(rows) != 0 {
				t.Fatalf("still undelivered: %d %v", len(rows), err)
			}
			frames := 0
			for _, raw := range drainFrames(f.dc) {
				var msg protocol.AgentUserMessage
				if json.Unmarshal(raw, &msg) == nil && msg.ClientID == clientID {
					frames++
					if msg.Source != questionAnswerSource || !strings.Contains(msg.Text, "main") {
						t.Fatalf("frame: %+v", msg)
					}
				}
			}
			if frames != 1 {
				t.Fatalf("frames = %d", frames)
			}
		})
	}
}

// A close with no answer behind it stays a plain close: nothing is invented, and a person-only
// question stays with the person.
func TestWaiterEndedWithoutAnAnswerIsAClose(t *testing.T) {
	f := newDelegFx(t)
	session, token := f.session(sessOpts{owner: "owner-1"})
	f.dc.SetInputReceipts(true)
	f.hub.SetSessionOwner(session, f.dc.ID)
	do := func(id string) int {
		req := httptest.NewRequest("POST", "/api/messages/"+id+"/expire", strings.NewReader(`{"waiter_ended":true}`))
		req.SetPathValue("id", id)
		req.Header.Set("Authorization", "Bearer "+delegDaemonToken)
		w := httptest.NewRecorder()
		f.api.HandlePostMessageExpire(w, req)
		return w.Code
	}
	plain := postAsk(t, f, token, session, "Which branch?", 1, "")
	if code := do(plain); code != 200 {
		t.Fatalf("plain: %d", code)
	}
	required := postAsk(t, f, token, session, "Ship it?", 1, `,"person_required":true`)
	if code := do(required); code != 403 {
		t.Fatalf("required: %d", code)
	}
	if row := mustChildMessage(t, f, required); row.Status != "open" {
		t.Fatalf("row: %+v", row)
	}
	if pending, err := db.HasPendingSessionInputs(t.Context(), f.pool, session); err != nil || pending {
		t.Fatalf("pending=%t err=%v", pending, err)
	}
}

// The inbox reply endpoint must preserve the same routing as child-question answers.
func TestMainReplyEndpointRefusesPersonQuestionAndKeepsWorkerRouting(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	f.hub.SetSessionOwner(child, f.dc.ID)
	person := postAsk(t, f, token, child, "Choose a release?", 1, `,"person_required":true`)
	if code := replyCode(t, f, person, f.parentTok); code != http.StatusForbidden {
		t.Fatalf("Main replied to person-only question: %d", code)
	}
	if row := mustChildMessage(t, f, person); row.Status != "open" || !row.PersonRequired {
		t.Fatalf("person question changed: %+v", row)
	}
	rec := privDo(f.api.HandlePostMessages, "POST", "/api/messages", "", token, `{"kind":"ask","body":"Which branch?"}`)
	var posted struct{ ID, Recipient string }
	if err := json.Unmarshal(rec.Body.Bytes(), &posted); err != nil || rec.Code != http.StatusCreated || posted.Recipient != "parent" {
		t.Fatalf("worker question did not route to Main: %d %s", rec.Code, rec.Body.String())
	}
	code, body, _ := f.do("POST", childRoute(f.parent, child, "/questions/"+posted.ID+"/answer"), f.parentTok, map[string]string{"answer": "main"}, nil)
	if code != http.StatusOK {
		t.Fatalf("Main answer: %d %v", code, body)
	}
	if row := mustChildMessage(t, f, posted.ID); row.Status != "answered" || row.AnsweredBy != "session:"+f.parent {
		t.Fatalf("worker answer: %+v", row)
	}
}
