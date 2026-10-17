package daemon

// Subagents of a Claude Code session (its Agent tool): one subagent_started when the main agent
// calls Agent, throttled subagent_update events with the model and running token totals while
// the subagent works, and subagent_done when its call returns. A background subagent's call
// returns at launch: that subagent_done is marked Background, and the real end is read from the
// <task-notification> the CLI injects when the subagent finishes.

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// ccSubagentUpdateEvery is the least time between two subagent_update events of one subagent.
const ccSubagentUpdateEvery = 5 * time.Second

// ccSubagent is what the driver knows of one running subagent.
type ccSubagent struct {
	p          agent.SubagentPayload // as started
	inBack     bool                  // the call asked for run_in_background
	background bool                  // its call returned at launch; it still runs
	resumed    bool                  // a run of a finished subagent, resumed by SendMessage (clauderesume.go)
	resumedBg  bool                  // a resumed run's original ran in the background
	agentID    string                // a resumed run's agentId
	model      string                // the model its API calls report
	usage      map[string]agent.Usage
	toolUses   int
	started    time.Time
	lastUpdate time.Time // zero: no update sent yet
}

func (s *ccSubagent) total() agent.Usage {
	var t agent.Usage
	for _, u := range s.usage {
		t.InputTokens += u.InputTokens
		t.OutputTokens += u.OutputTokens
		t.CacheReadTokens += u.CacheReadTokens
		t.CacheWriteTokens += u.CacheWriteTokens
	}
	return t
}

// payload is the subagent's payload with its totals so far.
func (s *ccSubagent) payload() agent.SubagentPayload {
	p := s.p
	if s.model != "" {
		p.Model = s.model
	}
	if u := s.total(); u != (agent.Usage{}) {
		p.Usage = &u
	}
	p.ToolUses = s.toolUses
	return p
}

func (d *claudeCodeDriver) clock() time.Time {
	if d.now != nil {
		return d.now()
	}
	return time.Now()
}

// startSubagent emits subagent_started for the main agent's Agent call id and remembers it until
// it ends.
func (d *claudeCodeDriver) startSubagent(id string, raw json.RawMessage) {
	var in struct {
		SubagentType    string `json:"subagent_type"`
		Model           string `json:"model"`
		Description     string `json:"description"`
		Prompt          string `json:"prompt"`
		RunInBackground bool   `json:"run_in_background"`
	}
	_ = json.Unmarshal(raw, &in)
	p := agent.SubagentPayload{ChildID: id, AgentType: in.SubagentType, Model: in.Model, Summary: in.Description}
	if p.AgentType == "" {
		p.AgentType = "general-purpose"
	}
	if strings.TrimSpace(p.Summary) == "" {
		p.Summary = cutRunes(in.Prompt, 120)
	}
	d.subMu.Lock()
	if d.subagents == nil {
		d.subagents = map[string]*ccSubagent{}
	}
	d.subagents[id] = &ccSubagent{p: p, inBack: in.RunInBackground, usage: map[string]agent.Usage{}, started: d.clock()}
	d.reportOpenWorkLocked()
	d.subMu.Unlock()
	d.emit("subagent_started", p)
}

// noteSubagentLine adds an assistant line of the subagent started by call id parent to its
// totals (the model, the usage of each API call once, the tool calls), and reports them as a
// subagent_update at once for its first line and then at most every ccSubagentUpdateEvery.
func (d *claudeCodeDriver) noteSubagentLine(parent string, ev *ccLine) {
	d.subMu.Lock()
	s := d.subagents[parent]
	if s == nil {
		d.subMu.Unlock()
		return
	}
	if ev.Message.Model != "" {
		s.model = ev.Message.Model
	}
	// The CLI writes one line per content block of a response, each with that response's usage:
	// keyed by message id, a response counts once (its last line's figures).
	if u := ev.Message.Usage.usage(); u != (agent.Usage{}) {
		key := ev.Message.ID
		if key == "" {
			key = "line-" + strconv.Itoa(len(s.usage))
		}
		s.usage[key] = u
	}
	for _, b := range ev.blocks() {
		if b.Type == "tool_use" {
			s.toolUses++
		}
	}
	now := d.clock()
	due := s.lastUpdate.IsZero() || now.Sub(s.lastUpdate) >= ccSubagentUpdateEvery
	var p agent.SubagentPayload
	if due {
		s.lastUpdate = now
		p = s.payload()
		if !s.resumed { // a resumed run's Background is its original's, as started
			p.Background = s.background
		}
	}
	d.subMu.Unlock()
	if due {
		d.emit("subagent_update", p)
	}
}

// ccAgentIDLine finds the agentId in an Agent call's result ("agentId: a5ef89e0e0329265b (…)").
var ccAgentIDLine = regexp.MustCompile(`(?m)^agentId:\s*([A-Za-z0-9_-]+)`)

// endSubagent handles the main agent's tool_result for call id: when id is a running subagent's
// call, a background launch emits a subagent_done marked Background (the subagent keeps running
// and is ended by its task-notification); any other result is its end.
func (d *claudeCodeDriver) endSubagent(id, output string, isError bool) {
	d.subMu.Lock()
	s := d.subagents[id]
	if s == nil || s.background {
		d.subMu.Unlock()
		return
	}
	if m := ccAgentIDLine.FindStringSubmatch(output); !isError && m != nil &&
		(s.inBack || strings.Contains(output, "Async agent launched")) {
		s.background = true
		if d.agentIDs == nil {
			d.agentIDs = map[string]string{}
		}
		d.agentIDs[m[1]] = id
		d.rememberAgentLocked(m[1], id, s, true)
		p := s.p
		if s.model != "" {
			p.Model = s.model
		}
		p.Summary = "running in the background"
		p.Background = true
		bg := d.trackBackgroundTaskLocked(m[1], id, "agent", s.p.Summary, "", s.started)
		d.reportOpenWorkLocked()
		d.subMu.Unlock()
		d.emit("subagent_done", p)
		d.emit("background_task_started", bg)
		return
	}
	delete(d.subagents, id)
	d.forgetRunLocked(id, s)
	if s.resumed {
		// A hand-back (SendMessage waited for the run): the report follows the CLI's JSON.
		output = handBackReport(output)
		for k, v := range d.agentIDs {
			if v == id {
				delete(d.agentIDs, k)
			}
		}
	}
	if m := ccAgentIDLine.FindStringSubmatch(output); !isError && m != nil {
		// A foreground subagent's report names its agentId too: SendMessage can resume it.
		d.rememberAgentLocked(m[1], id, s, false)
	}
	d.reportOpenWorkLocked()
	p := s.payload()
	p.DurationMs = d.clock().Sub(s.started).Milliseconds()
	d.subMu.Unlock()
	if isError {
		p.Summary = "error: " + cutRunes(output, 120)
		p.IsError = true
	} else {
		p.Summary = cutRunes(reportText(output), 120)
	}
	d.emit("subagent_done", p)
}

// reportText is a subagent's report without the trailer the CLI adds for the main agent (its
// agentId, how to resume it, its usage).
func reportText(output string) string {
	if loc := ccAgentIDLine.FindStringIndex(output); loc != nil {
		if head := strings.TrimSpace(output[:loc[0]]); head != "" {
			return head
		}
	}
	return output
}

var (
	ccNoticeTag   = func(name string) *regexp.Regexp { return regexp.MustCompile(`(?s)<` + name + `>(.*?)</` + name + `>`) }
	ccNoticeTask  = ccNoticeTag("task-id")
	ccNoticeCall  = ccNoticeTag("tool-use-id")
	ccNoticeState = ccNoticeTag("status")
	ccNoticeBody  = ccNoticeTag("result")
	ccNoticeToks  = ccNoticeTag("subagent_tokens")
	ccNoticeUses  = ccNoticeTag("tool_uses")
	ccNoticeTime  = ccNoticeTag("duration_ms")
)

func noticeField(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// ccNotice is one task notification, in either form the CLI writes it: a <task-notification> block
// in a user line (a notice that reaches a turn in progress), or a system line of subtype
// task_notification (a task that ends while the session is idle: the CLI starts a turn of its own
// and writes no user line for the notice at all).
type ccNotice struct {
	taskID, callID, status, summary, result, event string
	tokens, toolUses                               int
	durationMs                                     int64
}

// noticeFromBlock reads one <task-notification> block.
func noticeFromBlock(block string) ccNotice {
	n := ccNotice{
		taskID: noticeField(ccNoticeTask, block), callID: noticeField(ccNoticeCall, block),
		status: noticeField(ccNoticeState, block), summary: noticeField(ccNoticeSum, block),
		result: noticeField(ccNoticeBody, block), event: noticeField(ccNoticeEvent, block),
	}
	n.tokens, _ = strconv.Atoi(noticeField(ccNoticeToks, block))
	n.toolUses, _ = strconv.Atoi(noticeField(ccNoticeUses, block))
	n.durationMs, _ = strconv.ParseInt(noticeField(ccNoticeTime, block), 10, 64)
	return n
}

// noticeFromSystemLine reads a system task_notification line. Its status is always an end
// (completed, failed or stopped); it carries a summary but never the result, and an agent's usage.
func noticeFromSystemLine(line []byte) (ccNotice, bool) {
	var l struct {
		TaskID    string `json:"task_id"`
		ToolUseID string `json:"tool_use_id"`
		Status    string `json:"status"`
		Summary   string `json:"summary"`
		Usage     *struct {
			TotalTokens int   `json:"total_tokens"`
			ToolUses    int   `json:"tool_uses"`
			DurationMs  int64 `json:"duration_ms"`
		} `json:"usage"`
	}
	if json.Unmarshal(line, &l) != nil || l.TaskID == "" && l.ToolUseID == "" {
		return ccNotice{}, false
	}
	// An empty status is left empty: closeBackgroundTask decides, as for a block without one (a
	// monitor's event does not end it; a subagent's notice does).
	n := ccNotice{taskID: l.TaskID, callID: l.ToolUseID, status: strings.TrimSpace(l.Status), summary: strings.TrimSpace(l.Summary)}
	if l.Usage != nil {
		n.tokens, n.toolUses, n.durationMs = l.Usage.TotalTokens, l.Usage.ToolUses, l.Usage.DurationMs
	}
	return n, true
}

// closeNotified ends what one notice names: the background subagent this driver launched, and the
// background task it started. Either may be gone already (the same end seen in the other form, or
// the engine ended it), and then nothing is reported again.
func (d *claudeCodeDriver) closeNotified(n ccNotice) {
	d.closeBackgroundSubagent(n)
	d.closeBackgroundTask(n)
}

// noticeTaskNotifications reads the <task-notification> blocks of a user line the CLI injected:
// one for a background subagent this driver launched ends it with a final subagent_done (its
// result, totals and duration). Each also ends the background task it names, if this driver
// started one (claudebackground.go); a notification for anything else is left alone.
func (d *claudeCodeDriver) noticeTaskNotifications(ev *ccLine) {
	text, ok := ev.text()
	if !ok {
		var parts []string
		for _, b := range ev.blocks() {
			if b.Type == "text" {
				parts = append(parts, b.Text)
			}
		}
		text = strings.Join(parts, "\n")
	}
	for {
		i := strings.Index(text, "<task-notification>")
		if i < 0 {
			return
		}
		text = text[i+len("<task-notification>"):]
		block := text
		if j := strings.Index(text, "</task-notification>"); j >= 0 {
			block, text = text[:j], text[j:]
		}
		d.closeNotified(noticeFromBlock(block))
	}
}

func (d *claudeCodeDriver) closeBackgroundSubagent(n ccNotice) {
	task, call := n.taskID, n.callID
	d.subMu.Lock()
	id := ""
	if s := d.subagents[call]; call != "" && s != nil && s.background {
		id = call
	} else if c, ok := d.agentIDs[task]; task != "" && ok {
		id = c
	}
	s := d.subagents[id]
	if s == nil || !s.background {
		d.subMu.Unlock()
		return
	}
	delete(d.subagents, id)
	for k, v := range d.agentIDs {
		if v == id {
			delete(d.agentIDs, k)
		}
	}
	d.forgetRunLocked(id, s)
	var closeProc *ccProc
	if s.resumed && d.backgroundRunningLocked() == 0 {
		closeProc = d.backgroundIdleLocked()
	}
	d.reportOpenWorkLocked()
	p := s.payload()
	p.DurationMs = d.clock().Sub(s.started).Milliseconds()
	d.subMu.Unlock()
	if closeProc != nil {
		defer d.finishStdinClose(closeProc)
	}

	if p.Usage == nil && n.tokens > 0 && !s.resumed {
		// Either form's figure (<subagent_tokens>, usage.total_tokens) is the subagent's TOTAL, not
		// its output alone; with nothing accumulated it is the only figure there is. Not for a
		// resumed run: the figure then counts the agent's every run, and would count them again.
		p.Usage = &agent.Usage{OutputTokens: n.tokens}
	}
	if n.toolUses > 0 {
		p.ToolUses = n.toolUses
	}
	if n.durationMs > 0 {
		p.DurationMs = n.durationMs
	}
	body := n.result
	if body == "" {
		body = n.summary // a system notice carries no result
	}
	body = cutRunes(body, 120)
	// Only "failed" is a failure: killed and stopped mean a person stopped the subagent.
	p.IsError = n.status == "failed"
	switch n.status {
	case "", "completed":
		p.Summary = body
	case "failed":
		p.Summary = "error: " + body
	default: // killed, stopped…
		p.Summary = n.status
		if body != "" {
			p.Summary += ": " + body
		}
	}
	d.emit("subagent_done", p)
}
