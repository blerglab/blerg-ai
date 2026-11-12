package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type authoritativeProvider struct {
	mu       sync.Mutex
	calls    []agent.Request
	question string
	called   chan struct{}
}

func (p *authoritativeProvider) Stream(_ context.Context, r agent.Request) (<-chan agent.StreamEvent, error) {
	p.mu.Lock()
	p.calls = append(p.calls, r)
	first := len(p.calls) == 1
	question := p.question
	p.mu.Unlock()
	if p.called != nil {
		select {
		case p.called <- struct{}{}:
		default:
		}
	}
	if question == "" {
		question = "May I force push?"
	}
	input, _ := json.Marshal(map[string]string{"question": question})
	out := make(chan agent.StreamEvent, 2)
	if first {
		out <- agent.StreamEvent{Kind: "block", Block: &agent.Block{Type: "tool_use", ID: "ask-call", Name: "ask", Input: input}}
		out <- agent.StreamEvent{Kind: "done", StopReason: "tool_use"}
	} else {
		out <- agent.StreamEvent{Kind: "done", StopReason: "end_turn"}
	}
	close(out)
	return out, nil
}

// escalateNativeQuestionForPerson follows the public Main escalation route.
// A worker's approval classification alone never grants the person an answer turn.
func escalateNativeQuestionForPerson(t *testing.T, f *delegFx, child, id string) {
	t.Helper()
	row := mustChildMessage(t, f, id)
	if row.ChildEscalated || row.EscalationReason != "" {
		t.Fatalf("question bypassed Main: %+v", row)
	}
	rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", id, f.human("owner-1"), `{"answer":"no"}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("person answered before Main escalated: %d %s", rec.Code, rec.Body.String())
	}
	code, body, _ := f.do("POST", childRoute(f.parent, child, "/questions/"+id+"/escalate"), f.parentTok, map[string]string{
		"summary":        "May the worker proceed with this change?",
		"recommendation": "I recommend declining the change.",
	}, nil)
	if code != http.StatusOK {
		t.Fatalf("Main escalation: %d %v", code, body)
	}
	row = mustChildMessage(t, f, id)
	if !row.ChildEscalated || row.EscalatedBy != "session:"+f.parent || row.EscalationReason != "parent_requested" {
		t.Fatalf("Main escalation audit: %+v", row)
	}
}

func TestAuthoritativeNativeAskQueuesEveryOrdinarySource(t *testing.T) {
	for _, top := range []bool{false, true} {
		t.Run(map[bool]string{true: "top-level", false: "child"}[top], func(t *testing.T) {
			// The topology owns one private database; every source owns a fresh
			// session, loop, HTTP listener and question within it.
			fixture := newDelegFx(t)
			for _, source := range []string{"agent-token", "blerg-board", "other-person", "child_turn", "usage_limit", "review", "worker-question"} {
				t.Run(source, func(t *testing.T) {
					caseFixture := *fixture
					caseFixture.t = t
					f := &caseFixture
					parent := ""
					if !top {
						parent = f.parent
					}
					session, _ := f.session(sessOpts{owner: "owner-1", parent: parent})
					f.hub.SetSessionOwner(session, f.dc.ID)
					mux := http.NewServeMux()
					mux.HandleFunc("POST /api/messages", f.api.HandlePostMessages)
					mux.HandleFunc("GET /api/messages/{id}/answer", f.api.HandleGetMessageAnswer)
					mux.HandleFunc("POST /api/messages/{id}/reply", f.api.HandlePostMessageReply)
					srv := httptest.NewServer(mux)
					defer srv.Close()
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					events := make(chan agent.Event, 100)
					provider := &authoritativeProvider{called: make(chan struct{}, 1)}
					var loop *agent.Loop
					messenger := daemon.NewLoopMessenger(daemon.LoopMessengerConfig{Context: ctx, BaseURL: srv.URL, SessionID: session, TokenSource: func() string { return delegDaemonToken }, OnAnswer: func(a agent.AskAnswer) { loop.AnswerAsk(a) }})
					loop = agent.NewLoop(agent.Config{Provider: provider, Emitter: questionChainEmitter{events}, Registry: agent.NewRegistry(), Messenger: messenger, Model: "fixture"})
					loop.InstallMessagingTools()
					done := make(chan struct{})
					go func() { defer close(done); loop.Run(ctx) }()
					defer func() { cancel(); <-done }()
					loop.Enqueue("start", "chat")
					var question string
					wait := time.After(5 * time.Second)
					for question == "" {
						select {
						case ev := <-events:
							if ev.Kind == "ask" {
								question = ev.Payload.(agent.MessagingPayload).MessageID
							}
						case <-wait:
							t.Fatal("ask never posted")
						}
					}
					drain(f.dc.send)
					text := "ordinary " + source
					var problem *APIError
					switch source {
					case childTurnSource:
						worker := f.childOf(session)
						problem = f.api.sendWorkerNotice(ctx, runnerPrincipal{Kind: "human", Sub: "owner-1"}, session, workerEvent{worker, childTurnAsked, newUUID()})
					case "worker-question":
						_, workerToken := f.session(sessOpts{owner: "owner-1", parent: session})
						body, _ := json.Marshal(map[string]any{"kind": "ask", "body": text})
						rec := privDo(f.api.HandlePostMessages, "POST", "/x", "", workerToken, string(body))
						if rec.Code != 201 {
							t.Fatalf("worker ask: %d %s", rec.Code, rec.Body.String())
						}
					case "agent-token":
						_, problem = f.api.SendMessage(ctx, runnerPrincipal{Kind: agentPrincipalKind, Sub: "agent-fixture", OnBehalfOf: "owner-1"}, session, text, "runner")
					case "blerg-board":
						_, problem = f.api.SendMessage(ctx, runnerPrincipal{Kind: "human", Sub: "owner-1"}, session, text, "blerg-board")
					case "other-person":
						_, problem = f.api.SendMessage(ctx, runnerPrincipal{Kind: "human", Sub: "other"}, session, text, "human")
					default:
						problem = f.api.sendRelay(ctx, runnerPrincipal{Kind: "human", Sub: "owner-1"}, session, text, source)
					}
					if problem != nil {
						t.Fatalf("ordinary input refused: %v", problem)
					}
					// Use the actual server-produced frame and the actual host's input method.
					dispatched := false
					dispatchDeadline := time.After(5 * time.Second)
					for !dispatched {
						select {
						case raw := <-f.dc.send:
							var frame protocol.AgentUserMessage
							if json.Unmarshal(raw, &frame) == nil && frame.Type == "agent_user_message" && frame.SessionID == session {
								text = frame.Text
								loop.EnqueueInput(agent.Input{Text: frame.Text, Source: frame.Source, ClientID: frame.ClientID})
								dispatched = true
							}
						case <-dispatchDeadline:
							t.Fatal("ordinary input was not dispatched")
						}
					}
					row, _ := db.GetMessage(ctx, f.pool, question)
					if row.Status != "open" || row.Answer != nil {
						t.Fatalf("ordinary source changed question: %+v", row)
					}
					if !top {
						escalateNativeQuestionForPerson(t, f, session, question)
					}
					rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", question, f.human("owner-1"), `{"answer":"no"}`)
					if rec.Code != 200 {
						t.Fatalf("owner answer: %d %s", rec.Code, rec.Body.String())
					}
					result, queued := false, false
					deadline := time.After(5 * time.Second)
					for !result || !queued {
						select {
						case ev := <-events:
							if ev.Kind == "tool_result" {
								p := ev.Payload.(agent.ToolResultPayload)
								want := "[Answered by the person.]\nno"
								if top {
									want = "no"
								}
								if p.Output != want {
									t.Errorf("tool result = %q, want %q", p.Output, want)
								}
								result = true
							}
							if ev.Kind == "user_message" {
								p := ev.Payload.(agent.UserMessagePayload)
								if p.Source == "ask_answer" && p.Text != "no" {
									t.Errorf("ordinary input recorded as an answer: %+v", p)
								}
								if p.Text == text && p.Source != "ask_answer" {
									queued = true
								}
							}
						case <-deadline:
							t.Fatalf("answer result=%t queued=%t", result, queued)
						}
					}
					found := false
					providerDeadline := time.After(5 * time.Second)
					for !found {
						provider.mu.Lock()
						for _, call := range provider.calls[1:] {
							for _, msg := range call.Messages {
								for _, block := range msg.Blocks {
									if block.Type == "text" && strings.Contains(block.Text, text) {
										found = true
									}
								}
							}
						}
						provider.mu.Unlock()
						if !found {
							select {
							case <-provider.called:
							case <-providerDeadline:
								t.Fatal("ordinary input never reached provider after ask")
							}
						}
					}
					if !found {
						t.Error("ordinary input never reached provider after ask")
					}

				})
			}
		})
	}
}

func TestDesktopNativeHelperApprovalUsesChildSessionToken(t *testing.T) {
	f := newDelegFx(t)
	child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/messages", f.api.HandlePostMessages)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	messenger := daemon.NewLoopMessenger(daemon.LoopMessengerConfig{Context: t.Context(), BaseURL: srv.URL, SessionID: child, OwnerOnly: true, TokenSource: func() string { return token }})
	id, err := messenger.SendAsk("Allow this command?")
	if err != nil {
		t.Fatalf("desktop helper approval rejected: %v", err)
	}
	row := mustChildMessage(t, f, id)
	if !row.PersonRequired || row.ChildEscalated {
		t.Fatalf("helper approval must wait for Main: %+v", row)
	}
	escalateNativeQuestionForPerson(t, f, child, id)
}

func TestNativeInterruptedCloseAndMissingEventChat(t *testing.T) {
	for _, mode := range []string{"daemon-close", "daemon-reply-close", "child-close", "owner-chat"} {
		t.Run(mode, func(t *testing.T) {
			// Only the person resolves this one, so no close can end it.
			person := mode != "owner-chat"
			f := newDelegFx(t)
			child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent})
			f.hub.SetSessionOwner(child, f.dc.ID)
			mux := http.NewServeMux()
			mux.HandleFunc("POST /api/messages", f.api.HandlePostMessages)
			mux.HandleFunc("GET /api/messages/{id}/answer", f.api.HandleGetMessageAnswer)
			mux.HandleFunc("POST /api/messages/{id}/reply", f.api.HandlePostMessageReply)
			mux.HandleFunc("POST /api/messages/{id}/expire", f.api.HandlePostMessageExpire)
			srv := httptest.NewServer(mux)
			defer srv.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			credential := delegDaemonToken
			if mode == "child-close" {
				credential = token
			}
			answers := make(chan agent.AskAnswer, 1)
			m := daemon.NewLoopMessenger(daemon.LoopMessengerConfig{Context: ctx, BaseURL: srv.URL, SessionID: child, TokenSource: func() string { return credential }, OnAnswer: func(a agent.AskAnswer) { answers <- a }})
			id, err := m.SendAsk("May I force push?")
			if err != nil {
				t.Fatal(err)
			}
			if recorded, err := db.HasAgentEvent(ctx, f.pool, child, messageEventID(id, "ask")); mode != "child-close" && (err != nil || recorded) {
				t.Fatalf("expected missing native ask event: %t %v", recorded, err)
			}
			escalateNativeQuestionForPerson(t, f, child, id)
			switch mode {
			case "owner-chat":
				body, p := f.api.SendMessage(ctx, runnerPrincipal{Kind: "human", Sub: "owner-1"}, child, "no", "human")
				if p != nil || body["answered_ask"] == true {
					t.Errorf("owner chat missed DB ask without event: %v %v", body, p)
				}
				replyAs(t, f, id, f.human("owner-1"), "no")
			case "daemon-reply-close":
				r := privDo(f.api.HandlePostMessageReply, "POST", "/x", id, delegDaemonToken, `{"answer":""}`)
				if r.Code != 403 {
					t.Fatalf("daemon reply close of a person-only question = %d %s", r.Code, r.Body.String())
				}
			default:
				m.(agent.AskCloser).CloseAsk(id, "")
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				row, _ := db.GetMessage(ctx, f.pool, id)
				if person && row.WaiterEnded {
					if row.Status != "open" || row.Answer != nil || !row.ChildEscalated {
						t.Errorf("interrupted person-only question = %+v", row)
					}
					break
				}
				if !person && row.Status != "open" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("question not settled: %+v", row)
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}
func TestRecoveryWithdrawsOnlyDeadBlockingQuestions(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	f.mux.HandleFunc("POST /api/messages/recover", f.api.HandleRecoverQuestions)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	blocking, err := db.CreateMessage(t.Context(), f.pool, child, "ask", "Which branch?")
	if err != nil {
		t.Fatal(err)
	}
	question, err := db.CreateQuestion(t.Context(), f.pool, child, "Review later?")
	if err != nil {
		t.Fatal(err)
	}
	w := postRecover(t, f, delegDaemonToken, child, 1)
	if w.Code != 200 {
		t.Errorf("recovery: %d %s", w.Code, w.Body.String())
	}
	if row := mustChildMessage(t, f, blocking); row.Status != "withdrawn" {
		t.Errorf("restart stranded blocking ask: %+v", row)
	}
	if row := mustChildMessage(t, f, question); row.Status != "open" {
		t.Error("restart withdrew no-wait question")
	}
	raw := awaitDaemonMessage(t, f.dc.send, "agent_user_message")
	var notice protocol.AgentUserMessage
	if json.Unmarshal(raw, &notice) != nil || notice.SessionID != f.parent || notice.Source != childTurnSource || !strings.Contains(notice.Text, blocking) || !strings.Contains(notice.Text, "kind=question_resolved") {
		t.Fatalf("restart did not tell parent about dead ask: %s", raw)
	}
}

func TestEscalatedQuestionNoticeDoesNotRepeatPrefix(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	_, token := f.session(sessOpts{owner: "owner-1", parent: f.parent, title: "Worker"})
	r := privDo(f.api.HandlePostMessages, "POST", "/x", "", token, `{"kind":"ask","body":"May I force push?"}`)
	if r.Code != 201 {
		t.Fatal(r.Body.String())
	}
	raw := awaitDaemonMessage(t, f.dc.send, "agent_user_message")
	var frame protocol.AgentUserMessage
	_ = json.Unmarshal(raw, &frame)
	if strings.Count(frame.Text, "Worker event:") != 1 || strings.Contains(frame.Text, "force push") || strings.Contains(frame.Text, "Worker\"") {
		t.Fatalf("escalation notice repeated prefix: %s", frame.Text)
	}
}
func TestPersonResolutionTellsParentWhenToResend(t *testing.T) {
	f := newDelegFx(t)
	f.hub.SetSessionOwner(f.parent, f.dc.ID)
	child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	r := privDo(f.api.HandlePostMessages, "POST", "/x", "", token, `{"kind":"ask","body":"May I force push?"}`)
	var out struct{ ID string }
	_ = json.Unmarshal(r.Body.Bytes(), &out)
	if r.Code != http.StatusCreated || out.ID == "" {
		t.Fatalf("worker ask: %d %s", r.Code, r.Body.String())
	}
	escalateNativeQuestionForPerson(t, f, child, out.ID)
	drain(f.dc.send)
	r = privDo(f.api.HandlePostMessageReply, "POST", "/x", out.ID, f.human("owner-1"), `{"answer":"no"}`)
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	raw := awaitDaemonMessage(t, f.dc.send, "agent_user_message")
	var frame protocol.AgentUserMessage
	_ = json.Unmarshal(raw, &frame)
	if frame.SessionID != f.parent || frame.Source != childTurnSource || !strings.Contains(frame.Text, out.ID) || !strings.Contains(frame.Text, "kind=question_resolved") {
		t.Fatalf("parent resolution notice: %s", raw)
	}
	code, result, _ := f.do("GET", childRoute(f.parent, child, "/result"), f.parentTok, nil, nil)
	if code != 200 {
		t.Fatalf("Main cannot read resolution status: %d", code)
	}
	outcome := result["closed_questions"].([]any)[0].(map[string]any)
	if _, exposed := outcome["answer"]; exposed || outcome["status"] != "answered" {
		t.Fatal("Main must see resolution status without the person's answer")
	}
}

func TestChildCannotCloseOwnPersonQuestion(t *testing.T) {
	f := newDelegFx(t)
	child, token := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	id, err := db.CreateAsk(t.Context(), f.pool, child, "Person decision", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(t.Context(), `UPDATE messages SET person_required=true,child_escalated=true WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	rec := privDo(f.api.HandlePostMessageExpire, "POST", "/x", id, token, `{}`)
	if rec.Code != 403 {
		t.Fatalf("own expire = %d %s", rec.Code, rec.Body.String())
	}
	row, _ := db.GetMessage(t.Context(), f.pool, id)
	if row.Status != "open" || !row.WaiterEnded || row.Answer != nil {
		t.Fatalf("self close resolved a person-only question: %+v", row)
	}
	var count int
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND client_event_id=$2 AND kind='update'`, child, messageEventID(id, "closed")).Scan(&count); err != nil || count != 1 {
		t.Fatalf("missing close audit: count=%d err=%v", count, err)
	}
	if err := f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_events WHERE session_id=$1 AND kind='user_message' AND payload->>'message_id'=$2`, child, id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("close recorded answer: count=%d err=%v", count, err)
	}
}
