package daemon

// Hosting agent sessions again after a daemon restart (design: docs/design/agent-session-recovery.md).
//
// Adoption happens once, at process start. BeginAdoption runs before the daemon connects and
// only claims the recorded sessions, so the hello lists them and the server keeps their rows.
// RunAdoption then hosts each one again — resuming its Claude conversation — in the background,
// because reinstalling a session's plugins can take minutes and the daemon must not stay offline
// for that. It never runs on a reconnect: the sessions are in memory then.

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	failed  string        // retained while recovery is paused; input retries adoption
	// awaitsServer: it failed only because the server was not connected yet, so the next
	// connection retries it without waiting for a message.
	awaitsServer bool
}

// adoptAwaitsServer is adoptOne's reason when a host workspace could not be re-proven because
// the server was not connected yet.
const adoptAwaitsServer = "Workspace recovery is waiting for the server connection."

// Why a recorded session's host workspace journal cannot be resumed: adoptOne's reasons, shown
// to the person on the paused session.
const (
	adoptWorkspaceUnverified = "Workspace recovery needs attention: the saved workspace record could not be verified."
	adoptWorkspaceForeign    = "Workspace recovery needs attention: the saved workspace record does not belong to this host session."
	adoptWorkspaceStopping   = "Its workspace was being stopped when the daemon went down, and the stop is not finished. It stays paused so no work is lost."
	adoptWorkspaceClosed     = "Its workspace was already closed and its work delivered, so it cannot continue there. Start a new session to continue."
)

// workspaceResumeRefusal is why a host workspace journal cannot be resumed, "" when it can be
// tried. A journal whose launch was closed (stop requested, final delivery made) can never be
// resumed in place: its runtime authority is spent, and resuming it would run the session outside
// its workspace. The person is told which case it is.
func workspaceResumeRefusal(state workspacePrivateManifest, readErr error, sessionID string) string {
	switch {
	case readErr != nil:
		return adoptWorkspaceUnverified
	case !state.Host || state.Session != sessionID:
		return adoptWorkspaceForeign
	case state.Stopped && workspaceFinalComplete(state):
		return adoptWorkspaceClosed
	case state.StopRequested || state.FinalDelivery != nil:
		return adoptWorkspaceStopping
	}
	return ""
}

type heldMessage = agent.Input

// maxHeldMessages bounds what is kept for one session while it is being brought back.
const maxHeldMessages = 32

// claudeTranscriptExists reports whether Claude Code still has the conversation. A var so tests
// need no ~/.claude.
var claudeTranscriptExists = transcriptExists

// adoptContinueDelay is how long after the daemon first connects a cut-off turn is continued.
// The wait is for the server's answer to the hello: a session that was stopped while the daemon
// was down is killed then, and must not have started working again first.
var adoptContinueDelay = 5 * time.Second

// recordable reports whether a Claude Code or Codex session gets a recovery record:
// a host session that is neither restricted nor granted, on a host that keeps records.
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
		SessionID: msg.SessionID, Repo: msg.Repo, WorkDir: workDir, Engine: msg.Engine,
		RequireRecoveryReady: msg.RequireRecoveryReady,
		Interaction:          msg.Interaction, Assist: msg.Assist, BoardID: msg.BoardID, TicketID: msg.TicketID,
		SessionToken: msg.SessionToken, BoardToken: msg.BoardToken, ExtraEnv: msg.ExtraEnv,
		Plugins: msg.Plugins, Model: model, Effort: msg.Effort,
	}
	if ad != nil {
		rec.CodexThreadID = ad.rec.CodexThreadID
		rec.EngineSession = ad.rec.EngineSession
		rec.KnownSubagents = ad.rec.KnownSubagents
		rec.PluginRequests = ad.rec.PluginRequests
		rec.CreatedAt = ad.rec.CreatedAt
		rec.ClaudeSessionID = ad.rec.ClaudeSessionID
		rec.CurrentTurnID = ad.rec.CurrentTurnID
		rec.TurnActive = ad.rec.TurnActive
		rec.ToolRunning, rec.OpenCalls, rec.Waiting = ad.rec.ToolRunning, ad.rec.OpenCalls, ad.rec.Waiting
		rec.AutoContinues = ad.rec.AutoContinues
	}
	return newRecordKeeper(h.cfg.RecordsDir, &h.frozen, rec)
}

// Freeze stops every recovery-record write. It is the daemon's first act when it is told to
// stop: the engines are stopped with it, each driver then reports its turn over, and that
// report must not erase the "a turn was running" the next daemon needs to see.
func (h *AgentHost) Freeze() { h.explorer.Close(); h.frozen.Store(true) }

// DropRecord forgets a session this daemon is not hosting: a stop that arrived for a session
// that was never adopted, or before it could be (it is then not brought back at all).
func (h *AgentHost) DropRecord(sessionID string) {
	h.mu.Lock()
	if a := h.adopting[sessionID]; a != nil {
		a.dropped = true
		a.held = nil
		if a.failed != "" {
			delete(h.adopting, sessionID)
		}
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
// hosted. Failed recovery keeps its record and reports a retryable pause.
func (h *AgentHost) RunAdoption() int {
	h.adoptionMu.Lock()
	defer h.adoptionMu.Unlock()
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
		if reason == "" || dropped {
			delete(h.adopting, id)
		}
		if reason != "" && !dropped {
			a.failed = reason
			a.awaitsServer = reason == adoptAwaitsServer
			h.adoptFailed = append(h.adoptFailed, id)
		}
		h.mu.Unlock()
		switch {
		case dropped:
			h.Kill(id)
		case reason != "":
			log.Printf("agent session %s: not recovered after the restart: %s", id, reason)
		default:
			adopted++
			for _, m := range held {
				h.UserInput(id, m)
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
func (h *AgentHost) holdForAdoption(sessionID string, in agent.Input) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	a := h.adopting[sessionID]
	if a == nil || a.dropped {
		return false
	}
	if in.ClientID != "" {
		for _, held := range a.held {
			if held.ClientID == in.ClientID {
				return true
			}
		}
	}
	if len(a.held) >= maxHeldMessages {
		return false
	}
	a.held = append(a.held, in)
	if a.failed != "" {
		a.failed = ""
		go h.RunAdoption()
	}
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
		Type: "spawn_session", SessionID: rec.SessionID, Kind: "agent", Engine: rec.Engine,
		ProjectPath: rec.WorkDir, Repo: rec.Repo,
		Interaction: rec.Interaction, Assist: rec.Assist, BoardID: rec.BoardID, TicketID: rec.TicketID,
		SessionToken: rec.SessionToken, BoardToken: rec.BoardToken, ExtraEnv: rec.ExtraEnv,
		Plugins: rec.Plugins, Model: rec.Model, Effort: rec.Effort,
		RequireRecoveryReady: rec.RequireRecoveryReady,
	}
	if h.cfg.Workspaces != nil {
		path, err := workspaceRecord(rec.SessionID)
		if err != nil {
			return "Workspace recovery is unavailable; retry after restarting the daemon."
		}
		if _, err = os.Lstat(path); err == nil {
			state, readErr := readPrivateWorkspace(path)
			if reason := workspaceResumeRefusal(state, readErr, rec.SessionID); reason != "" {
				return reason
			}
			if authority, ok := h.cfg.Workspaces.Authority.(*WorkspaceAuthority); ok {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				err = authority.client.WaitWorkspaceRuntime(ctx, "host")
				cancel()
				if err != nil {
					return adoptAwaitsServer
				}
			}
			msg.Workspace = &state.Launch
			msg.WorkspaceCapabilities = protocol.WorkspaceCapabilitiesFor("host", "linux")
		} else if !os.IsNotExist(err) {
			return "Workspace recovery is unavailable; retry after restarting the daemon."
		}
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
	if kind, refusal := h.driverFor(msg); kind == driverNone {
		return refusal
	} else if kind != driverClaudeCode && rec.Engine != "codex" && engineRegistry[rec.Engine].NativeSessionVersion == 0 {
		return "Claude Code is no longer available on this machine"
	}
	if rec.Engine == "codex" {
		if _, err := exec.LookPath("codex"); err != nil {
			return "Codex is no longer available on this machine"
		}
	}
	stopLeftoverEngine(rec)

	ad := &adoptRequest{rec: rec}
	switch {
	case rec.Engine == "codex":
		if codexTranscriptExists(rec.CodexThreadID) {
			ad.resumeID = rec.CodexThreadID
		}
	case rec.EngineSession != nil:
		if !rec.EngineSession.valid(rec.Engine) {
			return "its native conversation identity is invalid"
		}
		ad.resumeID = rec.EngineSession.ID
	case engineRegistry[rec.Engine].NativeSessionVersion > 0:
		return "its native conversation identity was not retained; start a new session explicitly"
	case rec.ClaudeSessionID != "" && claudeTranscriptExists(rec.ClaudeSessionID):
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

// adoptPlan is what recovery does about a session it hosts again. cont: pick its work up again
// by itself, now. gaveUp: it would, but it already did once since a turn last finished.
//
// What a restart cuts off — a turn (mid-generation, or in one of the agent's own tool calls), or
// background tasks or subagents whose end would have woken the agent — is picked up again by
// itself, unattended or interactive, so that nobody has to go through every session after a
// restart. Two exceptions. An interactive session that was waiting for the person (an open
// `blerg-runner ask`, a permission prompt: waitsOnPerson; the server's open asks are checked once
// it is connected, in finishAdoption): its question is theirs to answer. And any session cut off
// in a command on restartDenyList (a force push, a deploy, a migration, the installer...): the
// rule is deterministic, not left to the model. Both are paused, for the person to resume. Only
// once per finished turn (AutoContinues): a turn whose own command restarts or crashes the daemon
// would otherwise be re-run on every start, for ever.
func adoptPlan(rec AgentRecord, resumed bool, interaction string) (cont, gaveUp bool) {
	return adoptPlanAfter(rec, resumed, interaction, RestartCause{})
}
func adoptPlanAfter(rec AgentRecord, resumed bool, interaction string, cause RestartCause) (cont, gaveUp bool) {
	eligible := cutOffWork(rec) && resumed && (!destructiveCutOff(rec) || cause.updateSucceeded(rec))
	if interaction != protocol.InteractionUnattended {
		eligible = eligible && !waitsOnPerson(rec)
	}
	cont = eligible && rec.AutoContinues == 0
	return cont, eligible && !cont
}

// cutOffWork reports whether a restart cut anything off: a turn, background tasks or subagents.
func cutOffWork(rec AgentRecord) bool {
	return rec.TurnActive || len(rec.OpenTasks)+len(rec.OpenSubagents) > 0
}

// NoteOpenAsks records which sessions the server says have an open `blerg-runner ask` (the
// server's open_asks, sent after each hello). A record cannot always say (an older daemon kept no
// open calls), and the server keeps the asks.
func (h *AgentHost) NoteOpenAsks(sessionIDs []string) {
	asks := make(map[string]bool, len(sessionIDs))
	for _, id := range sessionIDs {
		asks[id] = true
	}
	h.mu.Lock()
	h.openAsks = asks
	h.mu.Unlock()
}

func (h *AgentHost) hasOpenAsk(sessionID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.openAsks[sessionID]
}

// finishAdoption is the tail of an adopting spawn: say in the transcript what happened, report
// stopped what the restart cut off, and pick up the session's work again when adoptPlan says so
// — once. A session that is not picked up again shows the cut-off notice as the system's message;
// the agent gets it with the next message (spawn puts it in the driver's adoption note), and the
// server is told it is paused by the restart (session_paused), so the sessions list can count it
// and offer to resume it.
func (h *AgentHost) finishAdoption(ad *adoptRequest, sess *agentSession, driver sessionDriver, msg protocol.SpawnSession) {
	if native, ok := driver.(engineSessionDriver); ok {
		go func() {
			select {
			case err := <-native.EngineSessionLoaded():
				if err != nil {
					sess.emitter.Emit(agent.Event{ClientEventID: ccUUID(), Ts: time.Now(), Kind: "error", Payload: agent.ErrorPayload{Message: "The native conversation could not be restored. Saved history was not replaced; send a message to retry it.", Retryable: true}})
					return
				}
				if h.get(msg.SessionID) == sess {
					h.finishAdoptionConfirmed(ad, sess, driver, msg)
				}
			case <-sess.workspaceCtx.Done():
			}
		}()
		return
	}
	h.finishAdoptionConfirmed(ad, sess, driver, msg)
}
func (h *AgentHost) finishAdoptionConfirmed(ad *adoptRequest, sess *agentSession, driver sessionDriver, msg protocol.SpawnSession) {
	hadConversation := ad.rec.ClaudeSessionID != "" || ad.rec.CodexThreadID != "" || ad.rec.EngineSession != nil
	resumed := ad.resumeID != ""
	cutOff := ad.rec.TurnActive
	cont, gaveUp := adoptPlanAfter(ad.rec, resumed, msg.Interaction, h.cfg.RestartCause)
	tasks, subs := ad.rec.OpenTasks, ad.rec.OpenSubagents
	notice := cutOffNotice(ad.rec.OpenCalls, tasks, subs)

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
	if n := cutOffCount(tasks, subs); n != "" {
		parts = append(parts, n+" stopped.")
	}
	switch {
	case cont:
		parts = append(parts, "The agent is being asked to continue.")
	case gaveUp:
		parts = append(parts, "It was cut off after being continued once already, so it is waiting for a message.")
	case cutOffWork(ad.rec) && !hadConversation:
		parts = append(parts, adoptNoTaskWords)
	case cutOffWork(ad.rec) && resumed && destructiveCutOff(ad.rec):
		parts = append(parts, adoptDestructiveWords)
	case cutOffWork(ad.rec) && resumed && waitsOnPerson(ad.rec) && msg.Interaction != protocol.InteractionUnattended:
		parts = append(parts, adoptWaitingWords)
	}
	if !cont && notice != "" && hadConversation {
		parts = append(parts, "The agent is told what was cut off with the next message.")
	}
	if ad.pluginsLost {
		parts = append(parts, "Its plugins could not all be reinstalled.")
	}
	// The diagnostic closes the interrupted tool calls. Chat hides this detail;
	// one compact row below reports the final outcome. Retryable keeps the session alive.
	sess.emitter.Emit(agent.Event{ClientEventID: ccUUID(), Ts: time.Now(), Kind: "error",
		Payload: agent.ErrorPayload{Message: strings.Join(parts, " "), Retryable: true, Code: "session_recovery"}})
	emitCutOffWork(sess.emitter, tasks, subs)
	var noticeOnce sync.Once
	showNotice := func() {
		if !cutOffWork(ad.rec) {
			return
		}
		noticeOnce.Do(func() {
			sess.emitter.Emit(agent.Event{ClientEventID: ccUUID(), Ts: time.Now(), Kind: "user_message",
				Payload: agent.UserMessagePayload{Text: h.recoveryRow(ad.rec, resumed, false, gaveUp), Source: "system"}})
		})
	}
	if !cont {
		showNotice()
	}
	log.Printf("agent session %s: recovered after a daemon restart (conversation resumed=%v, turn cut off=%v, tool calls cut off=%d, background tasks cut off=%d, subagents cut off=%d, continuing=%v)",
		msg.SessionID, resumed, cutOff, len(ad.rec.OpenCalls), len(tasks), len(subs), cont)

	if !cutOffWork(ad.rec) {
		// Nothing was cut off: nothing to continue or report paused.
		showNotice()
		return
	}
	// The adoption note gives the agent its full recovery instructions once;
	// the recorded system message is the person's compact outcome row.
	shown := h.recoveryRow(ad.rec, resumed, true, false)
	if cont {
		// Counted now, before anything runs: if this continue is what takes the daemon down again,
		// the next start must already know it was tried.
		sess.record.update(func(r *AgentRecord) bool { r.AutoContinues++; return true })
	}
	go func() {
		// Not before the server has had its say: its answer to the hello is where a session
		// that was stopped while the daemon was down gets killed, and where it says which
		// sessions have a question open. With no server, no continue.
		if msg.RequireRecoveryReady {
			h.waitRecoveryReady(sess.workspaceCtx)
			if sess.workspaceCtx.Err() != nil {
				return
			}
		} else {
			select {
			case <-h.connected:
			case <-sess.workspaceCtx.Done():
				return
			}
			select {
			case <-time.After(adoptContinueDelay):
			case <-sess.workspaceCtx.Done():
				return
			}
		}
		if h.get(msg.SessionID) != sess {
			return // stopped meanwhile
		}
		cc, isCC := driver.(*claudeCodeDriver)
		if cont && msg.Interaction != protocol.InteractionUnattended && h.hasOpenAsk(msg.SessionID) {
			// The record could not tell (an older daemon's), but the server can: it was waiting for
			// the person. Not continued after all, and not counted. The agent is told its question
			// was cut off, with the person's next message.
			cont = false
			if notice == "" {
				notice = "[system] The process hosting this session restarted, and work that was running was cut off."
			}
			notice += "\n" + adoptAskCutOff
			if isCC {
				cc.addAdoptionWork(adoptAskCutOff)
			} else if cx, ok := driver.(interface{ addAdoptionWork(string) }); ok {
				cx.addAdoptionWork(adoptAskCutOff)
			}
			sess.record.update(func(r *AgentRecord) bool {
				if r.AutoContinues == 0 {
					return false
				}
				r.AutoContinues--
				return true
			})

			sess.emitter.Emit(agent.Event{ClientEventID: ccUUID(), Ts: time.Now(), Kind: "error", Payload: agent.ErrorPayload{Message: "It had asked a question when the host restarted, so it waits for your message instead of continuing by itself.", Retryable: true, Code: "session_recovery"}})
			log.Printf("agent session %s: not continued after the restart: the server has a question of its open", msg.SessionID)
		}
		if !cont {
			// Idle first: an adopted session reports no state of its own, and its row may still
			// read "running" from before the restart. One connection keeps the order, and the
			// driver's lock keeps a message from the person from slipping in between: one that came
			// first means it is not paused; one that comes after reports "running" after these.
			report := func() {
				showNotice()
				if err := h.sender.Send(protocol.SessionStateChanged{Type: "session_state_changed", SessionID: msg.SessionID, Status: "idle"}); err != nil {
					log.Printf("agent session %s: reporting it idle after the restart: %v", msg.SessionID, err)
				}
				if err := h.sender.Send(protocol.SessionPaused{Type: "session_paused", SessionID: msg.SessionID, Reason: protocol.PausedByRestart, Detail: h.recoveryPauseReason(ad.rec, resumed, gaveUp)}); err != nil {
					log.Printf("agent session %s: reporting it paused by the restart: %v", msg.SessionID, err)
				}
			}
			if isCC {
				cc.unlessHeardFromPerson(report)
			} else if cx, ok := driver.(interface{ unlessHeardFromPerson(func()) bool }); ok {
				cx.unlessHeardFromPerson(report)
			} else {
				report()
			}
			return
		}
		// Source "system": the chat does not show it as the person's message.
		if isCC {
			if !cc.continueAdopted(adoptContinuePrompt, shown) {
				log.Printf("agent session %s: not continued after the restart: a message from the person came first", msg.SessionID)
			}
			return
		}
		if cx, ok := driver.(interface{ continueAdopted(string, string) bool }); ok {
			cx.continueAdopted(adoptContinuePrompt, shown)
			return
		}
		text := adoptContinuePrompt
		if notice != "" {
			text = notice + "\n\n" + h.restartCauseWords(ad.rec) + "\n\n" + adoptContinuePrompt
		}
		driver.EnqueueInput(agent.Input{Text: text, Shown: shown, Source: "system"})
	}()
}

// The notice's sentence for a session that is not continued: it was waiting for the person; it was
// cut off in a command on restartDenyList; it never had a conversation to resume.
const (
	adoptWaitingWords     = "It was waiting for your answer, so it waits for your message instead of continuing by itself."
	adoptDestructiveWords = "It is paused because the cut-off command looked destructive (a force push, a deploy, a migration, an install or a service restart, a recursive delete, dropping data): check it, then resume it."
	adoptNoTaskWords      = "It was cut off before it had a conversation to resume, so the agent does not know its task: send it its task again."
	// adoptAskCutOff is what the agent is told when its question was cut off.
	adoptAskCutOff = "A question you had asked the person was cut off before its answer came: the message that follows may answer it; if it does not, ask again."
)

// The summaries the stopped events carry.
const (
	cutOffTaskSummary     = "the daemon restarted"
	cutOffSubagentSummary = "stopped: the daemon restarted"
)

// emitCutOffWork reports stopped every background task and subagent a restart cut off, as their
// own ends would have been: background_task_done (status "stopped") for each task, which also
// moves the session's running-task count on the server, and a final subagent_done for each
// subagent, which ends its card.
func emitCutOffWork(e agent.Emitter, tasks []agent.BackgroundTaskStartedPayload, subs []OpenSubagent) {
	for _, t := range tasks {
		e.Emit(agent.Event{ClientEventID: ccUUID(), Ts: time.Now(), Kind: "background_task_done",
			Payload: agent.BackgroundTaskDonePayload{TaskID: t.TaskID, CallID: t.CallID, Kind: t.Kind, Status: "stopped",
				Summary: cutOffTaskSummary, Description: t.Description}})
	}
	for _, s := range subs {
		e.Emit(agent.Event{ClientEventID: ccUUID(), Ts: time.Now(), Kind: "subagent_done",
			Payload: agent.SubagentPayload{ChildID: s.ChildID, AgentType: s.AgentType, Model: s.Model, Summary: cutOffSubagentSummary}})
	}
}

// cutOffSplit separates the shell and monitor tasks from the subagents: a background subagent is
// both a task (kind "agent") and a subagent, and is counted and listed once, as a subagent. A
// subagent task with no subagent of its own (it cannot happen, but it costs nothing) becomes one.
func cutOffSplit(tasks []agent.BackgroundTaskStartedPayload, subs []OpenSubagent) ([]agent.BackgroundTaskStartedPayload, []OpenSubagent) {
	calls := make(map[string]bool, len(subs))
	for _, s := range subs {
		calls[s.ChildID] = true
	}
	var shells []agent.BackgroundTaskStartedPayload
	agents := append([]OpenSubagent(nil), subs...)
	for _, t := range tasks {
		switch {
		case t.Kind != "agent":
			shells = append(shells, t)
		case !calls[t.CallID]:
			bg := true
			agents = append(agents, OpenSubagent{ChildID: t.CallID, Summary: t.Description, AgentID: t.TaskID, Background: &bg})
		}
	}
	return shells, agents
}

// cutOffCount says how much was cut off ("2 background tasks and 1 subagent"), "" for nothing.
func cutOffCount(tasks []agent.BackgroundTaskStartedPayload, subs []OpenSubagent) string {
	shells, agents := cutOffSplit(tasks, subs)
	var parts []string
	if n := len(shells); n > 0 {
		parts = append(parts, plural(n, "background task", "background tasks"))
	}
	if n := len(agents); n > 0 {
		parts = append(parts, plural(n, "subagent", "subagents"))
	}
	if len(parts) == 0 {
		return ""
	}
	verb := " were"
	if len(shells)+len(agents) == 1 {
		verb = " was"
	}
	return strings.Join(parts, " and ") + verb
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// restartCauseWords is why the daemon restarted, for a session it cut off (restartcause.go).
func (h *AgentHost) restartCauseWords(rec AgentRecord) string {
	if h.cfg.RestartCause.updateSucceeded(rec) {
		return "The daemon update you started succeeded (now on " + h.cfg.RestartCause.update.Commit + "). Continue where you left off; do not repeat the update."
	}
	return restartCauseWords(h.cfg.RestartCause, rec.OpenCalls)
}

// recoveryRow is the person's summary; the driver's adoption note carries the
// full instructions once, with the next input to the agent.
func (h *AgentHost) recoveryRow(rec AgentRecord, resumed, continuing, gaveUp bool) string {
	prefix := "Host restarted"
	if h.cfg.RestartCause.updateSucceeded(rec) {
		commit := h.cfg.RestartCause.update.Commit
		if len(commit) > 12 {
			commit = commit[:12]
		}
		prefix = "Updated to " + commit
	}
	if !cutOffWork(rec) {
		return ""
	}
	row := "[system] ↻ " + prefix + " · "
	if continuing {
		return row + "continued"
	}
	var detail strings.Builder
	cutOffCallList(&detail, rec.OpenCalls)
	shells, agents := cutOffSplit(rec.OpenTasks, rec.OpenSubagents)
	cutOffBackgroundList(&detail, shells, agents)
	if detail.Len() == 0 {
		detail.WriteString("\nThe step that was running was cut off.")
	}
	return row + "paused — " + h.recoveryPauseReason(rec, resumed, gaveUp) + detail.String()
}

func (h *AgentHost) recoveryPauseReason(rec AgentRecord, resumed, gaveUp bool) string {
	switch {
	case !resumed:
		return "send the task again"
	case gaveUp:
		return "already continued once"
	case destructiveCutOff(rec) && !h.cfg.RestartCause.updateSucceeded(rec):
		return "check the interrupted command"
	default:
		return "waiting for your message"
	}
}

// cutOffNotice is what the agent is told about the work a restart cut off ("" for none): its own
// tool calls that had not returned, then the background work, each item by kind, with what it ran
// and its id, and how to pick it up again safely.
func cutOffNotice(calls []OpenCall, tasks []agent.BackgroundTaskStartedPayload, subs []OpenSubagent) string {
	shells, agents := cutOffSplit(tasks, subs)
	if len(calls)+len(shells)+len(agents) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[system] The process hosting this session restarted, and work that was running was cut off.")
	if len(calls) > 0 {
		cutOffCallList(&b, calls)
		b.WriteString("\nCheck its state first: it may not have finished, or may have half written a file. " +
			"Never re-run anything destructive without checking.")
		for _, c := range calls {
			if c.destructive() {
				b.WriteString(" A command cut off here is on the list of commands a restart never lets you pick up by yourself " +
					"(a force push, a deploy, a migration, an install or a service restart, a recursive delete, dropping data): " +
					"do not run it again unless the person asks you to.")
				break
			}
		}
		for _, c := range calls {
			if c.asksPerson() {
				b.WriteString(" " + adoptAskCutOff)
				break
			}
		}
		if len(shells)+len(agents) == 0 {
			return b.String()
		}
	}
	cutOffBackgroundList(&b, shells, agents)
	b.WriteString("\nBefore trusting anything they did, check the state of their worktrees and outputs. " +
		"Resume a subagent by sending a message to its agent id; re-run a background command that did not finish. " +
		"Do not re-run anything destructive without checking.")
	return b.String()
}

// These lists are shared by the person's disclosure and the agent's recovery note.
// Keep instructions outside them so only the agent receives the recovery guidance.
func cutOffCallList(b *strings.Builder, calls []OpenCall) {
	if len(calls) == 0 {
		return
	}
	b.WriteString("\nTool calls cut off before they returned:")
	for _, c := range calls {
		if c.Detail != "" {
			fmt.Fprintf(b, "\n- %s: %s", c.Tool, c.Detail)
		} else {
			fmt.Fprintf(b, "\n- %s", c.Tool)
		}
	}
}

func cutOffBackgroundList(b *strings.Builder, shells []agent.BackgroundTaskStartedPayload, agents []OpenSubagent) {
	if len(shells) > 0 {
		b.WriteString("\nBackground tasks that were cut off:")
		for _, t := range shells {
			fmt.Fprintf(b, "\n- %s %s: %s", t.Kind, t.TaskID, cutRunes(t.Description, 100))
		}
	}
	if len(agents) > 0 {
		b.WriteString("\nSubagents that were cut off:")
		for _, s := range agents {
			kind := s.AgentType
			if kind == "" {
				kind = "subagent"
			}
			fmt.Fprintf(b, "\n- %s %q", kind, cutRunes(s.Summary, 100))
			if s.AgentID != "" {
				fmt.Fprintf(b, ", agent id %s", s.AgentID)
			} else {
				b.WriteString(", in the foreground (no agent id: start it again if it is still needed)")
			}
		}
	}
}

// NoteConnected is called each time the daemon has a connection to its server: what adoption
// was holding back for one can go ahead.
func (h *AgentHost) NoteConnected() {
	h.connectedOnce.Do(func() { close(h.connected) })
	// A session that waited for this connection is retried rather than reported as paused.
	h.mu.Lock()
	retry := false
	for _, a := range h.adopting {
		if a.failed != "" && a.awaitsServer && !a.dropped {
			a.failed, a.awaitsServer, retry = "", false, true
		}
	}
	h.mu.Unlock()
	if retry {
		go h.RunAdoption()
	}
	h.ReportAdoptionFailures()
}

// ReportAdoptionFailures tells the server about the sessions that could not be hosted again, so
// their rows stay paused with a reason instead of ending. Before the first
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
		h.mu.Lock()
		pending := h.adopting[id]
		detail := ""
		if pending != nil && !pending.dropped {
			detail = pending.failed
		}
		h.mu.Unlock()
		if detail == "" {
			continue
		}
		detail = "Recovery paused: " + strings.TrimSuffix(detail, ".") + ". Send a message to retry."
		if err := h.sender.Send(protocol.SessionPaused{Type: "session_paused", SessionID: id, Reason: protocol.PausedByRestart, Detail: detail}); err != nil {
			// The connection went away again: keep it for the next one.
			h.mu.Lock()
			h.adoptFailed = append(h.adoptFailed, id)
			h.mu.Unlock()
		} else {
			newWSEmitter(h.sender, id, nil).Emit(agent.Event{ClientEventID: ccUUID(), Ts: time.Now(), Kind: "error", Payload: agent.ErrorPayload{Message: detail, Retryable: true, Code: "session_recovery"}})
		}
	}
}

// reportUndelivered writes a retryable error into the transcript of a session this daemon was
// asked to message but is not hosting.
func (h *AgentHost) reportUndelivered(sessionID string, clientIDs ...string) {
	clientID := ""
	if len(clientIDs) > 0 {
		clientID = clientIDs[0]
	}
	log.Printf("agent session %s: message for a session this daemon is not hosting — not delivered", sessionID)
	e := newWSEmitter(h.sender, sessionID, nil)
	e.Emit(agent.Event{ClientEventID: ccUUID(), Ts: time.Now(), Kind: "error",
		Payload: agent.ErrorPayload{Message: "Message not delivered: this session is not running on its host any more.", Retryable: true, ClientID: clientID}})
}
