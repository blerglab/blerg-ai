package daemon

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// backgroundEvents keeps the background_task_* events of a run, in order.
func backgroundEvents(events []agent.Event) []any {
	var out []any
	for _, ev := range events {
		if ev.Kind == "background_task_started" || ev.Kind == "background_task_done" {
			out = append(out, ev.Payload)
		}
	}
	return out
}

// feedFixture's clock, as background_task_started reports it.
const fixtureStart = "2027-01-15T08:00:00Z"

// A background Bash call and a Monitor start a task each once their result names it; a
// foreground Bash call, a background call whose result names no task (a refusal) and a notice for
// a task nobody here started report nothing. A monitor's own events do not end it; its expiry
// does. A notice that names only the call (an unknown task id) still ends its task. A Bash call
// with no description is described by the start of its command.
func TestBackgroundTasksFromBashAndMonitor(t *testing.T) {
	got := backgroundEvents(feedFixture(t, "claude_background_tasks.jsonl"))
	want := []any{
		agent.BackgroundTaskStartedPayload{TaskID: "bu90kf38e", CallID: "toolu_sh", Kind: "bash", Description: "Wait for the deploy", Command: "until curl -s http://example.test/ready; do sleep 20; done", StartedAt: fixtureStart},
		agent.BackgroundTaskStartedPayload{TaskID: "bs3tbt4gf", CallID: "toolu_mon", Kind: "monitor", Description: "Watch the build log", Command: "tail -f /tmp/build.log | grep --line-buffered ERROR", StartedAt: fixtureStart},
		agent.BackgroundTaskStartedPayload{TaskID: "b2longcmd", CallID: "toolu_sh2", Kind: "bash",
			Description: "while true; do date; sleep 600; done # a long command that goes on well past eig",
			Command:     "while true; do date; sleep 600; done # a long command that goes on well past eighty characters", StartedAt: fixtureStart},
		agent.BackgroundTaskDonePayload{TaskID: "bu90kf38e", CallID: "toolu_sh", Kind: "bash", Status: "completed",
			Summary: `Background command "Wait for the deploy" completed (exit code 0)`, Description: "Wait for the deploy"},
		agent.BackgroundTaskDonePayload{TaskID: "b2longcmd", CallID: "toolu_sh2", Kind: "bash", Status: "failed",
			Summary:     `Background command "while true" failed with exit code 1`,
			Description: "while true; do date; sleep 600; done # a long command that goes on well past eig"},
		agent.BackgroundTaskDonePayload{TaskID: "bs3tbt4gf", CallID: "toolu_mon", Kind: "monitor", Status: "expired",
			Summary: `Monitor event: "Watch the build log"`, Description: "Watch the build log"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("background events:\n got  %+v\n want %+v", got, want)
	}
}

// A background subagent is a background task too: started (kind agent, its agentId, its
// description) when its launch returns, done when its notification ends it, beside the subagent
// card's own events.
func TestBackgroundTasksFromBackgroundAgent(t *testing.T) {
	got := backgroundEvents(feedFixture(t, "claude_background.jsonl"))
	want := []any{
		agent.BackgroundTaskStartedPayload{TaskID: "a5ef89e0e0329265b", CallID: "toolu_bg", Kind: "agent", Description: "Watch the build", StartedAt: fixtureStart},
		agent.BackgroundTaskStartedPayload{TaskID: "a0000000000000002", CallID: "toolu_bg2", Kind: "agent", Description: "Read the docs", StartedAt: fixtureStart},
		agent.BackgroundTaskDonePayload{TaskID: "a5ef89e0e0329265b", CallID: "toolu_bg", Kind: "agent", Status: "completed",
			Summary: `Agent "Watch the build" completed`, Description: "Watch the build"},
		// no <summary>: the result stands in
		agent.BackgroundTaskDonePayload{TaskID: "a0000000000000002", CallID: "toolu_bg2", Kind: "agent", Status: "failed",
			Summary: "Could not read them.", Description: "Read the docs"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("background events:\n got  %+v\n want %+v", got, want)
	}
}

// The events go on the wire with the field names the chat reads.
func TestBackgroundTaskPayloadJSON(t *testing.T) {
	raw, _ := json.Marshal(agent.BackgroundTaskStartedPayload{TaskID: "b1", CallID: "c1", Kind: "bash", Description: "d", StartedAt: fixtureStart})
	if string(raw) != `{"task_id":"b1","call_id":"c1","kind":"bash","description":"d","started_at":"2027-01-15T08:00:00Z"}` {
		t.Fatalf("started = %s", raw)
	}
	raw, _ = json.Marshal(agent.BackgroundTaskDonePayload{TaskID: "b1", CallID: "c1", Kind: "bash", Status: "failed", Summary: "s"})
	if string(raw) != `{"task_id":"b1","call_id":"c1","kind":"bash","status":"failed","summary":"s"}` {
		t.Fatalf("done = %s", raw)
	}
}

// When the engine process ends, the tasks it was running in the background end with it: each is
// reported done as stopped, oldest first, and a later notice for one of them reports nothing.
func TestBackgroundTasksEndWithTheProcess(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	now := time.Unix(1_800_000_000, 0)
	d.now = func() time.Time { return now }
	d.noteBackgroundCall("c1", "Monitor", json.RawMessage(`{"command":"tail -f log"}`))
	d.startBackgroundTask("c1", "Monitor started (task bm1, expires in 5m).", false)
	now = now.Add(time.Second)
	d.noteBackgroundCall("c2", "Bash", json.RawMessage(`{"command":"sleep 99","description":"Nap","run_in_background":true}`))
	d.startBackgroundTask("c2", "Command running in background with ID: bs2. Output is being written to: x", false)
	d.endBackgroundTasks()
	d.closeBackgroundTask(noticeFromBlock("<task-id>bs2</task-id><status>completed</status>"))
	var done []agent.BackgroundTaskDonePayload
	for _, ev := range em.snapshot() {
		if ev.Kind == "background_task_done" {
			done = append(done, ev.Payload.(agent.BackgroundTaskDonePayload))
		}
	}
	want := []agent.BackgroundTaskDonePayload{
		{TaskID: "bm1", CallID: "c1", Kind: "monitor", Status: "stopped", Summary: "the engine process ended", Description: "tail -f log"},
		{TaskID: "bs2", CallID: "c2", Kind: "bash", Status: "stopped", Summary: "the engine process ended", Description: "Nap"},
	}
	if !reflect.DeepEqual(done, want) {
		t.Fatalf("done:\n got  %+v\n want %+v", done, want)
	}
}

// startTestTask makes the driver know of one running background task, as a Bash call's result would.
func startTestTask(d *claudeCodeDriver, id string) {
	d.noteBackgroundCall("call-"+id, "Bash", json.RawMessage(`{"command":"sleep 999","description":"Nap","run_in_background":true}`))
	d.startBackgroundTask("call-"+id, "Command running in background with ID: "+id+". Output is being written to: x", false)
}

func setDeferrals(t *testing.T, n int) {
	t.Helper()
	old := ccIdleMaxDeferrals
	ccIdleMaxDeferrals = n
	t.Cleanup(func() { ccIdleMaxDeferrals = old })
}

// An idle process running a background task is not closed by the idle shutdown (closing it would end
// the task); after ccIdleMaxDeferrals waits it is closed anyway and the task is reported stopped.
func TestIdleShutdownWaitsForBackgroundTasksUpToTheCap(t *testing.T) {
	shrink(t, &ccIdleShutdown, 200*time.Millisecond)
	setDeferrals(t, 3)
	d, em, logPath := newSteeringDriver(t)
	d.Enqueue("one", "chat")
	waitIdle(t, em, 1)
	startTestTask(d, "b1")
	time.Sleep(500 * time.Millisecond) // past two idle periods
	if contains(logLines(t, logPath), "eof") {
		t.Fatal("the idle shutdown closed a process running a background task")
	}
	waitUntil(t, "the idle shutdown after the cap", func() bool { return contains(logLines(t, logPath), "eof") })
	waitUntil(t, "the task reported stopped", func() bool {
		for _, ev := range em.snapshot() {
			if p, ok := ev.Payload.(agent.BackgroundTaskDonePayload); ok && p.TaskID == "b1" && p.Status == "stopped" {
				return true
			}
		}
		return false
	})
}

// When the last background task ends, an idle process gets the ordinary idle shutdown again, counted
// from the task's end.
func TestIdleShutdownResumesWhenTheLastBackgroundTaskEnds(t *testing.T) {
	shrink(t, &ccIdleShutdown, 200*time.Millisecond)
	setDeferrals(t, 1000)
	d, em, logPath := newSteeringDriver(t)
	d.Enqueue("one", "chat")
	waitIdle(t, em, 1)
	startTestTask(d, "b1")
	startTestTask(d, "b2")
	time.Sleep(500 * time.Millisecond)
	d.closeBackgroundTask(noticeFromBlock("<task-id>b1</task-id><status>completed</status>"))
	time.Sleep(300 * time.Millisecond)
	if contains(logLines(t, logPath), "eof") {
		t.Fatal("closed while a background task still ran")
	}
	d.closeBackgroundTask(noticeFromBlock("<task-id>b2</task-id><status>completed</status>"))
	ended := time.Now()
	waitUntil(t, "the idle shutdown after the last task ended", func() bool { return contains(logLines(t, logPath), "eof") })
	if since := time.Since(ended); since < 150*time.Millisecond {
		t.Fatalf("closed %v after the task ended, want a full idle period", since)
	}
}

// A task that ends while the session is idle is reported by CLI 2.1.290 as a system line
// (subtype task_notification: task_id, tool_use_id, status, summary, an agent's usage), with no
// <task-notification> user line at all. It ends its task just as the user-line form does: a shell,
// a monitor and a background subagent each get background_task_done, a notice for a task nobody here
// started ends nothing, and the same task's notice seen again in the other form is not reported twice.
func TestBackgroundTasksEndedBySystemNotices(t *testing.T) {
	events := feedFixture(t, "claude_background_system_notices.jsonl")
	var done []agent.BackgroundTaskDonePayload
	for _, ev := range events {
		if p, ok := ev.Payload.(agent.BackgroundTaskDonePayload); ok {
			done = append(done, p)
		}
	}
	want := []agent.BackgroundTaskDonePayload{
		{TaskID: "b94r3e0mp", CallID: "toolu_sh", Kind: "bash", Status: "completed",
			Summary: `Background command "sleep 4 ; echo done" completed (exit code 0)`, Description: "Wait for the deploy"},
		{TaskID: "a132a055ed2edcceb", CallID: "toolu_bg", Kind: "agent", Status: "completed",
			Summary: `Agent "Review the change" finished`, Description: "Review the change"},
		{TaskID: "bt2xk9q1z", CallID: "toolu_sh2", Kind: "bash", Status: "failed",
			Summary: `Background command "Run the tests" failed with exit code 2`, Description: "Run the tests"},
		{TaskID: "bm4w8d2pc", CallID: "toolu_mon", Kind: "monitor", Status: "stopped",
			Summary: `Monitor "Watch the build log" stopped`, Description: "Watch the build log"},
	}
	if !reflect.DeepEqual(done, want) {
		t.Fatalf("background_task_done:\n got  %+v\n want %+v", done, want)
	}
}

// The background subagent a system notice ends gets its final subagent_done: the notice's summary
// (it carries no result), its usage standing in for the totals nothing accumulated, its tool uses
// and duration.
func TestBackgroundSubagentEndedBySystemNotice(t *testing.T) {
	var done []agent.SubagentPayload
	for _, ev := range feedFixture(t, "claude_background_system_notices.jsonl") {
		if ev.Kind == "subagent_done" {
			done = append(done, ev.Payload.(agent.SubagentPayload))
		}
	}
	want := []agent.SubagentPayload{
		{ChildID: "toolu_bg", AgentType: "general-purpose", Summary: "running in the background", Background: true},
		{ChildID: "toolu_bg", AgentType: "general-purpose", Summary: `Agent "Review the change" finished`,
			Usage: &agent.Usage{OutputTokens: 26941}, ToolUses: 4, DurationMs: 14037},
	}
	if !reflect.DeepEqual(done, want) {
		t.Fatalf("subagent_done:\n got  %+v\n want %+v", done, want)
	}
}

// The other order: a task's notice reaches a turn in progress as a user line first, and a system
// line for the same task follows; the task is reported done once. A system line with no status (a
// monitor's event) does not end a monitor that is still watching.
func TestBackgroundTaskNoticeUserLineThenSystemLine(t *testing.T) {
	var done []agent.BackgroundTaskDonePayload
	for _, ev := range feedFixture(t, "claude_background_notice_order.jsonl") {
		if p, ok := ev.Payload.(agent.BackgroundTaskDonePayload); ok {
			done = append(done, p)
		}
	}
	want := []agent.BackgroundTaskDonePayload{
		{TaskID: "b94r3e0mp", CallID: "toolu_sh", Kind: "bash", Status: "completed",
			Summary: `Background command "Nap" completed (exit code 0)`, Description: "Nap"},
	}
	if !reflect.DeepEqual(done, want) {
		t.Fatalf("background_task_done:\n got  %+v\n want %+v", done, want)
	}
}

// A started shell task carries its command (cut to 500 runes) beside the description.
func TestBackgroundTaskStartCarriesTheCommand(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	long := strings.Repeat("x", 600)
	d.noteBackgroundCall("c1", "Bash", json.RawMessage(`{"command":"bash run.sh","description":"Run it","run_in_background":true}`))
	d.startBackgroundTask("c1", "Command running in background with ID: bs1. Output is being written to: x", false)
	d.noteBackgroundCall("c2", "Bash", json.RawMessage(`{"command":"`+long+`","run_in_background":true}`))
	d.startBackgroundTask("c2", "Command running in background with ID: bs2. Output is being written to: x", false)
	var got []agent.BackgroundTaskStartedPayload
	for _, ev := range em.snapshot() {
		if ev.Kind == "background_task_started" {
			got = append(got, ev.Payload.(agent.BackgroundTaskStartedPayload))
		}
	}
	if len(got) != 2 || got[0].Command != "bash run.sh" || got[0].Description != "Run it" || len([]rune(got[1].Command)) > 500 || got[1].Command == "" {
		t.Fatalf("started = %+v", got)
	}
}
