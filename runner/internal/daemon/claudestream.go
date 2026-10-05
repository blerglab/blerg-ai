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
	text, source string
	retried      bool // already failed to reach a process once
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
	sawInit, sawResult bool
	inTurn             bool // a turn has begun (init) and not yet ended (result)
	stdinClosed        bool
	intentionalClose   bool
	killedByDriver     bool
	dying              bool // being killed: no new message may be written to it
	lastNoise          string
}

type steerState struct {
	legacy  bool
	closed  bool // the session is ending: no timer may emit
	proc    *ccProc
	nextGen int
	pidTag  string
	sq      []steerQueued // accepted, not yet written
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
	wake           chan struct{}
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
	if d.steering() {
		d.runSteering(ctx)
	}
	if !d.steering() {
		// Anything accepted by the steering engine before it gave way continues on the per-turn one.
		d.sm.Lock()
		pending := d.steer.sq
		d.steer.sq = nil
		d.sm.Unlock()
		for _, m := range pending {
			d.enqueueLegacy(m.text, m.source)
		}
		d.runLegacy(ctx)
	}
}

// Enqueue accepts a message. It never blocks.
func (d *claudeCodeDriver) Enqueue(text, source string) {
	if source == "" {
		source = "chat"
	}
	d.sm.Lock()
	if d.steer.legacy {
		d.sm.Unlock()
		d.enqueueLegacy(text, source)
		return
	}
	if len(d.steer.sq)+len(d.steer.awaiting) >= ccMaxPending {
		d.sm.Unlock()
		d.emit("error", agent.ErrorPayload{Message: "message queue full — dropped", Retryable: true})
		return
	}
	d.steer.sq = append(d.steer.sq, steerQueued{text: text, source: source})
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
		for {
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
		}
	}
}

// deliverOne writes the oldest queued message to the process, starting one first when needed. The process
// is secured BEFORE the message is taken from the queue, so a process that goes away in between (and puts
// its unconsumed messages back at the front) can never make a newer message overtake an older one.
func (d *claudeCodeDriver) deliverOne(ctx context.Context) {
	p, err := d.ensureProc(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		d.sm.Lock()
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
	uuid := ccUUID()
	d.sm.Lock()
	if d.steer.proc != p || p.dying || p.stdinClosed || len(d.steer.sq) == 0 {
		d.sm.Unlock()
		return // the world changed under us: look again
	}
	m := d.steer.sq[0]
	line, err := json.Marshal(map[string]any{
		"type": "user", "uuid": uuid,
		"message": map[string]any{"role": "user", "content": m.text},
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
	d.emit("user_message", agent.UserMessagePayload{Text: m.text, Source: m.source})
	d.emit("error", agent.ErrorPayload{Message: why, Retryable: true})
}

// ensureProc returns a live process, starting one when there is none. Only the run loop calls it, so starts
// never overlap; a process on its way out is waited for first (two live processes on one session would fork
// its conversation).
func (d *claudeCodeDriver) ensureProc(ctx context.Context) (*ccProc, error) {
	for {
		d.sm.Lock()
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
	} else if d.mcpFile == nil {
		opts = append(opts, withSessionGuide())
	}
	if d.mcpFile != nil {
		path, err := d.mcpFile.Ensure()
		if err != nil {
			// Never run a grant session without its allow-list.
			return nil, fmt.Errorf("MCP gateway config: %w", err)
		}
		opts = append(opts, withMCPConfigPath(path))
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
	opts = append(opts, withPluginDirs(d.pluginDirs))
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
	if err := cmd.Start(); err != nil {
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
		_ = p.cmd.Process.Kill()
	}
}

// ── events ───────────────────────────────────────────────────────────────────

func (d *claudeCodeDriver) onLine(p *ccProc, line []byte) {
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
		}
	case "assistant":
		d.markRunning("agent turn")
		d.emitAssistantBlocks(&ev)
	case "user":
		d.onUser(&ev)
	case "result":
		d.onResult(p, &ev)
	case "control_response":
		d.onControlResponse(p)
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
	d.sm.Lock()
	p.sawInit, p.inTurn = true, true
	d.steer.turnSeq++ // a turn is starting: timers armed for an earlier one are stale
	d.stopTimerLocked(&d.steer.settleTimer)
	d.sm.Unlock()
	if ev.SessionID != "" {
		d.mu.Lock()
		d.ccSessionID = ev.SessionID
		d.mu.Unlock()
	}
	d.markRunning("agent turn")
	if pl, ok := claudeInitCapabilities(line, d.skills, d.workDir, d.home); ok {
		d.caps.emit(d.emitter, pl)
	}
}

func (d *claudeCodeDriver) onUser(ev *ccLine) {
	if !ev.IsReplay {
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
	switch {
	case matched:
		d.emit("user_message", agent.UserMessagePayload{Text: m.text, Source: m.source})
	case lateSettled || !isString || strings.HasPrefix(strings.TrimSpace(echo), "<local-command"):
		// already shown, or the CLI's own rendering of a command's output
	default:
		// A message nobody here wrote (a hook, a resumed history): show it as the user's.
		d.emit("user_message", agent.UserMessagePayload{Text: echo, Source: "chat"})
	}
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
	noise := p.lastNoise
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

	if ev.SessionID != "" {
		d.mu.Lock()
		d.ccSessionID = ev.SessionID
		d.mu.Unlock()
	}
	stop := "end_turn"
	if interrupted {
		stop = "interrupted"
	} else if ev.IsError {
		// A TURN outcome (the model could not finish), not a driver failure: the session takes the next message.
		msg := firstN(ev.Result, 2000)
		if msg == "" {
			msg = "claude-code turn failed" + turnFailureDetail(noise)
		}
		d.emit("error", agent.ErrorPayload{Message: msg, Retryable: true})
	}
	d.emit("turn_done", agent.TurnDonePayload{StopReason: stop, Model: p.model, Usage: ev.ccUsage()})
	if !settle {
		d.syncStatus("turn_done")
	}
	if closeNow {
		d.closeStdin(p)
	}
}

// onControlResponse: the interrupt was acknowledged. The turn is being cut, which can take a while (a slow
// tool), so the short "not honoured" timer gives way to a long one waiting for the result.
func (d *claudeCodeDriver) onControlResponse(p *ccProc) {
	d.sm.Lock()
	defer d.sm.Unlock()
	if d.steer.killTimer == nil {
		return
	}
	d.stopTimerLocked(&d.steer.killTimer)
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
	closeNow := d.idleLocked(p)
	d.sm.Unlock()
	for _, a := range left {
		d.emit("user_message", agent.UserMessagePayload{Text: a.text, Source: a.source})
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
	line, _ := json.Marshal(map[string]any{
		"type": "control_request", "request_id": ccUUID(),
		"request": map[string]any{"subtype": "interrupt"},
	})
	select {
	case p.writeCh <- append(line, '\n'):
	default:
		d.sm.Unlock()
		return
	}
	d.steer.interruptUntil = time.Now().Add(ccInterruptFlagTTL)
	turn, gen := d.steer.turnSeq, p.gen
	d.stopTimerLocked(&d.steer.killTimer)
	d.steer.killTimer = time.AfterFunc(ccInterruptKillAfter, func() { d.interruptTimeout(gen, turn) })
	d.sm.Unlock()
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
	d.sm.Unlock()

	d.killProc(p)
	if wasInTurn {
		d.emit("turn_done", agent.TurnDonePayload{StopReason: "interrupted", Model: p.model})
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
	d.stopIdleTimerLocked()
	gen := p.gen
	d.steer.idleTimer = time.AfterFunc(ccIdleShutdown, func() { d.idleFired(gen) })
	return false
}

func (d *claudeCodeDriver) idleFired(gen int) {
	d.sm.Lock()
	p := d.steer.proc
	if d.steer.closed || p == nil || p.gen != gen || !d.isIdleLocked(p) {
		d.sm.Unlock()
		return
	}
	d.sm.Unlock()
	d.closeStdin(p)
}

// closeStdin shuts an idle process down cleanly; that exit is not a crash. A process that does not leave
// within the grace period is killed.
func (d *claudeCodeDriver) closeStdin(p *ccProc) {
	d.sm.Lock()
	if p.stdinClosed {
		d.sm.Unlock()
		return
	}
	p.stdinClosed, p.intentionalClose = true, true
	d.sm.Unlock()
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

func (d *claudeCodeDriver) stopIdleTimerLocked() { d.stopTimerLocked(&d.steer.idleTimer) }

// ── exit handling ────────────────────────────────────────────────────────────

func (d *claudeCodeDriver) procExited(ctx context.Context, p *ccProc, werr error) {
	d.sm.Lock()
	if d.steer.proc == p {
		d.steer.proc = nil
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
	}
	// Decide the fate of the messages the process never consumed while still holding the lock, and put the
	// ones that get another go back BEFORE announcing the exit, so nothing newer can overtake them.
	var retry, fail []awaitMsg
	switch {
	case shuttingDown || killed || probe:
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

	if badResume {
		d.mu.Lock()
		d.ccSessionID = "" // that conversation is gone: the retry starts a fresh one
		d.mu.Unlock()
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
			d.enqueueLegacy(a.text, a.source)
		}
		for _, m := range pending {
			d.enqueueLegacy(m.text, m.source)
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
			}
			d.emit("error", agent.ErrorPayload{Message: msg, Retryable: true})
			if wasInTurn { // a turn was under way (not merely a message that never got consumed)
				d.emit("turn_done", agent.TurnDonePayload{StopReason: "error", Model: p.model})
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
