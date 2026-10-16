package daemon

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// feedFixture runs every line of a stream-json fixture through the streaming engine's line handler
// and returns the transcript events it emitted (status changes left out).
func feedFixture(t *testing.T, name string) []agent.Event {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	d.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	d.steer.legacy = false
	p := &ccProc{}
	d.steer.proc = p
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		d.onLine(p, sc.Bytes())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	var out []agent.Event
	for _, ev := range em.snapshot() {
		if ev.Kind != "status_changed" && ev.Kind != "capabilities" {
			out = append(out, ev)
		}
	}
	return out
}

// describe renders an event as "kind[parent]:detail" for the order assertions.
func describe(ev agent.Event) string {
	s := ev.Kind
	parent, detail := "", ""
	switch p := ev.Payload.(type) {
	case agent.AssistantTextPayload:
		parent, detail = p.ParentCallID, p.Text
	case agent.AssistantThinkingPayload:
		parent, detail = p.ParentCallID, p.Text
	case agent.ToolCallPayload:
		parent, detail = p.ParentCallID, p.Tool+" "+p.CallID
	case agent.ToolResultPayload:
		parent, detail = p.ParentCallID, p.CallID
	case agent.SubagentPayload:
		detail = p.ChildID
	}
	if parent != "" {
		s += "[" + parent + "]"
	}
	return s + ":" + detail
}

func TestClaudeTranscriptThinkingAndSubagents(t *testing.T) {
	events := feedFixture(t, "claude_subagent.jsonl")
	var got []string
	for _, ev := range events {
		got = append(got, describe(ev))
	}
	want := []string{
		// redacted_thinking and the empty thinking block emit nothing
		"assistant_thinking:The repo is large.\nA subagent can survey it.",
		"assistant_text:I'll have a subagent look around.",
		"tool_call:Agent toolu_agent1",
		"subagent_started:toolu_agent1",
		// the subagent's prompt line is not a transcript event
		"assistant_thinking[toolu_agent1]:Start with ls.",
		"assistant_text[toolu_agent1]:Listing the directories.",
		// the first line of the subagent reports its model and tokens; the second (the same
		// response, one block later) is within the throttle
		"subagent_update:toolu_agent1",
		"tool_call[toolu_agent1]:Bash toolu_sub1",
		"tool_result[toolu_agent1]:toolu_sub1",
		// a plain Bash call of the main agent starts no subagent
		"tool_call:Bash toolu_main1",
		"tool_result:toolu_main1",
		"subagent_done:toolu_agent1",
		"tool_result:toolu_agent1",
		"tool_call:Agent toolu_agent2",
		"subagent_started:toolu_agent2",
		"subagent_done:toolu_agent2",
		"tool_result:toolu_agent2",
		"assistant_text:Done.",
		"turn_done:",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	subs := map[string][]agent.SubagentPayload{}
	for _, ev := range events {
		if p, ok := ev.Payload.(agent.SubagentPayload); ok {
			subs[ev.Kind] = append(subs[ev.Kind], p)
		}
	}
	wantStarted := []agent.SubagentPayload{
		{ChildID: "toolu_agent1", AgentType: "Explore", Model: "haiku", Summary: "Survey the repo"},
		// no subagent_type: general-purpose; no description: the prompt, cut to 120 bytes
		{ChildID: "toolu_agent2", AgentType: "general-purpose", Model: "",
			Summary: "Run the whole test suite and report every failure with its file and line, then suggest a fix for each one found in the o"},
	}
	if !reflect.DeepEqual(subs["subagent_started"], wantStarted) {
		t.Fatalf("subagent_started = %+v, want %+v", subs["subagent_started"], wantStarted)
	}
	// The response's usage counts once, with its last line's figures.
	wantUsage := &agent.Usage{InputTokens: 100, OutputTokens: 20, CacheReadTokens: 5, CacheWriteTokens: 7}
	wantUpdate := []agent.SubagentPayload{
		{ChildID: "toolu_agent1", AgentType: "Explore", Model: "claude-haiku-5", Summary: "Survey the repo",
			Usage: &agent.Usage{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 5, CacheWriteTokens: 7}},
	}
	if !reflect.DeepEqual(subs["subagent_update"], wantUpdate) {
		t.Fatalf("subagent_update = %+v, want %+v", subs["subagent_update"], wantUpdate)
	}
	wantDone := []agent.SubagentPayload{
		{ChildID: "toolu_agent1", AgentType: "Explore", Model: "claude-haiku-5",
			Summary: "Two directories: docs holds the documentation, runner holds the runner.",
			Usage:   wantUsage, ToolUses: 1},
		{ChildID: "toolu_agent2", AgentType: "general-purpose", Model: "", Summary: "error: agent crashed", IsError: true},
	}
	if !reflect.DeepEqual(subs["subagent_done"], wantDone) {
		t.Fatalf("subagent_done = %+v, want %+v", subs["subagent_done"], wantDone)
	}
}

// The wire shape: parent_call_id is present on a subagent's events and absent from the main
// agent's, and a thinking event is {"text": ...}.
func TestClaudeTranscriptPayloadJSON(t *testing.T) {
	events := feedFixture(t, "claude_subagent.jsonl")
	var lines []string
	for _, ev := range events {
		switch ev.Kind {
		case "assistant_thinking", "assistant_text", "tool_call", "tool_result":
			raw, err := json.Marshal(ev.Payload)
			if err != nil {
				t.Fatal(err)
			}
			lines = append(lines, ev.Kind+" "+string(raw))
		}
	}
	for _, want := range []string{
		`assistant_thinking {"text":"The repo is large.\nA subagent can survey it."}`,
		`assistant_thinking {"text":"Start with ls.","parent_call_id":"toolu_agent1"}`,
		`assistant_text {"text":"Listing the directories.","done":true,"parent_call_id":"toolu_agent1"}`,
		`tool_call {"tool":"Bash","call_id":"toolu_sub1","input":{"command":"ls"},"parent_call_id":"toolu_agent1"}`,
		`tool_result {"call_id":"toolu_sub1","output":"docs\nrunner","is_error":false,"duration_ms":0,"parent_call_id":"toolu_agent1"}`,
		`tool_call {"tool":"Bash","call_id":"toolu_main1","input":{"command":"git status"}}`,
		`assistant_text {"text":"Done.","done":true}`,
	} {
		if !slices.Contains(lines, want) {
			t.Errorf("missing %s\nhave:\n%s", want, strings.Join(lines, "\n"))
		}
	}
}

// A subagent's own Agent call (nested) is recorded as its tool call, but only the main agent's
// Agent calls open a subagent card; a result for an unknown id ends nothing.
func TestClaudeTranscriptNestedAgentCallStartsNoSubagent(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	for _, line := range []string{
		`{"type":"assistant","parent_tool_use_id":"toolu_outer","message":{"content":[{"type":"tool_use","id":"toolu_inner","name":"Agent","input":{"prompt":"x"}}]}}`,
		`{"type":"user","parent_tool_use_id":"toolu_outer","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_inner","content":"y"}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_never_started","content":"z"}]}}`,
	} {
		var ev ccLine
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type == "assistant" {
			d.emitAssistantBlocks(&ev)
		} else {
			d.emitToolResults(&ev)
		}
	}
	if got, want := em.kinds(), []string{"tool_call", "tool_result", "tool_result"}; !slices.Equal(got, want) {
		t.Fatalf("kinds = %v, want %v", got, want)
	}
}

func TestCutRunesKeepsCharactersWhole(t *testing.T) {
	if got := cutRunes("abécd", 3); got != "ab" {
		t.Fatalf("cutRunes = %q, want %q", got, "ab")
	}
	if got := cutRunes("short", 120); got != "short" {
		t.Fatalf("cutRunes = %q", got)
	}
}

// A background subagent: its call returns at launch (subagent_done marked Background, its
// agentId remembered), its work keeps arriving, and the task-notification the CLI injects when it
// finishes ends it with a final subagent_done carrying the result, totals and duration. A
// notification for anything else (a background shell task) ends nothing.
func TestClaudeTranscriptBackgroundSubagentClosedByItsNotification(t *testing.T) {
	events := feedFixture(t, "claude_background.jsonl")
	var done []agent.SubagentPayload
	for _, ev := range events {
		if ev.Kind == "user_message" && ev.Payload.(agent.UserMessagePayload).Source != "system" {
			t.Errorf("a subagent's or the harness's line became the person's message: %+v", ev.Payload)
		}
		if ev.Kind == "subagent_done" {
			done = append(done, ev.Payload.(agent.SubagentPayload))
		}
	}
	want := []agent.SubagentPayload{
		{ChildID: "toolu_bg", AgentType: "general-purpose", Model: "", Summary: "running in the background", Background: true},
		{ChildID: "toolu_bg2", AgentType: "general-purpose", Model: "", Summary: "running in the background", Background: true},
		// closed by tool-use-id; its own accumulated usage is kept, the notification's counts win
		{ChildID: "toolu_bg", AgentType: "general-purpose", Model: "claude-sonnet-5", Summary: "The build is green.",
			Usage: &agent.Usage{InputTokens: 50, OutputTokens: 5}, ToolUses: 4, DurationMs: 12345},
		// closed by task-id alone; nothing accumulated, so subagent_tokens stands in as output
		{ChildID: "toolu_bg2", AgentType: "general-purpose", Model: "", Summary: "error: Could not read them.",
			Usage: &agent.Usage{OutputTokens: 777}, ToolUses: 2, DurationMs: 900, IsError: true},
	}
	if !reflect.DeepEqual(done, want) {
		t.Fatalf("subagent_done:\n got  %+v\n want %+v", done, want)
	}
}

// Updates are throttled per subagent: the first line reports at once, later ones only once
// ccSubagentUpdateEvery has passed, each with the running totals.
func TestSubagentUpdatesAreThrottled(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	now := time.Unix(1_800_000_000, 0)
	d.now = func() time.Time { return now }
	d.startSubagent("toolu_a", json.RawMessage(`{"description":"x","prompt":"y"}`))
	line := func(id string, out int) {
		var ev ccLine
		raw := fmt.Sprintf(`{"type":"assistant","parent_tool_use_id":"toolu_a","message":{"id":%q,"model":"claude-haiku-5","usage":{"input_tokens":10,"output_tokens":%d},"content":[{"type":"tool_use","id":"t-%s","name":"Read","input":{}}]}}`, id, out, id)
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			t.Fatal(err)
		}
		d.emitAssistantBlocks(&ev)
	}
	line("m1", 1)
	now = now.Add(2 * time.Second)
	line("m2", 2)
	now = now.Add(3 * time.Second)
	line("m3", 3)
	var updates []agent.SubagentPayload
	for _, ev := range em.snapshot() {
		if ev.Kind == "subagent_update" {
			updates = append(updates, ev.Payload.(agent.SubagentPayload))
		}
	}
	if len(updates) != 2 {
		t.Fatalf("updates = %d, want 2 (at once, then 5 s later): %+v", len(updates), updates)
	}
	if u := updates[1]; u.Model != "claude-haiku-5" || u.ToolUses != 3 || u.Usage == nil || *u.Usage != (agent.Usage{InputTokens: 30, OutputTokens: 6}) {
		t.Fatalf("second update = %+v (usage %+v)", u, u.Usage)
	}
}

// A replayed user line of a subagent is never the person's message.
func TestSubagentReplayIsNotAUserMessage(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	d.steer.legacy = false
	p := &ccProc{}
	d.steer.proc = p
	d.onLine(p, []byte(`{"type":"user","parent_tool_use_id":"toolu_a","isReplay":true,"uuid":"u9","message":{"role":"user","content":"Look around the repo."}}`))
	for _, ev := range em.snapshot() {
		if ev.Kind == "user_message" {
			t.Fatalf("got a user_message: %+v", ev.Payload)
		}
	}
}

// The CLI's failure and housekeeping lines: an API retry is a notice, an API error written as an
// assistant line is an error (never the model's text) and the error result after it says nothing
// more, a compaction is recorded, a slash command's output is a system card, and an error result
// with no result text gives its first listed reason. Every errored turn ends with stop reason
// "error".
func TestClaudeTranscriptErrorsAndSystemLines(t *testing.T) {
	events := feedFixture(t, "claude_errors.jsonl")
	var got []string
	for _, ev := range events {
		switch p := ev.Payload.(type) {
		case agent.ErrorPayload:
			got = append(got, fmt.Sprintf("error(%s,%v):%s", p.Code, p.Retryable, p.Message))
		case agent.TurnDonePayload:
			got = append(got, "turn_done:"+p.StopReason)
		case agent.CompactionPayload:
			got = append(got, fmt.Sprintf("compaction:%s %d %q", p.Trigger, p.PreTokens, p.Summary))
		case agent.UserMessagePayload:
			got = append(got, "user_message("+p.Source+"):"+p.Text)
		default:
			got = append(got, describe(ev))
		}
	}
	apiErr := `API Error: 529 {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
	want := []string{
		"error(api_retry,true):retrying the API (attempt 1 of 10) after overloaded_error (529)…",
		"error(api_error,true):" + apiErr,
		"turn_done:error",
		"compaction:manual 151200 \"\"",
		"user_message(system):Total cost: $0.12\nTotal duration: 4m",
		"turn_done:end_turn",
		"error(api_error,true):API Error: Rate limit reached",
		"assistant_text:Still working on it.",
		"error(,true):Reached the maximum number of turns (3)",
		"turn_done:error",
		"error(,true):Tool execution failed",
		"turn_done:error",
		// no flag and no code: the synthetic model alone marks it
		"error(api_error,true):API Error: Connection error.",
		"turn_done:error",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// With --include-partial-messages the CLI writes the API's stream events too: a thinking block's
// start and stop are said as thinking_started / thinking_done (a subagent's with its parent), around
// the assistant_thinking its whole line gives; every other stream event says nothing. A message that
// starts while a block is open (a call retried) forgets it, so its stray stop says nothing.
func TestClaudeTranscriptThinkingStreamEvents(t *testing.T) {
	events := feedFixture(t, "claude_thinking_stream.jsonl")
	var got []string
	for _, ev := range events {
		if p, ok := ev.Payload.(agent.ThinkingPayload); ok {
			s := ev.Kind
			if p.ParentCallID != "" {
				s += "[" + p.ParentCallID + "]"
			}
			got = append(got, s)
			continue
		}
		got = append(got, describe(ev))
	}
	want := []string{
		"thinking_started",
		"assistant_thinking:Survey the repo first.",
		"thinking_done",
		"tool_call:Agent toolu_agent1",
		"subagent_started:toolu_agent1",
		"thinking_started[toolu_agent1]",
		"thinking_done[toolu_agent1]",
		"assistant_text[toolu_agent1]:docs, runner",
		"subagent_update:toolu_agent1",
		"subagent_done:toolu_agent1",
		"tool_result:toolu_agent1",
		"thinking_started",
		"assistant_text:Two directories.",
		"turn_done:",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, ev := range events {
		if (ev.Kind == agent.KindThinkingStarted || ev.Kind == agent.KindThinkingDone) && !agent.IsTransient(ev) {
			t.Fatalf("%s is not transient", ev.Kind)
		}
	}
}

// An API retry notice carries the CLI's attempt, its most attempts and the wait before it.
func TestClaudeAPIRetryNoticeCarriesTheAttempt(t *testing.T) {
	for _, ev := range feedFixture(t, "claude_errors.jsonl") {
		p, ok := ev.Payload.(agent.ErrorPayload)
		if !ok || p.Code != agent.ErrorCodeAPIRetry {
			continue
		}
		if p.Attempt != 1 || p.MaxRetries != 10 || p.RetryInMs != 600 {
			t.Fatalf("retry notice = %+v, want attempt 1 of 10 in 600 ms", p)
		}
		raw, _ := json.Marshal(p)
		if !strings.Contains(string(raw), `"attempt":1,"max_retries":10,"retry_in_ms":600`) {
			t.Fatalf("retry notice JSON = %s", raw)
		}
		return
	}
	t.Fatal("no api_retry notice")
}
