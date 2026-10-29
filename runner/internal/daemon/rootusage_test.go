package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
)

func rootDone(t *testing.T, em *collectEmitter) agent.TurnDonePayload {
	t.Helper()
	em.mu.Lock()
	defer em.mu.Unlock()
	for _, ev := range em.events {
		if p, ok := ev.Payload.(agent.TurnDonePayload); ok {
			return p
		}
	}
	t.Fatal("missing completed turn")
	return agent.TurnDonePayload{}
}
func TestCodexRootUsageKeepsKnownPrefixAndUnknownRerouteRemainder(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_ROOT_USAGE_EVENTS", `[
 {"method":"thread/tokenUsage/updated","params":{"threadId":"cx-thread-1","turnId":"turn-1","tokenUsage":{"last":{"inputTokens":10,"cachedInputTokens":2,"outputTokens":3},"total":{"inputTokens":110,"cachedInputTokens":22,"outputTokens":53}}}},
 {"method":"model/rerouted","params":{"threadId":"cx-thread-1","turnId":"old-turn","toModel":"fixture-foreign"}},
 {"method":"model/rerouted","params":{"threadId":"cx-thread-1","turnId":"turn-1","toModel":"fixture-rerouted"}},
 {"method":"thread/tokenUsage/updated","params":{"threadId":"cx-thread-1","turnId":"turn-1","tokenUsage":{"last":{"inputTokens":15,"cachedInputTokens":3,"outputTokens":4},"total":{"inputTokens":125,"cachedInputTokens":25,"outputTokens":57}}}},
 {"method":"thread/tokenUsage/updated","params":{"threadId":"cx-thread-1","turnId":"turn-1","tokenUsage":{"last":{"inputTokens":15,"cachedInputTokens":3,"outputTokens":4},"total":{"inputTokens":125,"cachedInputTokens":25,"outputTokens":57}}}},
 {"method":"thread/tokenUsage/updated","params":{"threadId":"cx-thread-1","turnId":"turn-1","tokenUsage":{"last":{"inputTokens":10,"cachedInputTokens":2,"outputTokens":3},"total":{"inputTokens":110,"cachedInputTokens":22,"outputTokens":53}}}},
 {"method":"thread/tokenUsage/updated","params":{"threadId":"cx-thread-1","turnId":"old-turn","tokenUsage":{"last":{"inputTokens":999,"outputTokens":999},"total":{"inputTokens":9999,"outputTokens":9999}}}},
 {"method":"turn/completed","params":{"threadId":"cx-thread-1","turn":{"id":"turn-1","status":"completed"}}}
 ]`)
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], Emitter: em, WorkDir: t.TempDir()})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopDriver := startTestCodexDriver(ctx, t, d)
	defer stopDriver()
	d.Enqueue("Review fixture", "chat")
	waitUntil(t, "completed mixed root usage", func() bool { return countKind(em, "turn_done") == 1 })
	em.mu.Lock()
	for _, event := range em.events {
		if changed, ok := event.Payload.(agent.ModelChangedPayload); ok && changed.Model == "fixture-foreign" {
			t.Error("stale reroute changed current native settings")
		}
	}
	em.mu.Unlock()
	p := rootDone(t, em)
	if p.Usage != (agent.Usage{InputTokens: 20, CacheReadTokens: 5, OutputTokens: 7}) {
		t.Fatalf("wrong aggregate after stale/duplicate/foreign-turn updates: %+v", p.Usage)
	}
	if len(p.UsageByModel) != 2 || p.UsageByModel["gpt-5-codex"] != (agent.Usage{InputTokens: 8, CacheReadTokens: 2, OutputTokens: 3}) || p.UsageByModel[""] != (agent.Usage{InputTokens: 12, CacheReadTokens: 3, OutputTokens: 4}) {
		t.Fatalf("invented or missing model attribution: %+v", p.UsageByModel)
	}
}

func TestClaudeRootUsageUsesRootMessageIdentitiesWithoutChildOrDuplicateSpend(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
cat <<'JSON'
{"type":"system","subtype":"init","session_id":"fixture-session"}
{"type":"assistant","message":{"id":"root-one","model":"claude-fixture-a","usage":{"input_tokens":10,"output_tokens":2},"content":[]}}
{"type":"assistant","message":{"id":"root-one","model":"claude-fixture-a","usage":{"input_tokens":10,"output_tokens":2},"content":[]}}
{"type":"assistant","parent_tool_use_id":"child-call","message":{"id":"child-one","model":"claude-fixture-child","usage":{"input_tokens":999,"output_tokens":99},"content":[]}}
{"type":"assistant","message":{"id":"root-two","model":"claude-fixture-b","usage":{"input_tokens":15,"output_tokens":3},"content":[]}}
{"type":"result","subtype":"success","usage":{"input_tokens":25,"output_tokens":5}}
JSON
`
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-requested", "", em, nil)
	d.runTurn(t.Context(), "Review fixture")
	p := rootDone(t, em)
	if p.Usage != (agent.Usage{InputTokens: 25, OutputTokens: 5}) {
		t.Fatalf("wrong aggregate: %+v", p.Usage)
	}
	if len(p.UsageByModel) != 2 || p.UsageByModel["claude-fixture-a"].InputTokens != 10 || p.UsageByModel["claude-fixture-b"].InputTokens != 15 {
		raw, _ := json.Marshal(p.UsageByModel)
		t.Fatalf("root identity attribution: %s", raw)
	}
}

func TestRootUsageReconciliationBoundsAndUnknownResidual(t *testing.T) {
	total := agent.Usage{InputTokens: 10, OutputTokens: 5}
	for _, parts := range []map[string]agent.Usage{
		{"model-a": {InputTokens: 11}},
		{"unsafe model": {InputTokens: 10}},
		{"model-a": {InputTokens: -1}},
	} {
		p := reconcileRootUsage(total, parts)
		if len(p) != 1 || p[""] != total {
			t.Fatalf("invalid partition retained: %+v", p)
		}
	}
	p := reconcileRootUsage(total, map[string]agent.Usage{"model-a": {InputTokens: 6, OutputTokens: 2}})
	if p[""] != (agent.Usage{InputTokens: 4, OutputTokens: 3}) {
		t.Fatalf("missing unknown residual: %+v", p)
	}
	for i := 0; i < 33; i++ {
		p[fmt.Sprintf("model-%d", i)] = agent.Usage{}
	}
	p = reconcileRootUsage(total, p)
	if len(p) != 1 || p[""] != total {
		t.Fatal("oversized model map retained")
	}
}
func TestClaudeRootMessageSnapshotsCannotDoubleCountOrRelabelIdentity(t *testing.T) {
	var u claudeRootUsage
	observe := func(id, model, parent string, n int) {
		var ev ccLine
		ev.Message.ID = id
		ev.Message.Model = model
		ev.ParentToolUseID = parent
		ev.Message.Usage.InputTokens = n
		u.observe(&ev)
	}
	observe("message-a", "model-a", "", 10)
	observe("message-a", "model-a", "", 8)  // out-of-order partial message snapshot
	observe("message-a", "model-a", "", 10) // duplicate
	observe("child-a", "child-model", "child-call", 99)
	observe("message-a", "model-b", "", 12) // contradictory model on one native identity
	observe("message-b", "model-b", "", 3)
	p := u.partition(agent.Usage{InputTokens: 20})
	if p[""].InputTokens != 17 || p["model-b"].InputTokens != 3 || len(p) != 2 {
		t.Fatalf("snapshot identity or residual attribution: %+v", p)
	}
	// Cap exhaustion is incomplete telemetry, not permission to assign its remainder.
	for i := 0; i < rootUsageMessageCap+1; i++ {
		observe(fmt.Sprintf("m-%d", i), "model-b", "", 0)
	}
	if len(u.messages) > rootUsageMessageCap || !u.overflow {
		t.Fatal("unbounded native message inventory")
	}
	p = u.partition(agent.Usage{InputTokens: 21})
	if p[""].InputTokens != 18 {
		t.Fatal("overflow remainder was not unknown")
	}
}
func TestCodexRootCounterDropsAreNotNewAccountingEpochs(t *testing.T) {
	u := newCodexRootUsage("model-a")
	one := agent.Usage{InputTokens: 100, OutputTokens: 20}
	u.observe(agent.Usage{InputTokens: 10, OutputTokens: 2}, &one)
	u.changeModel("model-b")
	reset := agent.Usage{InputTokens: 3, OutputTokens: 1}
	u.observe(reset, &reset)
	total, p := u.snapshot()
	if total != (agent.Usage{InputTokens: 10, OutputTokens: 2}) || p["model-a"] != total {
		t.Fatal("a reset/stale total erased or re-added spend")
	}
	// A later cumulative sample above the observed high-water mark adds only its delta.
	later := agent.Usage{InputTokens: 105, OutputTokens: 24}
	u.observe(agent.Usage{InputTokens: 5, OutputTokens: 4}, &later)
	total, p = u.snapshot()
	if total != (agent.Usage{InputTokens: 15, OutputTokens: 6}) || p[""] != (agent.Usage{InputTokens: 5, OutputTokens: 4}) {
		t.Fatalf("counter delta after boundary: %+v %+v", total, p)
	}
}
func TestClaudeStreamingRootUsageResetsAtEachResult(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "model-default", "", em, nil)
	p := &ccProc{model: "model-default", sawInit: true, inTurn: true}
	d.steer.proc = p
	for turn := 0; turn < 2; turn++ {
		d.onLine(p, []byte(`{"type":"assistant","message":{"id":"repeated-after-turn","model":"model-effective","usage":{"input_tokens":3},"content":[]}}`))
		d.onLine(p, []byte(`{"type":"result","usage":{"input_tokens":3}}`))
	}
	em.mu.Lock()
	defer em.mu.Unlock()
	n := 0
	for _, event := range em.events {
		if done, ok := event.Payload.(agent.TurnDonePayload); ok {
			n++
			if done.UsageByModel["model-effective"].InputTokens != 3 || len(done.UsageByModel) != 1 {
				t.Fatalf("streaming usage leaked across turn: %+v", done)
			}
		}
	}
	if n != 2 {
		t.Fatalf("completed turns=%d", n)
	}
	d.sm.Lock()
	d.stopIdleTimerLocked()
	d.sm.Unlock()
}

func TestCodexRootUsageManualNextModelDoesNotRelabelActiveTurn(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	gate := filepath.Join(t.TempDir(), "continue")
	t.Setenv("FAKE_CODEX_ROOT_USAGE_GATE", gate)
	t.Setenv("FAKE_CODEX_ROOT_USAGE_EVENTS", `[
 {"method":"thread/tokenUsage/updated","params":{"threadId":"cx-thread-1","turnId":"turn-1","tokenUsage":{"last":{"inputTokens":10},"total":{"inputTokens":110}}}},
 {"method":"turn/completed","params":{"threadId":"cx-thread-1","turn":{"id":"turn-1","status":"completed"}}}
 ]`)
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], Emitter: em, WorkDir: t.TempDir()})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stopDriver := startTestCodexDriver(ctx, t, d)
	defer stopDriver()
	d.Enqueue("Review fixture", "chat")
	waitUntil(t, "native usage gate", func() bool {
		em.mu.Lock()
		defer em.mu.Unlock()
		for _, event := range em.events {
			if text, ok := event.Payload.(agent.AssistantTextPayload); ok && text.Text == "Fixture awaiting usage" {
				return true
			}
		}
		return false
	})
	d.SetModel("fixture-manual-next", "medium", "ui")
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "native usage complete", func() bool { return countKind(em, "turn_done") == 1 })
	p := rootDone(t, em)
	if p.Model != "gpt-5-codex" || len(p.UsageByModel) != 1 || p.UsageByModel["gpt-5-codex"].InputTokens != 10 {
		t.Fatalf("next selection relabeled current usage: %+v", p)
	}
	d.mu.Lock()
	selected := d.model
	d.mu.Unlock()
	if selected != "fixture-manual-next" {
		t.Fatalf("manual next selection lost: %q", selected)
	}
}

// The context meter (#191): a Claude turn_done carries the main agent's latest prompt (input + cache
// read + cache write, never a subagent's) and the window the result line reports for that model; a
// compaction drops the reading until the next call and is said.
func TestClaudeTurnDoneCarriesContextReading(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "model-default", "", em, nil)
	p := &ccProc{model: "model-default", sawInit: true, inTurn: true}
	d.steer.proc = p
	result := `{"type":"result","usage":{"input_tokens":3},"modelUsage":{"model-effective":{"contextWindow":200000},"model-small":{"contextWindow":100000}}}`
	// Turn 1: two root calls (the later one counts) and a bigger subagent call (left out).
	d.onLine(p, []byte(`{"type":"assistant","message":{"id":"m1","model":"model-effective","usage":{"input_tokens":5,"cache_read_input_tokens":50},"content":[]}}`))
	d.onLine(p, []byte(`{"type":"assistant","message":{"id":"m2","model":"model-effective","usage":{"input_tokens":3,"cache_read_input_tokens":100,"cache_creation_input_tokens":20},"content":[]}}`))
	d.onLine(p, []byte(`{"type":"assistant","parent_tool_use_id":"toolu_1","message":{"id":"s1","model":"model-small","usage":{"input_tokens":90000},"content":[]}}`))
	d.onLine(p, []byte(result))
	// Turn 2: compacted at the end (a /compact turn): no reading after it.
	p.inTurn = true
	d.onLine(p, []byte(`{"type":"assistant","message":{"id":"m3","model":"model-effective","usage":{"input_tokens":150000},"content":[]}}`))
	d.onLine(p, []byte(`{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"auto","pre_tokens":150000}}`))
	d.onLine(p, []byte(result))
	// Turn 3: compacted, then a call: its reading, and the compaction said.
	p.inTurn = true
	d.onLine(p, []byte(`{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"manual","pre_tokens":90000}}`))
	d.onLine(p, []byte(`{"type":"assistant","message":{"id":"m4","model":"model-effective","usage":{"input_tokens":12,"cache_creation_input_tokens":8000},"content":[]}}`))
	d.onLine(p, []byte(result))
	// Turn 4: nothing reported about the context (no root call): no reading at all.
	p.inTurn = true
	d.onLine(p, []byte(result))
	em.mu.Lock()
	var got []*agent.ContextUsage
	for _, event := range em.events {
		if done, ok := event.Payload.(agent.TurnDonePayload); ok {
			got = append(got, done.Context)
		}
	}
	em.mu.Unlock()
	d.sm.Lock()
	d.stopIdleTimerLocked()
	d.sm.Unlock()
	want := []*agent.ContextUsage{
		{Tokens: 123, Window: 200000},
		{Compacted: true, CompactedFrom: 150000},
		{Tokens: 8012, Window: 200000, Compacted: true, CompactedFrom: 90000},
		nil,
	}
	if len(got) != len(want) {
		t.Fatalf("turns = %d", len(got))
	}
	for i := range want {
		if (got[i] == nil) != (want[i] == nil) || got[i] != nil && *got[i] != *want[i] {
			t.Fatalf("turn %d context = %+v, want %+v", i+1, got[i], want[i])
		}
	}
}

func TestClaudeResultContextWindowMatchesOnlyItsModel(t *testing.T) {
	var l ccLine
	if err := json.Unmarshal([]byte(`{"modelUsage":{"claude-x[1m]":{"contextWindow":1000000},"claude-y":{"contextWindow":200000}}}`), &l); err != nil {
		t.Fatal(err)
	}
	for model, want := range map[string]int{"claude-x": 1000000, "claude-y": 200000, "claude-z": 0, "": 0} {
		if got := l.contextWindow(model); got != want {
			t.Errorf("contextWindow(%q) = %d, want %d", model, got, want)
		}
	}
	_ = json.Unmarshal([]byte(`{"modelUsage":{"claude-x-a":{"contextWindow":1},"claude-x-b":{"contextWindow":2}}}`), &l)
	if got := l.contextWindow("claude-x"); got != 0 {
		t.Errorf("ambiguous window = %d, want 0", got)
	}
}

func TestCodexTurnDoneCarriesContextReading(t *testing.T) {
	for name, tc := range map[string]struct {
		events string
		want   *agent.ContextUsage
	}{
		"reading": {`[
 {"method":"thread/tokenUsage/updated","params":{"threadId":"cx-thread-1","turnId":"turn-1","tokenUsage":{"last":{"inputTokens":10,"outputTokens":3,"totalTokens":13},"total":{"inputTokens":10,"outputTokens":3},"modelContextWindow":272000}}},
 {"method":"thread/tokenUsage/updated","params":{"threadId":"cx-thread-1","turnId":"turn-1","tokenUsage":{"last":{"inputTokens":90000,"cachedInputTokens":80000,"outputTokens":500,"totalTokens":90500},"total":{"inputTokens":90010,"outputTokens":503},"modelContextWindow":272000}}},
 {"method":"turn/completed","params":{"threadId":"cx-thread-1","turn":{"id":"turn-1","status":"completed"}}}
 ]`, &agent.ContextUsage{Tokens: 90500, Window: 272000}},
		"compacted": {`[
 {"method":"thread/tokenUsage/updated","params":{"threadId":"cx-thread-1","turnId":"turn-1","tokenUsage":{"last":{"inputTokens":250000,"outputTokens":10},"total":{"inputTokens":250000,"outputTokens":10},"modelContextWindow":272000}}},
 {"method":"item/completed","params":{"threadId":"cx-thread-1","turnId":"turn-1","item":{"type":"contextCompaction","id":"cmp-1"}}},
 {"method":"turn/completed","params":{"threadId":"cx-thread-1","turn":{"id":"turn-1","status":"completed"}}}
 ]`, &agent.ContextUsage{Window: 272000, Compacted: true}},
		// A model change mid-turn: the earlier model's window is not this count's (#191 review).
		"rerouted, window unknown": {`[
 {"method":"thread/tokenUsage/updated","params":{"threadId":"cx-thread-1","turnId":"turn-1","tokenUsage":{"last":{"inputTokens":150000,"totalTokens":150000},"total":{"inputTokens":150000},"modelContextWindow":200000}}},
 {"method":"model/rerouted","params":{"threadId":"cx-thread-1","turnId":"turn-1","toModel":"fixture-rerouted"}},
 {"method":"thread/tokenUsage/updated","params":{"threadId":"cx-thread-1","turnId":"turn-1","tokenUsage":{"last":{"inputTokens":100000,"totalTokens":100000},"total":{"inputTokens":250000},"modelContextWindow":null}}},
 {"method":"turn/completed","params":{"threadId":"cx-thread-1","turn":{"id":"turn-1","status":"completed"}}}
 ]`, &agent.ContextUsage{Tokens: 100000}},
		"settings changed, no report since": {`[
 {"method":"thread/tokenUsage/updated","params":{"threadId":"cx-thread-1","turnId":"turn-1","tokenUsage":{"last":{"inputTokens":150000,"totalTokens":150000},"total":{"inputTokens":150000},"modelContextWindow":200000}}},
 {"method":"thread/settings/updated","params":{"threadId":"cx-thread-1","threadSettings":{"model":"fixture-other","effort":"low"}}},
 {"method":"turn/completed","params":{"threadId":"cx-thread-1","turn":{"id":"turn-1","status":"completed"}}}
 ]`, &agent.ContextUsage{Tokens: 150000}},
		"rerouted, new window reported": {`[
 {"method":"thread/tokenUsage/updated","params":{"threadId":"cx-thread-1","turnId":"turn-1","tokenUsage":{"last":{"inputTokens":150000,"totalTokens":150000},"total":{"inputTokens":150000},"modelContextWindow":200000}}},
 {"method":"model/rerouted","params":{"threadId":"cx-thread-1","turnId":"turn-1","toModel":"fixture-rerouted"}},
 {"method":"thread/tokenUsage/updated","params":{"threadId":"cx-thread-1","turnId":"turn-1","tokenUsage":{"last":{"inputTokens":100000,"totalTokens":100000},"total":{"inputTokens":250000},"modelContextWindow":400000}}},
 {"method":"turn/completed","params":{"threadId":"cx-thread-1","turn":{"id":"turn-1","status":"completed"}}}
 ]`, &agent.ContextUsage{Tokens: 100000, Window: 400000}},
		"unreported": {`[
 {"method":"turn/completed","params":{"threadId":"cx-thread-1","turn":{"id":"turn-1","status":"completed"}}}
 ]`, nil},
	} {
		t.Run(name, func(t *testing.T) {
			bin := fakeCodex(t)
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv("FAKE_CODEX_ROOT_USAGE_EVENTS", tc.events)
			em := &collectEmitter{}
			d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], Emitter: em, WorkDir: t.TempDir()})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stopDriver := startTestCodexDriver(ctx, t, d)
			defer stopDriver()
			d.Enqueue("Review fixture", "chat")
			waitUntil(t, "turn done", func() bool { return countKind(em, "turn_done") == 1 })
			got := rootDone(t, em).Context
			if (got == nil) != (tc.want == nil) || got != nil && *got != *tc.want {
				t.Fatalf("context = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A turn that compacted, then ended without a result line (interrupt timeout, the CLI exited) still
// says it compacted: the reading before it is gone (#191 review).
func TestClaudeCompactionThenInterruptOrExitSaysCompacted(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "model-default", "", em, nil)
	p := &ccProc{model: "model-default", sawInit: true, inTurn: true, cmd: exec.Command("true")} // never started
	d.steer.proc = p
	d.onLine(p, []byte(`{"type":"assistant","message":{"id":"m1","model":"model-effective","usage":{"input_tokens":170000},"content":[]}}`))
	d.onLine(p, []byte(`{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"auto","pre_tokens":170000}}`))
	d.sm.Lock()
	gen, turn := p.gen, d.steer.turnSeq
	d.sm.Unlock()
	d.interruptTimeout(gen, turn)
	em.mu.Lock()
	var got *agent.ContextUsage
	found := false
	for _, event := range em.events {
		if done, ok := event.Payload.(agent.TurnDonePayload); ok {
			got, found = done.Context, true
		}
	}
	em.mu.Unlock()
	if !found || got == nil || *got != (agent.ContextUsage{Compacted: true, CompactedFrom: 170000}) {
		t.Fatalf("interrupted turn context = %+v (turn_done seen %v)", got, found)
	}
	var none claudeRootUsage
	none.observe(&ccLine{Message: struct {
		ID      string          `json:"id"`
		Model   string          `json:"model"`
		Usage   ccTokens        `json:"usage"`
		Content json.RawMessage `json:"content"`
	}{ID: "m", Model: "x", Usage: ccTokens{InputTokens: 5}}})
	if c := none.compactionOnly(); c != nil {
		t.Fatalf("a turn without a compaction replaced the reading: %+v", c)
	}
}
