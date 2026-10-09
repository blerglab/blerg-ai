package daemon

// Hosting agent sessions again after a daemon restart (design: docs/design/agent-session-recovery.md).
//
// Adoption happens once, at process start. BeginAdoption runs before the daemon connects and
// only claims the recorded sessions, so the hello lists them and the server keeps their rows.
// RunAdoption then hosts each one again — resuming its Claude conversation — in the background,
// because reinstalling a session's plugins can take minutes and the daemon must not stay offline
// for that. It never runs on a reconnect: the sessions are in memory then.

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// adoptRequest carries a record into AgentHost.spawn and the outcome back out.
type adoptRequest struct {
	rec AgentRecord
	// resumeID is the Claude conversation to resume: the recorded one when its transcript is
	// still on disk, "" to start fresh.
	resumeID string

	failed      string // why the session could not be hosted ("" = it was)
	pluginsLost bool   // some or all of its plugins could not be reinstalled
}

// adoptingSession is a recorded session that has been claimed but is not hosted again yet.
type adoptingSession struct {
	rec     AgentRecord
	held    []heldMessage // messages that arrived meanwhile, in order
	dropped bool          // stopped meanwhile: it must not come back
}

type heldMessage struct{ text, source string }

// maxHeldMessages bounds what is kept for one session while it is being brought back.
const maxHeldMessages = 32

// claudeTranscriptExists reports whether Claude Code still has the conversation. A var so tests
// need no ~/.claude.
var claudeTranscriptExists = transcriptExists

// adoptContinueDelay is how long after the daemon first connects a cut-off turn is continued.
// The wait is for the server's answer to the hello: a session that was stopped while the daemon
// was down is killed then, and must not have started working again first.
var adoptContinueDelay = 5 * time.Second

// recordable reports whether a session gets a recovery record: a host-runtime Claude Code
// session that is neither restricted nor granted, on a host that keeps records. Called from
// spawn once the driver is known to be Claude Code.
//
// A restricted or granted session is left out because recovering it would mean writing its
// gateway credentials to disk; a sandboxed one because its engine dies in a container the
// restart sweep removes.
func (h *AgentHost) recordable(msg protocol.SpawnSession) bool {
	return h.cfg.RecordsDir != "" && !msg.Sandbox && !msg.RestrictTools && msg.MCPGateway == nil
}

// keepRecord starts the session's record: a new one for a spawn, the adopted one carried on for
// an adoption (its "a turn was running" stays until a turn runs and ends, so a second restart
// before the person has said anything still tells the model about the first).
func (h *AgentHost) keepRecord(msg protocol.SpawnSession, workDir, model string, ad *adoptRequest) *recordKeeper {
	rec := AgentRecord{
		SessionID: msg.SessionID, Repo: msg.Repo, WorkDir: workDir,
		Interaction: msg.Interaction, Assist: msg.Assist, BoardID: msg.BoardID, TicketID: msg.TicketID,
		SessionToken: msg.SessionToken, BoardToken: msg.BoardToken, ExtraEnv: msg.ExtraEnv,
		Plugins: msg.Plugins, Model: model, Effort: msg.Effort,
	}
	if ad != nil {
		rec.CreatedAt = ad.rec.CreatedAt
		rec.ClaudeSessionID = ad.rec.ClaudeSessionID
		rec.TurnActive = ad.rec.TurnActive
		rec.AutoContinues = ad.rec.AutoContinues
	}
	return newRecordKeeper(h.cfg.RecordsDir, &h.frozen, rec)
}

// Freeze stops every recovery-record write. It is the daemon's first act when it is told to
// stop: the engines are stopped with it, each driver then reports its turn over, and that
// report must not erase the "a turn was running" the next daemon needs to see.
func (h *AgentHost) Freeze() { h.frozen.Store(true) }

// DropRecord forgets a session this daemon is not hosting: a stop that arrived for a session
// that was never adopted, or before it could be (it is then not brought back at all).
func (h *AgentHost) DropRecord(sessionID string) {
	h.mu.Lock()
	if a := h.adopting[sessionID]; a != nil {
		a.dropped = true
		a.held = nil
	}
	h.mu.Unlock()
	deleteAgentRecord(h.cfg.RecordsDir, sessionID)
}

// BeginAdoption claims every recorded session and returns how many there are. From here on they
// are reported as this daemon's sessions; RunAdoption hosts them.
func (h *AgentHost) BeginAdoption() int {
	if h.cfg.RecordsDir == "" {
		return 0
	}
	recs := listAgentRecords(h.cfg.RecordsDir)
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, rec := range recs {
		if _, hosted := h.sessions[rec.SessionID]; hosted {
			continue
		}
		if _, claimed := h.adopting[rec.SessionID]; claimed {
			continue
		}
		h.adopting[rec.SessionID] = &adoptingSession{rec: rec}
		n++
	}
	return n
}

// RunAdoption hosts every claimed session again, one after another, and returns how many it
// hosted. A session that cannot be hosted loses its record and has its end reported.
func (h *AgentHost) RunAdoption() int {
	h.mu.Lock()
	ids := make([]string, 0, len(h.adopting))
	for id := range h.adopting {
		ids = append(ids, id)
	}
	h.mu.Unlock()

	adopted := 0
	for _, id := range ids {
		h.mu.Lock()
		a := h.adopting[id]
		skip := a == nil || a.dropped
		h.mu.Unlock()
		if skip {
			h.endAdoption(id)
			continue
		}
		reason := h.adoptOne(a.rec)

		// The session is hosted (or not): release what waited for it, in order. A stop that
		// arrived while it was being hosted wins.
		h.mu.Lock()
		dropped, held := a.dropped, a.held
		delete(h.adopting, id)
		if reason != "" {
			h.adoptFailed = append(h.adoptFailed, id)
		}
		h.mu.Unlock()
		switch {
		case reason != "":
			log.Printf("agent session %s: not recovered after the restart: %s", id, reason)
			deleteAgentRecord(h.cfg.RecordsDir, id)
		case dropped:
			h.Kill(id)
		default:
			adopted++
			for _, m := range held {
				h.UserMessage(id, m.text, m.source)
			}
		}
	}
	h.ReportAdoptionFailures()
	return adopted
}

func (h *AgentHost) endAdoption(id string) {
	h.mu.Lock()
	delete(h.adopting, id)
	h.mu.Unlock()
}

// AdoptRecorded is BeginAdoption and RunAdoption in one go.
func (h *AgentHost) AdoptRecorded() int {
	h.BeginAdoption()
	return h.RunAdoption()
}

// holdForAdoption keeps a message for a session that is still being brought back, and reports
// whether it did. RunAdoption delivers it once the session is hosted.
func (h *AgentHost) holdForAdoption(sessionID, text, source string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	a := h.adopting[sessionID]
	if a == nil || a.dropped {
		return false
	}
	if len(a.held) >= maxHeldMessages {
		return false
	}
	a.held = append(a.held, heldMessage{text: text, source: source})
	return true
}

// adoptOne hosts one recorded session; "" means it is hosted.
func (h *AgentHost) adoptOne(rec AgentRecord) string {
	if !filepath.IsAbs(rec.WorkDir) {
		return "its record names no workspace"
	}
	if info, err := os.Stat(rec.WorkDir); err != nil || !info.IsDir() {
		return fmt.Sprintf("its workspace %s is gone", rec.WorkDir)
	}
	msg := protocol.SpawnSession{
		Type: "spawn_session", SessionID: rec.SessionID, Kind: "agent",
		ProjectPath: rec.WorkDir, Repo: rec.Repo,
		Interaction: rec.Interaction, Assist: rec.Assist, BoardID: rec.BoardID, TicketID: rec.TicketID,
		SessionToken: rec.SessionToken, BoardToken: rec.BoardToken, ExtraEnv: rec.ExtraEnv,
		Plugins: rec.Plugins, Model: rec.Model, Effort: rec.Effort,
	}
	// A model or effort the daemon no longer accepts must not cost the session: fall back to the
	// default, as a spawn with none would.
	if !models.ValidModelFor(msg.Engine, msg.Model) {
		msg.Model = ""
	}
	if !models.ValidEffortFor(msg.Engine, msg.Effort) {
		msg.Effort = ""
	}
	// The session ran on Claude Code. If this daemon would now pick another driver (the CLI was
	// removed), the conversation cannot be continued by it.
	if kind, _ := h.driverFor(msg); kind != driverClaudeCode {
		return "Claude Code is no longer available on this machine"
	}
	stopLeftoverEngine(rec)

	ad := &adoptRequest{rec: rec}
	if rec.ClaudeSessionID != "" && claudeTranscriptExists(rec.ClaudeSessionID) {
		ad.resumeID = rec.ClaudeSessionID
	}
	h.spawn(msg, nil, "", ad)
	if ad.failed != "" {
		return ad.failed
	}
	if !h.Has(rec.SessionID) {
		return "it could not be started"
	}
	return ""
}

// finishAdoption is the tail of an adopting spawn: say in the transcript what happened, and
// continue a cut-off turn of an unattended session — once.
func (h *AgentHost) finishAdoption(ad *adoptRequest, sess *agentSession, driver sessionDriver, msg protocol.SpawnSession) {
	hadConversation := ad.rec.ClaudeSessionID != ""
	resumed := ad.resumeID != ""
	cutOff := ad.rec.TurnActive

	// Nobody is reading an unattended session, so a turn that was cut off is picked up again.
	// Only once per finished turn (AutoContinues): a turn whose own command restarts or crashes
	// the daemon would otherwise be re-run on every start, for ever.
	cont := cutOff && resumed && msg.Interaction == protocol.InteractionUnattended && ad.rec.AutoContinues == 0
	gaveUp := cutOff && resumed && msg.Interaction == protocol.InteractionUnattended && !cont

	parts := []string{"The session's host restarted."}
	switch {
	case !hadConversation:
		parts = append(parts, "The session is running again.")
	case !resumed:
		parts = append(parts, "The earlier conversation could not be restored: the agent starts fresh, with the files as they were.")
	default:
		parts = append(parts, "The conversation was restored.")
	}
	if cutOff {
		parts = append(parts, "The step that was running was cut off.")
	}
	switch {
	case cont:
		parts = append(parts, "The agent is being asked to continue.")
	case gaveUp:
		parts = append(parts, "It was cut off after being continued once already, so it is waiting for a message.")
	}
	if ad.pluginsLost {
		parts = append(parts, "Its plugins could not all be reinstalled.")
	}
	// A retryable error event: the chat renders it, it closes the tool call the restart left
	// spinning, and (being retryable) it does not end the session.
	sess.emitter.Emit(agent.Event{ClientEventID: ccUUID(), Ts: time.Now(), Kind: "error",
		Payload: agent.ErrorPayload{Message: strings.Join(parts, " "), Retryable: true}})
	log.Printf("agent session %s: recovered after a daemon restart (conversation resumed=%v, turn cut off=%v, continuing=%v)",
		msg.SessionID, resumed, cutOff, cont)

	if !cont {
		return
	}
	// Counted now, before anything runs: if this continue is what takes the daemon down again,
	// the next start must already know it was tried.
	sess.record.update(func(r *AgentRecord) bool { r.AutoContinues++; return true })
	go func() {
		// Not before the server has had its say: its answer to the hello is where a session
		// that was stopped while the daemon was down gets killed. With no server, no continue.
		<-h.connected
		time.Sleep(adoptContinueDelay)
		if h.get(msg.SessionID) != sess {
			return // stopped meanwhile
		}
		// Source "system": the chat does not show it as the person's message.
		driver.Enqueue(adoptContinuePrompt, "system")
	}()
}

// NoteConnected is called each time the daemon has a connection to its server: what adoption
// was holding back for one can go ahead.
func (h *AgentHost) NoteConnected() {
	h.connectedOnce.Do(func() { close(h.connected) })
	h.ReportAdoptionFailures()
}

// ReportAdoptionFailures tells the server about the sessions that could not be hosted again, so
// their rows end with a reason instead of waiting out the lost-daemon window. Before the first
// connection it does nothing; they are reported when there is one.
func (h *AgentHost) ReportAdoptionFailures() {
	select {
	case <-h.connected:
	default:
		return
	}
	h.mu.Lock()
	failed := h.adoptFailed
	h.adoptFailed = nil
	h.mu.Unlock()
	for _, id := range failed {
		if err := h.sender.Send(protocol.SessionEnded{Type: "session_ended", SessionID: id, ExitCode: 1}); err != nil {
			// The connection went away again: keep it for the next one.
			h.mu.Lock()
			h.adoptFailed = append(h.adoptFailed, id)
			h.mu.Unlock()
		}
	}
}

// reportUndelivered writes a retryable error into the transcript of a session this daemon was
// asked to message but is not hosting.
func (h *AgentHost) reportUndelivered(sessionID string) {
	log.Printf("agent session %s: message for a session this daemon is not hosting — not delivered", sessionID)
	e := newWSEmitter(h.sender, sessionID, nil)
	e.Emit(agent.Event{ClientEventID: ccUUID(), Ts: time.Now(), Kind: "error",
		Payload: agent.ErrorPayload{Message: "Message not delivered: this session is not running on its host any more.", Retryable: true}})
}
