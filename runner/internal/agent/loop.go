package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/models"
)

// Config configures a Loop.
type Config struct {
	Provider    Provider
	Emitter     Emitter
	Registry    *Registry // tools; check_in is added internally per turn
	System      string    // system prompt
	Model       string
	Effort      string
	MaxParallel int       // read-only tool concurrency (default 4)
	Messenger   Messenger // session→user messaging (update/note/ask)

	RetryBase time.Duration    // provider retry backoff base (default 1s)
	BudgetUSD float64          // hard per-session cost cap (default 25.0)
	Pricing   map[string]Price // per-model USD per million tokens

	ContextWindowTokens int     // model window (default 200_000)
	CompactAt           float64 // compaction trigger fraction (default 0.8)
}

// Price is a model's USD cost per million tokens, by token class.
type Price struct{ InputPerM, OutputPerM, CacheReadPerM, CacheWritePerM float64 }

type queuedMsg struct{ text, source string }

// Loop is one agent session's engine. Not safe for concurrent Run calls;
// Enqueue is safe from any goroutine.
type Loop struct {
	cfg      Config
	mu       sync.Mutex
	queue    []queuedMsg
	wake     chan struct{}
	messages []Message   // provider-format context
	askCh    chan string // non-nil while an ask blocks; Enqueue routes here

	pending    *modelChange // model/effort change awaiting application
	turnBlocks []Block      // assistant blocks emitted this turn (thinking detection)

	spentUSD   float64            // accumulated session cost
	turnCancel context.CancelFunc // cancels the in-flight turn (Interrupt)
}

// Interrupt cancels the in-flight turn: provider stream, running tools, and
// blocked asks all see ctx cancellation; the turn ends with
// turn_done{StopReason: "interrupted"}. No-op when idle.
func (l *Loop) Interrupt() {
	l.mu.Lock()
	cancel := l.turnCancel
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// SpentUSD returns the session's accumulated provider cost.
func (l *Loop) SpentUSD() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.spentUSD
}

func (l *Loop) addCost(u Usage) { l.addCostForModel(l.cfg.Model, u) }

// addCostForModel accumulates spend priced for a specific model — used for
// the loop's own calls and for folding subagent spend into the parent.
func (l *Loop) addCostForModel(model string, u Usage) {
	p, ok := l.cfg.Pricing[model]
	if !ok {
		return
	}
	cost := float64(u.InputTokens)*p.InputPerM/1e6 +
		float64(u.OutputTokens)*p.OutputPerM/1e6 +
		float64(u.CacheReadTokens)*p.CacheReadPerM/1e6 +
		float64(u.CacheWriteTokens)*p.CacheWritePerM/1e6
	l.mu.Lock()
	l.spentUSD += cost
	l.mu.Unlock()
}

func (l *Loop) budget() float64 {
	if l.cfg.BudgetUSD > 0 {
		return l.cfg.BudgetUSD
	}
	return 25.0
}

var errBudget = toolError("session budget exceeded; raise the cap to continue")

type modelChange struct{ model, effort, source string }

// Start emits the bootstrap model_changed event. Host calls once at session
// start so recovery always finds a model_changed in the transcript.
func (l *Loop) Start() {
	l.emit("model_changed", ModelChangedPayload{Model: l.cfg.Model, Effort: l.cfg.Effort, Source: "start"})
}

// SetModel changes model/effort. Effective at the next provider call, unless
// the current turn has thinking blocks in context — thinking-block signatures
// are model-specific, so the switch is deferred to the next turn.
//
// The loop talks to the Anthropic API, so both values must pass Claude's
// rules (internal/models); an invalid one is refused with an error event and
// never becomes the session's model/effort (model_changed is what the server
// persists, and what a cluster resume reads back).
func (l *Loop) SetModel(model, effort, source string) {
	if !models.ValidModelFor(models.ClaudeEngine, model) || !models.ValidEffortFor(models.ClaudeEngine, effort) {
		l.emit("error", ErrorPayload{Message: fmt.Sprintf("ignored invalid model %q / effort %q", model, effort), Retryable: true})
		return
	}
	l.mu.Lock()
	l.pending = &modelChange{model, effort, source}
	l.mu.Unlock()
}

// applyPendingModel applies a pending model change. force is true at turn
// boundaries (always safe); mid-turn it is skipped while thinking blocks are
// in flight.
func (l *Loop) applyPendingModel(force bool) {
	l.mu.Lock()
	p := l.pending
	l.mu.Unlock()
	if p == nil {
		return
	}
	if !force && l.thinkingInFlight() {
		return
	}
	// Effort-only changes pass model="" — never wipe the model to empty.
	if p.model != "" {
		l.cfg.Model = p.model
	}
	if p.effort != "" {
		l.cfg.Effort = p.effort
	}
	l.mu.Lock()
	l.pending = nil
	l.mu.Unlock()
	l.emit("model_changed", ModelChangedPayload{Model: l.cfg.Model, Effort: l.cfg.Effort, Source: p.source})
}

func (l *Loop) thinkingInFlight() bool {
	for _, b := range l.turnBlocks {
		if b.Type == "thinking" {
			return true
		}
	}
	return false
}

func NewLoop(cfg Config) *Loop {
	if cfg.MaxParallel <= 0 {
		cfg.MaxParallel = 4
	}
	return &Loop{cfg: cfg, wake: make(chan struct{}, 1)}
}

// Enqueue delivers a user message. If idle, it starts a turn; if mid-turn it
// is injected at the next provider call.
func (l *Loop) Enqueue(text, source string) {
	l.mu.Lock()
	if l.askCh != nil {
		// Non-blocking send under the lock: if the ask is mid-teardown or the
		// buffer already holds an answer, fall through to the normal queue so
		// no user message is ever lost or blocks the transport goroutine.
		select {
		case l.askCh <- text:
			l.mu.Unlock()
			return
		default:
		}
	}
	l.queue = append(l.queue, queuedMsg{text, source})
	l.mu.Unlock()
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func (l *Loop) dequeue() (queuedMsg, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.queue) == 0 {
		return queuedMsg{}, false
	}
	m := l.queue[0]
	l.queue = l.queue[1:]
	return m, true
}

func (l *Loop) drainQueue() []queuedMsg {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.queue
	l.queue = nil
	return out
}

func (l *Loop) emit(kind string, payload any) { l.cfg.Emitter.Emit(newEvent(kind, payload)) }

// Run processes queued messages until ctx is cancelled. Blocks.
func (l *Loop) Run(ctx context.Context) {
	for {
		msg, ok := l.dequeue()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-l.wake:
				continue
			}
		}
		tctx, cancel := context.WithCancel(ctx)
		l.mu.Lock()
		l.turnCancel = cancel
		l.mu.Unlock()
		l.runTurn(ctx, tctx, msg)
		cancel()
		l.mu.Lock()
		l.turnCancel = nil
		l.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
	}
}

const completionReminder = `[system reminder] You made changes this turn but did not check in. Summarize what you did and its verification status via check_in(phase="completion").`

func (l *Loop) runTurn(sessionCtx, ctx context.Context, msg queuedMsg) {
	l.emit("user_message", UserMessagePayload{Text: msg.text, Source: msg.source})
	l.emit("status_changed", StatusPayload{Status: "running", Reason: "turn started"})
	l.messages = append(l.messages, Message{Role: "user", Blocks: []Block{{Type: "text", Text: msg.text}}})
	l.turnBlocks = nil
	l.applyPendingModel(true) // turn boundary — always safe

	enf := &enforcer{}
	completionDone := false
	var totalUsage Usage

	for {
		blocks, stopReason, usage, err := l.callProvider(ctx)
		if err != nil {
			if ctx.Err() != nil && sessionCtx.Err() == nil {
				// Interrupted (not shutdown): consistent transcript end.
				l.emit("turn_done", TurnDonePayload{StopReason: "interrupted", Model: l.cfg.Model, Usage: totalUsage})
				l.emit("status_changed", StatusPayload{Status: "idle", Reason: "interrupted"})
				return
			}
			if sessionCtx.Err() != nil {
				return // daemon shutdown; no transcript noise
			}
			// Retryable: a provider failure ends this TURN, not the session —
			// the loop parks at idle below and takes the next message. A
			// non-retryable error is what the daemon turns into a terminal
			// session_state_changed{error}, which would contradict that.
			l.emit("error", ErrorPayload{Message: err.Error(), Retryable: true})
			// The turn IS over, so it ends like every other turn does: the
			// transcript never shows a turn that started and never finished,
			// and anything watching for the end of a turn — a one-shot
			// session's auto_stop above all — sees this one too. The external
			// drivers (hermes, openclaw) already end a failed turn this way.
			// StopReason "error" is what distinguishes it from a turn that
			// simply ran out of things to say; the `error` event above carries
			// the reason.
			l.emit("turn_done", TurnDonePayload{StopReason: "error", Model: l.cfg.Model, Usage: totalUsage})
			l.emit("status_changed", StatusPayload{Status: "idle", Reason: "provider error"})
			return
		}
		totalUsage.InputTokens += usage.InputTokens
		totalUsage.OutputTokens += usage.OutputTokens
		totalUsage.CacheReadTokens += usage.CacheReadTokens
		totalUsage.CacheWriteTokens += usage.CacheWriteTokens

		l.messages = append(l.messages, Message{Role: "assistant", Blocks: blocks})
		l.turnBlocks = append(l.turnBlocks, blocks...)
		l.emit("provider_blocks", ProviderBlocksPayload{Blocks: blocks})
		// Cached prefix tokens are excluded from InputTokens — with prompt
		// caching the sum is the real context size.
		l.maybeCompact(ctx, usage.InputTokens+usage.CacheReadTokens+usage.CacheWriteTokens)

		if stopReason != "tool_use" {
			if enf.mutated && !completionDone && !enf.reminded {
				enf.reminded = true
				// Persist as an event so crash-recovery replay reproduces the
				// exact live message list (source "system" hides it from chat).
				l.emit("user_message", UserMessagePayload{Text: completionReminder, Source: "system"})
				l.messages = append(l.messages, Message{Role: "user", Blocks: []Block{{Type: "text", Text: completionReminder}}})
				continue
			}
			l.emit("turn_done", TurnDonePayload{
				StopReason: stopReason, Model: l.cfg.Model,
				Usage: totalUsage, CheckInMissing: enf.mutated && !completionDone,
			})
			l.emit("status_changed", StatusPayload{Status: "idle", Reason: "turn complete"})
			return
		}

		resultBlocks, sawCompletion, mutatedInStep := l.executeToolCalls(ctx, blocks, enf)
		if sawCompletion {
			completionDone = true
		} else if mutatedInStep {
			// Mutations after a completion check-in re-arm the requirement:
			// the summary must cover the turn's final state.
			completionDone = false
		}
		// Inject queued user messages alongside tool results (tool_result
		// blocks must come first in the user message — API pairing rule).
		for _, q := range l.drainQueue() {
			l.emit("user_message", UserMessagePayload{Text: q.text, Source: q.source})
			resultBlocks = append(resultBlocks, Block{Type: "text", Text: q.text})
		}
		if len(resultBlocks) > 0 {
			l.messages = append(l.messages, Message{Role: "user", Blocks: resultBlocks})
		}
	}
}

// callProvider streams one response, forwarding text deltas, and returns the
// completed blocks, stop reason, and usage.
func (l *Loop) callProvider(ctx context.Context) ([]Block, string, Usage, error) {
	l.applyPendingModel(false)
	if l.SpentUSD() >= l.budget() {
		return nil, "", Usage{}, errBudget
	}
	ch, err := l.callWithRetry(ctx, Request{
		Model: l.cfg.Model, Effort: l.cfg.Effort, System: l.cfg.System,
		Messages: l.messages, Tools: l.toolDefs(),
	})
	if err != nil {
		return nil, "", Usage{}, err
	}
	var blocks []Block
	var stop string
	var usage Usage
	var textBuf string
	for ev := range ch {
		switch ev.Kind {
		case "text_delta":
			textBuf += ev.TextDelta
			l.emit("assistant_text", AssistantTextPayload{Text: ev.TextDelta, Done: false})
		case "block":
			blocks = append(blocks, *ev.Block)
		case "done":
			stop = ev.StopReason
			if ev.Usage != nil {
				usage = *ev.Usage
			}
		case "error":
			return nil, "", usage, ev.Err
		}
	}
	if textBuf != "" {
		l.emit("assistant_text", AssistantTextPayload{Text: textBuf, Done: true})
	}
	if ctx.Err() != nil {
		return nil, "", usage, ctx.Err()
	}
	if stop == "" {
		// Stream closed without a done event (mid-stream disconnect). Do not
		// let a possibly tool_use-terminated message poison the context.
		return nil, "", usage, toolError("provider stream ended without a stop reason")
	}
	l.addCost(usage)
	return blocks, stop, usage, nil
}

// toolDefs returns registry defs plus the per-turn check_in def.
func (l *Loop) toolDefs() []ToolDef {
	defs := l.cfg.Registry.Defs()
	defs = append(defs, checkInTool{}.Def())
	return defs
}

// executeToolCalls runs the tool_use blocks of one assistant message per the
// spec's concurrency rules and returns tool_result blocks in call order,
// plus whether a completion check-in occurred. check_in and read-only tools
// run concurrently (bounded); mutating and unknown tools run serially in
// call order — concurrent workspace mutation is a data race.
func (l *Loop) executeToolCalls(ctx context.Context, blocks []Block, enf *enforcer) ([]Block, bool, bool) {
	checkIn := newCheckInTool(enf)
	var phases []string
	for _, b := range blocks {
		if b.Type == "tool_use" && b.Name == "check_in" {
			var args struct {
				Phase string `json:"phase"`
			}
			_ = json.Unmarshal(b.Input, &args)
			phases = append(phases, args.Phase)
		}
	}
	riskyWasCovered := enf.riskyCovered
	sawCompletion := contains(phases, "completion")
	askSeen := false // at most one blocking ask per assistant message (spec)
	mutatedInStep := false

	type slot struct {
		idx   int
		block Block
	}
	var calls []slot
	for i, b := range blocks {
		if b.Type == "tool_use" {
			calls = append(calls, slot{i, b})
		}
	}
	results := make([]Block, len(calls))

	run := func(s slot, i int) {
		b := s.block
		l.emit("tool_call", ToolCallPayload{Tool: b.Name, CallID: b.ID, Input: b.Input})
		start := time.Now()
		var out string
		var execErr error
		var tool Tool
		if b.Name == "check_in" {
			tool = checkIn
			var args struct{ Phase, Summary string }
			_ = json.Unmarshal(b.Input, &args)
			l.emit("check_in", CheckInPayload{Phase: args.Phase, Summary: args.Summary})
		} else if t, ok := l.cfg.Registry.Get(b.Name); ok {
			tool = t
		}
		if tool == nil {
			out, execErr = "", errUnknownTool(b.Name)
		} else if b.Name == "ask" && askSeen {
			out, execErr = "", errRejected("only one ask per message; combine your questions")
		} else if rej := enf.gate(tool, b.Input, phases); rej != "" {
			out, execErr = "", errRejected(rej)
		} else {
			if b.Name == "ask" {
				askSeen = true
			}
			out, execErr = tool.Execute(ctx, b.Input)
			if execErr == nil && tool.Mutating() {
				enf.mutated = true
				mutatedInStep = true
			}
		}
		isErr := execErr != nil
		if isErr {
			out = execErr.Error()
		}
		l.emit("tool_result", ToolResultPayload{CallID: b.ID, Output: out, IsError: isErr, DurationMs: time.Since(start).Milliseconds()})
		results[i] = Block{Type: "tool_result", ToolUseID: b.ID, Content: out, IsError: isErr}
	}

	// Concurrent pass: check_in + read-only tools.
	var wg sync.WaitGroup
	sem := make(chan struct{}, l.cfg.MaxParallel)
	var serial []int
	for i, s := range calls {
		t, known := l.cfg.Registry.Get(s.block.Name)
		// ask blocks on the user — it must not hold a concurrent pool slot.
		readOnly := s.block.Name == "check_in" || (known && !t.Mutating() && s.block.Name != "ask")
		if !readOnly {
			serial = append(serial, i)
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(s slot, i int) {
			defer wg.Done()
			defer func() { <-sem }()
			run(s, i)
		}(s, i)
	}
	wg.Wait()
	// Serial pass: mutating + unknown tools, in call order.
	for _, i := range serial {
		run(calls[i], i)
	}

	if riskyWasCovered {
		enf.riskyCovered = false // consumed by this executor step
	}
	return results, sawCompletion, mutatedInStep
}

type toolError string

func (e toolError) Error() string      { return string(e) }
func errUnknownTool(name string) error { return toolError("unknown tool: " + name) }
func errRejected(msg string) error     { return toolError(msg) }
