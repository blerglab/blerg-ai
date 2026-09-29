package agent

import (
	"testing"
)

func TestStartEmitsBootstrapModelChanged(t *testing.T) {
	rec := &recorder{}
	l := NewLoop(Config{Provider: &fakeProvider{}, Emitter: rec, Registry: NewRegistry(), Model: "claude-sonnet-5", Effort: "high"})
	l.Start()
	mc := rec.byKind("model_changed")
	if len(mc) != 1 {
		t.Fatalf("kinds = %v", rec.kinds())
	}
	p := mc[0].Payload.(ModelChangedPayload)
	if p.Model != "claude-sonnet-5" || p.Effort != "high" || p.Source != "start" {
		t.Fatalf("payload = %+v", p)
	}
}

// An invalid in-chat /model or /effort never becomes pending, so it never
// reaches model_changed (what the server persists and a resume reads back).
func TestSetModelRefusesInvalidValues(t *testing.T) {
	rec := &recorder{}
	l := NewLoop(Config{Provider: &fakeProvider{}, Emitter: rec, Registry: NewRegistry(), Model: "claude-sonnet-5"})
	for _, c := range [][2]string{{"Foo Bar", ""}, {"", "foo"}, {"--x", ""}, {"", "ultra"}} {
		l.SetModel(c[0], c[1], "command")
	}
	l.applyPendingModel(true)
	if len(rec.byKind("model_changed")) != 0 || len(rec.byKind("error")) != 4 {
		t.Fatalf("kinds = %v", rec.kinds())
	}
	l.SetModel("", "max", "command")
	l.applyPendingModel(true)
	if mc := rec.byKind("model_changed"); len(mc) != 1 || mc[0].Payload.(ModelChangedPayload).Effort != "max" {
		t.Fatalf("valid effort not applied: %v", rec.kinds())
	}
}

func TestSetModelAppliesAtNextProviderCall(t *testing.T) {
	rec := &recorder{}
	fp := &fakeProvider{scripts: [][]StreamEvent{
		toolCallResponse("echo", "tu_1", `{}`),
		textResponse("done"),
	}}
	var l *Loop
	// change model mid-turn from inside tool execution (deterministic; the
	// fake stream has no thinking blocks so the switch applies immediately)
	echo := funcTool{name: "echo", fn: func() { l.SetModel("model-b", "", "ui") }}
	l = NewLoop(Config{Provider: fp, Emitter: rec, Registry: NewRegistry(echo), Model: "model-a"})
	ctx, _ := contextWithCancel(t)
	go l.Run(ctx)
	l.Enqueue("go", "chat")
	waitFor(t, rec, "turn_done")
	if fp.Calls[len(fp.Calls)-1].Model != "model-b" {
		t.Fatalf("last call model = %q", fp.Calls[len(fp.Calls)-1].Model)
	}
	if len(rec.byKind("model_changed")) != 1 {
		t.Fatalf("kinds = %v", rec.kinds())
	}
}

func TestSetModelDeferredWhileThinkingInFlight(t *testing.T) {
	rec := &recorder{}
	fp := &fakeProvider{scripts: [][]StreamEvent{
		{ // response with a thinking block + tool call
			{Kind: "block", Block: &Block{Type: "thinking", Raw: mustJSON(`{"sig":"x"}`)}},
			{Kind: "block", Block: &Block{Type: "tool_use", ID: "tu_1", Name: "echo", Input: mustJSON(`{}`)}},
			{Kind: "done", StopReason: "tool_use", Usage: &Usage{}},
		},
		textResponse("done a"),
		textResponse("done b"), // second turn
	}}
	var l *Loop
	echo := funcTool{name: "echo", fn: func() { l.SetModel("model-b", "", "ui") }}
	l = NewLoop(Config{Provider: fp, Emitter: rec, Registry: NewRegistry(echo), Model: "model-a"})
	ctx, _ := contextWithCancel(t)
	go l.Run(ctx)
	l.Enqueue("turn1", "chat")
	waitFor(t, rec, "turn_done")
	// mid-turn call (call index 1) must STILL be model-a
	if fp.Calls[1].Model != "model-a" {
		t.Fatalf("mid-turn call switched models despite thinking block: %q", fp.Calls[1].Model)
	}
	l.Enqueue("turn2", "chat")
	waitForN(t, rec, "turn_done", 2)
	if fp.Calls[2].Model != "model-b" {
		t.Fatalf("next turn should use model-b, got %q", fp.Calls[2].Model)
	}
}
