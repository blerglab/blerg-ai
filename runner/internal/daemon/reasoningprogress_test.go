package daemon

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
)

// reasoningCounts is every thinking_progress count emitted for a scope ("" = the main agent), in
// order; each must be transient.
func reasoningCounts(t *testing.T, em *collectEmitter, scope string) []int {
	t.Helper()
	var out []int
	for _, ev := range em.snapshot() {
		if ev.Kind != agent.KindThinkingProgress {
			continue
		}
		if !agent.IsTransient(ev) {
			t.Fatal("reasoning progress must stay transient")
		}
		raw, _ := json.Marshal(ev.Payload)
		var p struct {
			Tokens *int   `json:"tokens"`
			Parent string `json:"parent_call_id"`
		}
		if json.Unmarshal(raw, &p) == nil && p.Tokens != nil && p.Parent == scope {
			out = append(out, *p.Tokens)
		}
	}
	return out
}

func unthrottled(t *testing.T) {
	old := reasoningProgressEvery
	reasoningProgressEvery = 0
	t.Cleanup(func() { reasoningProgressEvery = old })
}

// Claude's message_delta carries the whole message's output_tokens and, on a real stream, arrives
// after the thinking block's content_block_stop: it is never a reasoning count (elapsed clock).
func TestClaudeNeverReportsMessageOutputAsReasoning(t *testing.T) {
	_, em, line := newLineDriver(t)
	// The real order (testdata/claude_thinking_stream.jsonl): start, stop, then message_delta.
	line(streamLine("", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`))
	line(streamLine("", `{"type":"content_block_stop","index":0}`))
	line(streamLine("", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":40}}`))
	// Even if one arrived while a block was open, it counts text and tool input too.
	line(streamLine("", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`))
	line(streamLine("", `{"type":"message_delta","usage":{"output_tokens":17}}`))
	if got := reasoningCounts(t, em, ""); len(got) != 0 {
		t.Fatalf("Claude reasoning counts = %v, want none (the elapsed clock)", got)
	}
	if countKind(em, agent.KindThinkingStarted) != 2 {
		t.Fatalf("thinking_started = %d, want 2 (the clock still runs)", countKind(em, agent.KindThinkingStarted))
	}
}

// Codex's thread/tokenUsage/updated comes after a response completes, so a reasoning item that is
// open when it arrives belongs to a later request: never show the previous request's count.
func TestCodexNeverShowsAPreviousRequestsReasoningCount(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_REASONING_PROGRESS", "1")
	unthrottled(t)
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	stop := startTestCodexDriver(t.Context(), t, d)
	defer stop()
	d.Enqueue("reason about fixture", "chat")
	waitUntil(t, "turn complete", func() bool { return countKind(em, "turn_done") == 1 })
	if got := reasoningCounts(t, em, ""); len(got) != 0 {
		t.Fatalf("Codex reasoning counts = %v, want none (the elapsed clock)", got)
	}
	if countKind(em, agent.KindThinkingStarted) != 2 {
		t.Fatalf("thinking_started = %d, want 2 (one clock per reasoning item)", countKind(em, agent.KindThinkingStarted))
	}
}

func TestHermesReportsReasoningProgress(t *testing.T) {
	unthrottled(t)
	r, em, _ := hermesSecurityRun(t)
	r.begin()
	r.handle(hermesSecurityEvent(r.sid, "reasoning.delta", map[string]any{"text": "fixture reasoning"}))
	r.handle(hermesSecurityEvent(r.sid, "session.usage", map[string]any{"usage": map[string]any{"input": 10, "output": 8, "reasoning": 7}}))
	if got := reasoningCounts(t, em, ""); len(got) != 1 || got[0] != 7 {
		t.Fatalf("counts = %v, want [7]", got)
	}
}

// Hermes's counters are lifetime totals: each thinking phase counts from where the counter stood
// when it began, across turns and within a turn (after text or a tool call).
func TestHermesReasoningCountIsThisPhasesNotTheSessionTotal(t *testing.T) {
	unthrottled(t)
	r, em, _ := hermesSecurityRun(t)
	usage := func(reasoning int) {
		r.handle(hermesSecurityEvent(r.sid, "session.usage", map[string]any{"usage": map[string]any{"input": 10, "output": 8, "reasoning": reasoning}}))
	}
	think := func() { r.handle(hermesSecurityEvent(r.sid, "reasoning.delta", map[string]any{"text": "fixture"})) }
	// Turn 1: reasons 300 tokens, then answers.
	r.begin()
	think()
	usage(300)
	r.handle(hermesSecurityEvent(r.sid, "message.delta", map[string]any{"text": "answer"}))
	r.finish("end_turn", "")
	// An idle report between turns moves the counter on.
	usage(48000)
	// Turn 2: 210 tokens of reasoning, a tool call, then 40 more in a second phase.
	r.begin()
	think()
	usage(48210)
	r.handle(hermesSecurityEvent(r.sid, "tool.start", map[string]any{"tool_id": "t1", "name": "read_file", "args": map[string]any{}}))
	think()
	usage(48250)
	if got := reasoningCounts(t, em, ""); len(got) != 3 || got[0] != 300 || got[1] != 210 || got[2] != 40 {
		t.Fatalf("counts = %v, want [300 210 40]", got)
	}
	if n := countKind(em, agent.KindThinkingStarted); n != 3 {
		t.Fatalf("thinking_started = %d, want 3 (one per phase, so the scope resets)", n)
	}
}

func TestHermesReasoningCounterResetNeverGoesNegative(t *testing.T) {
	unthrottled(t)
	r, em, _ := hermesSecurityRun(t)
	r.begin()
	r.handle(hermesSecurityEvent(r.sid, "session.usage", map[string]any{"usage": map[string]any{"input": 10, "output": 8, "reasoning": 5000}}))
	r.handle(hermesSecurityEvent(r.sid, "reasoning.delta", map[string]any{"text": "fixture"}))
	r.handle(hermesSecurityEvent(r.sid, "session.usage", map[string]any{"usage": map[string]any{"input": 1, "output": 1, "reasoning": 25}}))
	if got := reasoningCounts(t, em, ""); len(got) != 1 || got[0] != 25 {
		t.Fatalf("counts = %v, want [25] (counted from the reset)", got)
	}
}

// A child's output_tokens is its running total; its thinking count starts at each phase.
func TestHermesChildReasoningCountsPerPhase(t *testing.T) {
	unthrottled(t)
	r, em, _ := hermesSecurityRun(t)
	r.begin()
	ev := func(kind string, p map[string]any) {
		p["subagent_id"] = "c1"
		r.handle(hermesSecurityEvent(r.sid, kind, p))
	}
	ev("subagent.start", map[string]any{"output_tokens": 3})
	ev("subagent.progress", map[string]any{"output_tokens": 100})
	ev("subagent.thinking", map[string]any{"text": "a", "output_tokens": 130})
	ev("subagent.thinking", map[string]any{"text": "b", "output_tokens": 160})
	ev("subagent.tool", map[string]any{"output_tokens": 170})
	ev("subagent.thinking", map[string]any{"text": "c", "output_tokens": 175})
	scope := r.children["c1"].ChildID
	if got := reasoningCounts(t, em, scope); len(got) != 3 || got[0] != 30 || got[1] != 60 || got[2] != 5 {
		t.Fatalf("child counts = %v, want [30 60 5]", got)
	}
}

// A slow local model can report many frames a second: one count per scope per interval.
func TestHermesChildReasoningProgressIsThrottled(t *testing.T) {
	old := reasoningProgressEvery
	reasoningProgressEvery = time.Hour
	t.Cleanup(func() { reasoningProgressEvery = old })
	r, em, _ := hermesSecurityRun(t)
	r.begin()
	r.handle(hermesSecurityEvent(r.sid, "subagent.start", map[string]any{"subagent_id": "c1"}))
	for i := 1; i <= 50; i++ {
		r.handle(hermesSecurityEvent(r.sid, "subagent.thinking", map[string]any{"subagent_id": "c1", "text": "x", "output_tokens": i}))
	}
	r.handle(hermesSecurityEvent(r.sid, "reasoning.delta", map[string]any{"text": "fixture"}))
	for i := 1; i <= 50; i++ {
		r.handle(hermesSecurityEvent(r.sid, "session.usage", map[string]any{"usage": map[string]any{"input": 1, "output": 1, "reasoning": i}}))
	}
	if got := reasoningCounts(t, em, r.children["c1"].ChildID); len(got) != 1 {
		t.Fatalf("child counts = %v, want one inside the interval", got)
	}
	if got := reasoningCounts(t, em, ""); len(got) != 1 {
		t.Fatalf("main counts = %v, want one inside the interval", got)
	}
}

func TestHermesReasoningWithoutCountsStillStartsTheChildClock(t *testing.T) {
	r, em, _ := hermesSecurityRun(t)
	r.begin()
	r.handle(hermesSecurityEvent(r.sid, "subagent.start", map[string]any{"subagent_id": "reasoning-child"}))
	r.handle(hermesSecurityEvent(r.sid, "subagent.thinking", map[string]any{"subagent_id": "reasoning-child", "text": "fixture"}))
	found := false
	for _, ev := range em.snapshot() {
		if ev.Kind == agent.KindThinkingStarted {
			p := ev.Payload.(agent.ThinkingPayload)
			found = found || p.ParentCallID != "" && p.Tokens == nil
		}
	}
	if !found {
		t.Fatal("an uncounted reasoning child never started its elapsed clock")
	}
}
