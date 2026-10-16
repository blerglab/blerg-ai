package daemon

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

func newLineDriver(t *testing.T) (*claudeCodeDriver, *collectEmitter, func(string)) {
	t.Helper()
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	d.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	d.steer.legacy = false
	p := &ccProc{}
	d.steer.proc = p
	return d, em, func(line string) { d.onLine(p, []byte(line)) }
}

func streamLine(parent, event string) string {
	pid := "null"
	if parent != "" {
		pid = `"` + parent + `"`
	}
	return `{"type":"stream_event","session_id":"cc-d","parent_tool_use_id":` + pid + `,"uuid":"u","event":` + event + `}`
}

// The deltas, a message's delta and its stop (nearly every stream_event line) are dropped before
// they are decoded; a thinking block's start and stop, and a message's start, are read. A long think
// and a long answer still give exactly one thinking_started / thinking_done pair.
func TestStreamEventDeltasAreDroppedUndecoded(t *testing.T) {
	var lines []string
	add := func(s string) { lines = append(lines, s) }
	add(`{"type":"system","subtype":"init","session_id":"cc-d"}`)
	add(streamLine("", `{"type":"message_start","message":{"model":"claude-opus-5","id":"msg_d","type":"message","role":"assistant","content":[]}}`))
	add(streamLine("", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`))
	var skipped []string
	for i := 0; i < 400; i++ {
		skipped = append(skipped, streamLine("", fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"step %d "}}`, i)))
	}
	skipped = append(skipped, streamLine("", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig"}}`))
	lines = append(lines, skipped...)
	add(`{"type":"assistant","parent_tool_use_id":null,"message":{"model":"claude-opus-5","id":"msg_d","content":[{"type":"thinking","thinking":"Plan it.","signature":"sig"}]}}`)
	add(streamLine("", `{"type":"content_block_stop","index":0}`))
	add(streamLine("", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`))
	var more []string
	for i := 0; i < 400; i++ {
		more = append(more, streamLine("", fmt.Sprintf(`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"word %d "}}`, i)))
	}
	more = append(more,
		streamLine("", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":900}}`),
		streamLine("", `{"type":"message_stop"}`))
	skipped = append(skipped, more...)
	add(`{"type":"assistant","parent_tool_use_id":null,"message":{"model":"claude-opus-5","id":"msg_d","content":[{"type":"text","text":"Done."}]}}`)
	add(streamLine("", `{"type":"content_block_stop","index":1}`))
	lines = append(lines, more...)
	add(`{"type":"result","subtype":"success","is_error":false,"result":"Done.","session_id":"cc-d"}`)

	for _, l := range skipped {
		if !ccSkipStreamLine([]byte(l)) {
			t.Fatalf("not dropped: %s", l)
		}
	}
	for _, l := range lines {
		if !slices.Contains(skipped, l) && ccSkipStreamLine([]byte(l)) {
			t.Fatalf("dropped a line the driver reads: %s", l)
		}
	}
	// An assistant line that only quotes the type is never dropped.
	if ccSkipStreamLine([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"a \"type\":\"stream_event\" line"}]}}`)) {
		t.Fatal("dropped an assistant line")
	}

	_, em, feed := newLineDriver(t)
	for _, l := range lines {
		feed(l)
	}
	var got []string
	for _, ev := range em.snapshot() {
		if ev.Kind != "status_changed" && ev.Kind != "capabilities" {
			got = append(got, describe(ev))
		}
	}
	want := []string{"thinking_started:", "assistant_thinking:Plan it.", "thinking_done:", "assistant_text:Done.", "turn_done:"}
	if !slices.Equal(got, want) {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A subagent that ends with a thinking block still open (its stop never came) leaves nothing behind.
func TestSubagentThinkingIsForgottenWhenItEnds(t *testing.T) {
	d, em, feed := newLineDriver(t)
	feed(`{"type":"system","subtype":"init","session_id":"cc-d"}`)
	feed(`{"type":"assistant","parent_tool_use_id":null,"message":{"content":[{"type":"tool_use","id":"toolu_a","name":"Agent","input":{"description":"Look","prompt":"Look around."}}]}}`)
	feed(streamLine("toolu_a", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`))
	d.sm.Lock()
	open := len(d.thinking)
	d.sm.Unlock()
	if open != 1 {
		t.Fatalf("open thinking blocks = %d, want 1", open)
	}
	feed(`{"type":"user","parent_tool_use_id":null,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_a","content":"seen","is_error":false}]}}`)
	d.sm.Lock()
	open = len(d.thinking)
	d.sm.Unlock()
	if open != 0 {
		t.Fatalf("open thinking blocks after the subagent ended = %d, want 0", open)
	}
	var started int
	for _, ev := range em.snapshot() {
		if ev.Kind == agent.KindThinkingStarted {
			started++
		}
	}
	if started != 1 {
		t.Fatalf("thinking_started = %d, want 1", started)
	}
}
