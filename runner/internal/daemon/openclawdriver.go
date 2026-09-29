package daemon

// openclawDriver hosts an agent session by running OpenClaw's CLI headless
// (`openclaw agent exec --json`) instead of blerg-runner's native provider
// loop. Mirrors codexDriver/hermesDriver's role, but exists specifically to
// reach self-hosted OpenAI-compatible inference (e.g. a local GPU box
// running vLLM, registered as a custom OpenClaw provider via `openclaw
// config patch`) rather than a CLI-native subscription login.
//
// Turn model, chosen after empirically testing both of OpenClaw's headless
// primitives against a real vLLM box:
//
//   - `openclaw agent exec "<msg>" --json` is the clean one: a small,
//     predictable system prompt and a stable JSON envelope — but it is
//     COMPLETELY STATELESS. Verified empirically: two calls against the same
//     --state-dir got different session ids and no memory of each other.
//   - `openclaw agent --local --session-id <id> --message ...` has real,
//     pinnable session continuity (better than codex/hermes — no dynamic id
//     to capture) — but ships a much heavier default system prompt (~35K
//     chars: all of OpenClaw's 40+ built-in tools, skills, bootstrap files),
//     which overflowed a 32K-context self-hosted model before the
//     conversation even started.
//
// Since the whole point of this driver is reaching small(er)-context
// self-hosted inference, this driver uses `agent exec` (the lean primitive)
// and replays a truncated in-memory transcript into each turn's prompt
// itself, rather than relying on OpenClaw's own (heavier) session state.
// The replay budget is configurable (openclawContextBudgetChars) so an
// operator with bigger hardware (a larger-context model, more or larger GPUs)
// can raise it without any code change.
import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// defaultOpenclawContextBudgetChars is a conservative default: comfortably
// under a 32K-token small self-hosted model's context (the reference target
// this was tuned against) once OpenClaw's own — much smaller than the
// --local path's — agent-exec system prompt and the new message are added
// on top. Override with BLERG_OPENCLAW_CONTEXT_CHARS for a bigger-context
// target (a larger local model, more or larger GPUs, or a hosted API).
const defaultOpenclawContextBudgetChars = 16000

// openclawContextBudgetChars resolves the configured replay budget once per
// driver instance — see BLERG_OPENCLAW_CONTEXT_CHARS in DAEMON.md.
func openclawContextBudgetChars() int {
	if raw := os.Getenv("BLERG_OPENCLAW_CONTEXT_CHARS"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
		log.Printf("openclaw driver: BLERG_OPENCLAW_CONTEXT_CHARS=%q is not a positive integer, using default %d", raw, defaultOpenclawContextBudgetChars)
	}
	return defaultOpenclawContextBudgetChars
}

type openclawTurn struct {
	role string // "user" | "assistant"
	text string
}

type openclawDriver struct {
	workDir string
	engineModel
	contextChars int
	emitter      agent.Emitter
	env          []string // sanitised env every `openclaw` turn starts from
	queue        chan queuedMsg

	mu      sync.Mutex
	history []openclawTurn
	cmd     *exec.Cmd // in-flight turn, for Interrupt
}

// newOpenclawDriver: env is the environment for every `openclaw` subprocess;
// nil means sanitizedEnviron() (never the raw daemon env).
func newOpenclawDriver(o AgentDriverOpts) *openclawDriver {
	env := o.Env
	if env == nil {
		env = sanitizedEnviron()
	}
	return &openclawDriver{
		workDir: o.WorkDir, engineModel: newEngineModel(o), contextChars: openclawContextBudgetChars(),
		emitter: o.Emitter, env: env, queue: make(chan queuedMsg, 64),
	}
}

func (d *openclawDriver) emit(kind string, payload any) {
	d.emitter.Emit(agent.Event{
		ClientEventID: ccUUID(), Ts: time.Now(), Kind: kind, Payload: payload,
	})
}

func (d *openclawDriver) Start() {}

func (d *openclawDriver) Enqueue(text, source string) {
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
func (d *openclawDriver) SetModel(model, effort, _ string) {
	d.mu.Lock()
	ok := d.set(model, effort)
	d.mu.Unlock()
	if !ok {
		d.emit("error", agent.ErrorPayload{Message: fmt.Sprintf("ignored invalid model %q / effort %q", model, effort), Retryable: true})
	}
}

func (d *openclawDriver) Interrupt() {
	d.mu.Lock()
	cmd := d.cmd
	d.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (d *openclawDriver) RestoreContext(events []agent.RestoredEvent) {
	// Rebuild the replay transcript from the server-side event log — this
	// driver's own continuity lives only in d.history (agent exec is
	// stateless), so unlike claude-code/codex a daemon restart CAN recover
	// full memory here, as long as the server transcript survived.
	d.mu.Lock()
	defer d.mu.Unlock()
	d.history = d.history[:0]
	for _, ev := range events {
		switch ev.Kind {
		case "user_message":
			var p agent.UserMessagePayload
			if json.Unmarshal(ev.Payload, &p) == nil && p.Text != "" {
				d.history = append(d.history, openclawTurn{role: "user", text: p.Text})
			}
		case "assistant_text":
			var p agent.AssistantTextPayload
			if json.Unmarshal(ev.Payload, &p) == nil && p.Done && p.Text != "" {
				d.history = append(d.history, openclawTurn{role: "assistant", text: p.Text})
			}
		}
	}
}

func (d *openclawDriver) Run(ctx context.Context) {
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

// buildPrompt renders the truncated replay transcript plus the new message.
// Truncation drops the OLDEST turns first, keeping the most recent context —
// same tradeoff as any fixed context budget. Must be called with d.mu held.
func (d *openclawDriver) buildPrompt(newText string) string {
	var b strings.Builder
	budget := d.contextChars - len(newText)
	// Walk history newest-first, keep whatever fits, then re-emit oldest-first.
	var kept []openclawTurn
	used := 0
	for i := len(d.history) - 1; i >= 0; i-- {
		t := d.history[i]
		cost := len(t.role) + len(t.text) + 4 // ~formatting overhead
		if used+cost > budget && len(kept) > 0 {
			break
		}
		kept = append(kept, t)
		used += cost
	}
	for i := len(kept) - 1; i >= 0; i-- {
		t := kept[i]
		if t.role == "user" {
			b.WriteString("User: ")
		} else {
			b.WriteString("Assistant: ")
		}
		b.WriteString(t.text)
		b.WriteString("\n\n")
	}
	if len(kept) > 0 {
		b.WriteString("User: ")
		b.WriteString(newText)
		return b.String()
	}
	return newText
}

// openclawExecEnvelope is the "stable agent-exec JSON envelope" `openclaw
// agent exec --json` promises — captured empirically against a real
// successful and a real failed run (OpenClaw 2026.9.4).
type openclawExecEnvelope struct {
	OK    bool   `json:"ok"`
	Final string `json:"final"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (d *openclawDriver) runTurn(ctx context.Context, text string) {
	d.mu.Lock()
	openclawPath := resolveEngineBinary("openclaw", false) // agent-kind never runs sandboxed today
	prompt := d.buildPrompt(text)
	args := []string{"agent", "exec", prompt, "--json", "--cwd", d.workDir}
	args = append(args, d.args()...)
	cmd := exec.CommandContext(ctx, openclawPath, args...) //nolint:gosec // openclawPath is resolveEngineBinary of a fixed engine name; the prompt is one argv element, no shell
	cmd.Dir = d.workDir
	cmd.Env = d.env
	d.cmd = cmd
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		d.cmd = nil
		d.mu.Unlock()
	}()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		d.emit("error", agent.ErrorPayload{Message: "openclaw pipe: " + err.Error()})
		d.emit("turn_done", agent.TurnDonePayload{StopReason: "error", Model: d.model})
		return
	}
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	if err := cmd.Start(); err != nil {
		d.emit("error", agent.ErrorPayload{Message: "openclaw start: " + err.Error()})
		d.emit("turn_done", agent.TurnDonePayload{StopReason: "error", Model: d.model})
		return
	}

	out := readAllLimited(stdout, 1<<20)
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return // interrupted/cancelled — no event, matches the other drivers
	}

	var env openclawExecEnvelope
	parseErr := json.Unmarshal([]byte(strings.TrimSpace(out)), &env)

	stop := "end_turn"
	switch {
	case parseErr != nil:
		// Couldn't parse the promised JSON envelope at all — fall back to
		// whatever raw signal is available.
		stop = "error"
		msg := strings.TrimSpace(out)
		if msg == "" {
			msg = strings.TrimSpace(stderrBuf.String())
		}
		if msg == "" && waitErr != nil {
			msg = waitErr.Error()
		}
		if msg == "" {
			msg = "openclaw agent exec produced no parseable output"
		}
		d.emit("error", agent.ErrorPayload{Message: "openclaw exited: " + msg, Retryable: true})
	case !env.OK:
		stop = "error"
		msg := "unknown error"
		if env.Error != nil && env.Error.Message != "" {
			msg = env.Error.Message
		}
		d.emit("error", agent.ErrorPayload{Message: "openclaw turn failed: " + msg, Retryable: true})
	case env.Final != "":
		d.emit("assistant_text", agent.AssistantTextPayload{Text: env.Final, Done: true})
		d.mu.Lock()
		d.history = append(d.history, openclawTurn{role: "user", text: text}, openclawTurn{role: "assistant", text: env.Final})
		d.mu.Unlock()
	}
	if stderr := strings.TrimSpace(stderrBuf.String()); stderr != "" {
		log.Printf("openclaw driver: session stderr: %s", stderr)
	}
	d.emit("turn_done", agent.TurnDonePayload{StopReason: stop, Model: d.model})
}
