package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

// fakeProvider replays scripted responses, one per Stream call.
type fakeProvider struct {
	mu      sync.Mutex
	scripts [][]StreamEvent
	Calls   []Request
}

func (f *fakeProvider) Stream(ctx context.Context, req Request) (<-chan StreamEvent, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, req)
	var script []StreamEvent
	if len(f.scripts) > 0 {
		script = f.scripts[0]
		f.scripts = f.scripts[1:]
	} else {
		script = textResponse("(fake: script exhausted)")
	}
	f.mu.Unlock()

	ch := make(chan StreamEvent)
	go func() {
		defer close(ch)
		for _, ev := range script {
			select {
			case <-ctx.Done():
				return
			case ch <- ev:
			}
		}
	}()
	return ch, nil
}

func textResponse(text string) []StreamEvent {
	return []StreamEvent{
		{Kind: "text_delta", TextDelta: text},
		{Kind: "block", Block: &Block{Type: "text", Text: text}},
		{Kind: "done", StopReason: "end_turn", Usage: &Usage{InputTokens: 10, OutputTokens: 5}},
	}
}

func toolCallResponse(name, id, input string) []StreamEvent {
	return []StreamEvent{
		{Kind: "block", Block: &Block{Type: "tool_use", ID: id, Name: name, Input: json.RawMessage(input)}},
		{Kind: "done", StopReason: "tool_use", Usage: &Usage{InputTokens: 10, OutputTokens: 5}},
	}
}

func drain(t *testing.T, ch <-chan StreamEvent) []StreamEvent {
	t.Helper()
	var out []StreamEvent
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func TestFakeProviderReplaysScriptsInOrder(t *testing.T) {
	f := &fakeProvider{scripts: [][]StreamEvent{
		toolCallResponse("bash", "tu_1", `{"command":"ls"}`),
		textResponse("done"),
	}}
	ch, err := f.Stream(context.Background(), Request{Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	evs := drain(t, ch)
	if evs[0].Block == nil || evs[0].Block.Name != "bash" {
		t.Fatalf("first script: want bash tool_use, got %+v", evs[0])
	}
	ch2, _ := f.Stream(context.Background(), Request{})
	evs2 := drain(t, ch2)
	last := evs2[len(evs2)-1]
	if last.StopReason != "end_turn" {
		t.Fatalf("second script: want end_turn, got %+v", last)
	}
	if len(f.Calls) != 2 {
		t.Fatalf("want 2 recorded calls, got %d", len(f.Calls))
	}
}
