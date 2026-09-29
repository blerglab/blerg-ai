package agent

// Regression tests for the adversarial-review findings on the phase-1a
// implementation (ask/Enqueue race, child error hang, completion re-arm,
// empty stop reason).

import (
	"context"
	"strings"
	"testing"
)

// Finding 1: a second Enqueue during a blocked ask must never be lost or
// block the transport goroutine — it becomes the next queued turn.
func TestRapidEnqueuesDuringBlockedAskAreNotLost(t *testing.T) {
	rec := &recorder{}
	fm := &fakeMessenger{}
	fp := &fakeProvider{scripts: [][]StreamEvent{
		toolCallResponse("ask", "tu_1", `{"question":"q?"}`),
		textResponse("turn one done"),
		textResponse("turn two done"),
	}}
	l := NewLoop(Config{Provider: fp, Emitter: rec, Registry: NewRegistry(), Model: "m", Messenger: fm})
	l.installMessagingTools()
	ctx, _ := contextWithCancel(t)
	go l.Run(ctx)
	l.Enqueue("start", "chat")
	waitForStatus(t, rec, "waiting")
	l.Enqueue("the answer", "chat")     // fills the ask buffer
	l.Enqueue("follow-up task", "chat") // must fall through to the queue, not block or vanish
	waitFor(t, rec, "turn_done")        // returns at all = the transport goroutine never blocked

	var sawFollowUp bool
	for _, ev := range rec.byKind("user_message") {
		if ev.Payload.(UserMessagePayload).Text == "follow-up task" {
			sawFollowUp = true
		}
	}
	if !sawFollowUp {
		t.Fatalf("follow-up message lost; kinds %v", rec.kinds())
	}
}

// Finding 3: a child that dies on a provider error must not hang the parent.
func TestSubagentErrorPropagatesInsteadOfHanging(t *testing.T) {
	rec := &recorder{}
	fp := &fakeProvider{scripts: [][]StreamEvent{
		toolCallResponse("agent", "tu_1", `{"prompt":"doomed"}`),
		textResponse("recovered"),
	}}
	l := NewLoop(Config{Provider: fp, Emitter: rec, Registry: NewRegistry(), Model: "m"})
	spawn := func(ctx context.Context, childID string, cfg Config, prompt string) (string, error) {
		cfg.Provider = errProvider{} // child always fails non-retryably
		return DefaultSpawn(ctx, childID, cfg, prompt)
	}
	l.cfg.Registry = NewRegistry(AgentTool(l, nil, spawn))
	ctx, _ := contextWithCancel(t)
	go l.Run(ctx)
	l.Enqueue("go", "chat")
	waitFor(t, rec, "turn_done") // would time out before the fix

	var errResult bool
	for _, ev := range rec.byKind("tool_result") {
		tr := ev.Payload.(ToolResultPayload)
		if tr.CallID == "tu_1" && tr.IsError && strings.Contains(tr.Output, "subagent error") {
			errResult = true
		}
	}
	if !errResult {
		t.Fatalf("child error not surfaced as tool result; kinds %v", rec.kinds())
	}
}

// Finding 7: child lifecycle events must not pollute the parent transcript.
func TestSubagentEventsDoNotPolluteParentStream(t *testing.T) {
	rec := &recorder{}
	childProvider := &fakeProvider{scripts: [][]StreamEvent{textResponse("child result")}}
	fp := &fakeProvider{scripts: [][]StreamEvent{
		toolCallResponse("agent", "tu_1", `{"prompt":"sub"}`),
		textResponse("parent done"),
	}}
	l := NewLoop(Config{Provider: fp, Emitter: rec, Registry: NewRegistry(), Model: "m"})
	l.cfg.Registry = NewRegistry(AgentTool(l, nil, func(ctx context.Context, childID string, cfg Config, prompt string) (string, error) {
		cfg.Provider = childProvider
		return DefaultSpawn(ctx, childID, cfg, prompt)
	}))
	ctx, _ := contextWithCancel(t)
	go l.Run(ctx)
	l.Enqueue("go", "chat")
	waitFor(t, rec, "turn_done")

	// Exactly ONE turn_done (the parent's) and one user_message may appear.
	if n := len(rec.byKind("turn_done")); n != 1 {
		t.Fatalf("child turn_done leaked into parent stream: %d", n)
	}
	if n := len(rec.byKind("user_message")); n != 1 {
		t.Fatalf("child user_message leaked into parent stream: %d", n)
	}
}

// Finding 6: subagent spend must count against the parent session budget.
func TestSubagentSpendFoldsIntoParentBudget(t *testing.T) {
	rec := &recorder{}
	childProvider := &fakeProvider{scripts: [][]StreamEvent{textResponse("child result")}}
	fp := &fakeProvider{scripts: [][]StreamEvent{
		toolCallResponse("agent", "tu_1", `{"prompt":"sub","model":"child-model"}`),
		textResponse("parent done"),
	}}
	pricing := map[string]Price{
		"m":           {InputPerM: 3, OutputPerM: 15},
		"child-model": {InputPerM: 1000000, OutputPerM: 1000000}, // $1/token: unmistakable
	}
	l := NewLoop(Config{Provider: fp, Emitter: rec, Registry: NewRegistry(), Model: "m", Pricing: pricing, BudgetUSD: 1000})
	l.cfg.Registry = NewRegistry(AgentTool(l, nil, func(ctx context.Context, childID string, cfg Config, prompt string) (string, error) {
		cfg.Provider = childProvider
		return DefaultSpawn(ctx, childID, cfg, prompt)
	}))
	ctx, _ := contextWithCancel(t)
	go l.Run(ctx)
	l.Enqueue("go", "chat")
	waitFor(t, rec, "turn_done")
	// child used 10 in + 5 out tokens at $1/token = $15
	if spent := l.SpentUSD(); spent < 14 {
		t.Fatalf("child spend not folded into parent: spent=%f", spent)
	}
}

// Finding 9: mutating again after a completion check-in re-arms the
// completion requirement.
func TestCompletionRearmedByLaterMutation(t *testing.T) {
	rec := &recorder{}
	mut := stubTool{name: "write_file", mutating: true}
	fp := startLoop(t, rec, NewRegistry(mut),
		[]StreamEvent{ // check_in task_start + completion + write, then MORE mutation
			{Kind: "block", Block: &Block{Type: "tool_use", ID: "c1", Name: "check_in", Input: mustJSON(`{"phase":"task_start","summary":"s"}`)}},
			{Kind: "block", Block: &Block{Type: "tool_use", ID: "c2", Name: "check_in", Input: mustJSON(`{"phase":"completion","summary":"done"}`)}},
			{Kind: "done", StopReason: "tool_use", Usage: &Usage{}},
		},
		[]StreamEvent{ // now mutate AFTER the completion check-in
			{Kind: "block", Block: &Block{Type: "tool_use", ID: "w1", Name: "write_file", Input: mustJSON(`{}`)}},
			{Kind: "done", StopReason: "tool_use", Usage: &Usage{}},
		},
		textResponse("stopping without new completion"), // reminder fires
		textResponse("still no check-in"),               // gives up
	)
	td := waitFor(t, rec, "turn_done").Payload.(TurnDonePayload)
	if !td.CheckInMissing {
		t.Fatalf("completion latch not re-armed: %+v", td)
	}
	if len(fp.Calls) != 4 {
		t.Fatalf("want 4 provider calls (incl. one reminder), got %d", len(fp.Calls))
	}
}

// Finding 8: a stream that closes without a done event must not poison the
// context — the turn errors out cleanly.
func TestEmptyStopReasonIsAnError(t *testing.T) {
	rec := &recorder{}
	fp := &fakeProvider{scripts: [][]StreamEvent{
		{{Kind: "block", Block: &Block{Type: "tool_use", ID: "tu_1", Name: "bash", Input: mustJSON(`{}`)}}}, // no done event
	}}
	l := NewLoop(Config{Provider: fp, Emitter: rec, Registry: NewRegistry(), Model: "m"})
	ctx, _ := contextWithCancel(t)
	go l.Run(ctx)
	l.Enqueue("go", "chat")
	waitFor(t, rec, "error")
	waitForStatus(t, rec, "idle")
	// context must not contain an assistant message with unpaired tool_use
	for _, m := range l.messages {
		for _, b := range m.Blocks {
			if b.Type == "tool_use" {
				t.Fatal("unpaired tool_use left in context after stream failure")
			}
		}
	}
}
