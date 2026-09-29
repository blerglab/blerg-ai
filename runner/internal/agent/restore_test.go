package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func rev(kind string, payload any) RestoredEvent {
	raw, _ := json.Marshal(payload)
	return RestoredEvent{Kind: kind, Payload: raw}
}

func TestRestoreContextRebuildsMessages(t *testing.T) {
	events := []RestoredEvent{
		rev("model_changed", ModelChangedPayload{Model: "m", Source: "start"}),
		rev("user_message", UserMessagePayload{Text: "do it", Source: "chat"}),
		rev("provider_blocks", ProviderBlocksPayload{Blocks: []Block{
			{Type: "thinking", Raw: json.RawMessage(`{"type":"thinking","thinking":"t","signature":"s"}`)},
			{Type: "tool_use", ID: "tu_1", Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)},
		}}),
		rev("tool_result", ToolResultPayload{CallID: "tu_1", Output: "file.txt"}),
		rev("provider_blocks", ProviderBlocksPayload{Blocks: []Block{{Type: "text", Text: "done"}}}),
		rev("turn_done", TurnDonePayload{StopReason: "end_turn", Model: "m"}),
	}
	l := NewLoop(Config{Provider: &fakeProvider{}, Emitter: &recorder{}, Registry: NewRegistry(), Model: "m"})
	l.RestoreContext(events)

	if len(l.messages) != 4 {
		t.Fatalf("messages = %d: %+v", len(l.messages), l.messages)
	}
	// user, assistant(thinking+tool_use), user(tool_result), assistant(text)
	if l.messages[0].Role != "user" || l.messages[0].Blocks[0].Text != "do it" {
		t.Fatalf("msg0 = %+v", l.messages[0])
	}
	if l.messages[1].Role != "assistant" || l.messages[1].Blocks[1].ID != "tu_1" {
		t.Fatalf("msg1 = %+v", l.messages[1])
	}
	if l.messages[2].Blocks[0].Type != "tool_result" || l.messages[2].Blocks[0].ToolUseID != "tu_1" {
		t.Fatalf("msg2 = %+v", l.messages[2])
	}
	// thinking block preserved verbatim
	if !strings.Contains(string(l.messages[1].Blocks[0].Raw), `"signature":"s"`) {
		t.Fatalf("thinking lost: %+v", l.messages[1].Blocks[0])
	}
}

func TestRestoreContextDiscardsInFlightTurn(t *testing.T) {
	events := []RestoredEvent{
		rev("user_message", UserMessagePayload{Text: "turn 1", Source: "chat"}),
		rev("provider_blocks", ProviderBlocksPayload{Blocks: []Block{{Type: "text", Text: "ok"}}}),
		rev("turn_done", TurnDonePayload{StopReason: "end_turn"}),
		// in-flight turn: no turn_done — must be discarded entirely
		rev("user_message", UserMessagePayload{Text: "turn 2", Source: "chat"}),
		rev("provider_blocks", ProviderBlocksPayload{Blocks: []Block{
			{Type: "tool_use", ID: "tu_9", Name: "bash", Input: json.RawMessage(`{}`)},
		}}),
	}
	l := NewLoop(Config{Provider: &fakeProvider{}, Emitter: &recorder{}, Registry: NewRegistry(), Model: "m"})
	l.RestoreContext(events)
	if len(l.messages) != 2 {
		t.Fatalf("in-flight turn not discarded: %+v", l.messages)
	}
	for _, m := range l.messages {
		for _, b := range m.Blocks {
			if b.ID == "tu_9" {
				t.Fatal("in-flight tool_use leaked into restored context")
			}
		}
	}
}

func TestRestoreContextSynthesizesMissingToolResults(t *testing.T) {
	// A turn that COMPLETED but whose tool_result event was lost (unacked at
	// crash): pairing must be repaired so the API accepts the context.
	events := []RestoredEvent{
		rev("user_message", UserMessagePayload{Text: "go", Source: "chat"}),
		rev("provider_blocks", ProviderBlocksPayload{Blocks: []Block{
			{Type: "tool_use", ID: "tu_lost", Name: "bash", Input: json.RawMessage(`{}`)},
		}}),
		// tool_result missing
		rev("provider_blocks", ProviderBlocksPayload{Blocks: []Block{{Type: "text", Text: "done"}}}),
		rev("turn_done", TurnDonePayload{StopReason: "end_turn"}),
	}
	l := NewLoop(Config{Provider: &fakeProvider{}, Emitter: &recorder{}, Registry: NewRegistry(), Model: "m"})
	l.RestoreContext(events)
	found := false
	for _, m := range l.messages {
		for _, b := range m.Blocks {
			if b.Type == "tool_result" && b.ToolUseID == "tu_lost" && b.IsError {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("missing synthesized tool_result: %+v", l.messages)
	}
}

func TestRestoreContextUsesCompactionBase(t *testing.T) {
	events := []RestoredEvent{
		rev("user_message", UserMessagePayload{Text: "old stuff", Source: "chat"}),
		rev("provider_blocks", ProviderBlocksPayload{Blocks: []Block{{Type: "text", Text: "old reply"}}}),
		rev("turn_done", TurnDonePayload{StopReason: "end_turn"}),
		rev("compaction", CompactionPayload{Summary: "SUMMARY OF OLD WORK", ThroughIndex: 2}),
		rev("user_message", UserMessagePayload{Text: "new turn", Source: "chat"}),
		rev("provider_blocks", ProviderBlocksPayload{Blocks: []Block{{Type: "text", Text: "new reply"}}}),
		rev("turn_done", TurnDonePayload{StopReason: "end_turn"}),
	}
	l := NewLoop(Config{Provider: &fakeProvider{}, Emitter: &recorder{}, Registry: NewRegistry(), Model: "m"})
	l.RestoreContext(events)
	if len(l.messages) == 0 || !strings.Contains(l.messages[0].Blocks[0].Text, "SUMMARY OF OLD WORK") {
		t.Fatalf("compaction base missing: %+v", l.messages)
	}
	for _, m := range l.messages {
		for _, b := range m.Blocks {
			if strings.Contains(b.Text, "old stuff") {
				t.Fatal("pre-compaction content leaked")
			}
		}
	}
	// restored model from model_changed absent → keeps config; but ensure new turn present
	last := l.messages[len(l.messages)-1]
	if last.Blocks[0].Text != "new reply" {
		t.Fatalf("last = %+v", last)
	}
}

func TestRestoreContextRestoresModel(t *testing.T) {
	events := []RestoredEvent{
		rev("model_changed", ModelChangedPayload{Model: "model-x", Effort: "high", Source: "command"}),
	}
	l := NewLoop(Config{Provider: &fakeProvider{}, Emitter: &recorder{}, Registry: NewRegistry(), Model: "default"})
	l.RestoreContext(events)
	if l.cfg.Model != "model-x" || l.cfg.Effort != "high" {
		t.Fatalf("model not restored: %s/%s", l.cfg.Model, l.cfg.Effort)
	}
}

func TestRestoreContextMidTurnCompaction(t *testing.T) {
	// Live order for a mid-turn compaction: provider_blocks(tool_use) →
	// compaction → tool_result. Replay must keep the tool_use/tool_result
	// pair intact (the old base-index logic orphaned the result → API 400s).
	events := []RestoredEvent{
		rev("user_message", UserMessagePayload{Text: "t1", Source: "chat"}),
		rev("provider_blocks", ProviderBlocksPayload{Blocks: []Block{{Type: "text", Text: "r1"}}}),
		rev("turn_done", TurnDonePayload{StopReason: "end_turn"}),
		rev("user_message", UserMessagePayload{Text: "t2", Source: "chat"}),
		rev("provider_blocks", ProviderBlocksPayload{Blocks: []Block{
			{Type: "tool_use", ID: "tu_mid", Name: "bash", Input: json.RawMessage(`{}`)},
		}}),
		// live: at this point messages = [u1, a1, u2, a2(tool_use)]; cut at 2
		rev("compaction", CompactionPayload{Summary: "OLD SUMMARIZED", ThroughIndex: 2}),
		rev("tool_result", ToolResultPayload{CallID: "tu_mid", Output: "out"}),
		rev("provider_blocks", ProviderBlocksPayload{Blocks: []Block{{Type: "text", Text: "done"}}}),
		rev("turn_done", TurnDonePayload{StopReason: "end_turn"}),
	}
	l := NewLoop(Config{Provider: &fakeProvider{}, Emitter: &recorder{}, Registry: NewRegistry(), Model: "m"})
	l.RestoreContext(events)

	// summary first; tool_use still present and paired with its result.
	if !strings.Contains(l.messages[0].Blocks[0].Text, "OLD SUMMARIZED") {
		t.Fatalf("first = %+v", l.messages[0])
	}
	var sawUse, sawResult bool
	for _, m := range l.messages {
		for _, b := range m.Blocks {
			if b.Type == "tool_use" && b.ID == "tu_mid" {
				sawUse = true
			}
			if b.Type == "tool_result" && b.ToolUseID == "tu_mid" {
				sawResult = true
			}
			if strings.Contains(b.Text, "t1") || strings.Contains(b.Text, "r1") {
				t.Fatal("compacted content leaked")
			}
		}
	}
	if !sawUse || !sawResult {
		t.Fatalf("pair broken: use=%v result=%v msgs=%+v", sawUse, sawResult, l.messages)
	}
}
