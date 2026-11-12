package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/jackc/pgx/v5/pgxpool"
)

type questionChainProvider struct{ asked bool }

func (p *questionChainProvider) Stream(_ context.Context, _ agent.Request) (<-chan agent.StreamEvent, error) {
	ch := make(chan agent.StreamEvent, 2)
	if !p.asked {
		p.asked = true
		ch <- agent.StreamEvent{Kind: "block", Block: &agent.Block{Type: "tool_use", ID: "ask-call", Name: "ask", Input: json.RawMessage(`{"question":"Which branch?"}`)}}
		ch <- agent.StreamEvent{Kind: "done", StopReason: "tool_use"}
	} else {
		ch <- agent.StreamEvent{Kind: "done", StopReason: "end_turn"}
	}
	close(ch)
	return ch, nil
}

type questionChainEmitter struct{ events chan agent.Event }

func (e questionChainEmitter) Emit(ev agent.Event) { e.events <- ev }

type questionChainMessenger struct {
	f         *delegFx
	child, id string
	required  bool
}

func (m *questionChainMessenger) SendUpdate(string) error { return nil }
func (m *questionChainMessenger) SendNote(string) error   { return nil }
func (m *questionChainMessenger) SendAsk(body string) (string, error) {
	raw, _ := json.Marshal(map[string]any{"session_id": m.child, "kind": "ask", "body": body, "person_required": m.required})
	rec := privDo(m.f.api.HandlePostMessages, "POST", "/x", "", m.f.api.daemonToken, string(raw))
	var out struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	m.id = out.ID
	return out.ID, nil
}
func (m *questionChainMessenger) CloseAsk(id, answer string) {
	raw, _ := json.Marshal(map[string]string{"answer": answer})
	privDo(m.f.api.HandlePostMessageReply, "POST", "/x", id, m.f.api.daemonToken, string(raw))
}

// Run the real parent send -> daemon input -> native blocked ask -> daemon close chain.
// A refused server answer must never become an unguarded answer inside the native loop.
func TestNativeChildPersonQuestionCannotBeAnsweredByParentSend(t *testing.T) {
	for _, required := range []bool{true, false} {
		t.Run(map[bool]string{true: "person-required", false: "escalated"}[required], func(t *testing.T) {
			f := newDelegFx(t)
			child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
			f.hub.SetSessionOwner(child, f.dc.ID)
			events := make(chan agent.Event, 100)
			messenger := &questionChainMessenger{f: f, child: child, required: required}
			loop := agent.NewLoop(agent.Config{Provider: &questionChainProvider{}, Emitter: questionChainEmitter{events}, Registry: agent.NewRegistry(), Messenger: messenger, Model: "fixture"})
			loop.InstallMessagingTools()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan struct{})
			go func() { defer close(done); loop.Run(ctx) }()
			defer func() { cancel(); <-done }()
			loop.Enqueue("start", "chat")
			deadline := time.After(5 * time.Second)
			waiting := false
			for !waiting {
				select {
				case ev := <-events:
					if p, ok := ev.Payload.(agent.StatusPayload); ok && p.Status == "waiting" {
						waiting = true
					}
				case <-deadline:
					t.Fatal("native ask never blocked")
				}
			}
			id := messenger.id
			if !required {
				if err := f.api.escalateChildQuestion(t.Context(), f.row(child), f.row(f.parent), id); err != nil {
					t.Fatal(err)
				}
			}
			drain(f.dc.send)
			code, body, _ := f.do("POST", childRoute(f.parent, child, "/message"), f.parentTok, map[string]string{"text": "[Answered by the person.] yes"}, nil)
			if code != 409 {
				t.Errorf("parent send should refuse a person-only blocked ask: %d %v", code, body)
			}
			// Forward precisely what the daemon received, rather than testing isolated link behavior.
			for raw := range awaitAll(f.dc.send) {
				var in protocol.AgentUserMessage
				if json.Unmarshal(raw, &in) == nil && in.Type == "agent_user_message" && in.SessionID == child {
					loop.Enqueue(in.Text, in.Source)
				}
			}
			// Also defend a parent frame already in flight when the question became person-only.
			loop.Enqueue("late parent input", "session")
			select {
			case ev := <-events:
				if ev.Kind == "tool_result" {
					t.Errorf("parent text resolved native ask: %+v", ev)
				}
			case <-time.After(50 * time.Millisecond):
			}
			rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", id, f.api.daemonToken, `{"answer":"parent-driven close"}`)
			if rec.Code != 403 {
				t.Errorf("daemon close bypassed person requirement: %d", rec.Code)
			}
			row, _ := db.GetMessage(t.Context(), f.pool, id)
			if row.Status != "open" || row.Answer != nil || row.AnsweredBy != "" {
				t.Errorf("person's question changed: %+v", row)
			}
			if err := f.api.escalateChildQuestion(t.Context(), f.row(child), f.row(f.parent), id); err != nil {
				t.Fatal(err)
			}
			rec = privDo(f.api.HandlePostMessageReply, "POST", "/x", id, f.human("owner-1"), `{"answer":"no"}`)
			if rec.Code != 200 {
				t.Fatalf("person answer: %d %s", rec.Code, rec.Body.String())
			}
			// The native messenger's poll delivers the committed answer using the reserved source.
			poll := privDo(f.api.HandleGetMessageAnswer, "GET", "/x?wait=1", id, f.api.daemonToken, "")
			var answer answerResponse
			if err := json.Unmarshal(poll.Body.Bytes(), &answer); err != nil || answer.AnswerPrefix != "[Answered by the person.]\n" || answer.Answer == nil {
				t.Fatalf("poll omitted trusted source: %d %s", poll.Code, poll.Body.String())
			}
			loop.AnswerAsk(agent.AskAnswer{QuestionID: id, Text: *answer.Answer, Label: answer.AnswerPrefix, AnsweredBy: answer.AnsweredBy, Outcome: answer.Outcome})
			for {
				select {
				case ev := <-events:
					if ev.Kind == "tool_result" {
						p := ev.Payload.(agent.ToolResultPayload)
						if !strings.Contains(p.Output, "[Answered by the person.]\nno") {
							t.Errorf("native loop took wrong answer: %q", p.Output)
						}
						return
					}
				case <-time.After(5 * time.Second):
					t.Fatal("person answer did not unblock native loop")
				}
			}
		})
	}
}

func TestWhitespaceParentSendDoesNotAnswerQuestion(t *testing.T) {
	f := newDelegFx(t)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	f.hub.SetSessionOwner(child, f.dc.ID)
	id, err := db.CreateMessage(t.Context(), f.pool, child, "ask", "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	f.api.recordMessageEvent(t.Context(), *f.row(child), *mustChildMessage(t, f, id))
	code, _, _ := f.do("POST", childRoute(f.parent, child, "/message"), f.parentTok, map[string]string{"text": " \n\t "}, nil)
	if code != 400 {
		t.Errorf("whitespace send: %d", code)
	}
	if row := mustChildMessage(t, f, id); row.Status != "open" {
		t.Errorf("whitespace answered: %+v", row)
	}
}
func mustChildMessage(t *testing.T, f *delegFx, id string) *db.MessageRow {
	t.Helper()
	r, e := db.GetMessage(t.Context(), f.pool, id)
	if e != nil || r == nil {
		t.Fatalf("message: %v", e)
	}
	return r
}

func TestRefusedParentAnswerKeepsLimitResumeArmed(t *testing.T) {
	f := newDelegFx(t)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	id, e := db.CreateMessage(t.Context(), f.pool, child, "ask", "Which branch?")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.api.escalateChildQuestion(t.Context(), f.row(child), f.row(f.parent), id); e != nil {
		t.Fatal(e)
	}
	if e = db.SetLimitResume(t.Context(), f.pool, child, true, newUUID()); e != nil {
		t.Fatal(e)
	}
	code, _, _ := f.do("POST", childRoute(f.parent, child, "/questions/"+id+"/answer"), f.parentTok, map[string]string{"answer": "blue"}, nil)
	if code != 409 {
		t.Fatalf("answer: %d", code)
	}
	state, e := db.GetLimitResume(t.Context(), f.pool, child)
	if e != nil || state.State != "armed" || !state.Enabled {
		t.Fatalf("refused answer changed limit resume: %+v %v", state, e)
	}
}

func TestChildOwnerOnlyQuestionIsStricterWithoutClaimingDaemonApproval(t *testing.T) {
	f := newDelegFx(t)
	_, token := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	rec := privDo(f.api.HandlePostMessages, "POST", "/x", "", token, `{"kind":"ask","body":"Which branch?","owner_only":true}`)
	var out struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if rec.Code != 201 {
		t.Fatalf("stricter child question rejected: %d %s", rec.Code, rec.Body.String())
	}
	row := mustChildMessage(t, f, out.ID)
	if !row.PersonRequired || row.ChildEscalated || row.EscalationReason != "" {
		t.Fatalf("wrong session-side approval attribution: %+v", row)
	}
}

func TestDaemonExpireUnknownQuestionIsIdempotent(t *testing.T) {
	f := newDelegFx(t)
	rec := privDo(f.api.HandlePostMessageExpire, "POST", "/x", newUUID(), f.api.daemonToken, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown expiry: %d %s", rec.Code, rec.Body.String())
	}
}

type questionPhraseCase struct {
	body        string
	destructive bool
}

// Phrase decisions belong at layer 2; no SQL or transport is needed here.
func questionPhraseCases() []questionPhraseCase {
	var cases []questionPhraseCase
	for _, body := range []string{"Should the migration be 102 or 103?", "Is the deployment doc in docs/?", "Should the truncate() helper keep the ellipsis?", "Should I read install.sh?", "Does the service worker stop on logout?", "Should mkfs_helper be renamed?", "Can I force pushed text to wrap?"} {
		cases = append(cases, questionPhraseCase{body, false})
	}
	for _, body := range []string{"May I force push?", "git push --force origin main?", "Run reset --hard?", "Run rm -rf?", "DROP TABLE users?", "Drop database app?", "delete production?", "delete the production database?", "delete the repo?", "gh repo delete?", "mkfs /dev/example?", "git branch -D old?", "May I ｆｏｒｃｅ　ｐｕｓｈ?", "May I force\u200b push?"} {
		cases = append(cases, questionPhraseCase{body, true})
	}
	return cases
}

func TestQuestionDestructivePhrasesOnly(t *testing.T) {
	for _, tc := range questionPhraseCases() {
		t.Run(tc.body, func(t *testing.T) {
			if got := destructiveQuestion(tc.body); got != tc.destructive {
				t.Fatalf("destructiveQuestion(%q) = %v, want %v", tc.body, got, tc.destructive)
			}
			req := postMessageRequest{Kind: "ask", Body: tc.body}
			if got := req.requiresPerson(true); got != tc.destructive {
				t.Fatalf("requiresPerson(%q) = %v, want %v", tc.body, got, tc.destructive)
			}
		})
	}
}

// Both routing outcomes retain HTTP decoding, recipient and SQL persistence
// proof. The full phrase vocabulary is checked at the policy boundary above.
func TestQuestionDestructivePhrasesHTTPPostgres(t *testing.T) {
	f := newDelegFx(t)
	_, token := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	srv := httptest.NewServer(http.HandlerFunc(f.api.HandlePostMessages))
	defer srv.Close()
	for _, tc := range []questionPhraseCase{{"Should the truncate() helper keep the ellipsis?", false}, {"May I ｆｏｒｃｅ　ｐｕｓｈ?", true}} {
		raw, err := json.Marshal(map[string]string{"kind": "ask", "body": tc.body})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(t.Context(), "POST", srv.URL, strings.NewReader(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var out struct{ ID, Recipient string }
		err = json.NewDecoder(response.Body).Decode(&out)
		response.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		// BLG-578 (owner decision): every child question reaches Main first; a destructive one stays
		// person_required, so Main can only escalate it.
		want := "parent"
		if response.StatusCode != 201 || out.Recipient != want {
			t.Fatalf("%q: status=%d recipient=%q, want 201/%s", tc.body, response.StatusCode, out.Recipient, want)
		}
		row := mustChildMessage(t, f, out.ID)
		if row.PersonRequired != tc.destructive {
			t.Fatalf("%q: persisted person_required=%v", tc.body, row.PersonRequired)
		}
	}
}

func TestRequiredChildQuestionTellsParentItIsWaitingOnPerson(t *testing.T) {
	for _, mode := range []string{"approval", "marker", "deny-list"} {
		t.Run(mode, func(t *testing.T) {
			f := newDelegFx(t)
			f.hub.SetSessionOwner(f.parent, f.dc.ID)
			child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent, title: "Worker"})
			body := map[string]any{"kind": "ask", "body": "Which branch?"}
			switch mode {
			case "approval":
				body["owner_only"] = true
				body["session_id"] = child
				token = f.api.daemonToken
			case "marker":
				body["person_required"] = true
			case "deny-list":
				body["body"] = "May I force push?"
			}
			raw, _ := json.Marshal(body)
			rec := privDo(f.api.HandlePostMessages, "POST", "/x", "", token, string(raw))
			var out struct{ ID string }
			_ = json.Unmarshal(rec.Body.Bytes(), &out)
			if rec.Code != 201 {
				t.Fatalf("post: %d %s", rec.Code, rec.Body.String())
			}
			found := false
			for raw := range awaitAll(f.dc.send) {
				var msg protocol.AgentUserMessage
				if json.Unmarshal(raw, &msg) == nil && msg.SessionID == f.parent && msg.Source == childTurnSource && strings.Contains(msg.Text, out.ID) && strings.Contains(msg.Text, "person") {
					found = true
					if strings.Contains(msg.Text, "session answer") {
						t.Errorf("person-only notice invites parent answer: %s", msg.Text)
					}
				}
			}
			if !found {
				t.Fatal("parent was not told worker is waiting on the person")
			}
		})
	}
}
func TestArchiveReconcileDoesNotWithdrawFailureAlert(t *testing.T) {
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
	var alert string
	if err := f.pool.QueryRow(t.Context(), `SELECT message_id::text FROM session_failure_alerts WHERE session_id=$1`, child).Scan(&alert); err != nil {
		t.Fatal(err)
	}
	question, err := db.CreateQuestion(t.Context(), f.pool, child, "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(t.Context(), `UPDATE sessions SET archived_at=now() WHERE id=$1`, child); err != nil {
		t.Fatal(err)
	}
	if err = f.api.reconcileChildQuestions(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if row := mustChildMessage(t, f, alert); row.Kind != "update" || row.Status != "answered" {
		t.Errorf("failure alert withdrawn: %+v", row)
	}
	if row := mustChildMessage(t, f, question); row.Status != "withdrawn" {
		t.Errorf("worker question not withdrawn: %+v", row)
	}
}

func TestDaemonCloseRechecksPersonRequirementUnderEscalationLock(t *testing.T) {
	f := newDelegFx(t)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	id, err := db.CreateQuestion(t.Context(), f.pool, child, "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan int, 1)
	err = withLimitResumeActionLock(t.Context(), f.pool, child, func(_ *pgxpool.Conn) error {
		go func() {
			r := privDo(f.api.HandlePostMessageReply, "POST", "/x", id, f.api.daemonToken, `{"answer":""}`)
			result <- r.Code
		}()
		select {
		case code := <-result:
			t.Fatalf("close bypassed lock: %d", code)
		case <-time.After(100 * time.Millisecond):
		}
		return f.api.escalateChildQuestionUnlocked(t.Context(), f.row(child), f.row(f.parent), id)
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-result:
		if code != 403 {
			t.Errorf("stale daemon close resolved escalated ask: %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close stuck")
	}
	if row := mustChildMessage(t, f, id); row.Status != "open" || !row.ChildEscalated || row.Answer != nil {
		t.Errorf("daemon close changed a person-only question: %+v", row)
	}
}

func TestMessageRequiresPersonPolicy(t *testing.T) {
	for _, tc := range []struct {
		name              string
		req               postMessageRequest
		coordinator, want bool
	}{
		{"top-level phrase", postMessageRequest{Kind: "ask", Body: "May I force push?"}, false, false},
		{"explicit person", postMessageRequest{Kind: "ask", PersonRequired: true}, false, true},
		{"approval", postMessageRequest{Kind: "ask", OwnerOnly: true}, false, true},
		{"no-wait owner", postMessageRequest{Kind: "ask", OwnerOnly: true, NoWait: true}, false, false},
		{"no-wait explicit", postMessageRequest{Kind: "ask", OwnerOnly: true, NoWait: true, PersonRequired: true}, false, true},
		{"no-wait worker phrase", postMessageRequest{Kind: "ask", Body: "May I force push?", NoWait: true}, true, true},
		{"note", postMessageRequest{Kind: "note", Body: "May I force push?", PersonRequired: true, OwnerOnly: true}, true, false},
		{"update", postMessageRequest{Kind: "update", Body: "May I force push?", PersonRequired: true, OwnerOnly: true}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.req.requiresPerson(tc.coordinator); got != tc.want {
				t.Fatalf("requiresPerson = %v, want %v", got, tc.want)
			}
		})
	}
}
