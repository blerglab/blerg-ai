package agent

import "testing"

func TestOrdinaryInputQueuesBehindAsk(t *testing.T) {
	for _, source := range []string{"session", "runner", "blerg-board", "human", "child_turn", "usage_limit", "review", "worker-question"} {
		t.Run(source, func(t *testing.T) {
			l := NewLoop(Config{})
			pending := make(chan AskAnswer, 1)
			l.askCh, l.askID = pending, "question"
			input := Input{Text: "ordinary", Source: source, ClientID: "client"}
			l.EnqueueInput(input)
			if l.askCh != pending || l.askID != "question" || len(pending) != 0 {
				t.Fatal("ordinary input resolved or replaced the pending ask")
			}
			if len(l.queue) != 1 || l.queue[0] != input {
				t.Fatalf("ordinary input lost its attribution: %+v", l.queue)
			}
			if !l.AnswerAsk(AskAnswer{QuestionID: "question", Text: "person answer"}) {
				t.Fatal("ordinary input prevented the authoritative answer")
			}
			if answer := <-pending; answer.Text != "person answer" {
				t.Fatalf("answer=%+v", answer)
			}
			if len(l.queue) != 1 || l.queue[0] != input {
				t.Fatal("authoritative answer consumed the queued input")
			}
		})
	}
}

func TestAskAnswerRequiresMatchingQuestion(t *testing.T) {
	l := NewLoop(Config{})
	if l.AnswerAsk(AskAnswer{QuestionID: "old", Text: "stale"}) {
		t.Fatal("accepted result without a waiter")
	}
	l.askCh = make(chan AskAnswer, 1)
	l.askID = "current"
	for _, id := range []string{"", "old"} {
		if l.AnswerAsk(AskAnswer{QuestionID: id, Text: "wrong"}) {
			t.Fatalf("accepted question %q", id)
		}
	}
	if !l.AnswerAsk(AskAnswer{QuestionID: "current", Text: "committed"}) {
		t.Fatal("matching result rejected")
	}
	if l.AnswerAsk(AskAnswer{QuestionID: "current", Text: "duplicate"}) {
		t.Fatal("duplicate replaced buffered result")
	}
	if got := <-l.askCh; got.Text != "committed" {
		t.Fatalf("result = %+v", got)
	}
	if len(l.queue) != 0 {
		t.Fatal("stale or duplicate result leaked into ordinary input")
	}
}

type immediateAnswerMessenger struct {
	fakeMessenger
	answer func()
}

func (m *immediateAnswerMessenger) SendAsk(string) (string, error) { m.answer(); return "current", nil }

// An immediate server poll may finish before SendAsk returns its ID.
func TestAskAnswerBeforePostReturnsMatchesOnlyItsQuestion(t *testing.T) {
	rec := &recorder{}
	m := &immediateAnswerMessenger{}
	l := NewLoop(Config{Provider: &fakeProvider{scripts: [][]StreamEvent{toolCallResponse("ask", "call", `{"question":"q?"}`), textResponse("done")}}, Emitter: rec, Registry: NewRegistry(), Messenger: m, Model: "fixture"})
	m.answer = func() {
		l.AnswerAsk(AskAnswer{QuestionID: "stale", Text: "wrong"})
		l.AnswerAsk(AskAnswer{QuestionID: "current", Text: "committed"})
	}
	l.installMessagingTools()
	ctx, _ := contextWithCancel(t)
	go l.Run(ctx)
	l.Enqueue("start", "chat")
	waitFor(t, rec, "turn_done")
	results := rec.byKind("tool_result")
	if len(results) != 1 || results[0].Payload.(ToolResultPayload).Output != "committed" {
		t.Fatalf("early answer = %+v", results)
	}
	if l.AnswerAsk(AskAnswer{QuestionID: "current", Text: "late"}) {
		t.Fatal("accepted stale answer after ask ended")
	}
}
