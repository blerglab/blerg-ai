package agent

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCompactionTriggersAndReplacesPrefix(t *testing.T) {
	rec := &recorder{}
	echo := stubTool{name: "echo"}
	fp := &fakeProvider{scripts: [][]StreamEvent{
		// turn 1: tool call + finish — builds context
		toolCallResponse("echo", "tu_1", `{}`),
		textResponse("turn one done"),
		// turn 2 first call reports huge input usage → triggers compaction
		{
			{Kind: "block", Block: &Block{Type: "text", Text: "big"}},
			{Kind: "done", StopReason: "end_turn", Usage: &Usage{InputTokens: 900}},
		},
		// compaction summarizer call
		textResponse("SUMMARY OF EARLIER WORK"),
		// turn 3
		textResponse("turn three"),
	}}
	l := NewLoop(Config{Provider: fp, Emitter: rec, Registry: NewRegistry(echo), Model: "m",
		ContextWindowTokens: 1000, CompactAt: 0.8})
	ctx, _ := contextWithCancel(t)
	go l.Run(ctx)
	l.Enqueue("turn 1", "chat")
	waitFor(t, rec, "turn_done")
	l.Enqueue("turn 2", "chat")
	waitForN(t, rec, "turn_done", 2)
	l.Enqueue("turn 3", "chat")
	waitForN(t, rec, "turn_done", 3)

	comps := rec.byKind("compaction")
	if len(comps) != 1 {
		t.Fatalf("want 1 compaction, kinds = %v", rec.kinds())
	}
	// The context meter: turn 2 says it compacted (from the call that crossed the threshold) and
	// has no current reading; every turn carries the loop's own window and threshold.
	dones := rec.byKind("turn_done")
	ctx2 := dones[1].Payload.(TurnDonePayload).Context
	if ctx2 == nil || !ctx2.Compacted || ctx2.CompactedFrom != 900 || ctx2.Tokens != 0 || ctx2.Window != 1000 || ctx2.CompactAt != 800 {
		t.Fatalf("turn 2 context = %+v", ctx2)
	}
	if ctx1 := dones[0].Payload.(TurnDonePayload).Context; ctx1 != nil && ctx1.Compacted {
		t.Fatalf("turn 1 context = %+v", ctx1)
	}
	p := comps[0].Payload.(CompactionPayload)
	if !strings.Contains(p.Summary, "SUMMARY OF EARLIER WORK") {
		t.Fatalf("payload = %+v", p)
	}
	// turn-3 request must start with the summary message, not raw turn-1 blocks
	fp.mu.Lock()
	last := fp.Calls[len(fp.Calls)-1]
	fp.mu.Unlock()
	first := last.Messages[0]
	if first.Role != "user" || !strings.Contains(first.Blocks[0].Text, "SUMMARY OF EARLIER WORK") {
		t.Fatalf("context not replaced; first msg = %+v", first)
	}
	// no unpaired tool blocks may remain anywhere in the request
	for _, m := range last.Messages {
		for _, b := range m.Blocks {
			if b.Type == "tool_result" {
				found := false
				for _, m2 := range last.Messages {
					for _, b2 := range m2.Blocks {
						if b2.Type == "tool_use" && b2.ID == b.ToolUseID {
							found = true
						}
					}
				}
				if !found {
					t.Fatalf("orphaned tool_result %s after compaction", b.ToolUseID)
				}
			}
		}
	}
}

// A turn that compacted, then failed on its next call, still says it compacted (the meter must not
// keep the count from before the compaction; #191 review).
func TestCompactionThenProviderFailureSaysCompacted(t *testing.T) {
	rec := &recorder{}
	echo := stubTool{name: "echo"}
	fp := &fakeProvider{scripts: [][]StreamEvent{
		toolCallResponse("echo", "tu_1", `{}`),
		textResponse("turn one done"),
		{
			{Kind: "block", Block: &Block{Type: "tool_use", ID: "tu_2", Name: "echo", Input: []byte(`{}`)}},
			{Kind: "done", StopReason: "tool_use", Usage: &Usage{InputTokens: 900}},
		},
		textResponse("SUMMARY OF EARLIER WORK"),
		{{Kind: "error", Err: errors.New("provider down")}},
	}}
	l := NewLoop(Config{Provider: fp, Emitter: rec, Registry: NewRegistry(echo), Model: "m",
		ContextWindowTokens: 1000, CompactAt: 0.8, RetryBase: time.Millisecond})
	ctx, _ := contextWithCancel(t)
	go l.Run(ctx)
	l.Enqueue("turn 1", "chat")
	waitFor(t, rec, "turn_done")
	l.Enqueue("turn 2", "chat")
	waitForN(t, rec, "turn_done", 2)
	if len(rec.byKind("compaction")) != 1 {
		t.Fatalf("want 1 compaction, kinds = %v", rec.kinds())
	}
	done := rec.byKind("turn_done")[1].Payload.(TurnDonePayload)
	if done.StopReason != "error" || done.Context == nil || *done.Context != (ContextUsage{Window: 1000, CompactAt: 800, Compacted: true, CompactedFrom: 900}) {
		t.Fatalf("failed turn: stop=%s context=%+v", done.StopReason, done.Context)
	}
}
