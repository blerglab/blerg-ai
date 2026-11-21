package daemon

// Background tasks of a Claude Code session: what the main agent leaves running while it goes on
// (a Bash call with run_in_background, a Monitor, a background subagent). Each is reported as
// background_task_started once its call's result names it, and as background_task_done when the
// CLI's notice for it says it ended (a <task-notification> in a user line, or a system
// task_notification line: see ccNotice), so a reader can tell what the session is still waiting
// on. Engine-owned work ends with the process; detached Bash work may survive it. The engine's own
// list (a system background_tasks_changed line) says which shells are live, whoever started them: it
// is what keeps an idle process open, and an end nobody asked for is told to the agent and the person.

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// ccBgCall is a Bash or Monitor call whose result has not arrived yet. A foreground Bash call is kept
// too: the CLI can move it to the background, and then its result names a task.
type ccBgCall struct {
	kind, description, command string
	at                         time.Time
	foreground                 bool
}

// ccBgTask is a background task started and not yet done.
type ccBgTask struct {
	p agent.BackgroundTaskStartedPayload
	// engineEnded retains uncertain shell inventory without gating a replacement engine.
	engineEnded bool
	// agentID is the agentId of a resumed background subagent's run (whose task id is its
	// SendMessage call's id): a notice that names the agent alone still ends it.
	agentID string
	// listed: the engine's list has named this shell; unlisted: it has since dropped it, so the shell is
	// over and only the notice saying how is still to come.
	listed, unlisted bool
	// output is the file the task writes to, watched for a stall; stallTold says the current silence
	// was reported.
	output    string
	stallTold bool
}

var (
	// "Command running in background with ID: bu90kf38e. Output is being written to: …"
	ccBgBashID = regexp.MustCompile(`Command running in background with ID:\s*([A-Za-z0-9_-]+)`)
	// "Monitor started (task bs3tbt4gf, expires in 30m …)"
	ccBgMonitorID = regexp.MustCompile(`Monitor started \(task ([A-Za-z0-9_-]+)`)
	ccBgOutput    = regexp.MustCompile(`Output is being written to:\s*(\S+?)\.(?:\s|$)`)
	ccNoticeSum   = ccNoticeTag("summary")
	ccNoticeEvent = ccNoticeTag("event")
)

// noteBackgroundCall remembers a main-agent call that starts a background task, or may become one: a
// Bash call, or a Monitor. Any other call is ignored.
func (d *claudeCodeDriver) noteBackgroundCall(id, tool string, raw json.RawMessage) {
	var in struct {
		Description     string `json:"description"`
		Command         string `json:"command"`
		RunInBackground bool   `json:"run_in_background"`
	}
	_ = json.Unmarshal(raw, &in)
	var kind string
	switch tool {
	case "Bash":
		kind = "bash"
	case "Monitor":
		kind = "monitor"
	default:
		return
	}
	desc := strings.TrimSpace(in.Description)
	if desc == "" {
		desc = cutRunes(strings.TrimSpace(in.Command), 80)
	}
	d.subMu.Lock()
	if d.bgCalls == nil {
		d.bgCalls = map[string]ccBgCall{}
	}
	d.bgCalls[id] = ccBgCall{kind: kind, description: desc, command: cutRunes(strings.TrimSpace(in.Command), 500), at: d.clock(), foreground: tool == "Bash" && !in.RunInBackground}
	d.subMu.Unlock()
}

// startBackgroundTask handles the main agent's tool_result for call id: the result of a call
// noteBackgroundCall kept names the task it started, which is then reported as started. A result
// that names none (an error, a refusal, a foreground command that finished) starts nothing. taskID
// is the id the line's tool_use_result gave, if any; only that id starts a task from a foreground
// call, whose text is the command's own output and may print anything.
func (d *claudeCodeDriver) startBackgroundTask(callID, taskID, output string, isError bool) {
	d.subMu.Lock()
	c, ok := d.bgCalls[callID]
	if !ok {
		d.subMu.Unlock()
		return
	}
	delete(d.bgCalls, callID)
	re := ccBgBashID
	if c.kind == "monitor" {
		re = ccBgMonitorID
	}
	if taskID == "" && !c.foreground && !isError {
		if m := re.FindStringSubmatch(output); m != nil {
			taskID = m[1]
		}
	}
	if taskID == "" {
		d.subMu.Unlock()
		return
	}
	p := d.trackBackgroundTaskLocked(taskID, callID, c.kind, c.description, c.command, c.at)
	if m := ccBgOutput.FindStringSubmatch(output); m != nil {
		d.bgTasks[taskID].output = m[1]
	}
	t := d.bgTasks[taskID]
	_, t.listed = d.bgListed[taskID]
	if d.bgDropped[taskID] {
		t.listed, t.unlisted = true, true
		delete(d.bgDropped, taskID)
	}
	d.reportOpenWorkLocked()
	d.subMu.Unlock()
	d.emit("background_task_started", p)
}

// trackBackgroundTaskLocked records a started task and returns its payload. Called with subMu held.
func (d *claudeCodeDriver) trackBackgroundTaskLocked(taskID, callID, kind, description, command string, at time.Time) agent.BackgroundTaskStartedPayload {
	p := agent.BackgroundTaskStartedPayload{
		TaskID: taskID, CallID: callID, Kind: kind, Description: description, Command: command,
		StartedAt: at.UTC().Format(time.RFC3339),
	}
	if d.bgTasks == nil {
		d.bgTasks = map[string]*ccBgTask{}
	}
	d.bgTasks[taskID] = &ccBgTask{p: p}
	return p
}

// closeBackgroundTask reads one task notification (either form, see ccNotice): when it names a
// task this driver started (by task id, else by tool-use id) and says the task ended,
// background_task_done is emitted. A subagent's notice always ends it; a shell's or monitor's only
// with a status (a monitor's own events carry none), or the monitor's expiry.
func (d *claudeCodeDriver) closeBackgroundTask(n ccNotice) {
	taskID, callID, status, event := n.taskID, n.callID, n.status, n.event
	d.subMu.Lock()
	t := d.bgTasks[taskID]
	if t == nil && callID != "" {
		for _, x := range d.bgTasks {
			if x.p.CallID == callID {
				t = x
				break
			}
		}
	}
	if t == nil && taskID != "" {
		for _, x := range d.bgTasks {
			if x.agentID == taskID {
				t = x
				break
			}
		}
	}
	if t == nil {
		d.subMu.Unlock()
		return
	}
	switch {
	case status != "":
	case t.p.Kind == "agent":
		status = "completed"
	case t.p.Kind == "monitor" && strings.Contains(event, "Monitor expired"):
		status = "expired"
	default:
		d.subMu.Unlock()
		return // an event of a monitor that is still watching
	}
	delete(d.bgTasks, t.p.TaskID)
	// The engine's own shell list can lag its notice (noteEngineTasks's doc comment): a task closed
	// here may still be named by d.bgListed until a later background_tasks_changed line catches up,
	// which would otherwise make backgroundRunningLocked count it as still running and skip the
	// wake-up below for every last task that overlaps that lag (BLG-447).
	delete(d.bgListed, t.p.TaskID)
	wake := false
	if d.backgroundRunningLocked() == 0 {
		// backgroundIdleLocked's refresh (a fresh idle window before this function's own,
		// possibly slow, reporting work: BLG-375) must still happen unconditionally; the wake-up
		// below queues separately and starts its own turn, which re-arms the window again once
		// that turn ends.
		d.backgroundIdleLocked()
		wake = d.idleNowLocked()
	}
	d.reportOpenWorkLocked()
	d.subMu.Unlock()
	summary := n.summary
	if summary == "" {
		summary = n.result
	}
	if summary == "" {
		summary = event
	}
	done := agent.BackgroundTaskDonePayload{
		TaskID: t.p.TaskID, CallID: t.p.CallID, Kind: t.p.Kind, Status: status,
		Summary: cutRunes(strings.TrimSpace(summary), 120), Description: t.p.Description,
	}
	d.emit("background_task_done", done)
	if wake {
		// Arm a fallback rather than wake the session outright: Claude Code may already be
		// resuming the turn on its own (the ordinary case), and markRunning cancels this the
		// moment it does, so the two can never both start one for the same task's end (BLG-447
		// code review).
		d.armWakeFallback(done)
	}
}

// wakeForFinishedTask starts a turn for a background task that just ended while the session sat
// idle, instead of trusting Claude Code's own process to resume on its own: BLG-447 found workers
// left idle, with no live message, after their only running task ended. guard, when given, is
// re-checked under enqueueInputGuarded's own lock immediately before the append (BLG-447 review):
// the caller's own idle finding may already be stale by the time this runs. Called with no lock
// held.
func (d *claudeCodeDriver) wakeForFinishedTask(p agent.BackgroundTaskDonePayload, guard func() bool) {
	text := fmt.Sprintf("Background work finished: %s (%s)", p.Description, p.Status)
	if p.Summary != "" {
		text += ": " + p.Summary
	}
	d.enqueueInputGuarded(agent.Input{Text: text, Shown: text, Source: "system"}, guard)
}

// noteEngineTasks reads a system background_tasks_changed line, the CLI's list of what is running in the
// background. A shell on it counts as running whether or not a call here named it (a subagent's does
// not pass through noteBackgroundCall). A shell that was on it and has left is over; its notice normally
// came first, and when one reaches a turn in progress it comes later, so it is not reported yet
// (closeUnlistedTasks). A shell that ends at once can leave before its call's result names it, which
// bgDropped remembers. A shell an ended engine left behind is live again once this list names it. A line
// that cannot be read changes nothing.
func (d *claudeCodeDriver) noteEngineTasks(line []byte) {
	var l struct {
		Tasks *[]struct {
			TaskID      string `json:"task_id"`
			TaskType    string `json:"task_type"`
			Description string `json:"description"`
		} `json:"tasks"`
	}
	if json.Unmarshal(line, &l) != nil || l.Tasks == nil {
		return
	}
	listed := make(map[string]string, len(*l.Tasks))
	for _, task := range *l.Tasks {
		if task.TaskType == "local_bash" && task.TaskID != "" {
			listed[task.TaskID] = task.Description
		}
	}
	d.subMu.Lock()
	defer d.subMu.Unlock()
	for id := range d.bgListed {
		if _, on := listed[id]; !on {
			if d.bgDropped == nil {
				d.bgDropped = map[string]bool{}
			}
			d.bgDropped[id] = true
		}
	}
	for id := range listed {
		delete(d.bgDropped, id)
	}
	d.bgListed = listed
	for id, t := range d.bgTasks {
		_, on := listed[id]
		if t.p.Kind != "bash" || t.engineEnded && !on {
			continue
		}
		t.engineEnded = false
		t.unlisted = t.listed && !on
		t.listed = t.listed || on
	}
	if d.backgroundRunningLocked() == 0 {
		d.backgroundIdleLocked()
	}
}

// closeUnlistedTasks reports stopped the shells the engine stopped listing and no notice ever ended (the
// CLI ends some at a final response and says it sends no notification). Called at an idle tick, well
// after any notice would have arrived.
func (d *claudeCodeDriver) closeUnlistedTasks() {
	d.subMu.Lock()
	var ids []string
	for id, t := range d.bgTasks {
		if t.unlisted {
			ids = append(ids, id)
		}
	}
	d.subMu.Unlock()
	sort.Strings(ids)
	for _, id := range ids {
		d.closeBackgroundTask(ccNotice{taskID: id, status: "stopped", summary: "No longer listed as running by Claude Code"})
	}
}

// liveBackgroundWork names what the engine has running now.
func (d *claudeCodeDriver) liveBackgroundWork() []string {
	d.subMu.Lock()
	defer d.subMu.Unlock()
	var out []string
	for _, t := range d.bgTasks {
		if !t.engineEnded && !t.unlisted {
			out = append(out, t.p.Description)
		}
	}
	for id, description := range d.bgListed {
		if d.bgTasks[id] == nil {
			out = append(out, description)
		}
	}
	sort.Strings(out)
	return out
}

// tellBackgroundLost wakes the agent when the engine ended with background work running, and shows the
// person the same news: nobody asked for this end, and the CLI itself only reports it on the next
// resume, which may never come. The restarted engine's notice closes the retained tasks.
func (d *claudeCodeDriver) tellBackgroundLost(work []string) {
	if len(work) == 0 {
		return
	}
	list := cutRunes(strings.Join(work, "; "), 400)
	d.EnqueueInput(agent.Input{
		Text:   "The engine process ended while background work was running: " + list + ". It may have stopped. Check whether it finished before relying on it, and start again what did not.",
		Shown:  "The engine process ended while background work was running: " + list + ". It may have stopped.",
		Source: "system",
	})
}

// wakeForStalledTasks wakes an idle session for each running task whose output has not changed for
// ccBgStallAfter, once per silence: a hung run otherwise waits forever and nothing tells anyone. A task
// that is merely quiet costs one short turn. Tasks with no readable output file are skipped. The files
// are read without subMu: in a sandbox each read is a docker exec, which the event stream must not wait on.
func (d *claudeCodeDriver) wakeForStalledTasks() {
	now := d.clock()
	outputs := map[string]string{}
	d.subMu.Lock()
	for id, t := range d.bgTasks {
		if t.output != "" && !t.engineEnded && !t.unlisted {
			outputs[id] = t.output
		}
	}
	d.subMu.Unlock()
	written := make(map[string]time.Time, len(outputs))
	for id, output := range outputs {
		if at, err := d.outputWritten(output); err == nil {
			written[id] = at
		}
	}
	var stalled []string
	d.subMu.Lock()
	for id, at := range written {
		t := d.bgTasks[id]
		if t == nil || t.engineEnded || t.unlisted {
			continue
		}
		if now.Sub(at) < ccBgStallAfter {
			t.stallTold = false
		} else if !t.stallTold {
			t.stallTold = true
			stalled = append(stalled, t.p.Description)
		}
	}
	d.subMu.Unlock()
	if len(stalled) == 0 {
		return
	}
	sort.Strings(stalled)
	list := cutRunes(strings.Join(stalled, "; "), 400)
	quiet := ccBgStallAfter.Round(time.Minute).String()
	d.EnqueueInput(agent.Input{
		Text:   "Background work is still running but has written no output for " + quiet + ": " + list + ". Check whether it is stuck. If it is only quiet, keep waiting.",
		Shown:  "Background work has written no output for " + quiet + ": " + list + ".",
		Source: "system",
	})
}

// outputWritten is when a task's output file last changed. The path is the engine's, so a sandboxed
// session's file is read in its container, following a link as on the host.
func (d *claudeCodeDriver) outputWritten(path string) (time.Time, error) {
	if !d.prefix.enabled() {
		st, err := os.Stat(path)
		if err != nil {
			return time.Time{}, err
		}
		return st.ModTime(), nil
	}
	out, err := dockerOutput("exec", d.prefix.container(), "stat", "-L", "-c", "%Y", "--", path)
	if err != nil {
		return time.Time{}, err
	}
	secs, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(secs, 0), nil
}

// backgroundRunning counts live-engine background work, excluding retained shell inventory
// from ended engines. It includes shells only the engine's list knows of, and also counts resumed subagent runs whose SendMessage call
// has returned (they run on without a call, like a task): a
// resumed run of a background subagent is a task of its own (trackResumedRunLocked), a resumed run
// of a foreground one is counted here.
func (d *claudeCodeDriver) backgroundRunning() int {
	d.subMu.Lock()
	defer d.subMu.Unlock()
	return d.backgroundRunningLocked()
}

func (d *claudeCodeDriver) backgroundRunningLocked() int {
	n := 0
	for _, task := range d.bgTasks {
		if !task.engineEnded && !task.unlisted {
			n++
		}
	}
	for id := range d.bgListed {
		if d.bgTasks[id] == nil {
			n++
		}
	}
	for _, s := range d.subagents {
		if s.resumed && s.background && !s.resumedBg {
			n++
		}
	}
	return n
}

// trackResumedRunLocked makes the resumed run under call id call a background task, as the launch
// of a background subagent is one: when its original ran in the background and its SendMessage
// call has returned. Its task id is the call's id, its kind "agent", its description the run's
// title; its notice (or the engine's end) ends it. It returns the background_task_started to emit,
// or false when the run is not one, or is tracked already. The caller reports the open work.
// Called with subMu held.
func (d *claudeCodeDriver) trackResumedRunLocked(call string) (agent.BackgroundTaskStartedPayload, bool) {
	s := d.subagents[call]
	if s == nil || !s.resumed || !s.resumedBg || !s.background || d.bgTasks[call] != nil {
		return agent.BackgroundTaskStartedPayload{}, false
	}
	p := d.trackBackgroundTaskLocked(call, call, "agent", s.p.Summary, "", s.started)
	d.bgTasks[call].agentID = s.agentID
	return p, true
}

// endBackgroundTasks closes engine-owned work. At process exit keepBash retains
// detached shells until an authoritative notification confirms their completion.
// Passing false requires confirmed cleanup of those shells too.
func (d *claudeCodeDriver) endBackgroundTasks(keepBash bool) {
	d.subMu.Lock()
	tasks := make([]*ccBgTask, 0, len(d.bgTasks))
	for id, t := range d.bgTasks {
		if keepBash && t.p.Kind == "bash" {
			t.engineEnded = true
			continue
		}
		tasks = append(tasks, t)
		delete(d.bgTasks, id)
	}
	subs := make([]*ccSubagent, 0, len(d.subagents))
	for _, s := range d.subagents {
		subs = append(subs, s)
	}
	hadCalls := len(d.bgCalls) > 0
	d.bgCalls = nil
	d.bgListed = nil
	d.bgDropped = nil
	d.subagents = nil
	d.agentIDs = nil
	d.sends = nil
	d.resumedFrom = nil // known stays: a later process can resume what this one ran
	if len(tasks) > 0 || len(subs) > 0 || hadCalls {
		d.reportOpenWorkLocked()
	}
	now := d.clock()
	ends := make([]agent.SubagentPayload, 0, len(subs))
	sort.Slice(subs, func(i, j int) bool {
		a, b := subs[i], subs[j]
		return a.started.Before(b.started) || a.started.Equal(b.started) && a.p.ChildID < b.p.ChildID
	})
	for _, s := range subs {
		p := s.payload()
		p.DurationMs = now.Sub(s.started).Milliseconds()
		p.Summary = "stopped: the engine process ended"
		ends = append(ends, p)
	}
	d.subMu.Unlock()
	sort.Slice(tasks, func(i, j int) bool {
		a, b := tasks[i].p, tasks[j].p
		return a.StartedAt < b.StartedAt || a.StartedAt == b.StartedAt && a.TaskID < b.TaskID
	})
	for _, t := range tasks {
		d.emit("background_task_done", agent.BackgroundTaskDonePayload{
			TaskID: t.p.TaskID, CallID: t.p.CallID, Kind: t.p.Kind, Status: "stopped",
			Summary: "the engine process ended", Description: t.p.Description,
		})
	}
	for _, p := range ends {
		d.emit("subagent_done", p)
	}
}

// ccWaitTools are the main agent's tools that only wait on or read background work: a call of one
// running is the agent waiting on that work, not a step of its own (adoptPlan).
var ccWaitTools = map[string]bool{"TaskOutput": true, "BashOutput": true, "Monitor": true}

// ccPollCommands are the commands a sleep-poll Bash call may run besides sleep: they only read.
var ccPollCommands = map[string]bool{
	"sleep": true, "cat": true, "tail": true, "head": true, "ls": true, "echo": true, "grep": true,
	"wc": true, "test": true, "[": true, "true": true, "date": true, "stat": true, "pgrep": true,
}

// ccWaitCall reports whether a main-agent tool call only waits on background work: one of
// ccWaitTools, or a Bash call that sleeps and at most reads (a sleep-poll: "sleep 60 && tail -5
// out.log"). Anything that could write or run something else — a redirect, a substitution, a
// command not in ccPollCommands — is a step of the agent's own.
func ccWaitCall(tool string, raw json.RawMessage) bool {
	if ccWaitTools[tool] {
		return true
	}
	if tool != "Bash" {
		return false
	}
	var in struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return false
	}
	cmd := strings.TrimSpace(in.Command)
	if !strings.HasPrefix(cmd, "sleep ") || strings.ContainsAny(cmd, "><`$(){}\\") {
		return false
	}
	for _, part := range ccPollSplit.Split(cmd, -1) {
		f := strings.Fields(part)
		if len(f) > 0 && !ccPollCommands[f[0]] {
			return false
		}
		if len(f) > 0 && f[0] == "sleep" && (len(f) != 2 || !ccSleepArg.MatchString(f[1])) {
			return false
		}
	}
	return true
}

var (
	ccPollSplit = regexp.MustCompile(`&&|\|\||[;|\n]`)
	ccSleepArg  = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[smhd]?$`)
)
