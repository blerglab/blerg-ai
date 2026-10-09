package daemon

// What the Claude Code driver does for a session that is hosted again after a daemon restart
// (design: docs/design/agent-session-recovery.md).
//
// The driver reports the two things a recovery record needs from it (the Claude session id and
// the engine process), and, once adopted, tells the model what happened: a note prepended to the
// first message after the adoption, on the line written to the engine ONLY. The transcript's
// user_message keeps the text as the person wrote it, so the note is never shown as their words.

// adoption describes a driver that was rebuilt from a recovery record.
type adoption struct {
	// hadConversation: the record held a Claude session id, so there was a conversation to
	// resume (whether or not its transcript could be found).
	hadConversation bool
	// cutOff: a turn was running when the previous daemon stopped.
	cutOff bool
}

const (
	adoptNoteCutOff = "[system] The process hosting this session was restarted while you were working. " +
		"The conversation is intact, but the step that was running was cut off: a command may not have finished " +
		"and a file may be half written. The restart may have been caused by a command you ran: do not run it again. " +
		"Check the state of your work before continuing."
	adoptNoteLost = "[system] The process hosting this session was restarted and the earlier conversation could not be restored. " +
		"The files in the workspace are as they were. The person can still see the earlier transcript, but you cannot: " +
		"ask them for what you need."
	// adoptContinuePrompt is what recovery sends an unattended session whose turn was cut off.
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
// turn that was cut off, and a clean resume says nothing.
func (d *claudeCodeDriver) adoptionNote() string {
	if !d.adopted.hadConversation {
		return ""
	}
	d.mu.Lock()
	resumed := d.ccSessionID != ""
	d.mu.Unlock()
	switch {
	case !resumed:
		return adoptNoteLost
	case d.adopted.cutOff:
		return adoptNoteCutOff
	}
	return ""
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
