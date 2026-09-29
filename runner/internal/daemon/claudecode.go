package daemon

// claudeCodeDriver hosts an agent session by running Claude Code headless
// (`claude -p --output-format stream-json`) instead of blerg-runner's native
// provider loop. Purpose: sessions bill to the operator's Claude subscription
// (CLAUDE_CODE_OAUTH_TOKEN from `claude setup-token`) rather than metered API
// usage. It emits the same agent-event vocabulary through the same emitter,
// so the server, blerg-board's ingest, and every UI stay unchanged.
//
// Turn model: each user message runs one `claude -p` process; the first turn
// captures Claude Code's session id and later turns pass --resume so the
// conversation continues. Claude Code's session state lives in $HOME/.claude
// inside the pod — a pod death loses the CLI-side transcript (the card and
// the server-side event log remain), so a resumed pod starts a fresh CLI
// conversation seeded by the next message.

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/agent/skills"
	"github.com/blerglab/blerg-ai/runner/internal/models"
)

// sessionDriver is the surface AgentHost needs from a session engine.
// *agent.Loop satisfies it; claudeCodeDriver is the subscription-billed
// alternative.
type sessionDriver interface {
	Start()
	Run(ctx context.Context)
	Enqueue(text, source string)
	SetModel(model, effort, source string)
	Interrupt()
	RestoreContext(events []agent.RestoredEvent)
}

type queuedMsg struct{ text, source string }

// sandboxedDriver is a sessionDriver whose engine subprocess can be routed
// into a session's sandbox container. Drivers that cannot (the native
// in-process loop, whose tools act on host paths) simply don't implement it,
// and AgentHost refuses a sandboxed spawn that would select one.
type sandboxedDriver interface {
	useSandbox(prefix sandboxExec)
}

type claudeCodeDriver struct {
	workDir string
	model   string
	effort  string // "" = Claude Code's own default for the model
	emitter agent.Emitter
	env     []string // sanitised env every `claude` turn starts from
	// prefix routes each turn through `docker exec` into the session's
	// sandbox container; empty means the turn runs on the host (the default).
	prefix sandboxExec
	queue  chan queuedMsg

	// caps reports what Claude Code loaded (its init line) as a capabilities
	// event, deduped so a per-turn re-init that changed nothing is silent.
	// skills are the SKILL.md skills the daemon found for this session: they
	// lend descriptions to the names the init line lists. home renders the
	// working directory for display.
	caps   capEmitter
	skills []skills.Skill
	home   string

	mu          sync.Mutex
	ccSessionID string    // Claude Code's own session id (--resume)
	cmd         *exec.Cmd // in-flight turn, for Interrupt
}

// newClaudeCodeDriver: env is the environment for every `claude` subprocess;
// nil means sanitizedEnviron() (never the raw daemon env, which would carry
// the master token).
func newClaudeCodeDriver(workDir, model, effort string, emitter agent.Emitter, env []string) *claudeCodeDriver {
	if env == nil {
		env = sanitizedEnviron()
	}
	return &claudeCodeDriver{
		workDir: workDir, model: model, effort: effort, emitter: emitter, env: env,
		queue: make(chan queuedMsg, 64),
	}
}

func ccUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (d *claudeCodeDriver) emit(kind string, payload any) {
	d.emitter.Emit(agent.Event{
		ClientEventID: ccUUID(), Ts: time.Now(), Kind: kind, Payload: payload,
	})
}

func (d *claudeCodeDriver) useSandbox(prefix sandboxExec) { d.prefix = prefix }

func (d *claudeCodeDriver) Start() {}

func (d *claudeCodeDriver) Enqueue(text, source string) {
	if source == "" {
		source = "chat"
	}
	select {
	case d.queue <- queuedMsg{text: text, source: source}:
	default:
		d.emit("error", agent.ErrorPayload{Message: "message queue full — dropped", Retryable: true})
	}
}

// SetModel changes the model and/or effort from the next turn on. Both come
// from the UI or a /model, /effort chat command and become `claude` arguments,
// so a value that fails validation is ignored (and said so in the transcript)
// rather than applied. A change is reported as model_changed, which the server
// mirrors onto the session row.
func (d *claudeCodeDriver) SetModel(model, effort, source string) {
	if !models.ValidModelFor(claudeEngineSpec.ID, model) {
		d.emit("error", agent.ErrorPayload{Message: fmt.Sprintf("ignored invalid model %q", model), Retryable: true})
		return
	}
	if !models.ValidEffortFor(claudeEngineSpec.ID, effort) {
		d.emit("error", agent.ErrorPayload{Message: fmt.Sprintf("ignored invalid effort %q (want one of %s)", effort, strings.Join(models.EffortsFor(claudeEngineSpec.ID), ", ")), Retryable: true})
		return
	}
	d.mu.Lock()
	if model != "" {
		d.model = model
	}
	if effort != "" {
		d.effort = effort
	}
	d.effort = reconcileEffort(d.model, d.effort)
	cur := agent.ModelChangedPayload{Model: d.model, Effort: d.effort, Source: source}
	d.mu.Unlock()
	if model != "" || effort != "" {
		d.emit("model_changed", cur)
	}
}

// reconcileEffort keeps effort valid for model as far as the built-in Claude
// list knows it: an effort the model does not offer becomes the model's
// default ("" for a model with no effort levels, e.g. Haiku). A model the
// list does not know (an alias, a newer id) keeps the effort as is.
func reconcileEffort(model, effort string) string {
	efforts, def, ok := models.BuiltinModelInfo(model)
	if !ok || effort == "" {
		return effort
	}
	for _, e := range efforts {
		if e == effort {
			return effort
		}
	}
	return def
}

func (d *claudeCodeDriver) Interrupt() {
	d.mu.Lock()
	cmd, prefix := d.cmd, d.prefix
	d.mu.Unlock()
	interruptTurn(prefix, cmd)
}

func (d *claudeCodeDriver) RestoreContext(events []agent.RestoredEvent) {
	// The CLI-side conversation cannot be rebuilt from the server transcript;
	// context arrives with the next message. Not an error — log and move on.
	log.Printf("claude-code driver: restore requested (%d events) — CLI resumes fresh", len(events))
}

func (d *claudeCodeDriver) Run(ctx context.Context) {
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

// ── stream-json shapes (the subset we read) ──────────────────────────────────

type ccLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
	IsError   bool   `json:"is_error"`
	Usage     struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
	Message struct {
		Model   string `json:"model"`
		Content []struct {
			Type      string          `json:"type"`
			Text      string          `json:"text"`
			ID        string          `json:"id"`
			Name      string          `json:"name"`
			Input     json.RawMessage `json:"input"`
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
			IsError   bool            `json:"is_error"`
		} `json:"content"`
	} `json:"message"`
}

// ccTurnArgs is one headless turn's `claude` argument list. model/effort go
// through modelEffortArgs, so an invalid value never reaches it.
func ccTurnArgs(text, model, effort, resumeID string) []string {
	args := []string{"-p", text, "--output-format", "stream-json", "--verbose",
		"--dangerously-skip-permissions"}
	args = append(args, modelEffortArgs(claudeEngineSpec, model, effort)...)
	if resumeID != "" {
		args = append(args, "--resume", resumeID)
	}
	return args
}

func (d *claudeCodeDriver) runTurn(ctx context.Context, text string) {
	d.mu.Lock()
	args := ccTurnArgs(text, d.model, d.effort, d.ccSessionID)
	cmd := d.prefix.command(ctx, d.workDir, "claude", args...)
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
		d.emit("error", agent.ErrorPayload{Message: "claude-code pipe: " + err.Error()})
		return
	}
	cmd.Stderr = cmd.Stdout // interleave; stream-json is line-delimited on stdout only in practice
	if err := cmd.Start(); err != nil {
		d.emit("error", agent.ErrorPayload{Message: "claude-code start: " + err.Error()})
		return
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	sawResult := false
	lastNoise := "" // last non-JSON line: the only clue when the turn dies
	var turnUsage agent.Usage
	for sc.Scan() {
		line := sc.Bytes()
		var ev ccLine
		if json.Unmarshal(line, &ev) != nil {
			if t := strings.TrimSpace(string(line)); t != "" {
				lastNoise = t
			}
			continue // stderr noise or partial line
		}
		switch ev.Type {
		case "system":
			if ev.Subtype == "init" && ev.SessionID != "" {
				d.mu.Lock()
				d.ccSessionID = ev.SessionID
				d.mu.Unlock()
			}
			if ev.Subtype == "init" {
				if p, ok := claudeInitCapabilities(line, d.skills, d.workDir, d.home); ok {
					d.caps.emit(d.emitter, p)
				}
			}
		case "assistant":
			for _, block := range ev.Message.Content {
				switch block.Type {
				case "text":
					if block.Text != "" {
						d.emit("assistant_text", agent.AssistantTextPayload{Text: block.Text, Done: true})
					}
				case "tool_use":
					d.emit("tool_call", agent.ToolCallPayload{
						Tool: block.Name, CallID: block.ID, Input: block.Input,
					})
				}
			}
		case "user":
			for _, block := range ev.Message.Content {
				if block.Type == "tool_result" {
					out := decodeToolResultContent(block.Content)
					if len(out) > 4000 {
						out = out[:4000] + "…"
					}
					d.emit("tool_result", agent.ToolResultPayload{
						CallID: block.ToolUseID, Output: out, IsError: block.IsError,
					})
				}
			}
		case "result":
			sawResult = true
			turnUsage = agent.Usage{
				InputTokens:      ev.Usage.InputTokens,
				OutputTokens:     ev.Usage.OutputTokens,
				CacheReadTokens:  ev.Usage.CacheReadInputTokens,
				CacheWriteTokens: ev.Usage.CacheCreationInputTokens,
			}
			if ev.IsError {
				// Retryable: this is a TURN outcome (the model could not
				// finish), not a driver failure. The session is alive and
				// takes the next message; only non-retryable errors end a
				// session, and "error" is terminal server-side.
				d.emit("error", agent.ErrorPayload{Message: firstN(ev.Result, 2000), Retryable: true})
			}
			if ev.SessionID != "" {
				d.mu.Lock()
				d.ccSessionID = ev.SessionID
				d.mu.Unlock()
			}
		}
	}
	err = cmd.Wait()
	stop := "end_turn"
	if err != nil && ctx.Err() == nil {
		stop = "error"
		if !sawResult {
			// No result line: the engine never ran to an answer. Say why as far
			// as we can — for a sandboxed turn this is where docker's own
			// "no such container" lands, and without it the turn would end
			// silently empty.
			d.emit("error", agent.ErrorPayload{
				Message:   "claude-code exited: " + err.Error() + turnFailureDetail(lastNoise),
				Retryable: true,
			})
		}
	}
	d.emit("turn_done", agent.TurnDonePayload{StopReason: stop, Model: d.model, Usage: turnUsage})
}

// turnFailureDetail renders the last unparsed output line as a suffix for an
// engine-exit error, or "" when there was none.
func turnFailureDetail(noise string) string {
	if noise == "" {
		return ""
	}
	return " — " + firstN(noise, 500)
}

func firstN(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// decodeToolResultContent turns a tool_result content value into plain text.
// The wire value is either a JSON string or an array of content blocks;
// passing the raw JSON through (as string(raw)) leaks quoting and \n escapes
// into the event payload, which breaks anything downstream that reads it.
func decodeToolResultContent(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == "text" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}
