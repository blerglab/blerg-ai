package daemon

// hermesDriver hosts an agent session by running Nous Research's Hermes
// Agent CLI headless (`hermes chat --oneshot`) instead of blerg-runner's
// native provider loop — mirrors claudeCodeDriver/codexDriver's role and
// structure, for the same reason (billed to whatever provider the operator
// configured in ~/.hermes rather than a metered blerg-runner API key).
//
// Capability gap (accepted, documented): unlike claude/codex, Hermes has no
// real-time structured event stream in headless mode — `hermes chat
// --oneshot -Q` prints only the final response text (or, on failure, an
// error message) to stdout, nothing else. So this driver never emits
// tool_call/tool_result — only assistant_text and turn_done. Token usage is
// also not tracked (would need --usage-file plumbing, not done here).
//
// Turn model: every turn runs
//
//	hermes chat -q <text> --oneshot --continue <sessionID> --create-if-missing --yolo -Q
//
// with the *same* blerg session ID as the Hermes session title on every
// call — turn 1 creates it, later turns resume it. This is simpler than
// Claude Code/Codex's driver (no dynamic id to capture from output) because
// Hermes's own `-c/--continue <name> --create-if-missing` resolves a session
// by a caller-chosen name deterministically. Verified empirically: a second
// turn against the same name sees the first turn's message in its resumed
// history. `--create-if-missing` only exists on the `chat` subcommand, not
// the plainer top-level `-z/--oneshot` (confirmed against hermes-agent
// v0.21.2 — `-z --create-if-missing` is rejected as an unrecognized flag),
// which is why this drives `chat -q ... --oneshot` rather than `-z`.
//
// Exit code is the only reliable success/error signal (0 success, nonzero
// failure); on failure the error text lands on stdout, not stderr — stderr
// carries only session bookkeeping noise ("Resumed session …", a trailing
// "session_id: …" line), so it is logged but never surfaced as assistant
// text.
import (
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

type hermesDriver struct {
	workDir string
	engineModel
	sessionID string
	emitter   agent.Emitter
	env       []string // sanitised env every `hermes` turn starts from
	// prefix routes each turn into the session's sandbox container; empty
	// means the turn runs on the host (see sandbox.go's sandboxExec).
	prefix sandboxExec
	queue  chan queuedMsg

	mu  sync.Mutex
	cmd *exec.Cmd // in-flight turn, for Interrupt
}

// newHermesDriver: env is the environment for every `hermes` subprocess; nil
// means sanitizedEnviron() (never the raw daemon env).
func newHermesDriver(o AgentDriverOpts) *hermesDriver {
	env := o.Env
	if env == nil {
		env = sanitizedEnviron()
	}
	return &hermesDriver{
		workDir: o.WorkDir, engineModel: newEngineModel(o), sessionID: o.SessionID, emitter: o.Emitter, env: env,
		queue: make(chan queuedMsg, 64),
	}
}

func (d *hermesDriver) emit(kind string, payload any) {
	d.emitter.Emit(agent.Event{
		ClientEventID: ccUUID(), Ts: time.Now(), Kind: kind, Payload: payload,
	})
}

func (d *hermesDriver) useSandbox(prefix sandboxExec) { d.prefix = prefix }

func (d *hermesDriver) Start() {}

func (d *hermesDriver) Enqueue(text, source string) {
	if source == "" {
		source = "chat"
	}
	select {
	case d.queue <- queuedMsg{text: text, source: source}:
	default:
		d.emit("error", agent.ErrorPayload{Message: "message queue full — dropped", Retryable: true})
	}
}

// SetModel applies a model/effort change from the next turn on. Invalid
// values are refused (engineModel.set) — AgentHost checks the same rules
// first; this is the driver not trusting its caller.
func (d *hermesDriver) SetModel(model, effort, _ string) {
	d.mu.Lock()
	ok := d.set(model, effort)
	d.mu.Unlock()
	if !ok {
		d.emit("error", agent.ErrorPayload{Message: fmt.Sprintf("ignored invalid model %q / effort %q", model, effort), Retryable: true})
	}
}

func (d *hermesDriver) Interrupt() {
	d.mu.Lock()
	cmd, prefix := d.cmd, d.prefix
	d.mu.Unlock()
	interruptTurn(prefix, cmd)
}

func (d *hermesDriver) RestoreContext(events []agent.RestoredEvent) {
	// Hermes's own SQLite session store (keyed by --continue's name, which is
	// this session's ID) already has the full history — nothing to replay
	// from the server transcript.
	log.Printf("hermes driver: restore requested (%d events) — hermes resumes from its own session store", len(events))
}

func (d *hermesDriver) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-d.queue:
			d.emit("user_message", agent.UserMessagePayload{Text: msg.text, Source: msg.source})
			d.emit("status_changed", agent.StatusPayload{Status: "running", Reason: "turn"})
			d.runTurn(ctx, msg.text)
			d.emit("status_changed", agent.StatusPayload{Status: "idle", Reason: "turn_done"})
		}
	}
}

func (d *hermesDriver) runTurn(ctx context.Context, text string) {
	d.mu.Lock()
	// A sandboxed turn must use the bare name: the host's absolute path does
	// not exist inside the image (see resolveEngineBinary).
	hermesPath := resolveEngineBinary("hermes", d.prefix.enabled())
	args := []string{"chat", "-q", text, "--oneshot", "--continue", d.sessionID,
		"--create-if-missing", "--yolo", "-Q"}
	args = append(args, d.args()...)
	cmd := d.prefix.command(ctx, d.workDir, hermesPath, args...)
	cmd.Env = d.env
	d.cmd = cmd
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		d.cmd = nil
		prefix := d.prefix
		d.mu.Unlock()
		// Drop the finished turn's PID file: the container could recycle that
		// PID, and a later Interrupt must not signal whatever inherits it.
		prefix.clearTurnPID()
	}()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		d.emit("error", agent.ErrorPayload{Message: "hermes pipe: " + err.Error()})
		d.emit("turn_done", agent.TurnDonePayload{StopReason: "error", Model: d.model})
		return
	}
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	if err := cmd.Start(); err != nil {
		d.emit("error", agent.ErrorPayload{Message: "hermes start: " + err.Error()})
		d.emit("turn_done", agent.TurnDonePayload{StopReason: "error", Model: d.model})
		return
	}

	out := readAllLimited(stdout, 64*1024)
	err = cmd.Wait()
	if ctx.Err() != nil {
		return // interrupted/cancelled — no event, matches claudeCodeDriver/codexDriver
	}

	text = strings.TrimSpace(out)
	stop := "end_turn"
	if err != nil {
		stop = "error"
		msg := text
		if msg == "" {
			msg = strings.TrimSpace(stderrBuf.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		d.emit("error", agent.ErrorPayload{Message: "hermes exited: " + msg, Retryable: true})
	} else if text != "" {
		d.emit("assistant_text", agent.AssistantTextPayload{Text: text, Done: true})
	}
	if stderr := strings.TrimSpace(stderrBuf.String()); stderr != "" {
		log.Printf("hermes driver: session %s stderr: %s", d.sessionID, stderr)
	}
	d.emit("turn_done", agent.TurnDonePayload{StopReason: stop, Model: d.model})
}

// readAllLimited reads r to completion (or until limit bytes), discarding
// any remainder so a runaway process can't block on a full pipe buffer.
func readAllLimited(r io.Reader, limit int) string {
	buf := make([]byte, 0, 4096)
	chunk := make([]byte, 4096)
	for {
		n, err := r.Read(chunk)
		if n > 0 && len(buf) < limit {
			room := limit - len(buf)
			if n > room {
				n = room
			}
			buf = append(buf, chunk[:n]...)
		}
		if err != nil {
			break
		}
	}
	return string(buf)
}
