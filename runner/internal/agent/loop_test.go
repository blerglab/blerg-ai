package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// startLoop runs a loop over the scripts, enqueues one message, and waits for
// turn_done (or times out).
func startLoop(t *testing.T, rec *recorder, reg *Registry, scripts ...[]StreamEvent) *fakeProvider {
	t.Helper()
	fp := &fakeProvider{scripts: scripts}
	l := NewLoop(Config{Provider: fp, Emitter: rec, Registry: reg, Model: "claude-sonnet-5", System: "test"})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go l.Run(ctx)
	l.Enqueue("do the thing", "chat")
	waitFor(t, rec, "turn_done")
	return fp
}

func waitFor(t *testing.T, rec *recorder, kind string) Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		evs := rec.byKind(kind)
		if len(evs) > 0 {
			return evs[len(evs)-1]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; got kinds %v", kind, rec.kinds())
	return Event{}
}

func contextWithCancel(t *testing.T) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx, cancel
}

func mustJSON(s string) json.RawMessage { return json.RawMessage(s) }

func waitForStatus(t *testing.T, rec *recorder, status string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range rec.byKind("status_changed") {
			if ev.Payload.(StatusPayload).Status == status {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no status %q; kinds %v", status, rec.kinds())
}

func waitForN(t *testing.T, rec *recorder, kind string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(rec.byKind(kind)) >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d× %s; kinds %v", n, kind, rec.kinds())
}

func TestSimpleTextTurn(t *testing.T) {
	rec := &recorder{}
	startLoop(t, rec, NewRegistry(), textResponse("hello"))
	kinds := rec.kinds()
	want := []string{"user_message", "status_changed", "assistant_text", "assistant_text", "provider_blocks", "turn_done", "status_changed"}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds[%d] = %s, want %s (all: %v)", i, kinds[i], want[i], kinds)
		}
	}
	td := rec.byKind("turn_done")[0].Payload.(TurnDonePayload)
	if td.StopReason != "end_turn" || td.Model != "claude-sonnet-5" {
		t.Fatalf("turn_done = %+v", td)
	}
	status := rec.byKind("status_changed")
	if status[0].Payload.(StatusPayload).Status != "running" || status[1].Payload.(StatusPayload).Status != "idle" {
		t.Fatalf("status events = %+v", status)
	}
}

func TestToolCallTurn(t *testing.T) {
	rec := &recorder{}
	echo := stubTool{name: "echo"}
	fp := startLoop(t, rec, NewRegistry(echo),
		toolCallResponse("echo", "tu_1", `{}`),
		textResponse("done"))
	if len(rec.byKind("tool_call")) != 1 || len(rec.byKind("tool_result")) != 1 {
		t.Fatalf("kinds = %v", rec.kinds())
	}
	// second provider call must carry the tool_result paired to tu_1
	second := fp.Calls[1]
	lastMsg := second.Messages[len(second.Messages)-1]
	if lastMsg.Role != "user" || lastMsg.Blocks[0].Type != "tool_result" || lastMsg.Blocks[0].ToolUseID != "tu_1" {
		t.Fatalf("tool_result not paired: %+v", lastMsg)
	}
}

func TestUnknownToolReturnsErrorResult(t *testing.T) {
	rec := &recorder{}
	startLoop(t, rec, NewRegistry(),
		toolCallResponse("nope", "tu_1", `{}`),
		textResponse("ok"))
	tr := rec.byKind("tool_result")[0].Payload.(ToolResultPayload)
	if !tr.IsError {
		t.Fatalf("want error result for unknown tool, got %+v", tr)
	}
}

func TestMutationRejectedWithoutCheckIn(t *testing.T) {
	rec := &recorder{}
	mut := stubTool{name: "write_file", mutating: true}
	startLoop(t, rec, NewRegistry(mut),
		toolCallResponse("write_file", "tu_1", `{}`),
		textResponse("ok"))
	tr := rec.byKind("tool_result")[0].Payload.(ToolResultPayload)
	if !tr.IsError {
		t.Fatalf("want gate rejection, got %+v", tr)
	}
}

func TestCompletionReminderInjectedOnce(t *testing.T) {
	rec := &recorder{}
	mut := stubTool{name: "write_file", mutating: true}
	// Script: check_in+write in one message → ends without completion → reminder
	// → model STILL ends without completion → turn_done with CheckInMissing.
	fp := startLoop(t, rec, NewRegistry(mut),
		[]StreamEvent{
			{Kind: "block", Block: &Block{Type: "tool_use", ID: "c1", Name: "check_in", Input: json.RawMessage(`{"phase":"task_start","summary":"s"}`)}},
			{Kind: "block", Block: &Block{Type: "tool_use", ID: "w1", Name: "write_file", Input: json.RawMessage(`{}`)}},
			{Kind: "done", StopReason: "tool_use", Usage: &Usage{}},
		},
		textResponse("did it"),   // no completion check-in → reminder
		textResponse("whatever"), // still none → give up
	)
	td := waitFor(t, rec, "turn_done").Payload.(TurnDonePayload)
	if !td.CheckInMissing {
		t.Fatalf("want CheckInMissing, got %+v", td)
	}
	if len(fp.Calls) != 3 {
		t.Fatalf("want exactly one reminder round, got %d calls", len(fp.Calls))
	}
}

// funcTool runs an arbitrary func — test helper for mid-turn behaviors.
type funcTool struct {
	name string
	fn   func()
}

func (f funcTool) Def() ToolDef   { return ToolDef{Name: f.name, InputSchema: json.RawMessage(`{}`)} }
func (f funcTool) Mutating() bool { return false }
func (f funcTool) Execute(context.Context, json.RawMessage) (string, error) {
	f.fn()
	return "ok", nil
}

func TestQueuedMessageInjectedNextProviderCall(t *testing.T) {
	rec := &recorder{}
	fp := &fakeProvider{scripts: [][]StreamEvent{
		toolCallResponse("echo", "tu_1", `{}`),
		textResponse("done"),
	}}
	var l *Loop
	// Enqueue from inside tool execution — guaranteed mid-turn.
	echo := funcTool{name: "echo", fn: func() { l.Enqueue("second thought", "chat") }}
	l = NewLoop(Config{Provider: fp, Emitter: rec, Registry: NewRegistry(echo), Model: "m"})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go l.Run(ctx)
	l.Enqueue("first", "chat")
	waitFor(t, rec, "turn_done")
	second := fp.Calls[1]
	last := second.Messages[len(second.Messages)-1]
	found := false
	for _, b := range last.Blocks {
		if b.Type == "text" && b.Text == "second thought" {
			found = true
		}
	}
	if !found {
		t.Fatalf("queued message not injected: %+v", last)
	}
}
