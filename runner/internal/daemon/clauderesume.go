package daemon

// Resumed subagents. Claude Code's main agent can resume a subagent that has finished by sending it
// a message (its SendMessage tool, to the agentId the subagent's Agent call returned). The CLI then
// runs that subagent again in the background: SendMessage returns at once ("Resuming agent …", with
// resumedAgentId), the run's own lines carry the SendMessage call's id as their parent_tool_use_id,
// and its end is a task notification naming the same agentId (task-id) and the SendMessage call
// (tool-use-id). SendMessage can also hold its result until the run is over and return the
// subagent's report itself; the run's lines then arrive before that result.
//
// The driver reports each such run as a new subagent instance: a subagent_started with Resumed set,
// whose ChildID is the SendMessage call's id, with the original's type, title and model, Background
// as the original ran and ResumedFrom naming the original's ChildID. It is then tracked like any
// subagent (updates, the recovery record) and ended by its notice, by the SendMessage result that
// returns its report, or by the engine's end. A run of a background subagent whose SendMessage call
// has returned is also a background task, as the subagent's launch was (task id: the SendMessage
// call's id, kind "agent"), so the session's count of running tasks includes it.
//
// The agents a run can resume are known from their Agent calls' results, and after a daemon restart
// from the recovery record (seedKnownAgents). A resume of an agent known neither way still opens a
// run, titled "resumed agent". Known gap: the CLI also re-runs a background subagent on its own
// (when its own background children report back, with no SendMessage); such a run is not seen as
// one, and its lines fold into the finished card.

import (
	"encoding/json"
	"regexp"
	"strings"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// ccSendTool is the tool the main agent messages (and so resumes) a subagent with.
const ccSendTool = "SendMessage"

// ccKnownAgent is a subagent the main agent may resume, kept by its agentId after it ends.
type ccKnownAgent struct {
	p          agent.SubagentPayload // its original start: type, model as asked, title
	origin     string                // the call id of the Agent call that started it first
	background bool                  // the original ran in the background
	call       string                // the call id of its newest run (the Agent call, then each resume)
}

// ccMaxKnownAgents bounds known: past it the oldest id is dropped (a session that launched that
// many subagents is not going to resume its first ones; if it does, the run opens as an unknown
// agent's).
const ccMaxKnownAgents = 1000

// ccUnknownAgentTitle titles the run of a resumed agent the driver never saw start.
const ccUnknownAgentTitle = "resumed agent"

// ccHandBack begins the message of a SendMessage result that waited for the run and carries its
// report after the JSON ("Resumed agent. Its final report is not in this message." / "… follows
// this JSON, framed by the harness.").
const ccHandBack = "Resumed agent."

// What a SendMessage result says.
const (
	sendOther    = iota // nothing about a subagent run (an error, the main conversation, a teammate)
	sendResumed         // it resumed the agent; the run goes on without the call
	sendQueued          // it queued the message for a live agent
	sendHandBack        // it waited for the run: the report follows
)

var (
	// "Resuming agent a6f2d88": the CLI names the agent by a short prefix of its id.
	ccResumingAgent = regexp.MustCompile(`Resuming agent ([A-Za-z0-9_-]+)`)
	// "Message queued for delivery to a6fe90c6258de0cde at its next tool round."
	ccQueuedFor = regexp.MustCompile(`Message queued for delivery to (\S+?) at its next tool round`)
)

// rememberAgentLocked keeps agentID as resumable: a new id with the run s started by call id as its
// original, an id already known with call as its newest run. Called with subMu held.
func (d *claudeCodeDriver) rememberAgentLocked(agentID, call string, s *ccSubagent, background bool) {
	if d.known == nil {
		d.known = map[string]*ccKnownAgent{}
	}
	if k := d.known[agentID]; k != nil {
		k.call = call
		return
	}
	p := s.p
	p.Background = false
	d.addKnownLocked(agentID, &ccKnownAgent{p: p, origin: call, background: background, call: call})
}

// addKnownLocked keeps k as agentID, dropping the oldest past ccMaxKnownAgents. Called with subMu held.
func (d *claudeCodeDriver) addKnownLocked(agentID string, k *ccKnownAgent) {
	if d.known == nil {
		d.known = map[string]*ccKnownAgent{}
	}
	if d.known[agentID] == nil {
		d.knownOrder = append(d.knownOrder, agentID)
	}
	d.known[agentID] = k
	for len(d.known) > ccMaxKnownAgents && len(d.knownOrder) > 0 {
		delete(d.known, d.knownOrder[0])
		d.knownOrder = d.knownOrder[1:]
	}
}

// seedKnownAgents makes the background subagents a recovery record kept resumable: after a restart
// the agent is told to resume them, by their agentId. One without an agentId (cut off in the
// foreground) cannot be addressed and is left out.
func (d *claudeCodeDriver) seedKnownAgents(subs []OpenSubagent) {
	d.subMu.Lock()
	defer d.subMu.Unlock()
	for _, o := range subs {
		if o.AgentID == "" || o.ChildID == "" || d.known[o.AgentID] != nil {
			continue
		}
		p := agent.SubagentPayload{AgentType: o.AgentType, Model: o.Model, Summary: o.Summary}
		if p.AgentType == "" {
			p.AgentType = "general-purpose"
		}
		d.addKnownLocked(o.AgentID, &ccKnownAgent{p: p, origin: o.ChildID, background: o.ranInBackground(), call: o.ChildID})
	}
}

// unknownAgentLocked keeps an agent first seen being resumed by call id call, so its run opens with
// a placeholder title and a later resume of it is known. Called with subMu held.
func (d *claudeCodeDriver) unknownAgentLocked(agentID, call string) *ccKnownAgent {
	k := &ccKnownAgent{p: agent.SubagentPayload{AgentType: "general-purpose", Summary: ccUnknownAgentTitle}, origin: call, call: call}
	d.addKnownLocked(agentID, k)
	return k
}

// forgetRunLocked drops what pointed at a resumed run that ended (call id id). Called with subMu held.
func (d *claudeCodeDriver) forgetRunLocked(id string, s *ccSubagent) {
	if s.resumed && d.resumedFrom[s.p.ResumedFrom] == id {
		delete(d.resumedFrom, s.p.ResumedFrom)
	}
}

// noteSend keeps a SendMessage call of the main agent until its result: its recipient, so a run that
// sends lines before the result arrives can be told apart.
func (d *claudeCodeDriver) noteSend(id string, raw json.RawMessage) {
	var in struct {
		To string `json:"to"`
	}
	_ = json.Unmarshal(raw, &in)
	d.subMu.Lock()
	if d.sends == nil {
		d.sends = map[string]string{}
	}
	d.sends[id] = strings.TrimSpace(in.To)
	d.subMu.Unlock()
}

// resolveLocked finds the known subagent a name addresses: its agentId, else the one id it is a
// prefix of (the CLI's "Resuming agent a6f2d88"). Called with subMu held.
func (d *claudeCodeDriver) resolveLocked(name string) (string, *ccKnownAgent) {
	if name == "" {
		return "", nil
	}
	if k := d.known[name]; k != nil {
		return name, k
	}
	if len(name) < 6 {
		return "", nil
	}
	var id string
	for k := range d.known {
		if strings.HasPrefix(k, name) {
			if id != "" {
				return "", nil // ambiguous
			}
			id = k
		}
	}
	if id == "" {
		return "", nil
	}
	return id, d.known[id]
}

// startResumedLocked opens a resumed run of agent k (agentId agentID) under call id call, and
// returns the subagent_started to emit, or false when that agent's newest run is still open (a
// message to a live subagent resumes nothing). detached: the call has already returned. The caller
// reports the open work (once, with the background task a detached run may also be). Called with
// subMu held.
func (d *claudeCodeDriver) startResumedLocked(call, agentID string, k *ccKnownAgent, detached bool) (agent.SubagentPayload, bool) {
	if d.subagents[k.call] != nil || d.subagents[call] != nil {
		return agent.SubagentPayload{}, false
	}
	p := k.p
	p.ChildID, p.Resumed, p.ResumedFrom, p.Background = call, true, "", false
	if k.origin != call { // an agent first seen being resumed has no original to name
		p.ResumedFrom = k.origin
	}
	p.Usage, p.ToolUses, p.DurationMs, p.IsError = nil, 0, 0, false
	if d.subagents == nil {
		d.subagents = map[string]*ccSubagent{}
	}
	d.subagents[call] = &ccSubagent{p: p, resumed: true, resumedBg: k.background, background: detached, agentID: agentID,
		usage: map[string]agent.Usage{}, started: d.clock()}
	if d.agentIDs == nil {
		d.agentIDs = map[string]string{}
	}
	d.agentIDs[agentID] = call
	if p.ResumedFrom != "" {
		if d.resumedFrom == nil {
			d.resumedFrom = map[string]string{}
		}
		d.resumedFrom[k.origin] = call
	}
	k.call = call
	started := p
	started.Background = k.background // as the original ran: a background one stays one
	return started, true
}

// subagentParent is the parent call id a subagent's line is reported under. A line of a resumed run
// carries the SendMessage call's id: before that call's result it opens the run (the call waits for
// the subagent's report). A line that names the original Agent call while a resumed run of it is
// open (a CLI that keeps the first call's id) is put under that run.
func (d *claudeCodeDriver) subagentParent(parent string) string {
	if parent == "" {
		return ""
	}
	d.subMu.Lock()
	if d.subagents[parent] != nil {
		d.subMu.Unlock()
		return parent
	}
	if run, ok := d.resumedFrom[parent]; ok && d.subagents[run] != nil {
		d.subMu.Unlock()
		return run
	}
	to, ok := d.sends[parent]
	if !ok {
		d.subMu.Unlock()
		return parent
	}
	id, k := d.resolveLocked(to)
	var p agent.SubagentPayload
	started := false
	if k != nil {
		p, started = d.startResumedLocked(parent, id, k, false)
	}
	if started {
		d.reportOpenWorkLocked()
	}
	d.subMu.Unlock()
	if started {
		d.emit("subagent_started", p)
	}
	return parent
}

// readSend reads a SendMessage result: what it says (sendResumed, sendQueued, sendHandBack or
// sendOther), the agent it names when it names one, and for a hand-back the report that follows the
// CLI's JSON.
func readSend(output string) (kind int, name, report string) {
	var r struct {
		Success        *bool  `json:"success"`
		Message        string `json:"message"`
		ResumedAgentID string `json:"resumedAgentId"`
	}
	text, rest := output, ""
	dec := json.NewDecoder(strings.NewReader(output))
	if dec.Decode(&r) == nil {
		if r.Success != nil && !*r.Success {
			return sendOther, "", ""
		}
		if r.ResumedAgentID != "" {
			return sendResumed, r.ResumedAgentID, ""
		}
		text, rest = r.Message, output[dec.InputOffset():]
	}
	if strings.HasPrefix(text, ccHandBack) {
		return sendHandBack, "", strings.TrimSpace(rest)
	}
	if m := ccResumingAgent.FindStringSubmatch(text); m != nil {
		return sendResumed, m[1], ""
	}
	if m := ccQueuedFor.FindStringSubmatch(text); m != nil {
		return sendQueued, m[1], ""
	}
	return sendOther, "", ""
}

// handBackReport is the report of a hand-back result (the text after the CLI's JSON), else output.
func handBackReport(output string) string {
	if kind, _, report := readSend(output); kind == sendHandBack {
		return report
	}
	return output
}

// sendResult handles the main agent's tool_result for call id when it is a SendMessage call's: a
// result that says a subagent was resumed opens its run, detached (unless its lines opened it
// already: it is then marked detached). A result that returns the run's report (a hand-back) is the
// end of the run, which endSubagent (next) reports as for an Agent call: the run is opened first if
// no line of it came before. A queued message opens a run only for an agent this driver thinks
// ended. An agent this driver does not know is kept from then on, titled ccUnknownAgentTitle.
func (d *claudeCodeDriver) sendResult(id, output string, isError bool) {
	d.subMu.Lock()
	to, ok := d.sends[id]
	if !ok {
		d.subMu.Unlock()
		return
	}
	delete(d.sends, id)
	kind := sendOther
	name := ""
	if !isError {
		kind, name, _ = readSend(output)
	}
	if s := d.subagents[id]; s != nil && s.resumed {
		// Its lines opened the run: a result that returns at once leaves it running; any other is its
		// end (endSubagent, next).
		var bg agent.BackgroundTaskStartedPayload
		tracked := false
		if kind == sendResumed || kind == sendQueued {
			s.background = true
			bg, tracked = d.trackResumedRunLocked(id)
			d.reportOpenWorkLocked()
		}
		d.subMu.Unlock()
		if tracked {
			d.emit("background_task_started", bg)
		}
		return
	}
	if kind == sendOther {
		d.subMu.Unlock()
		return
	}
	agentID, k := d.resolveLocked(name)
	if k == nil {
		agentID, k = d.resolveLocked(to)
	}
	if k == nil && kind != sendQueued { // a queued message names a live agent, or a teammate
		agentID = name
		if to != "" && (agentID == "" || strings.HasPrefix(to, agentID)) {
			agentID = to // the full id the call addressed, not the CLI's short form
		}
		if agentID != "" {
			k = d.unknownAgentLocked(agentID, id)
		}
	}
	var p agent.SubagentPayload
	var bg agent.BackgroundTaskStartedPayload
	started, tracked := false, false
	if k != nil {
		p, started = d.startResumedLocked(id, agentID, k, kind != sendHandBack)
	}
	if started {
		bg, tracked = d.trackResumedRunLocked(id)
		d.reportOpenWorkLocked()
	}
	d.subMu.Unlock()
	if started {
		d.emit("subagent_started", p)
	}
	if tracked {
		d.emit("background_task_started", bg)
	}
}
