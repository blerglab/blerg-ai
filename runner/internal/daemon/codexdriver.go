package daemon

// codexDriver hosts an agent session by running OpenAI's Codex CLI headless
// (`codex exec --json`) instead of blerg-runner's native provider loop —
// mirrors claudeCodeDriver's role and structure exactly, for the same reason
// (billed to the operator's own Codex/ChatGPT login rather than a metered
// API key). It emits the same agent-event vocabulary through the same
// emitter, so the server, blerg-board's ingest, and every UI stay unchanged.
//
// Turn model: each user message runs one `codex exec` process; the first
// turn captures Codex's own thread id from the "thread.started" event, later
// turns pass `resume <thread-id>` so the conversation continues. Unlike
// Claude Code, Codex has no flag to pin a new thread's id upfront — this
// thread id lives only in this driver's memory, so a daemon restart loses it
// (matching claudeCodeDriver's own "pod death loses the CLI-side transcript"
// case: the card and server-side event log remain, but a resumed session
// starts a fresh conversation seeded by the next message).
import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

type codexDriver struct {
	workDir string
	engineModel
	emitter agent.Emitter
	env     []string // sanitised env every `codex` turn starts from
	// prefix routes each turn into the session's sandbox container; empty
	// means the turn runs on the host (see sandbox.go's sandboxExec).
	prefix sandboxExec
	queue  chan queuedMsg

	mu       sync.Mutex
	threadID string    // Codex's own thread id (for `codex exec resume`)
	cmd      *exec.Cmd // in-flight turn, for Interrupt
}

// newCodexDriver: env is the environment for every `codex` subprocess; nil
// means sanitizedEnviron() (never the raw daemon env).
func newCodexDriver(o AgentDriverOpts) *codexDriver {
	env := o.Env
	if env == nil {
		env = sanitizedEnviron()
	}
	return &codexDriver{
		workDir: o.WorkDir, engineModel: newEngineModel(o), emitter: o.Emitter, env: env,
		queue: make(chan queuedMsg, 64),
	}
}

func (d *codexDriver) emit(kind string, payload any) {
	d.emitter.Emit(agent.Event{
		ClientEventID: ccUUID(), Ts: time.Now(), Kind: kind, Payload: payload,
	})
}

func (d *codexDriver) useSandbox(prefix sandboxExec) { d.prefix = prefix }

func (d *codexDriver) Start() {}

func (d *codexDriver) Enqueue(text, source string) {
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
func (d *codexDriver) SetModel(model, effort, _ string) {
	d.mu.Lock()
	ok := d.set(model, effort)
	d.mu.Unlock()
	if !ok {
		d.emit("error", agent.ErrorPayload{Message: fmt.Sprintf("ignored invalid model %q / effort %q", model, effort), Retryable: true})
	}
}

func (d *codexDriver) Interrupt() {
	d.mu.Lock()
	cmd, prefix := d.cmd, d.prefix
	d.mu.Unlock()
	interruptTurn(prefix, cmd)
}

func (d *codexDriver) RestoreContext(events []agent.RestoredEvent) {
	// The CLI-side conversation cannot be rebuilt from the server transcript;
	// context arrives with the next message. Not an error — log and move on.
	log.Printf("codex driver: restore requested (%d events) — CLI resumes fresh", len(events))
}

func (d *codexDriver) Run(ctx context.Context) {
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

// ── `codex exec --json` event shapes (the subset we read) ────────────────────
// Captured empirically against codex-cli 0.154.0; there is no public schema
// to code against, so this may need updating against a future codex version.

type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id,omitempty"`
	Item     struct {
		ID               string `json:"id"`
		Type             string `json:"type"` // "agent_message" | "command_execution"
		Text             string `json:"text"`
		Command          string `json:"command"`
		AggregatedOutput string `json:"aggregated_output"`
		ExitCode         *int   `json:"exit_code"`
		Status           string `json:"status"` // "in_progress" | "completed" | "failed"
	} `json:"item"`
	Usage struct {
		InputTokens           int `json:"input_tokens"`
		CachedInputTokens     int `json:"cached_input_tokens"`
		CacheWriteInputTokens int `json:"cache_write_input_tokens"`
		OutputTokens          int `json:"output_tokens"`
	} `json:"usage"`
}

func (d *codexDriver) runTurn(ctx context.Context, text string) {
	d.mu.Lock()
	// A sandboxed turn must use the bare name: the host's absolute path does
	// not exist inside the image (see resolveEngineBinary).
	codexPath := resolveEngineBinary("codex", d.prefix.enabled())
	var args []string
	if d.threadID != "" {
		args = []string{"exec", "resume", d.threadID, "--json", "--skip-git-repo-check",
			"--dangerously-bypass-approvals-and-sandbox", text}
	} else {
		args = []string{"exec", "--json", "--skip-git-repo-check",
			"--dangerously-bypass-approvals-and-sandbox", text}
	}
	args = append(args, d.args()...)
	cmd := d.prefix.command(ctx, d.workDir, codexPath, args...)
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
		d.emit("error", agent.ErrorPayload{Message: "codex pipe: " + err.Error()})
		return
	}
	cmd.Stderr = cmd.Stdout // interleave; non-JSON lines (banners, warnings) are skipped below
	if err := cmd.Start(); err != nil {
		d.emit("error", agent.ErrorPayload{Message: "codex start: " + err.Error()})
		return
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	sawResult := false
	lastNoise := "" // last non-JSON line: the only clue when the turn dies
	var turnUsage agent.Usage
	for sc.Scan() {
		line := sc.Bytes()
		var ev codexEvent
		if json.Unmarshal(line, &ev) != nil {
			if t := strings.TrimSpace(string(line)); t != "" {
				lastNoise = t
			}
			continue // stderr noise, banners, or a partial line
		}
		switch ev.Type {
		case "thread.started":
			if ev.ThreadID != "" {
				d.mu.Lock()
				d.threadID = ev.ThreadID
				d.mu.Unlock()
			}
		case "item.started":
			if ev.Item.Type == "command_execution" {
				input, _ := json.Marshal(map[string]string{"command": ev.Item.Command})
				d.emit("tool_call", agent.ToolCallPayload{
					Tool: "bash", CallID: ev.Item.ID, Input: json.RawMessage(input),
				})
			}
		case "item.completed":
			switch ev.Item.Type {
			case "agent_message":
				if ev.Item.Text != "" {
					d.emit("assistant_text", agent.AssistantTextPayload{Text: ev.Item.Text, Done: true})
				}
			case "command_execution":
				out := ev.Item.AggregatedOutput
				if len(out) > 4000 {
					out = out[:4000] + "…"
				}
				isErr := ev.Item.Status != "completed" || (ev.Item.ExitCode != nil && *ev.Item.ExitCode != 0)
				d.emit("tool_result", agent.ToolResultPayload{
					CallID: ev.Item.ID, Output: out, IsError: isErr,
				})
			}
		case "turn.completed":
			sawResult = true
			turnUsage = agent.Usage{
				InputTokens:      ev.Usage.InputTokens,
				OutputTokens:     ev.Usage.OutputTokens,
				CacheReadTokens:  ev.Usage.CachedInputTokens,
				CacheWriteTokens: ev.Usage.CacheWriteInputTokens,
			}
		}
	}
	err = cmd.Wait()
	stop := "end_turn"
	if err != nil && ctx.Err() == nil {
		stop = "error"
		if !sawResult {
			// No turn.completed: say why as far as we can (a sandboxed turn's
			// docker failure lands here) instead of ending the turn silently.
			d.emit("error", agent.ErrorPayload{
				Message:   "codex exited: " + err.Error() + turnFailureDetail(lastNoise),
				Retryable: true,
			})
		}
	}
	d.emit("turn_done", agent.TurnDonePayload{StopReason: stop, Model: d.model, Usage: turnUsage})
}
