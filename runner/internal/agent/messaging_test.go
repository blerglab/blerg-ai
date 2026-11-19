package agent

import (
	"strings"
	"sync"
	"testing"
)

type fakeMessenger struct {
	mu      sync.Mutex
	updates []string
	notes   []string
	asks    []string
	closed  []string // CloseAsk calls, "id=answer"
}

func (m *fakeMessenger) CloseAsk(id, answer string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = append(m.closed, id+"="+answer)
}

func (m *fakeMessenger) SendUpdate(b string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updates = append(m.updates, b)
	return nil
}

func (m *fakeMessenger) SendNote(b string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.notes = append(m.notes, b)
	return nil
}

func (m *fakeMessenger) SendAsk(b string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.asks = append(m.asks, b)
	return "msg-1", nil
}

func TestUpdateToolEmitsAndDelivers(t *testing.T) {
	rec := &recorder{}
	fm := &fakeMessenger{}
	fp := &fakeProvider{scripts: [][]StreamEvent{
		toolCallResponse("update", "tu_1", `{"body":"halfway"}`),
		textResponse("ok"),
	}}
	l := NewLoop(Config{Provider: fp, Emitter: rec, Registry: NewRegistry(), Model: "m", Messenger: fm})
	l.installMessagingTools()
	ctx, _ := contextWithCancel(t)
	go l.Run(ctx)
	l.Enqueue("go", "chat")
	waitFor(t, rec, "turn_done")
	if len(rec.byKind("update")) != 1 {
		t.Fatalf("kinds = %v", rec.kinds())
	}
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if len(fm.updates) != 1 || fm.updates[0] != "halfway" {
		t.Fatalf("messenger updates = %v", fm.updates)
	}
}

func TestAskBlocksUntilAnswerAndStatusWaits(t *testing.T) {
	rec := &recorder{}
	fm := &fakeMessenger{}
	fp := &fakeProvider{scripts: [][]StreamEvent{
		toolCallResponse("ask", "tu_1", `{"question":"blue or green?"}`),
		textResponse("chose blue"),
	}}
	l := NewLoop(Config{Provider: fp, Emitter: rec, Registry: NewRegistry(), Model: "m", Messenger: fm})
	l.installMessagingTools()
	ctx, _ := contextWithCancel(t)
	go l.Run(ctx)
	l.Enqueue("start", "chat")

	waitForStatus(t, rec, "waiting")
	// Only the committed, matching answer frame resolves the ask.
	l.AnswerAsk(AskAnswer{QuestionID: "msg-1", Text: "blue", AnsweredBy: "person:owner"})
	waitFor(t, rec, "turn_done")

	// the answer must appear as the ask's tool_result
	for _, ev := range rec.byKind("tool_result") {
		tr := ev.Payload.(ToolResultPayload)
		if tr.CallID == "tu_1" && tr.Output == "blue" {
			return
		}
	}
	t.Fatalf("ask result not answered: %v", rec.kinds())
}

func TestSecondConcurrentAskErrors(t *testing.T) {
	rec := &recorder{}
	fm := &fakeMessenger{}
	fp := &fakeProvider{scripts: [][]StreamEvent{
		{
			{Kind: "block", Block: &Block{Type: "tool_use", ID: "a1", Name: "ask", Input: mustJSON(`{"question":"q1"}`)}},
			{Kind: "block", Block: &Block{Type: "tool_use", ID: "a2", Name: "ask", Input: mustJSON(`{"question":"q2"}`)}},
			{Kind: "done", StopReason: "tool_use", Usage: &Usage{}},
		},
		textResponse("ok"),
	}}
	l := NewLoop(Config{Provider: fp, Emitter: rec, Registry: NewRegistry(), Model: "m", Messenger: fm})
	l.installMessagingTools()
	ctx, _ := contextWithCancel(t)
	go l.Run(ctx)
	l.Enqueue("start", "chat")
	waitForStatus(t, rec, "waiting")
	l.AnswerAsk(AskAnswer{QuestionID: "msg-1", Text: "answer1", AnsweredBy: "person:owner"})
	waitFor(t, rec, "turn_done")

	errCount := 0
	for _, ev := range rec.byKind("tool_result") {
		if ev.Payload.(ToolResultPayload).IsError {
			errCount++
		}
	}
	if errCount != 1 {
		t.Fatalf("want exactly 1 errored ask, got %d (kinds %v)", errCount, rec.kinds())
	}
}

// An ask's answer never starts a turn, so it is recorded where it is taken: a user_message with
// source "ask_answer" and the ask's message id (and the client id it was sent with). The ask
// event carries the same message id. Typed in the chat, the answer also closes the ask's message;
// delivered from the ask itself (source ask_answer), it does not.
func TestAskAnswerIsRecordedAsTheAnswer(t *testing.T) {
	for _, c := range []struct {
		source     string
		wantClosed bool
		answeredBy string
	}{{"person:owner", false, ""}, {"session:parent", false, "session"}} {
		rec := &recorder{}
		fm := &fakeMessenger{}
		fp := &fakeProvider{scripts: [][]StreamEvent{
			toolCallResponse("ask", "tu_1", `{"question":"blue or green?"}`),
			textResponse("chose blue"),
		}}
		l := NewLoop(Config{Provider: fp, Emitter: rec, Registry: NewRegistry(), Model: "m", Messenger: fm})
		l.installMessagingTools()
		ctx, _ := contextWithCancel(t)
		go l.Run(ctx)
		l.Enqueue("start", "chat")
		waitForStatus(t, rec, "waiting")
		l.AnswerAsk(AskAnswer{QuestionID: "msg-1", Text: "blue", AnsweredBy: c.source, ClientID: "cl-9"})
		waitFor(t, rec, "turn_done")

		asks := rec.byKind("ask")
		if len(asks) != 1 || asks[0].Payload.(MessagingPayload).MessageID != "msg-1" {
			t.Fatalf("%s: ask events = %+v, want one with message_id msg-1", c.source, asks)
		}
		var answers []UserMessagePayload
		for _, ev := range rec.byKind("user_message") {
			if p := ev.Payload.(UserMessagePayload); p.Text == "blue" {
				answers = append(answers, p)
			}
		}
		// A parent's answer (source "session") is recorded as the parent's.
		want := UserMessagePayload{Text: "blue", Source: "ask_answer", ClientID: "cl-9", MessageID: "msg-1", AnsweredBy: c.answeredBy}
		if len(answers) != 1 || answers[0] != want {
			t.Fatalf("%s: answer events = %+v, want exactly %+v", c.source, answers, want)
		}
		fm.mu.Lock()
		closed := fm.closed
		fm.mu.Unlock()
		if got := len(closed) == 1 && closed[0] == "msg-1=blue"; got != c.wantClosed {
			t.Fatalf("%s: CloseAsk calls = %v, want closed=%v", c.source, closed, c.wantClosed)
		}
	}
}

// Parent frames can race escalation in transit. Only the server's committed ask
// answer may satisfy an ask; ordinary parent input must remain queued.
func TestParentInputQueuesBehindNativeAsk(t *testing.T) {
	rec := &recorder{}
	fm := &fakeMessenger{}
	fp := &fakeProvider{scripts: [][]StreamEvent{toolCallResponse("ask", "tu_1", `{"question":"Which branch?"}`), textResponse("done"), textResponse("queued message read")}}
	l := NewLoop(Config{Provider: fp, Emitter: rec, Registry: NewRegistry(), Model: "m", Messenger: fm})
	l.installMessagingTools()
	ctx, _ := contextWithCancel(t)
	go l.Run(ctx)
	l.Enqueue("start", "chat")
	waitForStatus(t, rec, "waiting")
	l.Enqueue("parent input", "session")
	l.AnswerAsk(AskAnswer{QuestionID: "msg-1", Text: "person answer", AnsweredBy: "person:owner"})
	waitFor(t, rec, "turn_done")
	found := false
	for _, ev := range rec.byKind("tool_result") {
		p := ev.Payload.(ToolResultPayload)
		if p.CallID == "tu_1" {
			found = true
			if p.Output != "person answer" {
				t.Errorf("native ask consumed parent input: %q", p.Output)
			}
		}
	}
	if !found {
		t.Fatal("no ask result")
	}
	queued := false
	for _, ev := range rec.byKind("user_message") {
		p := ev.Payload.(UserMessagePayload)
		if p.Text == "parent input" && p.Source == "session" {
			queued = true
		}
	}
	if !queued {
		t.Error("parent input was lost instead of queued after the ask")
	}
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if len(fm.closed) > 0 {
		t.Errorf("parent frame closed ask: %v", fm.closed)
	}
}

// An answer the server committed as the ask was interrupted reaches the model.
func TestCommittedAnswerRacingInterruptIsKept(t *testing.T) {
	l := &Loop{cfg: Config{Emitter: &recorder{}}, wake: make(chan struct{}, 1)}
	ch := make(chan AskAnswer, 1)
	ch <- AskAnswer{QuestionID: "q", Text: "blue", Outcome: "answered"}
	l.askCh, l.askID = ch, "q"
	l.endInterruptedAsk("q", ch)
	if len(l.queue) != 1 || !strings.Contains(l.queue[0].Text, "blue") || l.queue[0].Source != "question_answer" {
		t.Fatalf("queued answer lost: %+v", l.queue)
	}
	ch = make(chan AskAnswer, 1)
	l.askCh, l.askID = ch, "r"
	l.endInterruptedAsk("r", ch)
	if !l.AnswerAsk(AskAnswer{QuestionID: "r", Text: "green", Label: "[Answered by the person.]\n", Outcome: "answered"}) {
		t.Fatal("late answer refused")
	}
	if len(l.queue) != 2 || !strings.HasPrefix(l.queue[1].Text, "[Answered by the person.]\n") {
		t.Fatalf("late answer lost: %+v", l.queue)
	}
	if l.AnswerAsk(AskAnswer{QuestionID: "r", Text: "again"}) || len(l.queue) != 2 {
		t.Fatal("a duplicate late answer queued twice")
	}
}

func TestInputQueueIsBoundedWhileAnAskWaits(t *testing.T) {
	l := &Loop{cfg: Config{Emitter: &recorder{}}, wake: make(chan struct{}, 1), askCh: make(chan AskAnswer, 1)}
	for i := 0; i < 200; i++ {
		l.EnqueueInput(Input{Text: "more"})
	}
	if len(l.queue) != 64 {
		t.Fatalf("queue = %d", len(l.queue))
	}
}
