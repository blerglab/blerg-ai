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
	"github.com/blerglab/blerg-ai/runner/internal/daemon"
	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// questionRoutes is the real private message API a host and the CLI talk to.
func questionRoutes(t *testing.T, f *delegFx) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/messages", f.api.HandlePostMessages)
	mux.HandleFunc("POST /api/messages/recover", f.api.HandleRecoverQuestions)
	mux.HandleFunc("GET /api/messages/{id}/answer", f.api.HandleGetMessageAnswer)
	mux.HandleFunc("POST /api/messages/{id}/reply", f.api.HandlePostMessageReply)
	mux.HandleFunc("POST /api/messages/{id}/expire", f.api.HandlePostMessageExpire)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type nativeAsk struct {
	f        *delegFx
	session  string
	id       string
	loop     *agent.Loop
	events   chan agent.Event
	provider *authoritativeProvider
	stop     context.CancelFunc
}

// startNativeAsk runs a real Loop with the real messenger until its ask is posted.
func startNativeAsk(t *testing.T, f *delegFx, session, question string) *nativeAsk {
	t.Helper()
	f.hub.SetSessionOwner(session, f.dc.ID)
	srv := questionRoutes(t, f)
	ctx, cancel := context.WithCancel(t.Context())
	events := make(chan agent.Event, 200)
	provider := &authoritativeProvider{question: question}
	var loop *agent.Loop
	messenger := daemon.NewLoopMessenger(daemon.LoopMessengerConfig{Context: ctx, BaseURL: srv.URL, SessionID: session, TokenSource: func() string { return delegDaemonToken }, OnAnswer: func(a agent.AskAnswer) { loop.AnswerAsk(a) }})
	loop = agent.NewLoop(agent.Config{Provider: provider, Emitter: questionChainEmitter{events}, Registry: agent.NewRegistry(), Messenger: messenger, Model: "fixture"})
	loop.InstallMessagingTools()
	done := make(chan struct{})
	go func() { defer close(done); loop.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	loop.Enqueue("start", "chat")
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Kind == "ask" {
				return &nativeAsk{f: f, session: session, id: ev.Payload.(agent.MessagingPayload).MessageID, loop: loop, events: events, provider: provider, stop: cancel}
			}
		case <-deadline:
			t.Fatal("ask never posted")
		}
	}
}

// toolResult waits for the ask's tool result, the text the model reads.
func (n *nativeAsk) toolResult(t *testing.T) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev := <-n.events:
			if ev.Kind == "tool_result" {
				return ev.Payload.(agent.ToolResultPayload).Output
			}
		case <-deadline:
			t.Fatal("ask never resolved")
		}
	}
}

func (n *nativeAsk) frames() []protocol.AgentUserMessage {
	var out []protocol.AgentUserMessage
	for _, raw := range drainFrames(n.f.dc) {
		var frame protocol.AgentUserMessage
		if json.Unmarshal(raw, &frame) == nil && frame.Type == "agent_user_message" && frame.SessionID == n.session {
			out = append(out, frame)
		}
	}
	return out
}

func browserSays(t *testing.T, f *delegFx, session, account, text, clientID, answers string) {
	t.Helper()
	handleBrowserAgentMessage(t.Context(), f.hub, &BrowserConn{ID: "b1", AccountID: account, send: make(chan []byte, 32)}, f.pool, protocol.AgentUserMessage{Type: "agent_user_message", SessionID: session, Text: text, ClientID: clientID, AnswerMessageID: answers})
}

// A session without a signed-in owner (a runner key started it) has nobody the
// answer could be reserved for: whoever is in its chat answers, on every path.
func TestOwnerlessSessionExplicitAnswerClosesItsAsk(t *testing.T) {
	cases := map[string]func(t *testing.T, n *nativeAsk){
		"chat card reply": func(t *testing.T, n *nativeAsk) {
			browserSays(t, n.f, n.session, "viewer-1", "Option A", newUUID(), n.id)
		},
	}
	for name, send := range cases {
		t.Run(name, func(t *testing.T) {
			f := newDelegFx(t)
			session, _ := f.session(sessOpts{})
			n := startNativeAsk(t, f, session, "Which option?")
			drainFrames(f.dc)
			send(t, n)
			if got := n.toolResult(t); !strings.Contains(got, "Option A") {
				t.Fatalf("tool result = %q", got)
			}
			row := mustChildMessage(t, f, n.id)
			if row.Status != "answered" || derefOrEmpty(row.Answer) != "Option A" {
				t.Fatalf("question = %+v", row)
			}
			if frames := n.frames(); len(frames) != 0 {
				t.Fatalf("the answer also queued as input: %+v", frames)
			}
			if pending, err := db.PendingSessionInputs(t.Context(), f.pool, session); err != nil || len(pending) != 0 {
				t.Fatalf("answer left pending input: %d %v", len(pending), err)
			}
		})
	}
}

// Claude's `blerg-runner ask` posts with the session token and polls /answer.
func TestOwnerlessSessionExplicitAnswerClosesCLIAsk(t *testing.T) {
	f := newDelegFx(t)
	session, token := f.session(sessOpts{})
	f.hub.SetSessionOwner(session, f.dc.ID)
	posted := privDo(f.api.HandlePostMessages, "POST", "/x", "", token, `{"kind":"ask","body":"Which option?"}`)
	var created struct {
		ID string `json:"id"`
	}
	if posted.Code != 201 || json.Unmarshal(posted.Body.Bytes(), &created) != nil {
		t.Fatalf("ask = %d %s", posted.Code, posted.Body.String())
	}
	drainFrames(f.dc)
	browserSays(t, f, session, "viewer-1", "Option A", newUUID(), created.ID)
	if row := mustChildMessage(t, f, created.ID); row.Status != "answered" || derefOrEmpty(row.Answer) != "Option A" {
		t.Fatalf("typed chat left the CLI ask open: %+v", row)
	}
	if frames := drainFrames(f.dc); len(frames) != 0 {
		t.Fatalf("answer also queued: %s", frames)
	}
}

// A worker with no owner still needs a person for its person-only questions.
func TestOwnerlessChildPersonQuestionReachesSignedInPerson(t *testing.T) {
	f := newDelegFx(t)
	parent, _ := f.session(sessOpts{})
	child, _ := f.session(sessOpts{parent: parent})
	f.hub.SetSessionOwner(child, f.dc.ID)
	id, err := db.CreateMessage(t.Context(), f.pool, child, "ask", "May I force push?", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(t.Context(), `UPDATE messages SET child_escalated=true WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if answerAskFromChat(t.Context(), f.hub, f.pool, child, "yes", newUUID(), "") {
		t.Fatal("an unnamed sender resolved a person-only question")
	}
	browserSays(t, f, child, "viewer-1", "yes", newUUID(), id)
	if row := mustChildMessage(t, f, id); row.Status != "answered" || !strings.HasPrefix(row.AnsweredBy, "person:") {
		t.Fatalf("question = %+v", row)
	}
}

// Input that was sent before the ask opened is ordinary input. Replay never answers.
func TestReplayNeverAnswersAQuestion(t *testing.T) {
	type sender struct {
		name, source, account string
	}
	for _, s := range []sender{{"owner", "chat", "owner-1"}, {"parent", "session", ""}} {
		t.Run(s.name, func(t *testing.T) {
			f := newDelegFx(t)
			session := f.parent
			if s.source == "session" {
				session, _ = f.session(sessOpts{owner: "owner-1", parent: f.parent})
			}
			f.hub.SetSessionOwner(session, f.dc.ID)
			f.dc.SetInputReceipts(true)
			text := "unrelated: also update the changelog"
			clientID := newUUID()
			if _, err := acceptSessionInput(t.Context(), f.hub, f.pool, protocol.AgentUserMessage{Type: "agent_user_message", SessionID: session, Text: text, Source: s.source, ClientID: clientID}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(5 * time.Millisecond)
			n := startNativeAsk(t, f, session, "Which option?")
			drainFrames(f.dc)
			replayPendingSessionInputs(t.Context(), f.hub, f.pool, f.dc, []string{session})
			row := mustChildMessage(t, f, n.id)
			if row.Status != "open" {
				t.Fatalf("earlier input became the answer: %+v", row)
			}
			frames := n.frames()
			if len(frames) != 1 || frames[0].Text != text {
				t.Fatalf("input not delivered once: %+v", frames)
			}
		})
	}
}

// Input typed after the ask opened still answers while the host is connected.
func TestInputAfterAskStaysOrdinaryUntilExplicitAnswer(t *testing.T) {
	f := newDelegFx(t)
	n := startNativeAsk(t, f, f.parent, "Which option?")
	f.dc.SetInputReceipts(true)
	drainFrames(f.dc)
	browserSays(t, f, f.parent, "owner-1", "Option A", newUUID(), "")
	if row := mustChildMessage(t, f, n.id); row.Status != "open" {
		t.Fatal("composer answered native ask")
	}
	replyAs(t, f, n.id, f.human("owner-1"), "Option A")
	if got := n.toolResult(t); got != "Option A" {
		t.Fatalf("top-level tool result = %q", got)
	}
}

// The engine matches its options against the raw answer, whoever gave it. Only
// a free-text answer to a child is labelled for the model.
func TestEngineReceivesRawAnswerForItsOptions(t *testing.T) {
	const question = "Which option?\n1. Option A\n2. Option B"
	for _, tc := range []struct {
		name string
		top  bool
		free bool
		want string
	}{
		{"top-level clarify", true, false, "Option A"},
		{"top-level free text", true, true, "Option A"},
		{"child clarify", false, false, "Option A"},
		{"child free text", false, true, "[Answered by the parent session.]\nOption A"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDelegFx(t)
			session := f.parent
			if !tc.top {
				session, _ = f.session(sessOpts{owner: "owner-1", parent: f.parent})
			}
			f.hub.SetSessionOwner(session, f.dc.ID)
			srv := questionRoutes(t, f)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.free {
				ctx = daemon.WithFreeTextQuestion(ctx)
			}
			events := make(chan agent.Event, 16)
			ask := daemon.NewQuestionHandler(srv.URL, func() string { return delegDaemonToken }, session, questionChainEmitter{events})
			got := make(chan string, 1)
			go func() {
				answer, err := ask(ctx, question)
				if err != nil {
					t.Error(err)
				}
				got <- answer
			}()
			var id string
			select {
			case ev := <-events:
				id = ev.Payload.(agent.MessagingPayload).MessageID
			case <-time.After(5 * time.Second):
				t.Fatal("question never posted")
			}
			if tc.top {
				if rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", id, f.human("owner-1"), `{"answer":"Option A"}`); rec.Code != 200 {
					t.Fatalf("answer = %d %s", rec.Code, rec.Body.String())
				}
			} else if code, _, _ := f.do("POST", childRoute(f.parent, session, "/questions/"+id+"/answer"), f.parentTok, map[string]any{"answer": "Option A"}, nil); code != 200 {
				t.Fatalf("parent answer = %d", code)
			}
			select {
			case answer := <-got:
				if answer != tc.want {
					t.Fatalf("engine read %q, want %q", answer, tc.want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("question never resolved")
			}
		})
	}
}

func openQuestionIDs(t *testing.T, f *delegFx, account string) map[string]protocol.MessageInfo {
	t.Helper()
	rows, err := db.ListMessages(t.Context(), f.pool, 200)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]protocol.MessageInfo{}
	for _, info := range messageInfosFor(t.Context(), f.pool, rows, account) {
		if info.Status == "open" {
			out[info.ID] = info
		}
	}
	return out
}

// The parent can interrupt its child. It cannot make the child's person-only
// question vanish, and it cannot drive past it.
func TestInterruptKeepsPersonQuestionWithThePerson(t *testing.T) {
	f := newDelegFx(t)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	n := startNativeAsk(t, f, child, "May I force push?")
	escalateNativeQuestionForPerson(t, f, child, n.id)
	drainFrames(f.dc)
	if code, _, _ := f.do("POST", childRoute(f.parent, child, "/interrupt"), f.parentTok, map[string]any{}, nil); code != 202 {
		t.Fatalf("parent interrupt = %d", code)
	}
	n.loop.Interrupt() // the daemon's handling of the interrupt frame
	deadline := time.Now().Add(5 * time.Second)
	for {
		row := mustChildMessage(t, f, n.id)
		if row.WaiterEnded {
			if row.Status != "open" || row.Answer != nil || row.AnsweredBy != "" || !row.ChildEscalated {
				t.Fatalf("interrupted question = %+v", row)
			}
			break
		}
		if row.Status != "open" || time.Now().After(deadline) {
			t.Fatalf("question not kept for the person: %+v", row)
		}
		time.Sleep(5 * time.Millisecond)
	}
	info, ok := openQuestionIDs(t, f, "owner-1")[n.id]
	if !ok || !info.Answerable || !info.WaiterEnded {
		t.Fatalf("person lost the question: %+v", info)
	}
	if code, _, _ := f.do("POST", childRoute(f.parent, child, "/questions/"+n.id+"/answer"), f.parentTok, map[string]any{"answer": "yes"}, nil); code != 403 {
		t.Fatalf("parent answered after interrupt: %d", code)
	}
	if code, _, _ := f.do("POST", childRoute(f.parent, child, "/message"), f.parentTok, map[string]string{"text": "force push anyway"}, nil); code != 409 {
		t.Fatalf("parent drove past the question: %d", code)
	}
	if ok, err := db.HasAgentEvent(t.Context(), f.pool, child, messageEventID(n.id, "closed")); err != nil || !ok {
		t.Fatalf("no visible notice in the child's chat: %t %v", ok, err)
	}
	drainFrames(f.dc)
	f.dc.SetInputReceipts(true)
	if rec := privDo(f.api.HandlePostMessageReply, "POST", "/x", n.id, f.human("owner-1"), `{"answer":"no, do not"}`); rec.Code != 200 {
		t.Fatalf("person answer = %d %s", rec.Code, rec.Body.String())
	}
	sent := time.Now().Add(5 * time.Second)
	for time.Now().Before(sent) {
		for _, frame := range n.frames() {
			if frame.Source == questionAnswerSource && strings.Contains(frame.Text, "no, do not") {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("the person's answer never reached the child")
}

// A plain child question has no such guard: closing it stays an ordinary close.
func TestInterruptClosesOrdinaryChildQuestion(t *testing.T) {
	f := newDelegFx(t)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	n := startNativeAsk(t, f, child, "Which option?")
	n.loop.Interrupt()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if row := mustChildMessage(t, f, n.id); row.Status != "open" {
			if row.AnsweredBy != "" || derefOrEmpty(row.Answer) != "" {
				t.Fatalf("close invented an answer: %+v", row)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("ordinary question left open")
}

func TestChildStatusListsNoWaitQuestions(t *testing.T) {
	f := newDelegFx(t)
	child, _ := f.session(sessOpts{owner: "owner-1", parent: f.parent})
	id, err := db.CreateQuestion(t.Context(), f.pool, child, "Review later?")
	if err != nil {
		t.Fatal(err)
	}
	code, body, _ := f.do("GET", childRoute(f.parent, child, ""), f.parentTok, nil, nil)
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	asks, _ := body["open_asks"].([]any)
	if len(asks) != 1 {
		t.Fatalf("open_asks = %+v", body["open_asks"])
	}
	ask := asks[0].(map[string]any)
	if ask["message_id"] != id || ask["no_wait"] != true {
		t.Fatalf("open ask = %+v", ask)
	}
}

// Chat cannot resolve a blocked native ask, so a deleted question must release it.
func TestDeletedQuestionReleasesBlockedNativeAsk(t *testing.T) {
	f := newDelegFx(t)
	n := startNativeAsk(t, f, f.parent, "Which option?")
	if _, err := f.pool.Exec(t.Context(), `DELETE FROM messages WHERE id=$1`, n.id); err != nil {
		t.Fatal(err)
	}
	f.api.waiters.signal(n.id)
	if got := n.toolResult(t); !strings.Contains(got, "no longer available") {
		t.Fatalf("tool result = %q", got)
	}
}

// An answer committed just before the interrupt's close is still delivered.
func TestCloseAskDeliversAnAnswerCommittedFirst(t *testing.T) {
	f := newDelegFx(t)
	srv := questionRoutes(t, f)
	id, err := db.CreateMessage(t.Context(), f.pool, f.parent, "ask", "Which option?")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.AnswerMessageAs(t.Context(), f.pool, id, "Option A", false, "person:owner-1"); err != nil {
		t.Fatal(err)
	}
	answers := make(chan agent.AskAnswer, 1)
	m := daemon.NewLoopMessenger(daemon.LoopMessengerConfig{Context: t.Context(), BaseURL: srv.URL, SessionID: f.parent, TokenSource: func() string { return delegDaemonToken }, OnAnswer: func(a agent.AskAnswer) { answers <- a }})
	m.(agent.AskCloser).CloseAsk(id, "")
	select {
	case a := <-answers:
		if a.Text != "Option A" || a.Outcome != "answered" {
			t.Fatalf("answer = %+v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("committed answer dropped by the close")
	}
}
