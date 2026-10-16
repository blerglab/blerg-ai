package daemon

// What the Claude Code driver does for a session that is hosted again after a daemon restart
// (design: docs/design/agent-session-recovery.md).
//
// The driver reports the two things a recovery record needs from it (the Claude session id and
// the engine process), and, once adopted, tells the model what happened: a note prepended to the
// first message after the adoption, on the line written to the engine ONLY. The transcript's
// user_message keeps the text as the person wrote it, so the note is never shown as their words.
// It also reports what is running in the background (reportOpenWorkLocked): the tasks and the
// subagents a restart cuts off, which adoption then reports stopped (agentadopt.go).

import (
	"sort"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// adoption describes a driver that was rebuilt from a recovery record.
type adoption struct {
	// hadConversation: the record held a Claude session id, so there was a conversation to
	// resume (whether or not its transcript could be found).
	hadConversation bool
	// cutOff: a turn was running when the previous daemon stopped.
	cutOff bool
	// work is what the restart cut off in the background (cutOffNotice), told with the next
	// message, whether that is the person's or recovery's own continue; "" when nothing was.
	work string
	// cause is why the daemon restarted and how to go on (restartCauseWords), told after the
	// rest whenever there is something to tell about a resumed conversation.
	cause string
}

const (
	adoptNoteCutOff = "[system] The process hosting this session was restarted while you were working. " +
		"The conversation is intact, but the step that was running was cut off: a command may not have finished " +
		"and a file may be half written."
	adoptNoteLost = "[system] The process hosting this session was restarted and the earlier conversation could not be restored. " +
		"The files in the workspace are as they were. The person can still see the earlier transcript, but you cannot: " +
		"ask them for what you need."
	// adoptContinuePrompt is what recovery sends a session whose work was cut off (adoptPlan), and
	// what the sessions list's "Resume all" sends one that was left paused.
	adoptContinuePrompt = "Continue the task you were working on."
)

// adopt marks the driver as hosting a recovered session: resumeID ("" = start fresh) becomes the
// conversation to resume, and the next message carries the adoption note. Called before Run.
func (d *claudeCodeDriver) adopt(resumeID string, a adoption) {
	d.mu.Lock()
	d.ccSessionID = resumeID
	d.mu.Unlock()
	d.sm.Lock()
	d.adopted, d.adoptPending = a, true
	d.sm.Unlock()
}

// adoptionNote is what the model is told, decided when the message is written: a conversation
// that could not be resumed (no transcript, or the CLI refused the id and cleared it) outranks a
// turn that was cut off, and a clean resume says nothing; what the restart cut off in the
// background follows either, and why the daemon restarted ends a resumed one's note.
func (d *claudeCodeDriver) adoptionNote() string {
	if !d.adopted.hadConversation {
		return ""
	}
	d.adoptMu.Lock()
	extra := d.adoptExtra
	d.adoptMu.Unlock()
	d.mu.Lock()
	resumed := d.ccSessionID != ""
	d.mu.Unlock()
	note := ""
	switch {
	case !resumed:
		note = adoptNoteLost
	case d.adopted.cutOff:
		note = adoptNoteCutOff
	}
	work := d.adopted.work
	if extra != "" {
		if work == "" {
			work = "[system] The process hosting this session restarted, and work that was running was cut off."
		}
		work += "\n" + extra
	}
	out := note
	switch {
	case work == "":
	case note == "":
		out = work
	default:
		out = note + "\n\n" + work
	}
	if out != "" && resumed && d.adopted.cause != "" {
		out += "\n\n" + d.adopted.cause
	}
	return out
}

// addAdoptionWork adds a line to what the adoption note says was cut off: something only learnt
// once connected (the server's open ask).
func (d *claudeCodeDriver) addAdoptionWork(line string) {
	d.adoptMu.Lock()
	defer d.adoptMu.Unlock()
	if d.adoptExtra != "" {
		d.adoptExtra += "\n"
	}
	d.adoptExtra += line
}

// continueAdopted sends recovery's continue for an adopted session (prompt to the engine, after
// the adoption note and what was cut off; shown in the transcript as the system's message), and
// reports whether it did. Not when a message from the person has reached the session first (one
// held while the daemon was down, or written since): that message carries the note and the list,
// and the person, not recovery, says what happens next.
func (d *claudeCodeDriver) continueAdopted(prompt, shown string) bool {
	d.sm.Lock()
	heard := d.heardFromPerson
	d.sm.Unlock()
	if heard {
		return false
	}
	d.EnqueueInput(agent.Input{Text: prompt, Shown: shown, Source: "system"})
	return true
}

// unlessHeardFromPerson runs fn, and reports that it did, only if no message from the person has
// reached the session since it was hosted again. Under the lock a person's message takes first, so
// what fn sends goes out before anything that message makes the session report.
func (d *claudeCodeDriver) unlessHeardFromPerson(fn func()) bool {
	d.sm.Lock()
	defer d.sm.Unlock()
	if d.heardFromPerson {
		return false
	}
	fn()
	return true
}

// engineText is the text written to the engine for a message: the message itself, preceded by
// the adoption note when this is the first message after an adoption.
func (d *claudeCodeDriver) engineText(text string, adopt bool) string {
	if !adopt {
		return text
	}
	if note := d.adoptionNote(); note != "" {
		return note + "\n\n" + text
	}
	return text
}

// setCCSessionID records the Claude session id the engine reported ("" is ignored) and tells the
// recovery record when it changed.
func (d *claudeCodeDriver) setCCSessionID(id string) {
	if id == "" {
		return
	}
	d.mu.Lock()
	changed := d.ccSessionID != id
	d.ccSessionID = id
	d.mu.Unlock()
	if changed && d.onSessionID != nil {
		d.onSessionID(id)
	}
}

// clearCCSessionID forgets a conversation the CLI refused to resume. The record keeps the old id
// until the fresh conversation reports its own: a daemon that dies in between then finds no
// transcript problem it has not already handled (the id is refused again, and cleared again).
func (d *claudeCodeDriver) clearCCSessionID() {
	d.mu.Lock()
	d.ccSessionID = ""
	d.mu.Unlock()
}

// reportEngine tells the recovery record that the engine process pid started or went away. The
// pid travels with "went away" so a late report about an old process cannot erase a newer one.
func (d *claudeCodeDriver) reportEngine(pid int, running bool) {
	if d.onEngine != nil && pid > 0 {
		d.onEngine(pid, running)
	}
}

// rearmAdoptNoteLocked makes the next message carry the adoption note again when one of msgs was
// to carry it but is being handed to the per-turn engine instead (which takes plain messages).
// The caller holds sm.
func (d *claudeCodeDriver) rearmAdoptNoteLocked(msgs []steerQueued) {
	for _, m := range msgs {
		if m.adopt {
			d.adoptPending = true
		}
	}
}

// reportOpenWorkLocked tells the recovery record which background tasks and subagents are running
// now, oldest first. The caller holds subMu, so two reports can never be written out of order.
func (d *claudeCodeDriver) reportOpenWorkLocked() {
	if d.onOpenWork == nil {
		return
	}
	tasks := make([]agent.BackgroundTaskStartedPayload, 0, len(d.bgTasks))
	for _, t := range d.bgTasks {
		tasks = append(tasks, t.p)
	}
	sort.Slice(tasks, func(i, j int) bool {
		a, b := tasks[i], tasks[j]
		return a.StartedAt < b.StartedAt || a.StartedAt == b.StartedAt && a.TaskID < b.TaskID
	})
	agentIDs := make(map[string]string, len(d.agentIDs))
	for agentID, call := range d.agentIDs {
		agentIDs[call] = agentID
	}
	type started struct {
		at time.Time
		s  OpenSubagent
	}
	subs := make([]started, 0, len(d.subagents))
	for id, s := range d.subagents {
		p := s.payload()
		o := OpenSubagent{ChildID: id, AgentType: p.AgentType, Model: p.Model, Summary: s.p.Summary, AgentID: agentIDs[id]}
		if o.AgentID != "" {
			// A resumed run is as its original ran; any other with an agentId was launched in the background.
			bg := !s.resumed || s.resumedBg
			o.Background = &bg
		}
		subs = append(subs, started{s.started, o})
	}
	sort.Slice(subs, func(i, j int) bool {
		a, b := subs[i], subs[j]
		return a.at.Before(b.at) || a.at.Equal(b.at) && a.s.ChildID < b.s.ChildID
	})
	out := make([]OpenSubagent, len(subs))
	for i, s := range subs {
		out[i] = s.s
	}
	d.onOpenWork(tasks, out)
}
