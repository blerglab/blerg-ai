package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
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
func TestBackgroundTasksEndAfterConfirmedCleanup(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	now := time.Unix(1_800_000_000, 0)
	d.now = func() time.Time { return now }
	d.noteBackgroundCall("c1", "Monitor", json.RawMessage(`{"command":"tail -f log"}`))
	d.startBackgroundTask("c1", "", "Monitor started (task bm1, expires in 5m).", false)
	now = now.Add(time.Second)
	d.noteBackgroundCall("c2", "Bash", json.RawMessage(`{"command":"sleep 99","description":"Nap","run_in_background":true}`))
	d.startBackgroundTask("c2", "", "Command running in background with ID: bs2. Output is being written to: x", false)
	d.endBackgroundTasks(false)
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
	d.startBackgroundTask("call-"+id, "", "Command running in background with ID: "+id+". Output is being written to: x", false)
}

// A background command the engine still reports as running keeps the process for as long as it runs,
// well past any number of idle periods (BLG-375: the old limit of 6 ended a live 1 h test gate at 70
// minutes). The real command finishes, its end is reported, and the engine's own turn runs.
func TestIdleShutdownNeverEndsLiveBackgroundWork(t *testing.T) {
	shrink(t, &ccIdleShutdown, 50*time.Millisecond)
	marker := filepath.Join(t.TempDir(), "gate-finished")
	d, em, logPath := newSteeringDriver(t, "FAKE_BG_SECONDS=1.5", "FAKE_BG_MARKER="+marker)
	d.Enqueue("REALBG run the gate", "chat")
	waitIdle(t, em, 1)
	waitUntil(t, "the real background command to finish", func() bool { _, err := os.Stat(marker); return err == nil })
	if contains(logLines(t, logPath), "eof") {
		t.Fatal("the idle shutdown closed a process running a background command")
	}
	waitUntil(t, "the engine's own turn after the command ended", func() bool { return contains(em.outline(), "text:the gate finished") })
	if n := countKind(em, "background_task_done"); n != 1 {
		t.Fatalf("background_task_done = %d, want 1", n)
	}
	waitUntil(t, "the idle shutdown once the command ended", func() bool { return contains(logLines(t, logPath), "eof") })
}

// A model change is applied at the next idle point, but never at the price of live background work: the
// process stays until the work ends, and the engine's wake-up turn for it is not cut off.
func TestStaleProcessWaitsForLiveBackgroundWork(t *testing.T) {
	shrink(t, &ccIdleShutdown, 100*time.Millisecond)
	marker := filepath.Join(t.TempDir(), "gate-finished")
	d, em, logPath := newSteeringDriver(t, "FAKE_BG_SECONDS=0.8", "FAKE_BG_MARKER="+marker)
	d.Enqueue("REALBG run the gate", "chat")
	waitIdle(t, em, 1)
	d.markStale()
	time.Sleep(400 * time.Millisecond)
	if contains(logLines(t, logPath), "eof") {
		t.Fatal("a model change closed a process running a background command")
	}
	waitUntil(t, "the engine's own turn after the command ended", func() bool { return contains(em.outline(), "text:the gate finished") })
	if _, err := os.Stat(marker); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the stale process to close", func() bool { return contains(logLines(t, logPath), "eof") })
}

// A foreground command the CLI moves to the background (a timeout, a message arriving) is a running
// task like any other. It is told apart by the structured tool_use_result, not the result's text, so a
// command that merely prints such a line starts nothing.
func TestForegroundCommandMovedToBackgroundIsTracked(t *testing.T) {
	d, em, _ := newSteeringDriver(t)
	d.Enqueue("MOVEDBG slow step", "chat")
	waitIdle(t, em, 1)
	if n := d.backgroundRunning(); n != 1 {
		t.Fatalf("running = %d, want 1", n)
	}
	got := backgroundEvents(em.snapshot())
	want := agent.BackgroundTaskStartedPayload{TaskID: "bg-moved", CallID: "moved-call", Kind: "bash", Description: "Run the slow step", Command: "./slow.sh"}
	p, ok := any(nil), false
	if len(got) == 1 {
		p, ok = got[0], true
	}
	started, _ := p.(agent.BackgroundTaskStartedPayload)
	started.StartedAt = ""
	if !ok || started != want {
		t.Fatalf("started = %+v", got)
	}

	spoof := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", &collectEmitter{}, nil)
	spoof.noteBackgroundCall("c1", "Bash", json.RawMessage(`{"command":"cat notes.txt"}`))
	spoof.startBackgroundTask("c1", "", "Command running in background with ID: bspoof. Output is being written to: x", false)
	if spoof.backgroundRunning() != 0 {
		t.Fatal("printed text started a task")
	}
}

func listShells(d *claudeCodeDriver, ids ...string) {
	tasks := []map[string]string{}
	for _, id := range ids {
		tasks = append(tasks, map[string]string{"task_id": id, "task_type": "local_bash", "description": "Shell " + id})
	}
	line, _ := json.Marshal(map[string]any{"type": "system", "subtype": "background_tasks_changed", "tasks": tasks})
	d.onSystem(&ccLine{Subtype: "background_tasks_changed"}, line)
}

// The engine's own list is the live inventory. A shell it lists that no call here named (a subagent's)
// counts as running; a tracked shell it stops listing no longer counts, and a later idle tick reports
// it stopped when no notice ever said how it ended.
func TestEngineListIsTheLiveInventory(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	listShells(d, "bsub")
	if d.backgroundRunning() != 1 {
		t.Fatal("a listed shell was not counted")
	}
	listShells(d)
	if d.backgroundRunning() != 0 {
		t.Fatal("an unlisted shell stayed counted")
	}

	startTestTask(d, "b1")
	listShells(d, "b1")
	listShells(d)
	if d.backgroundRunning() != 0 {
		t.Fatal("a tracked shell the engine stopped listing stayed counted")
	}
	if n := countKind(em, "background_task_done"); n != 0 {
		t.Fatalf("closed before its notice had time to arrive: %d", n)
	}
	d.closeUnlistedTasks()
	if n := countKind(em, "background_task_done"); n != 1 {
		t.Fatalf("background_task_done = %d, want 1", n)
	}

	// Its notice, when it does come first, is what reports the end.
	startTestTask(d, "b2")
	listShells(d, "b2")
	listShells(d)
	d.closeBackgroundTask(noticeFromBlock("<task-id>b2</task-id><status>completed</status>"))
	d.closeUnlistedTasks()
	if n := countKind(em, "background_task_done"); n != 2 {
		t.Fatalf("background_task_done = %d, want 2", n)
	}
}

// When the engine ends unasked while background work runs, nothing is left silent: the agent is woken
// with what was running and the person sees the same row, once.
func TestEngineExitWithLiveWorkTellsTheSessionAndThePerson(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "gate-finished")
	d, em, logPath := newSteeringDriver(t, "FAKE_BG_SECONDS=30", "FAKE_BG_MARKER="+marker)
	d.Enqueue("REALBG run the gate", "chat")
	waitIdle(t, em, 1)
	d.sm.Lock()
	proc := d.steer.proc.cmd.Process
	d.sm.Unlock()
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the engine to be restarted for the news", func() bool { return len(starts(logLines(t, logPath))) == 2 })
	waitUntil(t, "the person to see the row", func() bool {
		for _, line := range em.outline() {
			if strings.HasPrefix(line, "user:The engine process ended while background work was running: Run the gate.") {
				return true
			}
		}
		return false
	})
	waitIdle(t, em, 2)
	time.Sleep(100 * time.Millisecond)
	if n := len(starts(logLines(t, logPath))); n != 2 {
		t.Fatalf("told more than once: %d starts", n)
	}
}

// A shell retained from an ended engine is live again once the replacement engine lists it: the list is
// authoritative, so the idle shutdown waits for it, as it would for any other shell.
func TestRestartedEngineListKeepsARetainedShellLive(t *testing.T) {
	shrink(t, &ccIdleShutdown, 200*time.Millisecond)
	marker := filepath.Join(t.TempDir(), "gate-finished")
	d, em, logPath := newSteeringDriver(t, "FAKE_BG_SECONDS=30", "FAKE_BG_MARKER="+marker, "FAKE_RELIST=bg-real")
	d.Enqueue("REALBG run the gate", "chat")
	waitIdle(t, em, 1)
	d.sm.Lock()
	proc := d.steer.proc.cmd.Process
	d.sm.Unlock()
	if err := proc.Kill(); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, em, 2)
	if n := d.backgroundRunning(); n != 0 {
		t.Fatalf("running = %d before any engine listed the retained shell, want 0", n)
	}
	d.Enqueue("RELIST what runs", "chat")
	waitIdle(t, em, 3)
	if n := d.backgroundRunning(); n != 1 {
		t.Fatalf("running = %d after the new engine listed the shell, want 1", n)
	}
	eofs := func() (n int) {
		for _, l := range logLines(t, logPath) {
			if l == "eof" {
				n++
			}
		}
		return n
	}
	before := eofs()
	time.Sleep(700 * time.Millisecond)
	if eofs() != before {
		t.Fatal("the idle shutdown closed a process whose engine lists a live shell")
	}
}

// A shell that ends at once can leave the engine's list before its call's result names it, with no notice
// to follow. The list's history still counts: the task is over, and the process closes as usual.
func TestShellDroppedFromListBeforeItsResultDoesNotPinTheProcess(t *testing.T) {
	shrink(t, &ccIdleShutdown, 100*time.Millisecond)
	d, em, logPath := newSteeringDriver(t)
	d.Enqueue("FASTBG say hi", "chat")
	waitIdle(t, em, 1)
	waitUntil(t, "the idle shutdown", func() bool { return contains(logLines(t, logPath), "eof") })
	if n := countKind(em, "background_task_done"); n != 1 {
		t.Fatalf("background_task_done = %d, want 1", n)
	}
	if n := d.backgroundRunning(); n != 0 {
		t.Fatalf("running = %d, want 0", n)
	}
}

// A sandboxed task's output path names a file in the container, so its age is read there: the host may
// have no such file, or an unrelated one. The read does not hold the lock the event stream needs.
func TestStallCheckReadsTheOutputInTheSandbox(t *testing.T) {
	out := filepath.Join(t.TempDir(), "only-in-the-sandbox.output")
	var calls [][]string
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", &collectEmitter{}, nil)
	orig := dockerOutput
	t.Cleanup(func() { dockerOutput = orig })
	dockerOutput = func(args ...string) ([]byte, error) {
		calls = append(calls, args)
		if !d.subMu.TryLock() {
			t.Error("the sandbox read held the driver's event lock")
		} else {
			d.subMu.Unlock()
		}
		return []byte(strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10) + "\n"), nil
	}
	d.useSandbox(sandboxExecPrefix("box"))
	d.noteBackgroundCall("c1", "Bash", json.RawMessage(`{"command":"./gate.sh","description":"Run the gate","run_in_background":true}`))
	d.startBackgroundTask("c1", "", "Command running in background with ID: b1. Output is being written to: "+out+". You will be notified.", false)

	d.wakeForStalledTasks()
	d.wakeForStalledTasks()

	want := []string{"exec", "box", "stat", "-L", "-c", "%Y", "--", out}
	if len(calls) != 2 || !slices.Equal(calls[0], want) {
		t.Fatalf("sandbox reads = %q, want twice %q", calls, want)
	}
	if n := len(d.queue); n != 1 {
		t.Fatalf("wake-ups = %d, want 1", n)
	}
	if m := <-d.queue; !strings.Contains(m.Text, "Run the gate") {
		t.Fatalf("the wake-up does not name the task: %q", m.Text)
	}
}

// A task that goes quiet wakes an idle session once, with its name, and again only after it has
// written something and gone quiet anew.
func TestStalledBackgroundTaskWakesTheSessionOnce(t *testing.T) {
	shrink(t, &ccIdleShutdown, 50*time.Millisecond)
	shrink(t, &ccBgStallAfter, time.Minute)
	dir := t.TempDir()
	out := filepath.Join(dir, "task.output")
	if err := os.WriteFile(out, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(out, old, old); err != nil {
		t.Fatal(err)
	}
	d, em, _ := newSteeringDriver(t)
	d.Enqueue("one", "chat")
	waitIdle(t, em, 1)
	d.noteBackgroundCall("c1", "Bash", json.RawMessage(`{"command":"./gate.sh","description":"Run the gate","run_in_background":true}`))
	d.startBackgroundTask("c1", "", "Command running in background with ID: b1. Output is being written to: "+out+". You will be notified.", false)
	d.sm.Lock()
	d.idleLocked(d.steer.proc)
	d.sm.Unlock()
	waitIdle(t, em, 2)
	time.Sleep(300 * time.Millisecond)
	if n := em.count("turn_done"); n != 2 {
		t.Fatalf("woken %d times for one stall", n-1)
	}
	if !strings.Contains(strings.Join(em.outline(), "\n"), "Run the gate") {
		t.Fatalf("the wake-up does not name the task: %v", em.outline())
	}
	now := time.Now()
	if err := os.Chtimes(out, now, now); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if err := os.Chtimes(out, old, old); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, em, 3)
}

// When the last background task ends, an idle process gets the ordinary idle shutdown again, counted
// from the task's end.
func TestIdleShutdownResumesWhenTheLastBackgroundTaskEnds(t *testing.T) {
	shrink(t, &ccIdleShutdown, 200*time.Millisecond)
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

// BLG-447: a worker that ends its turn to wait for its own background job must be resumed when
// that job finishes, the way an interactive Claude Code session is — not left idle until someone
// happens to message it. closeBackgroundTask must not depend solely on Claude Code resuming the
// process on its own: it must start a turn itself, exactly once, naming the task. Unlike
// TestIdleShutdownResumesWhenTheLastBackgroundTaskEnds (which asserts the opposite: that nothing
// wakes the session and the ordinary idle shutdown eventually fires), this closes the only
// running task while idle and expects a new turn before any shutdown.
func TestLastBackgroundTaskEndWakesAnIdleSession(t *testing.T) {
	shrink(t, &ccIdleShutdown, 10*time.Minute) // only the wake-up should start a new turn, not the idle timer
	shrink(t, &ccWakeFallbackAfter, 50*time.Millisecond)
	d, em, _ := newSteeringDriver(t)
	d.Enqueue("one", "chat")
	waitIdle(t, em, 1)
	d.noteBackgroundCall("c1", "Bash", json.RawMessage(`{"command":"./gate.sh","description":"Run the gate","run_in_background":true}`))
	d.startBackgroundTask("c1", "", "Command running in background with ID: b1. Output is being written to: x", false)
	d.closeBackgroundTask(noticeFromBlock("<task-id>b1</task-id><status>completed</status>"))
	waitIdle(t, em, 2)
	if !strings.Contains(strings.Join(em.outline(), "\n"), "Run the gate") {
		t.Fatalf("the wake-up does not name the task: %v", em.outline())
	}
	if n := em.count("turn_done"); n != 2 {
		t.Fatalf("woken %d times for one finished task, want exactly once", n-1)
	}
}

// BLG-447 review (item 6): wakeFallbackFired's idle finding, taken under sm, goes stale the
// instant it releases that lock — a CLI self-resume (markRunning) landing in the gap before the
// actual enqueue must not still queue a second, redundant turn. afterWakeFallbackUnlock (a test
// seam, nil in production) deterministically forces that self-resume into the exact gap, and
// wakeFallbackFired is called directly (not via its real timer) so the whole race lives inside
// one synchronous call with no sleep needed to observe it.
func TestWakeFallbackRecheckedUnderLockBeforeEnqueueing(t *testing.T) {
	shrink(t, &ccIdleShutdown, 10*time.Minute)
	d, em, _ := newSteeringDriver(t)
	d.Enqueue("one", "chat")
	waitIdle(t, em, 1)

	d.afterWakeFallbackUnlock = func() {
		d.markRunning("simulated self-resume racing the fallback")
	}

	d.noteBackgroundCall("c1", "Bash", json.RawMessage(`{"command":"./gate.sh","description":"Run the gate","run_in_background":true}`))
	d.startBackgroundTask("c1", "", "Command running in background with ID: b1. Output is being written to: x", false)
	d.sm.Lock()
	gen := d.steer.proc.gen
	d.sm.Unlock()
	d.closeBackgroundTask(noticeFromBlock("<task-id>b1</task-id><status>completed</status>"))
	d.wakeFallbackFired(gen)

	d.sm.Lock()
	queued := len(d.steer.sq)
	running := d.steer.running
	d.sm.Unlock()
	if queued != 0 {
		t.Fatalf("the fallback enqueued a second, redundant turn despite the self-resume racing it: sq=%d", queued)
	}
	if !running {
		t.Fatal("the simulated self-resume's own running state was lost")
	}
}

// When the CLI resumes the idle session on its own for the task that just ended — REALBG, the
// ordinary case, also verified against the real binary (TestBackgroundWorkOutlivesIdleRealCLI) —
// the fallback armed for the same event must be canceled before it ever fires, not race it into
// a second, redundant turn (BLG-447 code review, finding H). The fallback's window is longer than
// REALBG's own self-wake takes, so passing proves the cancellation won, not that the fallback
// simply never got a chance to run.
func TestFinishedTaskSelfWakeCancelsTheFallback(t *testing.T) {
	shrink(t, &ccWakeFallbackAfter, time.Second)
	marker := filepath.Join(t.TempDir(), "gate-finished")
	d, em, _ := newSteeringDriver(t, "FAKE_BG_SECONDS=0.1", "FAKE_BG_MARKER="+marker)
	d.Enqueue("REALBG run the gate", "chat")
	waitIdle(t, em, 1)
	waitUntil(t, "the real background command to finish", func() bool { _, err := os.Stat(marker); return err == nil })
	// REALBG's own self-wake (initEv, text, result) runs this to idle a second time well inside
	// the fallback's window.
	waitIdle(t, em, 2)
	time.Sleep(2 * time.Second) // past the fallback's window, which must not have survived the self-wake
	if n := em.count("turn_done"); n != 2 {
		t.Fatalf("the canceled fallback still started a turn: %d turn_done, outline %v", n, em.outline())
	}
	if strings.Contains(strings.Join(em.outline(), "\n"), "Background work finished") {
		t.Fatalf("the fallback's own wording leaked even though the CLI resumed itself: %v", em.outline())
	}
}

// With steering off (the per-turn legacy engine), closeBackgroundTask must not arm a fallback at
// all: there is no long-lived process for it to wake, and armWakeFallback's own legacy guard is
// what stops it (BLG-447 code review: "also cover ... steering off").
func TestClosedTaskArmsNoFallbackWithSteeringOff(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil) // steer.legacy defaults true (testmain_test.go)
	d.steer.proc = claudeTestProc(t, d, &ccProc{gen: 1})                // present, so only the legacy guard is being tested
	startTestTask(d, "last")
	d.closeBackgroundTask(noticeFromBlock("<task-id>last</task-id><status>completed</status>"))
	d.sm.Lock()
	timer, pending := d.steer.wakeTimer, d.steer.wakePending
	d.sm.Unlock()
	if timer != nil || pending != nil {
		t.Fatalf("a fallback was armed with steering off: timer=%v pending=%v", timer, pending)
	}
}

// A session ending (shutdown(), the daemon restarting or the session itself stopping) while a
// fallback is armed must drop it: nothing should fire into a session that is no longer there,
// and a later resumed process must not see a duplicate wake-up for a task its predecessor already
// reported (BLG-447 code review: "also cover ... daemon restart with a pending fallback").
func TestSessionEndCancelsAPendingFallback(t *testing.T) {
	shrink(t, &ccWakeFallbackAfter, 3*time.Second) // long enough to still be pending when shutdown runs
	marker := filepath.Join(t.TempDir(), "gate-finished")
	d, em, _ := newSteeringDriver(t, "FAKE_BG_SECONDS=0.1", "FAKE_BG_MARKER="+marker)
	d.Enqueue("NOWAKEBG run the gate", "chat")
	waitIdle(t, em, 1)
	waitUntil(t, "the real background command to finish", func() bool { _, err := os.Stat(marker); return err == nil })
	waitUntil(t, "the fallback armed for the notice-only end", func() bool {
		d.sm.Lock()
		defer d.sm.Unlock()
		return d.steer.wakePending != nil
	})
	d.shutdown()
	d.sm.Lock()
	timer, pending := d.steer.wakeTimer, d.steer.wakePending
	d.sm.Unlock()
	if timer != nil || pending != nil {
		t.Fatalf("shutdown left a fallback armed: timer=%v pending=%v", timer, pending)
	}
	time.Sleep(4 * time.Second) // past the fallback's window, which must not have survived shutdown
	if n := em.count("turn_done"); n != 1 {
		t.Fatalf("a dropped fallback still started a turn: %d turn_done, outline %v", n, em.outline())
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
	d.startBackgroundTask("c1", "", "Command running in background with ID: bs1. Output is being written to: x", false)
	d.noteBackgroundCall("c2", "Bash", json.RawMessage(`{"command":"`+long+`","run_in_background":true}`))
	d.startBackgroundTask("c2", "", "Command running in background with ID: bs2. Output is being written to: x", false)
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

// A replacement engine must retain uncertain shell work without waiting on it.
func assertRetainedBackgroundTask(t *testing.T, d *claudeCodeDriver, em *collectEmitter, id string) {
	t.Helper()
	d.subMu.Lock()
	task := d.bgTasks[id]
	retained := task != nil && task.engineEnded && task.p.Kind == "bash"
	d.subMu.Unlock()
	if !retained || d.backgroundRunning() != 0 || countKind(em, "background_task_done") != 0 {
		t.Fatalf("unconfirmed task must remain listed without gating the new engine: %v", em.outline())
	}
}
