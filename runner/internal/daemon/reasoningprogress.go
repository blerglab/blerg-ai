package daemon

// reasoningprogress.go: "Thinking… N tokens" for Hermes. A count is shown only where the engine
// reports one natively, and only for the thinking phase it belongs to: Hermes's counters are
// lifetime totals, so each phase subtracts the counter as it stood when the phase began, and every
// phase begins with a fresh thinking_started (the server and the browser reset the scope on it).
//
// Claude and Codex show the elapsed clock instead. Neither reports reasoning tokens while a
// reasoning block is open: Claude's message_delta (output_tokens for the whole message, not the
// reasoning alone) arrives after the thinking block's content_block_stop, and Codex's
// thread/tokenUsage/updated arrives after the response completes, so its "last" counts belong to a
// request that has already ended.

import (
	"encoding/json"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
)

// reasoningProgressEvery coalesces the live count: at most one thinking_progress per scope per
// interval (a slow local model can report many frames a second). A var so tests can lower it.
var reasoningProgressEvery = time.Second

// Counts are native observations, never an estimate from characters or chunks.
func validReasoningTokens(n *int) bool { return n != nil && *n >= 0 && int64(*n) <= 1<<40 }

// hermesReasoning is the counting state of one scope (the main agent, or one native child).
type hermesReasoning struct {
	total    int  // the native counter as last reported (a lifetime total)
	seen     bool // total holds a report
	base     int  // total when the current thinking phase began
	thinking bool // a thinking phase is open
	lastEmit time.Time
}

// begin opens a thinking phase: the count starts again from the counter as it stands now.
func (s *hermesReasoning) begin() {
	s.thinking = true
	s.base = s.total
	s.lastEmit = time.Time{}
}

// observe records a reported counter and returns this phase's tokens to show, if any are due.
func (s *hermesReasoning) observe(n *int, now time.Time) (int, bool) {
	if !validReasoningTokens(n) {
		return 0, false
	}
	if s.seen && *n < s.total {
		// The native counter reset (an agent rebuild): count from the reset, never a negative.
		s.base = 0
	}
	s.total, s.seen = *n, true
	if !s.thinking || s.total < s.base {
		return 0, false
	}
	if !s.lastEmit.IsZero() && now.Sub(s.lastEmit) < reasoningProgressEvery {
		return 0, false
	}
	s.lastEmit = now
	return s.total - s.base, true
}

// startThinking opens the main agent's thinking phase (once per phase) and says so.
func (r *hermesRun) startThinking() {
	if r.thinking {
		return
	}
	r.thinking = true
	r.reasoning.begin()
	r.d.emit(agent.KindThinkingStarted, agent.ThinkingPayload{})
}

// endThinking notes that the main agent's thinking phase is over (text or the turn's end).
func (r *hermesRun) endThinking() {
	r.thinking = false
	r.reasoning.thinking = false
}

func (r *hermesRun) reasoningProgress(raw json.RawMessage) {
	var p struct {
		Reasoning *int `json:"reasoning"`
		Tokens    *int `json:"reasoning_tokens"`
	}
	if len(raw) > 64<<10 || json.Unmarshal(raw, &p) != nil {
		return
	}
	if p.Reasoning == nil {
		p.Reasoning = p.Tokens
	}
	// Idle reports still move the counter, so the next phase starts from the right place.
	n, due := r.reasoning.observe(p.Reasoning, time.Now())
	if due && r.active && r.thinking {
		r.d.emit(agent.KindThinkingProgress, agent.ThinkingPayload{Tokens: &n})
	}
}

// Native subagent events already carry their established display identity. This
// only observes their reasoning; it neither creates nor changes a child run. A child reports its
// output_tokens as a running total; while it thinks, what it outputs is its reasoning, so the
// phase's count is the total less the total when the phase began.
func (r *hermesRun) childReasoning(kind string, raw json.RawMessage) {
	var p struct {
		ID     string `json:"subagent_id"`
		Output *int   `json:"output_tokens"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	child, ok := r.children[p.ID]
	if !ok {
		return
	}
	if r.childReasoningState == nil {
		r.childReasoningState = map[string]*hermesReasoning{}
	}
	s := r.childReasoningState[p.ID]
	if s == nil {
		if len(r.childReasoningState) >= 256 {
			return
		}
		s = &hermesReasoning{}
		r.childReasoningState[p.ID] = s
	}
	if kind == "subagent.complete" || kind == "subagent.tool" {
		if s.thinking {
			r.d.emit(agent.KindThinkingDone, agent.ThinkingPayload{ParentCallID: child.ChildID})
		}
		s.thinking = false
		s.observe(p.Output, time.Now())
		return
	}
	if r.childDone[p.ID] {
		return
	}
	if kind != "subagent.thinking" {
		// Progress and start frames move the counter only.
		s.observe(p.Output, time.Now())
		return
	}
	if !s.thinking {
		s.begin()
		r.d.emit(agent.KindThinkingStarted, agent.ThinkingPayload{ParentCallID: child.ChildID})
	}
	if n, due := s.observe(p.Output, time.Now()); due {
		r.d.emit(agent.KindThinkingProgress, agent.ThinkingPayload{ParentCallID: child.ChildID, Tokens: &n})
	}
}
