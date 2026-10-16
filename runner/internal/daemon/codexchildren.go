package daemon

import (
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"sort"
	"time"
)

type codexChild struct {
	model, summary string
	callID         string // display identity of this run; native thread ID stays the map key
	resumedFrom    string
	usageBase      agent.Usage
	lastTotal      agent.Usage
	started        time.Time
	done           bool
	tools          int
	usage          agent.Usage
}

func (d *codexDriver) childKnown(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.children[id] != nil
}
func (d *codexDriver) childStatus(id, model, summary, status string) {
	if !validCodexThreadID(id) {
		return
	}
	d.mu.Lock()
	if d.children == nil {
		d.children = map[string]*codexChild{}
	}
	c := d.children[id]
	fresh := c == nil
	if fresh {
		if len(d.children) >= codexMaxKnownChildren || id == d.threadID {
			d.mu.Unlock()
			return
		}
		c = &codexChild{model: model, summary: summary, callID: ccUUID(), started: time.Now()}
		d.children[id] = c
	}
	if model != "" {
		c.model = model
	}
	if summary != "" {
		c.summary = summary
	}
	terminal := status == "completed" || status == "errored" || status == "shutdown" || status == "interrupted" || status == "notFound"
	// Only an explicit running/started notification reopens a finished child.
	// Late usage or settings notifications have no lifecycle meaning.
	resumed := c.done && (status == "running" || status == "started" || status == "pendingInit")
	if resumed {
		c.done = false
		c.resumedFrom = c.callID
		c.tools = 0
		c.usageBase = c.lastTotal
		c.usage = agent.Usage{}
		c.started = time.Now()
		c.callID = ccUUID()
	}
	wasDone := c.done
	p := agent.SubagentPayload{ChildID: c.callID, AgentType: "Codex", Model: c.model, Summary: c.summary, ToolUses: c.tools, Usage: &c.usage, DurationMs: time.Since(c.started).Milliseconds(), IsError: status == "errored" || status == "notFound", Resumed: c.resumedFrom != "", ResumedFrom: c.resumedFrom}
	// Copy usage: future token notifications must not mutate an emitted event.
	u := c.usage
	p.Usage = &u
	startedAt := c.started.Format(time.RFC3339)
	app := d.app
	if terminal {
		c.done = true
	}
	d.mu.Unlock()
	if terminal && app != nil {
		app.endChildThread(id)
	}
	if fresh || resumed {
		started := p
		if resumed {
			started.Background = true
		}
		d.emit("subagent_started", started)
		d.emit("background_task_started", agent.BackgroundTaskStartedPayload{TaskID: p.ChildID, CallID: p.ChildID, Kind: "agent", Description: p.Summary, StartedAt: startedAt})
	}
	if terminal && !wasDone {
		d.emit("subagent_done", p)
		d.emit("background_task_done", agent.BackgroundTaskDonePayload{TaskID: p.ChildID, CallID: p.ChildID, Kind: "agent", Status: status, Summary: p.Summary})
	} else if !terminal && !fresh && !wasDone {
		d.emit("subagent_update", p)
	}
}
func (d *codexDriver) childUsage(id string, u agent.Usage, cumulative bool) {
	d.mu.Lock()
	if c := d.children[id]; c != nil {
		if cumulative {
			c.lastTotal = u
			c.usage = subtractCodexUsage(u, c.usageBase)
		} else {
			c.usage = u
		}
	}
	d.mu.Unlock()
	d.childStatus(id, "", "", "")
}
func (d *codexDriver) childTool(id string) {
	d.mu.Lock()
	if c := d.children[id]; c != nil {
		c.tools++
	}
	d.mu.Unlock()
}

// Serialize recovery snapshots with lifecycle mutation, so a late snapshot
// cannot resurrect a task that already ended.
func (d *codexDriver) updateOpenWork(kind string, payload any) {
	switch kind {
	case "background_task_started", "background_task_done", "subagent_started", "subagent_update", "subagent_done":
	default:
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if p, ok := payload.(agent.BackgroundTaskStartedPayload); ok {
		if d.bgTasks == nil {
			d.bgTasks = map[string]agent.BackgroundTaskStartedPayload{}
		}
		d.bgTasks[p.TaskID] = p
	}
	if p, ok := payload.(agent.BackgroundTaskDonePayload); ok {
		delete(d.bgTasks, p.TaskID)
	}
	if d.onOpenWork == nil && d.onKnownChildren == nil {
		return
	}
	tasks := make([]agent.BackgroundTaskStartedPayload, 0, len(d.bgTasks))
	for _, p := range d.bgTasks {
		tasks = append(tasks, p)
	}
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].TaskID < tasks[j].TaskID })
	subs := []OpenSubagent{}
	for id, c := range d.children {
		if !c.done {
			bg := true
			subs = append(subs, OpenSubagent{ChildID: c.callID, AgentID: id, AgentType: "Codex", Model: c.model, Summary: c.summary, Background: &bg})
		}
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].ChildID < subs[j].ChildID })
	if d.onOpenWork != nil {
		d.onOpenWork(tasks, subs)
	}
	if d.onKnownChildren != nil {
		known := make([]OpenSubagent, 0, len(d.children))
		for id, child := range d.children {
			total := child.lastTotal
			known = append(known, OpenSubagent{AgentID: id, ChildID: child.callID, AgentType: "Codex", Model: cutRunes(child.model, 128), Summary: cutRunes(child.summary, 200), CumulativeUsage: &total})
		}
		sort.Slice(known, func(i, j int) bool { return known[i].AgentID < known[j].AgentID })
		d.onKnownChildren(known)
	}
}
func (d *codexDriver) stopChildren() {
	d.mu.Lock()
	ids := []string{}
	for id, c := range d.children {
		if !c.done {
			ids = append(ids, id)
		}
	}
	d.mu.Unlock()
	for _, id := range ids {
		d.childStatus(id, "", "Codex runtime disconnected", "interrupted")
	}
}

// Child threads keep their native identity across follow-ups, while chat and
// Live group immutable runs by child_id. Route new activity to the current run.
func (d *codexDriver) childCallID(threadID string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if child := d.children[threadID]; child != nil {
		return child.callID
	}
	return threadID
}
