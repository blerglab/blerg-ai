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
	"syscall"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// Timings, vars so tests can shrink them.
var (
	// ccIdleShutdown closes the process of a session that has been idle this long (it restarts with --resume).
	ccIdleShutdown = 10 * time.Minute
	// ccBgStallAfter is how long a running background task's output may stay unchanged before an idle
	// session is woken to look at it.
	ccBgStallAfter = 30 * time.Minute
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
	// ccWakeFallbackAfter is how long closeBackgroundTask's own wake-up waits, once a last task ends
	// while the session is idle, for the CLI to resume the turn on its own before sending it
	// (BLG-447 code review): long enough that an ordinary self-wake preempts it every time.
	ccWakeFallbackAfter = 5 * time.Second
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
	stopMu  sync.Mutex // serializes cleanup outcome with process exit accounting
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
	sawInit, sawResult      bool
	inTurn                  bool // a turn has begun (init) and not yet ended (result)
	stdinClosed             bool
	intentionalClose        bool
	killedByDriver          bool
	interruptRefused        bool // keep admission closed until Stop is retried or the turn ends
	stopAfterTurnEnd        bool
	commandUUID             string
	turnEndWork             bool // turn-end emission can block on sync or pending event capacity
	dying                   bool // being killed: no new message may be written to it
	lastNoise               string
	rootUsage               claudeRootUsage
	mcpInit                 mcpProbe
	mcpWait                 map[string]chan ccMCPReply
	interruptID             string
	interrupts              map[string]time.Time // pending ACK deadlines, bounded by ccMaxPending
	interruptPending        bool                 // Stop of a written turn awaiting init, including fallback-only
	interruptAckPending     bool                 // acknowledged cancellation of a written turn awaiting init
	interruptBackgroundWait bool                 // unconsumed input held behind background work
	interruptQueueWait      bool                 // original turn ended; the next init must run queued input
	interruptDeadline       time.Time            // repeated Stop/ACKs keep the same deadline
	interruptAcknowledged   bool
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
	// deferLogged says an idle shutdown put off for background tasks was logged once for this session.
	deferLogged bool
	wake        chan struct{}
	// wakeTimer and wakePending are closeBackgroundTask's own fallback wake-up for a task that
	// ended while the session was idle (BLG-447): armed for ccWakeFallbackAfter, canceled by any
	// sign the CLI is resuming on its own (markRunning) or that another input is already headed to
	// it (EnqueueInput), so the two can never both start a turn for the same task's end.
	wakeTimer   *time.Timer
	wakePending *agent.BackgroundTaskDonePayload
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
	d.enqueueInputGuarded(in, nil)
}

// enqueueInputGuarded is EnqueueInput with an optional guard checked under the same sm lock as the
// actual append — not merely before it. A caller that decided "idle, so wake it" outside that lock
// (wakeFallbackFired) cannot otherwise rule out the CLI self-resuming in the gap between its own
// check and the enqueue, which would start a second, redundant turn (BLG-447 review). A false
// guard silently drops the input: the caller's premise no longer holds, so nothing should be
// queued at all. A nil guard always proceeds, exactly like EnqueueInput.
func (d *claudeCodeDriver) enqueueInputGuarded(in agent.Input, guard func() bool) {
	in = d.WithPersonTime(in)
	if in.Source == "" {
		in.Source = "chat"
	}
	background := d.backgroundRunning()
	d.mu.Lock()
	legacyBusy := d.legacyTurn != nil
	d.mu.Unlock()
	d.sm.Lock()
	if guard != nil && !guard() {
		d.sm.Unlock()
		return
	}
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
	// Any input about to be delivered makes a pending background-task wake-up moot: it would
	// otherwise risk a second, redundant turn once the fallback fires too (BLG-447).
	d.cancelWakeFallbackLocked()
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
			if p := d.steer.proc; p != nil && (p.turnEndWork || p.interruptRefused || (!p.interruptDeadline.IsZero() && !p.interruptQueueWait)) {
				pending = false
			}
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
			d.emit("error", agent.ErrorPayload{Message: "The worker could not start: " + d.safeClaudeStopError(err) + ". This message was not delivered; try again.", Retryable: true, ClientID: m.ClientID})
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
	if d.steer.proc != p || p.dying || p.stdinClosed || p.turnEndWork || p.interruptRefused || (!p.interruptDeadline.IsZero() && !p.interruptQueueWait) || len(d.steer.sq) == 0 {
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
	if !p.inTurn && strings.HasPrefix(strings.TrimSpace(m.Text), "/") {
		p.commandUUID = uuid
	}
	d.steer.running = true
	d.stopIdleTimerLocked()
	if p.interruptQueueWait && !p.turnEndWork {
		if len(d.steer.awaiting) == 1 {
			p.interruptDeadline = time.Time{}
		}
		d.armInterruptKillLocked(p)
	}
	d.sm.Unlock()
	// Busy from the write, not from the replay (which comes a second or so later).
	d.syncStatus("turn")
}

// failMessage makes sure a message that could not be delivered still shows in the transcript, then says why.
func (d *claudeCodeDriver) failMessage(m steerQueued, why string) {
	d.emitTurnMessage(m.Payload())
	d.emit("error", agent.ErrorPayload{Message: why, Retryable: true, ClientID: m.ClientID})
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
	opts = append(opts, func(o *ccOptions) { o.SkillPolicy = d.skillPolicy; o.WorkspaceGuide = d.workspaceGuide }, withMarkerNonce(d.markerNonce), withPromptFile(d.promptFile))
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
	cmd := d.prefix.commandGroupPID(ctx, d.workDir, "claude", pidFile, ccSessionArgs(model, effort, resume, opts...)...)
	cmd.Env = d.env
	if !d.prefix.enabled() && d.startCommand == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.Cancel = func() error {
			err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if err == syscall.ESRCH {
				return os.ErrProcessDone
			}
			return err
		}
	}
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
				d.cleanupProc(p) // a pipe that no longer takes writes belongs to a process that is gone or wedged
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
		d.cleanupProc(p)
	}
	werr := p.cmd.Wait()
	d.procExited(ctx, p, werr)
}

// killProc ends the process for good: SIGKILL inside the container (SIGTERM could be ignored by a wedged
// engine, and a process that never exits would block every later message), and the host-side child.
func (d *claudeCodeDriver) killProc(p *ccProc) error {
	if d.prefix.enabled() {
		if err := d.prefix.killGroupPID(p.pidFile); err != nil {
			return err
		}
	}
	if p.cmd.Process != nil {
		return cancelEngineCommand(p.cmd, d.startCommand != nil || !d.prefix.enabled())
	}
	return nil
}

// Teardown outside an explicit Stop has no waiting request to answer. Retain a
// sanitized diagnostic if its cancellation boundary refuses cleanup.
func (d *claudeCodeDriver) cleanupProc(p *ccProc) {
	if err := d.killProc(p); err != nil {
		log.Printf("claude-code: engine cleanup failed: %s", d.safeClaudeStopError(err))
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
		var ev ccLine
		if json.Unmarshal(line, &ev) == nil && ev.Type == "user" && ev.IsReplay && !ev.IsSynthetic && ev.ParentToolUseID == "" {
			d.sm.Lock()
			var consumed *awaitMsg
			for i, a := range d.steer.awaiting {
				if a.uuid == ev.UUID {
					receipt := a
					consumed = &receipt
					d.steer.awaiting = append(d.steer.awaiting[:i], d.steer.awaiting[i+1:]...)
					break
				}
			}
			d.sm.Unlock()
			if consumed != nil {
				receipt := consumed.Payload()
				receipt.StoppedTurn = true
				d.emitTurnMessage(receipt)
			}
		} else {
			// A refused cleanup leaves a live reader. Process its ordinary output
			// once the cleanup outcome is known, rather than discarding a result.
			p.stopMu.Lock()
			d.sm.Lock()
			live := !p.dying && !p.killedByDriver
			d.sm.Unlock()
			p.stopMu.Unlock()
			if live {
				d.onLine(p, line)
			}
		}
		return
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
	// The CLI is resuming this turn by itself (or the driver already started one some other way):
	// either way, a pending fallback wake-up for the same idle gap is now moot (BLG-447).
	d.cancelWakeFallbackLocked()
	d.sm.Unlock()
	if !was {
		d.syncStatus(reason)
	}
}

func (d *claudeCodeDriver) onInit(p *ccProc, ev *ccLine, line []byte) {
	d.observeMCPInit(p, line)
	d.sm.Lock()
	p.sawInit, p.inTurn = true, true
	if len(d.steer.awaiting) > 0 && strings.HasPrefix(strings.TrimSpace(d.steer.awaiting[0].Text), "/") {
		p.commandUUID = d.steer.awaiting[0].uuid
	}
	d.steer.turnSeq++ // a turn is starting: timers armed for an earlier one are stale
	switch {
	case p.interruptQueueWait:
		// The stopped turn already ended. Initialization proves its queued input
		// can proceed; its recovery deadline must never cut this new turn.
		d.stopTimerLocked(&d.steer.killTimer)
		p.interruptDeadline = time.Time{}
		p.interruptAcknowledged = false
		d.steer.interruptUntil = time.Time{}
	case p.interruptAckPending:
		p.interruptDeadline = time.Time{}
		p.interruptAcknowledged = false
		d.armInterruptResultLocked(p)
	case p.interruptPending:
		// Initialization starts this turn's deadline, even in a warm process.
		p.interruptDeadline = time.Time{}
		d.armInterruptKillLocked(p)
	}
	p.interruptPending, p.interruptAckPending = false, false
	p.interruptBackgroundWait, p.interruptQueueWait = false, false
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
	background := d.backgroundRunning()
	d.sm.Lock()
	var command *awaitMsg
	if p.commandUUID != "" && (!ev.IsError || p.sawInit) {
		for i, a := range d.steer.awaiting {
			if a.uuid == p.commandUUID {
				command = &a
				d.steer.awaiting = append(d.steer.awaiting[:i], d.steer.awaiting[i+1:]...)
				break
			}
		}
		p.commandUUID = ""
	}
	if ev.IsError && !p.sawInit {
		// The process failed before it began a turn (a --resume that does not exist): not a turn outcome.
		// Its exit follows and is reported once, by procExited.
		d.sm.Unlock()
		return
	}
	stopRequested := !d.steer.interruptUntil.IsZero() && time.Now().Before(d.steer.interruptUntil)
	interrupted := stopRequested && p.sawInit && ev.Subtype == "error_during_execution"
	d.steer.interruptUntil = time.Time{}
	d.stopTimerLocked(&d.steer.killTimer)
	d.steer.turnSeq++
	turn, gen := d.steer.turnSeq, p.gen
	p.sawResult, p.inTurn, p.turnEndWork = true, false, true
	p.interruptRefused = false
	p.interruptPending, p.interruptAckPending = false, false
	p.interruptBackgroundWait, p.interruptQueueWait, p.interruptAcknowledged = false, false, false
	p.interruptDeadline = time.Time{}
	noise := p.lastNoise
	partition := p.rootUsage.partition(ev.ccUsage())
	turnContext := p.rootUsage.contextUsage(ev)
	p.rootUsage = claudeRootUsage{}
	settle := len(d.steer.awaiting) > 0
	recoverQueue := stopRequested && background > 0 && (settle || len(d.steer.sq) > 0)
	settle = settle || recoverQueue
	closeNow := false
	if settle {
		d.stopTimerLocked(&d.steer.settleTimer)
		if recoverQueue {
			// A completed Stop cannot leave pending input behind a background wait,
			// nor can slash-command settlement pretend that input was consumed.
			p.interruptBackgroundWait, p.interruptQueueWait = true, true
			// Arm after turn-end callbacks have released the output reader.
		} else {
			// A message was written but not echoed yet: the CLI starts another turn for it.
			d.steer.settleTimer = time.AfterFunc(ccSettleAfter, func() { d.settleFired(gen, turn) })
		}
	} else {
		d.steer.running = false
		closeNow = d.idleLocked(p)
	}
	d.sm.Unlock()

	if command != nil {
		d.emitTurnMessage(command.Payload())
	}
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
	d.sm.Lock()
	p.turnEndWork = false
	deferredStop := p.stopAfterTurnEnd
	p.stopAfterTurnEnd = false
	d.sm.Unlock()
	if deferredStop {
		d.Interrupt()
	}
	if recoverQueue {
		d.sm.Lock()
		if d.steer.proc == p && d.steer.turnSeq == turn && p.interruptQueueWait && len(d.steer.awaiting) > 0 {
			p.interruptDeadline = time.Time{}
			d.armInterruptKillLocked(p)
		}
		d.sm.Unlock()
	}
	if !settle {
		d.syncStatus("turn_done")
	}
	if closeNow {
		d.closeIdle(p)
	}
	d.signal()
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
	if !p.inTurn {
		d.stopTimerLocked(&d.steer.killTimer)
		// deliverOne has already marked a written message running, but init has not
		// arrived. A background wait needs a deadline even if init never comes;
		// other written input retains the ordinary init-bound timer.
		p.interruptAckPending = len(d.steer.awaiting) > 0
		if p.interruptAckPending && p.interruptBackgroundWait {
			d.armInterruptResultLocked(p)
		} else {
			p.interruptDeadline = time.Time{}
			p.interruptAcknowledged = false
			if !p.interruptAckPending {
				p.interruptPending = false
				d.steer.interruptUntil = time.Time{}
			}
		}
		return
	}
	d.armInterruptResultLocked(p)
}

func (d *claudeCodeDriver) armInterruptResultLocked(p *ccProc) {
	if !p.interruptAcknowledged {
		p.interruptAcknowledged = true
		p.interruptDeadline = time.Now().Add(ccInterruptResultAfter)
	}
	d.stopTimerLocked(&d.steer.killTimer)
	d.steer.interruptUntil = p.interruptDeadline
	turn, gen, deadline := d.steer.turnSeq, p.gen, p.interruptDeadline
	d.steer.killTimer = time.AfterFunc(time.Until(deadline), func() { d.interruptTimeoutFor(gen, turn, deadline) })
}

// settleFired: the turn that was expected after a result never announced itself, so the messages still
// waiting for an echo were consumed without one (a slash command). Show them and go idle.
func (d *claudeCodeDriver) settleFired(gen, turn int) {
	background := d.backgroundRunning()
	d.sm.Lock()
	if background > 0 {
		d.sm.Unlock()
		return
	}
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
	p.interruptBackgroundWait, p.interruptQueueWait, p.interruptAcknowledged = false, false, false
	p.interruptDeadline = time.Time{}
	d.steer.interruptUntil = time.Time{}
	closeNow := d.idleLocked(p)
	d.sm.Unlock()
	for _, a := range left {
		d.emitTurnMessage(a.Payload())
	}
	d.syncStatus("turn_done")
	if closeNow {
		d.closeIdle(p)
	}
}

// ── interrupt ────────────────────────────────────────────────────────────────

// Interrupt cancels the turn in progress (and one about to start) without ending the process; queued
// messages are kept and run next, as with the per-turn engine. The control request goes through the same
// ordered channel as the messages, so it always follows any message already accepted.
func (d *claudeCodeDriver) Interrupt() {
	background := d.backgroundRunning()
	d.sm.Lock()
	if d.steer.legacy {
		d.sm.Unlock()
		d.interruptLegacy()
		return
	}
	p := d.steer.proc
	if !d.steer.closed && !d.steer.running && len(d.steer.awaiting) == 0 && background > 0 && (p == nil || !p.turnEndWork) {
		d.sm.Unlock()
		d.emit("background_wait_stopped", map[string]string{"message": "Stopped waiting; background work may still be running."})
		return
	}
	if p != nil && p.turnEndWork {
		p.stopAfterTurnEnd = true
		d.sm.Unlock()
		return
	}
	if d.steer.closed || p == nil || p.dying || p.stdinClosed || (!d.steer.running && len(d.steer.awaiting) == 0) {
		d.sm.Unlock()
		return
	}
	if p.interruptQueueWait {
		// The earlier Stop finished. This press applies to the held input now,
		// rather than granting it permission to run when init finally arrives.
		p.interruptQueueWait = false
		p.interruptDeadline = time.Time{}
		p.interruptAcknowledged = false
		d.stopTimerLocked(&d.steer.killTimer)
	}
	if !p.inTurn && len(d.steer.awaiting) > 0 && background > 0 {
		p.interruptBackgroundWait = true
		// Settlement is for slash commands, not input the engine has not consumed.
		d.stopTimerLocked(&d.steer.settleTimer)
	}
	p.interruptRefused = false
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
	if p.turnEndWork || (!p.inTurn && !p.interruptBackgroundWait) {
		p.interruptDeadline = time.Time{}
		return
	}
	if p.interruptDeadline.IsZero() {
		p.interruptDeadline = time.Now().Add(ccInterruptKillAfter)
	}
	if !p.interruptAcknowledged {
		d.steer.interruptUntil = time.Now().Add(ccInterruptFlagTTL)
	}
	turn, gen, deadline := d.steer.turnSeq, p.gen, p.interruptDeadline
	d.stopTimerLocked(&d.steer.killTimer)
	d.steer.killTimer = time.AfterFunc(time.Until(deadline), func() { d.interruptTimeoutFor(gen, turn, deadline) })
}

// interruptTimeout: the interrupt did not end the turn in time. Kill the process; the next message starts a
// new one with --resume. Messages it never consumed go back to the front of the queue. Nothing happens
// unless a turn is running or unconsumed input is held behind background work.
// An interrupt that found nothing to cut leaves the process alone.
func (d *claudeCodeDriver) interruptTimeout(gen, turn int) {
	d.interruptTimeoutFor(gen, turn, time.Time{})
}

func (d *claudeCodeDriver) interruptTimeoutFor(gen, turn int, deadline time.Time) {
	d.sm.Lock()
	candidate := d.steer.proc
	d.sm.Unlock()
	if candidate == nil {
		return
	}
	candidate.stopMu.Lock()
	defer candidate.stopMu.Unlock()
	d.sm.Lock()
	p := d.steer.proc
	if d.steer.closed || p == nil || p.gen != gen || d.steer.turnSeq != turn ||
		p.turnEndWork || (!deadline.IsZero() && !p.interruptDeadline.Equal(deadline)) ||
		(!p.inTurn && (!p.interruptBackgroundWait || len(d.steer.awaiting) == 0)) {
		d.sm.Unlock()
		return
	}
	p.dying = true
	d.sm.Unlock()
	if err := d.killProc(p); err != nil {
		d.sm.Lock()
		p.dying = false
		p.interruptRefused = true
		p.interruptDeadline = time.Time{}
		p.interruptAcknowledged = false
		d.sm.Unlock()
		d.emit("error", agent.ErrorPayload{Message: "The worker could not stop safely: " + d.safeClaudeStopError(err) + ". Queued messages are still waiting; try Stop again.", Retryable: true, Code: agent.ErrorCodeStopFailed})
		return
	}
	d.sm.Lock()
	p.killedByDriver = true
	// Keep unconfirmed inputs with this reader until EOF. A replay already in
	// stdout may be behind a blocked Emit; only EOF proves no receipt remains.
	wasInTurn := p.inTurn
	d.steer.running = false
	p.inTurn = false
	d.steer.interruptUntil = time.Time{}
	d.steer.turnSeq++
	d.stopTimerLocked(&d.steer.settleTimer)
	compacted := p.rootUsage.compactionOnly()
	p.rootUsage = claudeRootUsage{}
	d.sm.Unlock()

	if wasInTurn {
		d.emit("turn_done", agent.TurnDonePayload{StopReason: "interrupted", Model: p.model, Context: compacted})
	}
	if !wasInTurn {
		d.emit("background_wait_stopped", map[string]string{"message": "Stopped waiting; background work may still be running."})
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
		d.closeIdle(p)
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
// the shutdown waits another ccIdleShutdown, for as long as the engine reports them running. There is no
// limit: a command that runs for hours is live work, a session's Stop still ends it, and the CLI itself
// bounds a background command with its timeout. Each wait also looks for a task that has gone quiet.
func (d *claudeCodeDriver) idleFired(gen int, epoch uint64) {
	d.closeUnlistedTasks()
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
	if running > 0 {
		logIt := !d.steer.deferLogged
		d.steer.deferLogged = true
		d.armIdleTimerLocked(gen)
		d.sm.Unlock()
		d.subMu.Unlock()
		if logIt {
			log.Printf("claude-code: idle shutdown deferred: %d background tasks running", running)
		}
		d.wakeForStalledTasks()
		return
	}
	// Validation and the irreversible state transition belong to one critical
	// section. Channel closure and the grace timer need no state locks.
	closeNow := d.markStdinClosedLocked(p)
	d.stopIdleTimerLocked()
	d.sm.Unlock()
	d.subMu.Unlock()
	if closeNow {
		log.Printf("claude-code: idle shutdown: generation=%d idle_for=%s", p.gen, ccIdleShutdown)
		d.finishStdinClose(p)
	}
}

// backgroundIdleLocked rearms an idle session as part of the last-task removal,
// before subMu is released or callbacks/events can delay it. It never closes the
// process, even a stale one: closeBackgroundTask wakes it itself for the task that
// just ended (wakeForFinishedTask, BLG-447), so closing now would lose nothing,
// but the ordinary idle shutdown still needs its own full window after that. The
// caller holds subMu; this is the same subMu -> sm order used by idleFired.
func (d *claudeCodeDriver) backgroundIdleLocked() {
	d.sm.Lock()
	defer d.sm.Unlock()
	p := d.steer.proc
	if d.steer.legacy || d.steer.closed || p == nil || !d.isIdleLocked(p) {
		return
	}
	d.armIdleTimerLocked(p.gen)
}

// idleNowLocked reports whether the process is idle right now (no turn running, nothing
// queued or awaiting): closeBackgroundTask's cue that a fallback wake-up may be worth arming,
// since Claude Code resuming itself (BLG-447 found it does not always do so promptly, if at all)
// is otherwise the only thing that would start a turn for the task that just ended. The caller
// holds subMu; this is the same subMu -> sm order used by idleFired and backgroundIdleLocked.
func (d *claudeCodeDriver) idleNowLocked() bool {
	d.sm.Lock()
	defer d.sm.Unlock()
	p := d.steer.proc
	return !d.steer.legacy && p != nil && d.isIdleLocked(p)
}

// armWakeFallback arms (replacing any earlier one) closeBackgroundTask's own fallback wake-up for
// a task that ended while the session was idle, rechecking eligibility itself (idleNowLocked's
// caller may have released sm since): a message, Stop, or the CLI resuming meanwhile makes this a
// no-op. Canceled by markRunning (the CLI resumed, or anything else started a turn) or by any
// input reaching the queue (EnqueueInput) before ccWakeFallbackAfter elapses — never both.
func (d *claudeCodeDriver) armWakeFallback(p agent.BackgroundTaskDonePayload) {
	d.sm.Lock()
	defer d.sm.Unlock()
	proc := d.steer.proc
	if d.steer.legacy || proc == nil || !d.isIdleLocked(proc) {
		return
	}
	d.stopTimerLocked(&d.steer.wakeTimer)
	cp := p
	d.steer.wakePending = &cp
	gen := proc.gen
	d.steer.wakeTimer = time.AfterFunc(ccWakeFallbackAfter, func() { d.wakeFallbackFired(gen) })
}

// cancelWakeFallbackLocked drops a pending fallback wake-up, if there is one. The caller holds sm.
func (d *claudeCodeDriver) cancelWakeFallbackLocked() {
	d.stopTimerLocked(&d.steer.wakeTimer)
	d.steer.wakePending = nil
}

// wakeFallbackFired delivers a still-pending wake-up once ccWakeFallbackAfter has passed with no
// sign the CLI resumed on its own: rechecked under sm so a stale timer (an older process
// generation, or one already canceled or delivered) does nothing.
func (d *claudeCodeDriver) wakeFallbackFired(gen int) {
	d.sm.Lock()
	proc := d.steer.proc
	pending := d.steer.wakePending
	if pending == nil || proc == nil || proc.gen != gen || d.steer.legacy || !d.isIdleLocked(proc) {
		d.sm.Unlock()
		return
	}
	d.steer.wakeTimer, d.steer.wakePending = nil, nil
	p := *pending
	d.sm.Unlock()
	if d.afterWakeFallbackUnlock != nil {
		d.afterWakeFallbackUnlock()
	}
	// The idle finding above is already stale the instant sm is released: the CLI could
	// self-resume in the gap before the guard below re-acquires it. Re-check isIdleLocked under
	// that same lock, right before the append, instead of trusting this snapshot (BLG-447 review).
	d.wakeForFinishedTask(p, func() bool {
		return d.steer.proc == proc && proc.gen == gen && !d.steer.legacy && d.isIdleLocked(proc)
	})
}

// closeIdle closes a process that wants closing at an idle point (a stale one), unless background work
// still runs on it: then the idle timer decides again, after the work has ended.
func (d *claudeCodeDriver) closeIdle(p *ccProc) {
	d.subMu.Lock()
	d.sm.Lock()
	if !d.isIdleLocked(p) {
		d.sm.Unlock()
		d.subMu.Unlock()
		return
	}
	closeNow := false
	if d.backgroundRunningLocked() > 0 {
		d.armIdleTimerLocked(p.gen)
	} else {
		closeNow = d.markStdinClosedLocked(p)
	}
	d.sm.Unlock()
	d.subMu.Unlock()
	if closeNow {
		d.finishStdinClose(p)
	}
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
			d.cleanupProc(p)
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
	p.stopMu.Lock()
	defer p.stopMu.Unlock()
	defer func() {
		d.sm.Lock()
		if d.steer.proc == p {
			d.steer.proc = nil
		}
		d.sm.Unlock()
		close(p.done)
		d.signal()
	}()
	d.sm.Lock()
	if d.steer.proc == p {
		p.dying = true
		d.progress.clear()
	}
	d.stopTimerLocked(&d.steer.killTimer)
	d.stopTimerLocked(&d.steer.settleTimer)
	d.stopIdleTimerLocked()
	// This process (and, if a restart or a fallback to the per-turn engine follows, its own
	// background-task bookkeeping) is gone: a pending wake-up tagged to its generation would never
	// fire for the right process anyway, but dropping it here is explicit (BLG-447).
	d.cancelWakeFallbackLocked()
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
	case shuttingDown || probe:
	case killed:
		retry = left
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
	pid := 0
	if p.cmd != nil && p.cmd.Process != nil {
		pid = p.cmd.Process.Pid
	}
	log.Printf("claude-code: process exited: generation=%d pid=%d status=%v intentional=%t driver_kill=%t shutdown=%t", p.gen, pid, werr, intentional, killed, shuttingDown)

	if p.cmd != nil && p.cmd.Process != nil && !d.prefix.enabled() {
		d.reportEngine(p.cmd.Process.Pid, false)
	}
	if badResume && !d.requireNativeResume {
		d.clearCCSessionID() // that conversation is gone: the retry starts a fresh one
	}
	lost := d.liveBackgroundWork()
	// Bash processes may outlive the engine; its subagents, Monitors, and
	// pending native calls cannot. Retain only the uncertain shell inventory.
	d.endBackgroundTasks(true)
	if shuttingDown || killed {
		d.prefix.clearPID(p.pidFile)
		if killed && !shuttingDown {
			d.signal()
		}
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
	if !probe {
		d.tellBackgroundLost(lost)
	}
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
	d.cancelWakeFallbackLocked()
	d.sm.Unlock()
	if p != nil {
		d.cleanupProc(p)
	}
}

func (d *claudeCodeDriver) safeClaudeStopError(err error) string {
	return safeEngineCommandError(err, d.env)
}

func (d *claudeCodeDriver) HasInterruptibleWork() bool {
	d.sm.Lock()
	legacy := d.steer.legacy
	work := d.steer.running || len(d.steer.awaiting) > 0 || (d.steer.proc != nil && d.steer.proc.turnEndWork)
	d.sm.Unlock()
	if legacy {
		d.mu.Lock()
		work = work || d.legacyTurn != nil
		d.mu.Unlock()
	}
	return work
}
