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

func subagentEvents(events []agent.Event) []string {
	var out []string
	for _, ev := range events {
		switch ev.Kind {
		case "subagent_started", "subagent_done", "assistant_text", "tool_call", "tool_result":
			out = append(out, describe(ev))
		}
	}
	return out
}

// A finished subagent the main agent resumes with SendMessage is a new subagent instance under the
// SendMessage call's id, with its original's type and title (testdata: the CLI's line shapes). Its
// lines (which carry the SendMessage call's id, or the original call's on a CLI that keeps it) go
// under it; its notice ends it, in either form; a SendMessage that waits for the run ends it with
// its result. A message to the main conversation and a failed send resume nothing; a resume of an
// agent never seen to start opens a run all the same.
func TestClaudeResumedSubagents(t *testing.T) {
	events := feedFixture(t, "claude_subagent_resume.jsonl")
	got := subagentEvents(events)
	want := []string{
		"tool_call:Agent toolu_fg",
		"subagent_started:toolu_fg",
		"assistant_text[toolu_fg]:Starting.",
		"subagent_done:toolu_fg",
		"tool_result:toolu_fg",
		"tool_call:Agent toolu_bgx",
		"subagent_started:toolu_bgx",
		"subagent_done:toolu_bgx", // the launch
		"tool_result:toolu_bgx",
		"subagent_done:toolu_bgx", // its notice
		// resume 1: the result says it resumed; the run's lines follow, then the notice ends it
		"tool_call:SendMessage toolu_send1",
		"subagent_started:toolu_send1",
		"tool_result:toolu_send1",
		"tool_call[toolu_send1]:Bash toolu_r1",
		"tool_result[toolu_send1]:toolu_r1",
		"assistant_text:Waiting on the fix.",
		"subagent_done:toolu_send1",
		// resume 2: named by its short id; a line under the original call goes under the run
		"tool_call:SendMessage toolu_send2",
		"subagent_started:toolu_send2",
		"tool_result:toolu_send2",
		"assistant_text[toolu_send2]:Checking.",
		"subagent_done:toolu_send2",
		// resume 3: the run's lines come before the call's result, which is its report
		"tool_call:SendMessage toolu_send3",
		"subagent_started:toolu_send3",
		"tool_call[toolu_send3]:Bash toolu_r3",
		"tool_result[toolu_send3]:toolu_r3",
		"subagent_done:toolu_send3",
		"tool_result:toolu_send3",
		"tool_call:SendMessage toolu_send4",
		"tool_call:SendMessage toolu_send5",
		"tool_call:SendMessage toolu_send6",
		"tool_result:toolu_send4",
		"tool_result:toolu_send5",
		// an agent this driver never saw start (one from before a restart) still opens a run
		"subagent_started:toolu_send6",
		"tool_result:toolu_send6",
		// two sends to the stopped agent at once: the second finds it already waking, opens nothing
		"tool_call:SendMessage toolu_send7",
		"tool_call:SendMessage toolu_send8",
		"subagent_started:toolu_send7",
		"tool_result:toolu_send7",
		"tool_result:toolu_send8",
		"subagent_done:toolu_send7",
		"assistant_text:Done.",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("events:\n got  %q\n want %q", got, want)
	}

	var subs []agent.SubagentPayload
	for _, ev := range events {
		if ev.Kind == "subagent_started" || ev.Kind == "subagent_done" {
			p := ev.Payload.(agent.SubagentPayload)
			if p.ChildID != "toolu_fg" && p.ChildID != "toolu_bgx" {
				subs = append(subs, p)
			}
		}
	}
	sonnet := func(in, out int) *agent.Usage { return &agent.Usage{InputTokens: in, OutputTokens: out} }
	wantSubs := []agent.SubagentPayload{
		{ChildID: "toolu_send1", AgentType: "general-purpose", Summary: "Implement the data tasks", Resumed: true, ResumedFrom: "toolu_fg"},
		{ChildID: "toolu_send1", AgentType: "general-purpose", Model: "claude-sonnet-5", Summary: "Fixed both.", Usage: sonnet(30, 4),
			ToolUses: 1, DurationMs: 4000, Resumed: true, ResumedFrom: "toolu_fg"},
		// the original ran in the background: so does its resumed run, as started
		{ChildID: "toolu_send2", AgentType: "Explore", Summary: "Watch the build", Background: true, Resumed: true, ResumedFrom: "toolu_bgx"},
		{ChildID: "toolu_send2", AgentType: "Explore", Model: "claude-haiku-5", Summary: `Agent "Watch the build" finished`, Usage: sonnet(5, 1),
			DurationMs: 1500, Resumed: true, ResumedFrom: "toolu_bgx"},
		{ChildID: "toolu_send3", AgentType: "general-purpose", Summary: "Implement the data tasks", Resumed: true, ResumedFrom: "toolu_fg"},
		// the hand-back: its summary is the report after the CLI's JSON head
		{ChildID: "toolu_send3", AgentType: "general-purpose", Model: "claude-sonnet-5", Summary: "Renamed it.\nThe file is now b.", Usage: sonnet(8, 3),
			ToolUses: 1, Resumed: true, ResumedFrom: "toolu_fg"},
		{ChildID: "toolu_send6", AgentType: "general-purpose", Summary: "resumed agent", Resumed: true},
		{ChildID: "toolu_send7", AgentType: "Explore", Summary: "Watch the build", Background: true, Resumed: true, ResumedFrom: "toolu_bgx"},
		// the notice's token figure is the agent's whole life's (214948): not this run's usage
		{ChildID: "toolu_send7", AgentType: "Explore", Summary: `Agent "Watch the build" finished`, ToolUses: 4, DurationMs: 211000,
			Resumed: true, ResumedFrom: "toolu_bgx"},
	}
	if !reflect.DeepEqual(subs, wantSubs) {
		t.Fatalf("resumed payloads:\n got  %+v\n want %+v", subs, wantSubs)
	}
	// The foreground report's summary leaves out the trailer the CLI adds for the main agent.
	for _, ev := range events {
		if ev.Kind == "subagent_done" && ev.Payload.(agent.SubagentPayload).ChildID == "toolu_fg" {
			if s := ev.Payload.(agent.SubagentPayload).Summary; s != "All five tasks are in." {
				t.Errorf("foreground summary = %q", s)
			}
		}
	}
}

func TestResumedPayloadJSON(t *testing.T) {
	raw, _ := json.Marshal(agent.SubagentPayload{ChildID: "s", AgentType: "Explore", Summary: "t", Background: true, Resumed: true, ResumedFrom: "a"})
	if string(raw) != `{"effort":"","child_id":"s","agent_type":"Explore","model":"","summary":"t","background":true,"resumed":true,"resumed_from":"a"}` {
		t.Fatalf("payload = %s", raw)
	}
}

// resumeDriver is a driver that knows one finished foreground subagent (agentId "a1b2c3d4e5f6a7b80",
// started by call "toolu_fg").
func resumeDriver(t *testing.T) (*claudeCodeDriver, *collectEmitter) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	d.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	d.startSubagent("toolu_fg", json.RawMessage(`{"description":"Fix it","subagent_type":"general-purpose"}`))
	d.endSubagent("toolu_fg", "Fixed.\nagentId: a1b2c3d4e5f6a7b80 (use SendMessage with to: 'a1b2c3d4e5f6a7b80' to continue this agent)", false)
	return d, em
}

func resume(d *claudeCodeDriver, call string) {
	d.noteSend(call, json.RawMessage(`{"to":"a1b2c3d4e5f6a7b80","message":"again"}`))
	d.sendResult(call, `{"success":true,"message":"Resuming agent a1b2c3d","resumedAgentId":"a1b2c3d4e5f6a7b80"}`, false)
	d.endSubagent(call, "", false)
}

// A resumed run that is open when the engine ends is reported stopped with the rest; it is in the
// recovery record (with its agentId) while it runs; it keeps an idle process like a background task;
// and a message to it while it runs (queued, not a resume) opens nothing more.
func TestResumedRunOpenWorkAndEngineEnd(t *testing.T) {
	d, em := resumeDriver(t)
	var open []OpenSubagent
	d.onOpenWork = func(_ []agent.BackgroundTaskStartedPayload, subs []OpenSubagent) { open = subs }
	resume(d, "toolu_send1")
	fg := false
	if want := []OpenSubagent{{ChildID: "toolu_send1", AgentType: "general-purpose", Summary: "Fix it", AgentID: "a1b2c3d4e5f6a7b80", Background: &fg}}; !reflect.DeepEqual(open, want) {
		t.Fatalf("open work = %+v, want %+v", open, want)
	}
	if n := d.backgroundRunning(); n != 1 {
		t.Fatalf("backgroundRunning = %d, want 1", n)
	}
	d.noteSend("toolu_send2", json.RawMessage(`{"to":"a1b2c3d4e5f6a7b80","message":"also"}`))
	d.sendResult("toolu_send2", `{"success":true,"message":"Message queued for delivery to a1b2c3d4e5f6a7b80 at its next tool round."}`, false)
	d.endBackgroundTasks()
	if len(open) != 0 {
		t.Fatalf("open work after the engine's end = %+v", open)
	}
	var got []agent.SubagentPayload
	for _, ev := range em.snapshot() {
		if (ev.Kind == "subagent_started" || ev.Kind == "subagent_done") && ev.Payload.(agent.SubagentPayload).ChildID != "toolu_fg" {
			got = append(got, ev.Payload.(agent.SubagentPayload))
		}
	}
	want := []agent.SubagentPayload{
		{ChildID: "toolu_send1", AgentType: "general-purpose", Summary: "Fix it", Resumed: true, ResumedFrom: "toolu_fg"},
		{ChildID: "toolu_send1", AgentType: "general-purpose", Summary: "stopped: the engine process ended", Resumed: true, ResumedFrom: "toolu_fg"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events:\n got  %+v\n want %+v", got, want)
	}
	// The agent stays resumable by a later process.
	resume(d, "toolu_send3")
	if n := d.backgroundRunning(); n != 1 {
		t.Fatalf("after a second resume backgroundRunning = %d, want 1", n)
	}
}

// A queued message to a subagent this driver thinks has ended is the sign it runs again; to an
// unknown name it is nothing.
func TestQueuedMessageToAnEndedSubagentOpensARun(t *testing.T) {
	d, em := resumeDriver(t)
	d.noteSend("toolu_q", json.RawMessage(`{"to":"a1b2c3d4e5f6a7b80","message":"also"}`))
	d.sendResult("toolu_q", "Message queued for delivery to a1b2c3d4e5f6a7b80 at its next tool round.", false)
	d.noteSend("toolu_x", json.RawMessage(`{"to":"reviewer","message":"hi"}`))
	d.sendResult("toolu_x", "Message queued for delivery to reviewer at its next tool round.", false)
	var started []string
	for _, ev := range em.snapshot() {
		if ev.Kind == "subagent_started" {
			started = append(started, ev.Payload.(agent.SubagentPayload).ChildID)
		}
	}
	if !slices.Equal(started, []string{"toolu_fg", "toolu_q"}) {
		t.Fatalf("started = %v", started)
	}
}

func TestReadSendReadsResults(t *testing.T) {
	for _, c := range []struct {
		out, name, report string
		kind              int
	}{
		{`{"success":true,"message":"Resuming agent a6f2d88","resumedAgentId":"a6f2d881a4a5e2727","pin":{"id":"x"}}`, "a6f2d881a4a5e2727", "", sendResumed},
		{`{"success":true,"message":"Resuming agent a6f2d88"}`, "a6f2d88", "", sendResumed},
		{`{"success":true,"message":"Message queued for delivery to a6fe90c6258de0cde at its next tool round."}`, "a6fe90c6258de0cde", "", sendQueued},
		{"Message queued for delivery to a6fe90c6258de0cde at its next tool round.", "a6fe90c6258de0cde", "", sendQueued},
		{`{"success":true,"message":"Resumed agent. Its final report is not in this message."}` + "\nRenamed it.\nThe file is now b.", "", "Renamed it.\nThe file is now b.", sendHandBack},
		{`{"success":true,"message":"a6f2d881a4a5e2727 is already waking; message queued for its next tool round (not delivered if the agent turns out to have been stopped)."}`, "", "", sendOther},
		{`{"success":false,"message":"Resuming agent a6f2d88 failed"}`, "", "", sendOther},
		{`{"success":true,"message":"Message queued for the main conversation's next turn."}`, "", "", sendOther},
		{"Renamed it.", "", "", sendOther},
	} {
		kind, name, report := readSend(c.out)
		if kind != c.kind || name != c.name || report != c.report {
			t.Errorf("readSend(%q) = %d %q %q, want %d %q %q", c.out, kind, name, report, c.kind, c.name, c.report)
		}
	}
}

func startedPayloads(em *collectEmitter, kind string) []agent.SubagentPayload {
	var out []agent.SubagentPayload
	for _, ev := range em.snapshot() {
		if ev.Kind == kind {
			out = append(out, ev.Payload.(agent.SubagentPayload))
		}
	}
	return out
}

// After a daemon restart the driver knows the background subagents the recovery record kept: the
// agent's resume of one (as the recovery notice tells it to) opens a run with its title.
func TestResumeAfterARestartUsesTheRecoveryRecord(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	d.seedKnownAgents([]OpenSubagent{
		{ChildID: "toolu_bg", AgentType: "Explore", Model: "haiku", Summary: "Watch the build", AgentID: "a9f8e7d6c5b4a3920"},
		{ChildID: "toolu_fg", Summary: "cut off in the foreground"}, // no agentId: nothing to resume by
	})
	d.noteSend("toolu_s", json.RawMessage(`{"to":"a9f8e7d6c5b4a3920","message":"carry on"}`))
	d.sendResult("toolu_s", `{"success":true,"message":"Resuming agent a9f8e7d","resumedAgentId":"a9f8e7d6c5b4a3920"}`, false)
	want := []agent.SubagentPayload{{ChildID: "toolu_s", AgentType: "Explore", Model: "haiku", Summary: "Watch the build",
		Background: true, Resumed: true, ResumedFrom: "toolu_bg"}}
	if got := startedPayloads(em, "subagent_started"); !reflect.DeepEqual(got, want) {
		t.Fatalf("started:\n got  %+v\n want %+v", got, want)
	}
}

// A resume the driver cannot match to a subagent it knows (one from before a restart that the
// record did not keep) still opens a run, "resumed agent", and a later resume of it is known.
func TestResumeOfAnUnknownAgentStillOpensARun(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	d.noteSend("toolu_s1", json.RawMessage(`{"to":"a5555555555555555","message":"again"}`))
	d.sendResult("toolu_s1", `{"success":true,"message":"Resuming agent a555555","resumedAgentId":"a5555555555555555"}`, false)
	d.closeNotified(noticeFromBlock("<task-id>a5555555555555555</task-id><tool-use-id>toolu_s1</tool-use-id><status>completed</status><result>ok</result>"))
	d.noteSend("toolu_s2", json.RawMessage(`{"to":"a5555555555555555","message":"and again"}`))
	d.sendResult("toolu_s2", `{"success":true,"message":"Resuming agent a555555"}`, false)
	got := startedPayloads(em, "subagent_started")
	want := []agent.SubagentPayload{
		{ChildID: "toolu_s1", AgentType: "general-purpose", Summary: "resumed agent", Resumed: true},
		{ChildID: "toolu_s2", AgentType: "general-purpose", Summary: "resumed agent", Resumed: true, ResumedFrom: "toolu_s1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("started:\n got  %+v\n want %+v", got, want)
	}
}

// A SendMessage that waits for the run and returns its report, with no line of the run seen before:
// the run opens and ends with that report; its agentId no longer names an open run.
func TestHandBackWithoutLinesOpensAndEndsARun(t *testing.T) {
	d, em := resumeDriver(t)
	d.noteSend("toolu_h", json.RawMessage(`{"to":"a1b2c3d4e5f6a7b80","message":"one more"}`))
	out := `{"success":true,"message":"Resumed agent. Its final report is not in this message."}` + "\nDone: renamed."
	d.sendResult("toolu_h", out, false)
	d.endSubagent("toolu_h", out, false)
	done := startedPayloads(em, "subagent_done")
	if last := done[len(done)-1]; last.ChildID != "toolu_h" || last.Summary != "Done: renamed." || !last.Resumed {
		t.Fatalf("done = %+v", last)
	}
	d.subMu.Lock()
	defer d.subMu.Unlock()
	if call, ok := d.agentIDs["a1b2c3d4e5f6a7b80"]; ok {
		t.Fatalf("agentIDs still names %q", call)
	}
}

// known keeps the newest ccMaxKnownAgents: past it the oldest goes, not all of them.
func TestKnownAgentsDropTheOldest(t *testing.T) {
	d, _ := resumeDriver(t)
	s := &ccSubagent{p: agent.SubagentPayload{Summary: "x"}}
	d.subMu.Lock()
	defer d.subMu.Unlock()
	for i := 0; i < ccMaxKnownAgents; i++ {
		d.rememberAgentLocked(fmt.Sprintf("agent-%04d", i), fmt.Sprintf("call-%d", i), s, false)
	}
	if len(d.known) != ccMaxKnownAgents || d.known["a1b2c3d4e5f6a7b80"] != nil || d.known["agent-0000"] == nil {
		t.Fatalf("known: %d entries, first kept %v", len(d.known), d.known["agent-0000"] != nil)
	}
}

// The recovery record says how each cut-off run ran: a resumed run of a foreground subagent is seeded
// as foreground, so its resume after the restart is drawn as one; a record older than the field
// seeds an agent as background, as every subagent with an agentId then was.
func TestRecoveryRecordKeepsWhetherARunRanInTheBackground(t *testing.T) {
	d, _ := resumeDriver(t)
	var open []OpenSubagent
	d.onOpenWork = func(_ []agent.BackgroundTaskStartedPayload, subs []OpenSubagent) { open = subs }
	resume(d, "toolu_send1")
	fg := false
	if want := []OpenSubagent{{ChildID: "toolu_send1", AgentType: "general-purpose", Summary: "Fix it", AgentID: "a1b2c3d4e5f6a7b80", Background: &fg}}; !reflect.DeepEqual(open, want) {
		t.Fatalf("open work = %+v, want %+v", open, want)
	}
	em := &collectEmitter{}
	d2 := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	d2.seedKnownAgents(append(open, OpenSubagent{ChildID: "toolu_old", Summary: "Old record", AgentID: "a0000000000000old"}))
	for _, c := range []struct{ call, to string }{{"toolu_r1", "a1b2c3d4e5f6a7b80"}, {"toolu_r2", "a0000000000000old"}} {
		d2.noteSend(c.call, json.RawMessage(`{"to":"`+c.to+`"}`))
		d2.sendResult(c.call, `{"success":true,"message":"Resuming agent x","resumedAgentId":"`+c.to+`"}`, false)
	}
	got := startedPayloads(em, "subagent_started")
	if len(got) != 2 || got[0].Background || !got[1].Background {
		t.Fatalf("started = %+v", got)
	}
}

// A resumed run of a background subagent is a background task, as the subagent's launch was: it
// starts when its SendMessage call returns (task id: the call's id, kind "agent", its title), it is
// in the recovery record's open tasks, the count of running tasks goes up with it and back down at
// its notice, in either form. A resumed run of a foreground subagent is not a task.
func TestResumedBackgroundRunIsABackgroundTask(t *testing.T) {
	f, err := os.Open("testdata/claude_subagent_resume.jsonl")
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
	var openTasks []agent.BackgroundTaskStartedPayload
	d.onOpenWork = func(tasks []agent.BackgroundTaskStartedPayload, _ []OpenSubagent) { openTasks = tasks }
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	counts := map[string]int{} // the running count after the line that names each call
	var whileSend2 []agent.BackgroundTaskStartedPayload
	for sc.Scan() {
		d.onLine(p, sc.Bytes())
		line := sc.Text()
		for _, mark := range []string{
			`"tool_use_id": "toolu_send2", "content"`, `"tool_use_id": "toolu_send2", "status"`,
			`"tool_use_id": "toolu_send7", "content"`, `"tool_use_id": "toolu_send7", "status"`,
			`"tool_use_id": "toolu_send1", "content"`,
		} {
			if strings.Contains(line, mark) {
				counts[mark] = d.backgroundRunning()
				if mark == `"tool_use_id": "toolu_send2", "content"` {
					whileSend2 = openTasks
				}
			}
		}
	}
	for mark, want := range map[string]int{
		`"tool_use_id": "toolu_send1", "content"`: 1, // a foreground subagent's run: counted, no task
		`"tool_use_id": "toolu_send2", "content"`: 1, `"tool_use_id": "toolu_send2", "status"`: 0,
		// the unknown agent's run (toolu_send6) goes on: it and toolu_send7 make two, then one
		`"tool_use_id": "toolu_send7", "content"`: 2, `"tool_use_id": "toolu_send7", "status"`: 1,
	} {
		if counts[mark] != want {
			t.Errorf("running after %s = %d, want %d", mark, counts[mark], want)
		}
	}
	want := []agent.BackgroundTaskStartedPayload{{TaskID: "toolu_send2", CallID: "toolu_send2", Kind: "agent", Description: "Watch the build", StartedAt: fixtureStart}}
	if !reflect.DeepEqual(whileSend2, want) {
		t.Errorf("open tasks while the run goes on:\n got  %+v\n want %+v", whileSend2, want)
	}
	var got []string
	for _, ev := range em.snapshot() {
		switch pl := ev.Payload.(type) {
		case agent.BackgroundTaskStartedPayload:
			got = append(got, "started:"+pl.TaskID+":"+pl.Kind+":"+pl.Description)
		case agent.BackgroundTaskDonePayload:
			got = append(got, "done:"+pl.TaskID+":"+pl.Status)
		}
	}
	if w := []string{
		"started:a9f8e7d6c5b4a3920:agent:Watch the build", "done:a9f8e7d6c5b4a3920:completed", // the launch
		"started:toolu_send2:agent:Watch the build", "done:toolu_send2:completed",
		"started:toolu_send7:agent:Watch the build", "done:toolu_send7:completed",
	}; !slices.Equal(got, w) {
		t.Fatalf("task events:\n got  %q\n want %q", got, w)
	}
}

// Alongside a background subagent still running, a resumed run of another makes two, and its
// notice — even one naming the agent alone — leaves one.
func TestResumedBackgroundRunCountsWithTheOthers(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	d.now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	d.startSubagent("toolu_a", json.RawMessage(`{"description":"Watch the build","run_in_background":true}`))
	d.endSubagent("toolu_a", "Async agent launched successfully.\nagentId: a0000000000000aaa", false)
	d.startSubagent("toolu_b", json.RawMessage(`{"description":"Read the docs","run_in_background":true}`))
	d.endSubagent("toolu_b", "Async agent launched successfully.\nagentId: a0000000000000bbb", false)
	d.closeNotified(noticeFromBlock("<task-id>a0000000000000bbb</task-id><tool-use-id>toolu_b</tool-use-id><status>completed</status>"))
	if n := d.backgroundRunning(); n != 1 {
		t.Fatalf("running = %d, want 1", n)
	}
	d.noteSend("toolu_s", json.RawMessage(`{"to":"a0000000000000bbb","message":"again"}`))
	d.sendResult("toolu_s", `{"success":true,"message":"Resuming agent a000000","resumedAgentId":"a0000000000000bbb"}`, false)
	d.endSubagent("toolu_s", "", false)
	if n := d.backgroundRunning(); n != 2 {
		t.Fatalf("running after the resume = %d, want 2", n)
	}
	d.closeNotified(ccNotice{taskID: "a0000000000000bbb", status: "completed"})
	if n := d.backgroundRunning(); n != 1 {
		t.Fatalf("running after its notice = %d, want 1", n)
	}
	d.subMu.Lock()
	defer d.subMu.Unlock()
	if d.subagents["toolu_s"] != nil || d.bgTasks["toolu_s"] != nil {
		t.Fatal("the run's notice left it open")
	}
}
