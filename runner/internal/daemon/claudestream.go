package daemon

// Mid-turn steering for the Claude Code driver (design: docs/design/mid-turn-steering.md).
//
// Instead of one `claude -p "<text>"` process per message, a session keeps one long-lived
// `claude -p --input-format stream-json --replay-user-messages` process and writes every message to its stdin
// as it arrives. The CLI folds a message that arrives while a tool call is running into the turn in progress
// (delivered when the tool finishes) and starts a new turn for one that arrives otherwise, so a long turn no
// longer blocks what you type behind it. Esc becomes a control request (interrupt) instead of a kill, which
// keeps the process and the conversation.
//
// Correlation is by uuid: every message carries one, the CLI echoes it on the "replay" line it emits when it
// CONSUMES the message, and that is when the transcript's user_message event is emitted (as the per-turn
// driver did when it started the turn). The running/idle status is derived only from what the process
// reports, never from "I wrote a message" alone, apart from the optimistic running at write time that keeps
// the UI busy during the second or so between the write and the replay.
//
// All state is guarded by claudeCodeDriver.sm. It is never held across blocking I/O: lines for the process go
// through ONE ordered channel (messages and control requests alike, pushed under sm without blocking) to a
// writer goroutine that owns stdin, so an interrupt can never overtake the message it was meant for. Events
// are emitted after sm is released; status events go through syncStatus, which re-reads the state under
// statusMu so the last status emitted always matches it.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// Timings, vars so tests can shrink them.
var (
	// ccIdleShutdown closes the process of a session that has been idle this long (it restarts with --resume).
	ccIdleShutdown = 10 * time.Minute
	// ccIdleMaxDeferrals is how many times in a row the idle shutdown waits another ccIdleShutdown because
	// background tasks are running (6: an hour), so a task that never reports its end cannot keep a process.
	ccIdleMaxDeferrals = 6
	// ccInterruptKillAfter is how long an interrupt may go unacknowledged before the process is killed.
	ccInterruptKillAfter = 5 * time.Second
	// ccInterruptResultAfter is how long an ACKNOWLEDGED interrupt may take to end the turn (cutting a tool
	// can be slow) before the process is killed.
	ccInterruptResultAfter = 30 * time.Second
	// ccSettleAfter is how long, after a result, a written-but-unconsumed message may take to start a turn
	// before it is assumed consumed without an echo (a slash command).
	ccSettleAfter = 8 * time.Second
	// ccInterruptFlagTTL bounds how long a requested interrupt can turn an error result into "interrupted".
	ccInterruptFlagTTL = 10 * time.Second
	// ccLegacyProbeWindow is how soon after start an unknown-option exit means "this CLI cannot stream".
	ccLegacyProbeWindow = 10 * time.Second
	// ccCloseGrace is how long a process may take to exit after its stdin was closed before it is killed.
	ccCloseGrace = 10 * time.Second
)

const (
	ccMaxLine    = 32 << 20 // a stdout line longer than this is discarded
	ccMaxPending = 64       // queued + written-but-unconsumed messages
	ccSettledMax = 64
)

// ccSteeringEnabled is the kill switch: BLERG_CLAUDE_STEERING=0 selects the per-turn engine.
func ccSteeringEnabled() bool { return os.Getenv("BLERG_CLAUDE_STEERING") != "0" }

type steerQueued struct {
	agent.Input
	retried bool // already failed to reach a process once
	// adopt: this is the first message after an adoption, so the line written to the engine
	// carries the adoption note (claudeadopt.go). It stays set across a retry.
	adopt bool
}

type awaitMsg struct {
	uuid string
	steerQueued
}

// ccProc is one streaming `claude` process.
type ccProc struct {
	gen     int
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	writeCh chan []byte // everything for stdin, in order
	done    chan struct{}
	// closeReq asks the writer to close stdin (a clean shutdown of an idle process).
	closeReq  chan struct{}
	closeOnce sync.Once
	pidFile   string
	model     string // what this process was started with
	started   time.Time

	// guarded by the driver's sm
	sawInit, sawResult  bool
	inTurn              bool // a turn has begun (init) and not yet ended (result)
	stdinClosed         bool
	intentionalClose    bool
	killedByDriver      bool
	dying               bool // being killed: no new message may be written to it
	lastNoise           string
	rootUsage           claudeRootUsage
	mcpInit             mcpProbe
	mcpWait             map[string]chan ccMCPReply
	interruptID         string
	interrupts          map[string]time.Time // pending ACK deadlines, bounded by ccMaxPending
	interruptPending    bool                 // Stop of a written turn awaiting init, including fallback-only
	interruptAckPending bool                 // acknowledged cancellation of a written turn awaiting init
}

type steerState struct {
	// startMu serializes the run loop and read-only MCP preparation. Never take it while holding sm.
	startMu           sync.Mutex
	mcpInputGate      func(context.Context)
	recoveryInputGate func(context.Context)
	legacy            bool
	closed            bool // the session is ending: no timer may emit
	proc              *ccProc
	nextGen           int
	pidTag            string
	sq                []steerQueued // accepted, not yet written
	// awaiting: written to the process, not yet echoed back (consumed).
	awaiting []awaitMsg
	settled  []string // uuids whose user_message was emitted without an echo
	running  bool
	turnSeq  int
	// cfgGen counts model/effort changes; a process started under an older one is stale and is restarted at
	// the next idle point.
	cfgGen int
	stale  bool

	interruptUntil time.Time
	killTimer      *time.Timer
	settleTimer    *time.Timer
	idleTimer      *time.Timer
	// idleEpoch fences callbacks that Stop cannot retract once they have begun.
	idleEpoch uint64
	// idleDeferrals counts the idle shutdowns put off in a row for background tasks (reset when the
	// timer is armed afresh); deferLogged says the deferral was logged once for this session.
	idleDeferrals int
	deferLogged   bool
	wake          chan struct{}
}

func (d *claudeCodeDriver) steering() bool {
	d.sm.Lock()
	defer d.sm.Unlock()
	return !d.steer.legacy
}

func (d *claudeCodeDriver) signal() {
	select {
	case d.steer.wake <- struct{}{}:
	default:
	}
}

// Run dispatches to the steering engine, or to the per-turn one when steering is off or gave way.
func (d *claudeCodeDriver) Run(ctx context.Context) {
	defer d.promptFile.Remove()
	if d.steering() {
		d.runSteering(ctx)
	}
	if !d.steering() {
		// Anything accepted by the steering engine before it gave way continues on the per-turn one.
		d.sm.Lock()
		pending := d.steer.sq
		d.steer.sq = nil
		d.rearmAdoptNoteLocked(pending)
		d.sm.Unlock()
		for _, m := range pending {
			d.enqueueLegacy(m.Input)
		}
		d.runLegacy(ctx)
	}
}

// Enqueue accepts a message. It never blocks.
func (d *claudeCodeDriver) Enqueue(text, source string) {
	d.EnqueueInput(agent.Input{Text: text, Source: source})
}

// EnqueueInput accepts a message (agent.Input). It never blocks.
func (d *claudeCodeDriver) EnqueueInput(in agent.Input) {
	if in.Source == "" {
		in.Source = "chat"
	}
	background := d.backgroundRunning()
	d.mu.Lock()
	legacyBusy := d.cmd != nil
	d.mu.Unlock()
	d.sm.Lock()
	if in.ExpectedTurnID != "" && (in.ExpectedTurnID != d.usageTurnID || legacyBusy || d.steer.running || len(d.steer.sq) > 0 || len(d.steer.awaiting) > 0 || len(d.queue) > 0 || background > 0) {
		d.sm.Unlock()
		d.emit("error", staleLimitResumeError())
		return
	}
	if in.Source != "system" {
		d.heardFromPerson = true
	}
	if d.steer.legacy {
		d.sm.Unlock()
		d.enqueueLegacy(in)
		return
	}
	if len(d.steer.sq)+len(d.steer.awaiting) >= ccMaxPending {
		d.sm.Unlock()
		d.emit("error", queueFullError(in))
		return
	}
	d.steer.sq = append(d.steer.sq, steerQueued{Input: in})
	d.sm.Unlock()
	d.signal()
}

// syncStatus emits a status_changed when the running state differs from the one last emitted. It reads the
// state itself, under statusMu, so interleaved callers always converge on the current state.
func (d *claudeCodeDriver) syncStatus(reason string) {
	d.statusMu.Lock()
	defer d.statusMu.Unlock()
	d.sm.Lock()
	want := d.steer.running
	d.sm.Unlock()
	if want == d.statusOn {
		return
	}
	d.statusOn = want
	if want {
		d.emit("status_changed", agent.StatusPayload{Status: "running", Reason: reason})
		return
	}
	d.emit("status_changed", agent.StatusPayload{Status: "idle", Reason: "turn_done"})
}

// ── the run loop ─────────────────────────────────────────────────────────────

func (d *claudeCodeDriver) runSteering(ctx context.Context) {
	defer d.shutdown()
	for {
		d.applyPendingPlugins(ctx)
		for {
			d.applyPendingPlugins(ctx)
			d.sm.Lock()
			if d.steer.legacy {
				d.sm.Unlock()
				return
			}
			pending := len(d.steer.sq) > 0
			d.sm.Unlock()
			if !pending {
				break
			}
			d.deliverOne(ctx)
			if ctx.Err() != nil {
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-d.steer.wake:
		case <-d.pluginUpdateWake():
		}
	}
}

// deliverOne writes the oldest queued message to the process, starting one first when needed. The process
// is secured BEFORE the message is taken from the queue, so a process that goes away in between (and puts
// its unconsumed messages back at the front) can never make a newer message overtake an older one.
func (d *claudeCodeDriver) deliverOne(ctx context.Context) {
	d.sm.Lock()
	recoveryGate := d.steer.recoveryInputGate
	d.sm.Unlock()
	if recoveryGate != nil {
		recoveryGate(ctx)
	}
	if ctx.Err() != nil {
		return
	}
	p, err := d.ensureProc(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		d.sm.Lock()
		if d.steer.legacy || d.steer.closed {
			d.sm.Unlock()
			return
		}
		var m steerQueued
		ok := len(d.steer.sq) > 0
		if ok {
			m = d.steer.sq[0]
			d.steer.sq = d.steer.sq[1:]
		}
		d.sm.Unlock()
		if ok {
			d.failMessage(m, "claude-code start: "+err.Error())
		}
		return
	}
	d.sm.Lock()
	gate := d.steer.mcpInputGate
	d.sm.Unlock()
	if gate != nil {
		gate(ctx)
	}
	// Discovery or process startup may have overlapped a socket replacement.
	// Recheck the current recovery link at actual message admission.
	if recoveryGate != nil {
		recoveryGate(ctx)
	}
	if ctx.Err() != nil {
		return
	}
	uuid := ccUUID()
	background := d.backgroundRunning()
	d.sm.Lock()
	if d.steer.proc != p || p.dying || p.stdinClosed || len(d.steer.sq) == 0 {
		d.sm.Unlock()
		return // the world changed under us: look again
	}
	if d.adoptPending {
		d.steer.sq[0].adopt, d.adoptPending = true, false
	}
	m := d.steer.sq[0]
	if m.ExpectedTurnID != "" && (m.ExpectedTurnID != d.usageTurnID || d.steer.running || len(d.steer.awaiting) > 0 || len(d.steer.sq) > 1 || background > 0) {
		d.steer.sq = d.steer.sq[1:]
		d.sm.Unlock()
		d.emit("error", staleLimitResumeError())
		return
	}
	// engineText is computed at every write, not once: after a refused resume the retry must
	// carry the "conversation could not be restored" note instead.
	line, err := json.Marshal(map[string]any{
		"type": "user", "uuid": uuid,
		"message": map[string]any{"role": "user", "content": d.engineText(m.ModelText(d.markerNonce), m.adopt)},
	})
	if err != nil {
		d.steer.sq = d.steer.sq[1:]
		d.sm.Unlock()
		d.failMessage(m, "could not encode the message: "+err.Error())
		return
	}
	select {
	case p.writeCh <- append(line, '\n'):
	default:
		d.sm.Unlock()
		time.Sleep(10 * time.Millisecond) // cannot happen below the pending cap; never spin if it does
		return
	}
	d.steer.sq = d.steer.sq[1:]
	d.steer.awaiting = append(d.steer.awaiting, awaitMsg{uuid: uuid, steerQueued: m})
	d.steer.running = true
	d.stopIdleTimerLocked()
	d.sm.Unlock()
	// Busy from the write, not from the replay (which comes a second or so later).
	d.syncStatus("turn")
}

// failMessage makes sure a message that could not be delivered still shows in the transcript, then says why.
func (d *claudeCodeDriver) failMessage(m steerQueued, why string) {
	d.emitTurnMessage(m.Payload())
	d.emit("error", agent.ErrorPayload{Message: why, Retryable: true})
}

// ensureProc returns a live process, starting one when there is none. The run loop and
// read-only MCP preparation serialize starts; two processes would fork the conversation.
func (d *claudeCodeDriver) ensureProc(ctx context.Context) (*ccProc, error) {
	d.steer.startMu.Lock()
	defer d.steer.startMu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		d.sm.Lock()
		if d.steer.closed || d.steer.legacy {
			d.sm.Unlock()
			return nil, fmt.Errorf("claude streaming process unavailable")
		}
		p := d.steer.proc
		if p != nil && !p.dying && !p.stdinClosed {
			d.sm.Unlock()
			return p, nil
		}
		d.sm.Unlock()
		if p != nil {
			select {
			case <-p.done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return d.startProc(ctx)
	}
}

func (d *claudeCodeDriver) startProc(ctx context.Context) (*ccProc, error) {
	var opts []ccOption
	if d.restrict {
		opts = append(opts, withRestrictTools())
	} else {
		opts = append(opts, withSessionGuide(), withInteraction(d.interaction))
	}
	opts = append(opts, withMarkerNonce(d.markerNonce), withPromptFile(d.promptFile))
	if d.mcpFile != nil {
		path, err := d.mcpFile.Ensure()
		if err != nil {
			// Never run a grant session without its allow-list.
			return nil, fmt.Errorf("MCP gateway config: %w", err)
		}
		opts = append(opts, withMCPConfigPath(path))
		if d.mcpFile.sessionFiling && d.mcpFile.cfg == nil {
			opts = append(opts, func(o *ccOptions) { o.MCPAmbient = true })
		}
	}
	// cfgGen is read BEFORE the model: a change that lands in between leaves this process stale (restarted
	// at the next idle point) rather than silently running the old model with the flag cleared.
	d.sm.Lock()
	d.steer.nextGen++
	gen, cfgGen := d.steer.nextGen, d.steer.cfgGen
	if d.steer.pidTag == "" {
		d.steer.pidTag = strings.ReplaceAll(ccUUID(), "-", "")[:8]
	}
	tag := d.steer.pidTag
	d.sm.Unlock()
	d.mu.Lock()
	model, effort, resume := d.model, d.effort, d.ccSessionID
	d.mu.Unlock()

	pidFile := fmt.Sprintf("%s.%s.%d", sandboxTurnPIDFile, tag, gen)
	opts = append(opts, withPluginDirs(d.pluginDirs), withClaudeSandboxSettings(d.sandboxSettings))
	opts = append(opts, withCLIFlags(d.cliFlags(ctx))...)
	cmd := d.prefix.commandPID(ctx, d.workDir, "claude", pidFile, ccSessionArgs(model, effort, resume, opts...)...)
	cmd.Env = d.env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	cmd.Stderr = cmd.Stdout // interleaved, as in the per-turn engine
	if err := startEngineCommand(cmd, d.startCommand); err != nil {
		_ = stdin.Close()
		return nil, err
	}
	p := &ccProc{
		gen: gen, cmd: cmd, stdin: stdin,
		writeCh: make(chan []byte, ccMaxPending+8),
		done:    make(chan struct{}), closeReq: make(chan struct{}),
		pidFile: pidFile, model: model, started: time.Now(),
	}
	d.sm.Lock()
	d.steer.proc = p
	d.steer.stale = d.steer.cfgGen != cfgGen
	d.sm.Unlock()
	go d.writer(p)
	go d.reader(ctx, p, stdout)
	if !d.prefix.enabled() {
		d.reportEngine(cmd.Process.Pid, true) // in a sandbox this is `docker exec`, not the engine
	}
	return p, nil
}

// writer owns the process's stdin.
func (d *claudeCodeDriver) writer(p *ccProc) {
	for {
		select {
		case l := <-p.writeCh:
			if _, err := p.stdin.Write(l); err != nil {
				d.killProc(p) // a pipe that no longer takes writes belongs to a process that is gone or wedged
				return
			}
		case <-p.closeReq:
			_ = p.stdin.Close()
			return
		case <-p.done:
			return
		}
	}
}

// reader decodes the process's stdout until it ends, then reports the exit.
func (d *claudeCodeDriver) reader(ctx context.Context, p *ccProc, stdout io.Reader) {
	br := bufio.NewReaderSize(stdout, 64*1024)
	var buf []byte
	skipping := false
	var rerr error
	for {
		chunk, isPrefix, err := br.ReadLine()
		if err != nil {
			rerr = err
			break
		}
		if !skipping {
			buf = append(buf, chunk...)
			if len(buf) > ccMaxLine {
				skipping = true
				buf = buf[:0]
			}
		}
		if isPrefix {
			continue
		}
		if skipping {
			skipping = false
			log.Printf("claude-code: discarded an oversized output line")
			continue
		}
		d.onLine(p, buf)
		buf = buf[:0]
	}
	if rerr != io.EOF {
		// The pipe broke while the process may still be alive: an undrained pipe would wedge it.
		d.killProc(p)
	}
	werr := p.cmd.Wait()
	d.procExited(ctx, p, werr)
}

// killProc ends the process for good: SIGKILL inside the container (SIGTERM could be ignored by a wedged
// engine, and a process that never exits would block every later message), and the host-side child.
func (d *claudeCodeDriver) killProc(p *ccProc) {
	if d.prefix.enabled() {
		_ = d.prefix.killPID(p.pidFile)
	}
	if p.cmd.Process != nil {
		_ = cancelEngineCommand(p.cmd, d.startCommand != nil)
	}
}

// ── events ───────────────────────────────────────────────────────────────────

func (d *claudeCodeDriver) onLine(p *ccProc, line []byte) {
	if ccSkipStreamLine(line) {
		return
	}
	d.sm.Lock()
	gone := p.dying || p.killedByDriver
	d.sm.Unlock()
	if gone {
		return // output of a process that is being killed has already been accounted for
	}
	var ev ccLine
	if json.Unmarshal(line, &ev) != nil {
		if t := strings.TrimSpace(string(line)); t != "" {
			d.sm.Lock()
			p.lastNoise = t
			d.sm.Unlock()
		}
		return
	}
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			d.onInit(p, &ev, line)
		} else {
			if ev.Subtype == "compact_boundary" {
				d.sm.Lock()
				p.rootUsage.compacted(ev.CompactMetadata.PreTokens)
				d.sm.Unlock()
			}
			d.onSystem(&ev, line)
		}
	case "assistant":
		d.sm.Lock()
		p.rootUsage.observe(&ev)
		d.sm.Unlock()
		d.markRunning("agent turn")
		d.emitAssistantBlocks(&ev)
	case "stream_event":
		// A think that opens a turn the CLI began on its own shows the session working at once,
		// not only when its assistant line arrives (as the think ends).
		if d.onStreamEvent(&ev) == agent.KindThinkingStarted {
			d.markRunning("agent turn")
		}
	case "user":
		d.onUser(&ev)
	case "result":
		d.onResult(p, &ev)
	case "control_response":
		d.onControlResponse(p, line)
	}
}

// markRunning covers a turn the driver did not start itself: one the CLI began on its own (a background
// task finished, a monitor fired) shows the session as working just like a message does.
func (d *claudeCodeDriver) markRunning(reason string) {
	d.sm.Lock()
	was := d.steer.running
	d.steer.running = true
	if !was {
		d.stopIdleTimerLocked()
	}
	d.sm.Unlock()
	if !was {
		d.syncStatus(reason)
	}
}

func (d *claudeCodeDriver) onInit(p *ccProc, ev *ccLine, line []byte) {
	d.observeMCPInit(p, line)
	d.sm.Lock()
	p.sawInit, p.inTurn = true, true
	d.steer.turnSeq++ // a turn is starting: timers armed for an earlier one are stale
	if p.interruptAckPending {
		d.armInterruptResultLocked(p)
	} else if p.interruptPending {
		d.armInterruptKillLocked(p)
	}
	p.interruptPending, p.interruptAckPending = false, false
	d.apiErr = ""
	d.stopTimerLocked(&d.steer.settleTimer)
	d.sm.Unlock()
	d.setCCSessionID(ev.SessionID)
	d.markRunning("agent turn")
	if pl, ok := claudeInitCapabilities(line, d.skills, d.workDir, d.home); ok {
		d.caps.emit(d.emitter, pl)
	}
}

func (d *claudeCodeDriver) onUser(ev *ccLine) {
	// A subagent's user lines (its tool results, its prompt) are never the person's message.
	if ev.ParentToolUseID != "" {
		d.emitToolResults(ev)
		return
	}
	d.noticeTaskNotifications(ev)
	if !ev.IsReplay {
		if s, ok := ev.text(); ok {
			if out, ok := localCommandOutput(s); ok {
				if out != "" {
					d.emit("user_message", agent.UserMessagePayload{Text: out, Source: "system"})
				}
				return
			}
		}
		d.emitToolResults(ev)
		return
	}
	if ev.IsSynthetic {
		return
	}
	echo, isString := ev.text()
	d.sm.Lock()
	d.stopTimerLocked(&d.steer.settleTimer)
	var m awaitMsg
	matched := false
	if ev.UUID != "" {
		for i, a := range d.steer.awaiting {
			if a.uuid == ev.UUID {
				m, matched = a, true
				d.steer.awaiting = append(d.steer.awaiting[:i], d.steer.awaiting[i+1:]...)
				break
			}
		}
	}
	lateSettled := false
	if !matched && ev.UUID != "" {
		for _, u := range d.steer.settled {
			if u == ev.UUID {
				lateSettled = true
			}
		}
	}
	d.sm.Unlock()
	d.markRunning("turn")
	if out, ok := localCommandOutput(echo); isString && ok {
		// A slash command's output (/context, /cost): shown as a system card, never as the person.
		if matched {
			d.emitTurnMessage(m.Payload())
		}
		if out != "" {
			d.emit("user_message", agent.UserMessagePayload{Text: out, Source: "system"})
		}
		return
	}
	relayBody, relaySource, isRelay := agent.UnwrapRelay(echo, d.markerNonce)
	switch {
	case matched:
		d.emitTurnMessage(m.Payload())
	case lateSettled || !isString || strings.HasPrefix(strings.TrimSpace(echo), "<local-command"):
		// already shown, or the CLI's own rendering of a command (its caveat)
	case isRelay:
		// A decision Blerg relayed whose echo matched no message here: shown as that relay, by
		// its body, never as the marked block the engine read (the block carries the code).
		d.emit("user_message", agent.UserMessagePayload{Text: relayBody, Source: relaySource})
	case harnessInjected(echo):
		// The CLI's own user-role messages (a subagent's completion, a system reminder): kept
		// in the transcript as source "system", which the chat does not show as the person.
		d.emit("user_message", agent.UserMessagePayload{Text: agent.ScrubCode(echo, d.markerNonce), Source: "system"})
	default:
		// A message nobody here wrote (a hook, a resumed history): show it as the user's.
		d.emitTurnMessage(agent.UserMessagePayload{Text: agent.ScrubCode(echo, d.markerNonce), Source: "chat", TurnID: ev.UUID})
	}
}

// localCommandOutput is the output of a slash command the CLI ran itself, from the user line it
// writes for it (`<local-command-stdout>…</local-command-stdout>`, or stderr), with the tags taken
// off; false for any other text.
func localCommandOutput(text string) (string, bool) {
	s := strings.TrimSpace(text)
	for _, tag := range []string{"local-command-stdout", "local-command-stderr"} {
		open, closing := "<"+tag+">", "</"+tag+">"
		if !strings.HasPrefix(s, open) {
			continue
		}
		s = strings.TrimPrefix(s, open)
		if i := strings.LastIndex(s, closing); i >= 0 {
			s = s[:i]
		}
		return strings.TrimSpace(s), true
	}
	return "", false
}

// harnessInjected reports whether a user-role message is one Claude Code wrote itself: a
// background task's completion (<task-notification>), a system reminder, or a notice it labels
// as not user input. Such text is never the person's.
func harnessInjected(text string) bool {
	s := strings.TrimSpace(text)
	for _, p := range []string{"<task-notification>", "<system-reminder>", "[SYSTEM NOTIFICATION", "<command-name>"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func (d *claudeCodeDriver) onResult(p *ccProc, ev *ccLine) {
	d.sm.Lock()
	if ev.IsError && !p.sawInit {
		// The process failed before it began a turn (a --resume that does not exist): not a turn outcome.
		// Its exit follows and is reported once, by procExited.
		d.sm.Unlock()
		return
	}
	interrupted := !d.steer.interruptUntil.IsZero() && time.Now().Before(d.steer.interruptUntil) &&
		p.sawInit && ev.Subtype == "error_during_execution"
	d.steer.interruptUntil = time.Time{}
	d.stopTimerLocked(&d.steer.killTimer)
	d.steer.turnSeq++
	turn, gen := d.steer.turnSeq, p.gen
	p.sawResult, p.inTurn = true, false
	p.interruptPending, p.interruptAckPending = false, false
	noise := p.lastNoise
	partition := p.rootUsage.partition(ev.ccUsage())
	turnContext := p.rootUsage.contextUsage(ev)
	p.rootUsage = claudeRootUsage{}
	settle := len(d.steer.awaiting) > 0
	closeNow := false
	if settle {
		// A message was written but not echoed yet: the CLI starts another turn for it.
		d.stopTimerLocked(&d.steer.settleTimer)
		d.steer.settleTimer = time.AfterFunc(ccSettleAfter, func() { d.settleFired(gen, turn) })
	} else {
		d.steer.running = false
		closeNow = d.idleLocked(p)
	}
	d.sm.Unlock()

	d.setCCSessionID(ev.SessionID)
	stop := "end_turn"
	switch {
	case interrupted:
		stop = "interrupted"
		d.takeAPIError()
	case ev.IsError:
		// A TURN outcome (the model could not finish), not a driver failure: the session takes the next
		// message. Its reason is the result text or its first listed error; an API error the turn already
		// reported is not repeated.
		stop = "error"
		if e, ok := d.resultError(ev, noise); ok {
			d.emit("error", e)
		}
	default:
		d.takeAPIError()
	}
	d.emit("turn_done", agent.TurnDonePayload{StopReason: stop, Model: p.model, Usage: ev.ccUsage(), UsageByModel: partition, Context: turnContext})
	if !settle {
		d.syncStatus("turn_done")
	}
	if closeNow {
		d.closeStdin(p)
	}
}

// onControlResponse: the interrupt was acknowledged. The turn is being cut, which can take a while (a slow
// tool), so the short "not honoured" timer gives way to a long one waiting for the result.
func (d *claudeCodeDriver) onControlResponse(p *ccProc, line []byte) {
	if d.routeMCPControlResponse(p, line) {
		return
	}
	var reply ccControlResponse
	if json.Unmarshal(line, &reply) != nil {
		return
	}
	d.sm.Lock()
	defer d.sm.Unlock()
	if d.steer.closed || d.steer.proc != p || reply.Response.RequestID == "" {
		return
	}
	deadline, pending := p.interrupts[reply.Response.RequestID]
	if !pending || (reply.Response.Subtype != "success" && reply.Response.Subtype != "error") {
		return
	}
	delete(p.interrupts, reply.Response.RequestID)
	if reply.Response.Subtype == "error" || !time.Now().Before(deadline) {
		return // terminal failures and abandoned requests no longer consume the budget
	}
	// A preceding result may have cleared the request's optimistic flag and timer before
	// this ACK arrived. The matching ACK identifies the turn the CLI is actually cutting.
	d.stopTimerLocked(&d.steer.killTimer)
	if !p.inTurn {
		// deliverOne has already marked a written message running, but init has not
		// arrived. Its turn sequence (and acknowledged-result timer) starts at init.
		p.interruptAckPending = len(d.steer.awaiting) > 0
		if !p.interruptAckPending {
			p.interruptPending = false
			d.steer.interruptUntil = time.Time{}
		}
		return
	}
	d.armInterruptResultLocked(p)
}

func (d *claudeCodeDriver) armInterruptResultLocked(p *ccProc) {
	d.stopTimerLocked(&d.steer.killTimer)
	d.steer.interruptUntil = time.Now().Add(ccInterruptResultAfter)
	turn, gen := d.steer.turnSeq, p.gen
	d.steer.killTimer = time.AfterFunc(ccInterruptResultAfter, func() { d.interruptTimeout(gen, turn) })
}

// settleFired: the turn that was expected after a result never announced itself, so the messages still
// waiting for an echo were consumed without one (a slash command). Show them and go idle.
func (d *claudeCodeDriver) settleFired(gen, turn int) {
	d.sm.Lock()
	p := d.steer.proc
	if d.steer.closed || p == nil || p.gen != gen || p.inTurn || d.steer.turnSeq != turn || len(d.steer.awaiting) == 0 {
		d.sm.Unlock()
		return
	}
	left := d.steer.awaiting
	d.steer.awaiting = nil
	for _, a := range left {
		d.steer.settled = append(d.steer.settled, a.uuid)
	}
	if n := len(d.steer.settled) - ccSettledMax; n > 0 {
		d.steer.settled = d.steer.settled[n:]
	}
	d.steer.running = false
	p.interruptPending, p.interruptAckPending = false, false
	d.steer.interruptUntil = time.Time{}
	closeNow := d.idleLocked(p)
	d.sm.Unlock()
	for _, a := range left {
		d.emitTurnMessage(a.Payload())
	}
	d.syncStatus("turn_done")
	if closeNow {
		d.closeStdin(p)
	}
}

// ── interrupt ────────────────────────────────────────────────────────────────

// Interrupt cancels the turn in progress (and one about to start) without ending the process; queued
// messages are kept and run next, as with the per-turn engine. The control request goes through the same
// ordered channel as the messages, so it always follows any message already accepted.
func (d *claudeCodeDriver) Interrupt() {
	d.sm.Lock()
	if d.steer.legacy {
		d.sm.Unlock()
		d.interruptLegacy()
		return
	}
	p := d.steer.proc
	if d.steer.closed || p == nil || p.dying || p.stdinClosed || (!d.steer.running && len(d.steer.awaiting) == 0) {
		d.sm.Unlock()
		return
	}
	now := time.Now()
	for id, deadline := range p.interrupts {
		if !now.Before(deadline) {
			delete(p.interrupts, id)
		}
	}
	if len(p.interrupts) < ccMaxPending {
		id := ccUUID()
		line, _ := json.Marshal(map[string]any{
			"type": "control_request", "request_id": id,
			"request": map[string]any{"subtype": "interrupt"},
		})
		select {
		case p.writeCh <- append(line, '\n'):
		default:
			d.sm.Unlock()
			return
		}
		p.interruptID = id
		if p.interrupts == nil {
			p.interrupts = make(map[string]time.Time)
		}
		// Results may precede their ACKs, so keep requests across turn boundaries,
		// but abandon them after the same bounded wait as an acknowledged result.
		p.interrupts[id] = now.Add(ccInterruptResultAfter)
	}
	// Saturation must not disable Stop: retain outstanding ACK identities and use
	// the kill fallback until an entry completes or expires.
	p.interruptPending = !p.inTurn && len(d.steer.awaiting) > 0
	d.armInterruptKillLocked(p)
	d.sm.Unlock()
}

func (d *claudeCodeDriver) armInterruptKillLocked(p *ccProc) {
	d.steer.interruptUntil = time.Now().Add(ccInterruptFlagTTL)
	turn, gen := d.steer.turnSeq, p.gen
	d.stopTimerLocked(&d.steer.killTimer)
	d.steer.killTimer = time.AfterFunc(ccInterruptKillAfter, func() { d.interruptTimeout(gen, turn) })
}

// interruptTimeout: the interrupt did not end the turn in time. Kill the process; the next message starts a
// new one with --resume. Messages it never consumed go back to the front of the queue. Nothing happens
// unless a turn is really running in that process (an interrupt that found nothing to cut leaves the
// process alone).
func (d *claudeCodeDriver) interruptTimeout(gen, turn int) {
	d.sm.Lock()
	p := d.steer.proc
	if d.steer.closed || p == nil || p.gen != gen || d.steer.turnSeq != turn || !p.inTurn {
		d.sm.Unlock()
		return
	}
	p.killedByDriver, p.dying = true, true
	back := make([]steerQueued, 0, len(d.steer.awaiting)+len(d.steer.sq))
	for _, a := range d.steer.awaiting {
		back = append(back, a.steerQueued)
	}
	d.steer.sq = append(back, d.steer.sq...)
	d.steer.awaiting = nil
	wasInTurn := p.inTurn
	d.steer.running = false
	p.inTurn = false
	d.steer.interruptUntil = time.Time{}
	d.steer.turnSeq++
	d.stopTimerLocked(&d.steer.settleTimer)
	compacted := p.rootUsage.compactionOnly()
	p.rootUsage = claudeRootUsage{}
	d.sm.Unlock()

	d.killProc(p)
	if wasInTurn {
		d.emit("turn_done", agent.TurnDonePayload{StopReason: "interrupted", Model: p.model, Context: compacted})
	}
	d.syncStatus("turn_done")
	d.signal()
}

// ── model change, idle shutdown ──────────────────────────────────────────────

// markStale is called after a model/effort change: a live process still runs with the old flags, so it is
// closed at the next idle point (now, if idle) and the next message starts one with the new flags.
func (d *claudeCodeDriver) markStale() {
	d.sm.Lock()
	d.steer.cfgGen++
	if d.steer.legacy || d.steer.proc == nil {
		d.sm.Unlock()
		return
	}
	d.steer.stale = true
	p := d.steer.proc
	closeNow := d.idleLocked(p)
	d.sm.Unlock()
	if closeNow {
		d.closeStdin(p)
	}
}

func (d *claudeCodeDriver) isIdleLocked(p *ccProc) bool {
	return d.steer.proc == p && !p.dying && !p.stdinClosed &&
		!d.steer.running && len(d.steer.awaiting) == 0 && len(d.steer.sq) == 0
}

// idleLocked is called when the driver may have just become idle. It reports whether the process should be
// closed now (it is stale); otherwise it starts the idle-shutdown timer.
func (d *claudeCodeDriver) idleLocked(p *ccProc) bool {
	if !d.isIdleLocked(p) {
		return false
	}
	if d.steer.stale {
		return true
	}
	d.steer.idleDeferrals = 0
	d.armIdleTimerLocked(p.gen)
	return false
}

// armIdleTimerLocked gives every idle window its own identity. The caller holds sm.
func (d *claudeCodeDriver) armIdleTimerLocked(gen int) {
	d.stopIdleTimerLocked()
	epoch := d.steer.idleEpoch
	d.steer.idleTimer = time.AfterFunc(ccIdleShutdown, func() { d.idleFired(gen, epoch) })
}

// idleFired closes an idle process, unless it is running background tasks: closing it would end them, so
// the shutdown waits another ccIdleShutdown, at most ccIdleMaxDeferrals times in a row. Past that the
// process is closed as usual and its tasks are reported stopped (procExited).
func (d *claudeCodeDriver) idleFired(gen int, epoch uint64) {
	// All combined acquisitions use subMu -> sm. Holding both through the close
	// decision prevents task start/end or an idle reset from slipping past it.
	d.subMu.Lock()
	running := d.backgroundRunningLocked()
	d.sm.Lock()
	p := d.steer.proc
	if d.steer.closed || p == nil || p.gen != gen || d.steer.idleEpoch != epoch || !d.isIdleLocked(p) {
		d.sm.Unlock()
		d.subMu.Unlock()
		return
	}
	if running > 0 && d.steer.idleDeferrals < ccIdleMaxDeferrals {
		d.steer.idleDeferrals++
		logIt := !d.steer.deferLogged
		d.steer.deferLogged = true
		d.armIdleTimerLocked(gen)
		d.sm.Unlock()
		d.subMu.Unlock()
		if logIt {
			log.Printf("claude-code: idle shutdown deferred: %d background tasks running", running)
		}
		return
	}
	// Validation and the irreversible state transition belong to one critical
	// section. Channel closure and the grace timer need no state locks.
	closeNow := d.markStdinClosedLocked(p)
	d.stopIdleTimerLocked()
	d.sm.Unlock()
	d.subMu.Unlock()
	if closeNow {
		d.finishStdinClose(p)
	}
}

// backgroundIdleLocked rearms an idle session as part of the last-task removal,
// before subMu is released or callbacks/events can delay it. The caller holds
// subMu; this is the same subMu -> sm order used by idleFired. A returned process
// has its close committed already; finishStdinClose runs outside both locks.
func (d *claudeCodeDriver) backgroundIdleLocked() *ccProc {
	d.sm.Lock()
	defer d.sm.Unlock()
	p := d.steer.proc
	if d.steer.legacy || d.steer.closed || p == nil {
		return nil
	}
	if d.idleLocked(p) && d.markStdinClosedLocked(p) {
		d.stopIdleTimerLocked()
		return p
	}
	return nil
}

// closeStdin shuts an idle process down cleanly; that exit is not a crash. A process that does not leave
// within the grace period is killed.
func (d *claudeCodeDriver) closeStdin(p *ccProc) {
	d.sm.Lock()
	closeNow := d.markStdinClosedLocked(p)
	d.sm.Unlock()
	if closeNow {
		d.finishStdinClose(p)
	}
}

// markStdinClosedLocked commits shutdown while the caller's validation is still
// protected by sm. It performs no I/O or callbacks.
func (d *claudeCodeDriver) markStdinClosedLocked(p *ccProc) bool {
	if p.stdinClosed {
		return false
	}
	p.stdinClosed, p.intentionalClose = true, true
	return true
}

func (d *claudeCodeDriver) finishStdinClose(p *ccProc) {
	p.closeOnce.Do(func() { close(p.closeReq) })
	time.AfterFunc(ccCloseGrace, func() {
		select {
		case <-p.done:
		default:
			d.killProc(p)
		}
	})
}

func (d *claudeCodeDriver) stopTimerLocked(t **time.Timer) {
	if *t != nil {
		(*t).Stop()
		*t = nil
	}
}

func (d *claudeCodeDriver) stopIdleTimerLocked() {
	d.steer.idleEpoch++
	d.stopTimerLocked(&d.steer.idleTimer)
}

// ── exit handling ────────────────────────────────────────────────────────────

func (d *claudeCodeDriver) procExited(ctx context.Context, p *ccProc, werr error) {
	d.sm.Lock()
	if d.steer.proc == p {
		d.steer.proc = nil
		d.progress.clear()
	}
	d.stopTimerLocked(&d.steer.killTimer)
	d.stopTimerLocked(&d.steer.settleTimer)
	d.stopIdleTimerLocked()
	d.steer.interruptUntil = time.Time{}
	wasInTurn := p.inTurn
	left := d.steer.awaiting
	d.steer.awaiting = nil
	d.steer.running = false
	killed, intentional := p.killedByDriver, p.intentionalClose
	shuttingDown := ctx.Err() != nil || d.steer.closed
	noise := p.lastNoise
	probe := !shuttingDown && !killed && !intentional && !p.sawInit && !p.sawResult &&
		time.Since(p.started) < ccLegacyProbeWindow && strings.Contains(noise, "unknown option")
	badResume := !shuttingDown && !killed && !intentional && !p.sawInit && strings.Contains(noise, "No conversation found")
	if probe {
		d.steer.legacy = true
		// The message that carried the adoption note never reached an engine: the per-turn
		// engine's first message carries it instead.
		for _, a := range left {
			if a.adopt {
				d.adoptPending = true
			}
		}
		d.rearmAdoptNoteLocked(d.steer.sq)
	}
	// Decide the fate of the messages the process never consumed while still holding the lock, and put the
	// ones that get another go back BEFORE announcing the exit, so nothing newer can overtake them.
	var retry, fail []awaitMsg
	switch {
	case shuttingDown || killed || probe:
	case badResume && d.requireNativeResume:
		fail = left
	case intentional: // closed with something still unconsumed (nothing was lost): deliver it again
		retry = left
	default:
		for _, a := range left {
			if a.retried {
				fail = append(fail, a)
			} else {
				a.retried = true
				retry = append(retry, a)
			}
		}
	}
	if len(retry) > 0 {
		back := make([]steerQueued, 0, len(retry))
		for _, a := range retry {
			back = append(back, a.steerQueued)
		}
		d.steer.sq = append(back, d.steer.sq...)
	}
	var pending []steerQueued
	if probe {
		pending = d.steer.sq
		d.steer.sq = nil
	}
	d.sm.Unlock()
	close(p.done)

	if p.cmd != nil && p.cmd.Process != nil && !d.prefix.enabled() {
		d.reportEngine(p.cmd.Process.Pid, false)
	}
	if badResume && !d.requireNativeResume {
		d.clearCCSessionID() // that conversation is gone: the retry starts a fresh one
	}
	if !shuttingDown {
		// The tasks it left running in the background ended with it (the session ending says so itself).
		d.endBackgroundTasks()
	}
	if shuttingDown || killed {
		d.prefix.clearPID(p.pidFile)
		return // a driver-initiated end: whoever ended it has said what there was to say
	}
	switch {
	case probe:
		// This CLI cannot stream: the session continues on the per-turn engine.
		log.Printf("claude-code: the CLI does not support streaming input; using one process per turn")
		for _, a := range left {
			d.enqueueLegacy(a.Input)
		}
		for _, m := range pending {
			d.enqueueLegacy(m.Input)
		}
	case intentional:
	default:
		if wasInTurn || len(left) > 0 {
			msg := "claude-code exited"
			if werr != nil {
				msg += ": " + werr.Error()
			}
			msg += turnFailureDetail(noise)
			if badResume {
				msg = "the earlier Claude conversation could not be resumed — continuing in a new one" + turnFailureDetail(noise)
				if d.requireNativeResume {
					msg = "The retained native Claude conversation could not be resumed. A new conversation was not started."
				}
			}
			d.emit("error", agent.ErrorPayload{Message: msg, Retryable: true})
			if wasInTurn { // a turn was under way (not merely a message that never got consumed)
				d.sm.Lock()
				compacted := p.rootUsage.compactionOnly()
				d.sm.Unlock()
				d.emit("turn_done", agent.TurnDonePayload{StopReason: "error", Model: p.model, Context: compacted})
			}
		}
		for _, a := range fail {
			d.failMessage(a.steerQueued, "message not delivered: claude-code exited"+turnFailureDetail(noise))
		}
	}
	d.syncStatus("turn_done")
	d.prefix.clearPID(p.pidFile)
	d.signal()
}

// shutdown ends the process when the session does (context cancelled) or the engine gives way.
func (d *claudeCodeDriver) shutdown() {
	d.sm.Lock()
	d.steer.closed = true
	p := d.steer.proc
	if p != nil {
		p.killedByDriver, p.dying = true, true
	}
	d.stopTimerLocked(&d.steer.killTimer)
	d.stopTimerLocked(&d.steer.settleTimer)
	d.stopIdleTimerLocked()
	d.sm.Unlock()
	if p != nil {
		d.killProc(p)
	}
}
