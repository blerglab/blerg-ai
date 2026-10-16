package daemon

// Background tasks of a Claude Code session: what the main agent leaves running while it goes on
// (a Bash call with run_in_background, a Monitor, a background subagent). Each is reported as
// background_task_started once its call's result names it, and as background_task_done when the
// CLI's notice for it says it ended (a <task-notification> in a user line, or a system
// task_notification line: see ccNotice), so a reader can tell what the session is still waiting
// on. A task the CLI never reports the end of ends with the engine process.

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// ccBgCall is a background Bash or Monitor call whose result has not arrived yet.
type ccBgCall struct {
	kind, description string
	at                time.Time
}

// ccBgTask is a background task started and not yet done.
type ccBgTask struct {
	p agent.BackgroundTaskStartedPayload
	// agentID is the agentId of a resumed background subagent's run (whose task id is its
	// SendMessage call's id): a notice that names the agent alone still ends it.
	agentID string
}

var (
	// "Command running in background with ID: bu90kf38e. Output is being written to: …"
	ccBgBashID = regexp.MustCompile(`Command running in background with ID:\s*([A-Za-z0-9_-]+)`)
	// "Monitor started (task bs3tbt4gf, expires in 30m …)"
	ccBgMonitorID = regexp.MustCompile(`Monitor started \(task ([A-Za-z0-9_-]+)`)
	ccNoticeSum   = ccNoticeTag("summary")
	ccNoticeEvent = ccNoticeTag("event")
)

// noteBackgroundCall remembers a main-agent call that starts a background task: a Bash call with
// run_in_background, or a Monitor. Any other call is ignored.
func (d *claudeCodeDriver) noteBackgroundCall(id, tool string, raw json.RawMessage) {
	var in struct {
		Description     string `json:"description"`
		Command         string `json:"command"`
		RunInBackground bool   `json:"run_in_background"`
	}
	_ = json.Unmarshal(raw, &in)
	var kind string
	switch {
	case tool == "Bash" && in.RunInBackground:
		kind = "bash"
	case tool == "Monitor":
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
	d.bgCalls[id] = ccBgCall{kind: kind, description: desc, at: d.clock()}
	d.subMu.Unlock()
}

// startBackgroundTask handles the main agent's tool_result for call id: the result of a call
// noteBackgroundCall kept names the task it started, which is then reported as started. A result
// that names none (an error, a refusal) starts nothing.
func (d *claudeCodeDriver) startBackgroundTask(callID, output string, isError bool) {
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
	m := re.FindStringSubmatch(output)
	if isError || m == nil {
		d.subMu.Unlock()
		return
	}
	p := d.trackBackgroundTaskLocked(m[1], callID, c.kind, c.description, c.at)
	d.reportOpenWorkLocked()
	d.subMu.Unlock()
	d.emit("background_task_started", p)
}

// trackBackgroundTaskLocked records a started task and returns its payload. Called with subMu held.
func (d *claudeCodeDriver) trackBackgroundTaskLocked(taskID, callID, kind, description string, at time.Time) agent.BackgroundTaskStartedPayload {
	p := agent.BackgroundTaskStartedPayload{
		TaskID: taskID, CallID: callID, Kind: kind, Description: description,
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
	d.reportOpenWorkLocked()
	last := d.backgroundRunningLocked() == 0
	d.subMu.Unlock()
	summary := n.summary
	if summary == "" {
		summary = n.result
	}
	if summary == "" {
		summary = event
	}
	d.emit("background_task_done", agent.BackgroundTaskDonePayload{
		TaskID: t.p.TaskID, CallID: t.p.CallID, Kind: t.p.Kind, Status: status,
		Summary: cutRunes(strings.TrimSpace(summary), 120), Description: t.p.Description,
	})
	if last {
		d.backgroundIdle() // nothing keeps an idle process any more: the ordinary idle shutdown again
	}
}

// backgroundRunning is how many background tasks are started and not done, counting the resumed
// subagent runs whose SendMessage call has returned (they run on without a call, like a task): a
// resumed run of a background subagent is a task of its own (trackResumedRunLocked), a resumed run
// of a foreground one is counted here.
func (d *claudeCodeDriver) backgroundRunning() int {
	d.subMu.Lock()
	defer d.subMu.Unlock()
	return d.backgroundRunningLocked()
}

func (d *claudeCodeDriver) backgroundRunningLocked() int {
	n := len(d.bgTasks)
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
	p := d.trackBackgroundTaskLocked(call, call, "agent", s.p.Summary, s.started)
	d.bgTasks[call].agentID = s.agentID
	return p, true
}

// endBackgroundTasks reports every task still running as stopped, and every subagent still
// running with a final subagent_done: the engine process that ran them has ended (an idle close,
// a crash, the per-turn engine's turn). A subagent left open here would stay "running" in the
// chat and in the recovery record, and a later restart would report it again, with a resume
// instruction for an agent that is long gone.
func (d *claudeCodeDriver) endBackgroundTasks() {
	d.subMu.Lock()
	tasks := make([]*ccBgTask, 0, len(d.bgTasks))
	for _, t := range d.bgTasks {
		tasks = append(tasks, t)
	}
	subs := make([]*ccSubagent, 0, len(d.subagents))
	for _, s := range d.subagents {
		subs = append(subs, s)
	}
	d.bgTasks = nil
	d.bgCalls = nil
	d.subagents = nil
	d.agentIDs = nil
	d.sends = nil
	d.resumedFrom = nil // known stays: a later process can resume what this one ran
	if len(tasks) > 0 || len(subs) > 0 {
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
