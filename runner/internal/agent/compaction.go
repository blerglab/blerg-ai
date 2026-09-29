package agent

import (
	"context"
	"fmt"
	"strings"
)

const compactionSystem = "Summarize this agent conversation faithfully and compactly. Preserve: the task, decisions made, files changed, commands run and their outcomes, and any unresolved threads. Output only the summary."

func (l *Loop) windowTokens() int {
	if l.cfg.ContextWindowTokens > 0 {
		return l.cfg.ContextWindowTokens
	}
	return 200_000
}

func (l *Loop) compactAt() float64 {
	if l.cfg.CompactAt > 0 {
		return l.cfg.CompactAt
	}
	return 0.8
}

// maybeCompact summarizes the oldest ~60% of context when the last call's
// input tokens crossed the threshold. Turn-boundary aligned — never splits a
// tool_use/tool_result pair or strands thinking blocks. The full summary is
// emitted in the compaction event (persisted; crash recovery rebuilds as
// summary + events after it). Best-effort: failures leave context untouched.
func (l *Loop) maybeCompact(ctx context.Context, lastInputTokens int) {
	if float64(lastInputTokens) < l.compactAt()*float64(l.windowTokens()) {
		return
	}
	cut := l.turnAlignedCut(int(float64(len(l.messages)) * 0.6))
	if cut < 2 {
		return
	}
	old := l.messages[:cut]
	summaryReq := Request{
		Model: l.cfg.Model, System: compactionSystem,
		Messages: []Message{{Role: "user", Blocks: []Block{{Type: "text", Text: renderForSummary(old)}}}},
	}
	ch, err := l.callWithRetry(ctx, summaryReq)
	if err != nil {
		return // next call may still fit
	}
	var summary strings.Builder
	for ev := range ch {
		if ev.Kind == "block" && ev.Block.Type == "text" {
			summary.WriteString(ev.Block.Text)
		}
		if ev.Kind == "done" && ev.Usage != nil {
			l.addCost(*ev.Usage) // summarizer calls count against the budget
		}
	}
	if summary.Len() == 0 {
		return
	}
	replacement := Message{Role: "user", Blocks: []Block{{
		Type: "text",
		Text: "[Earlier conversation summary]\n" + summary.String(),
	}}}
	l.messages = append([]Message{replacement}, l.messages[cut:]...)
	l.emit("compaction", CompactionPayload{Summary: summary.String(), ThroughIndex: cut})
}

// turnAlignedCut returns the largest index <= want where messages[index] is a
// user message whose first block is plain text (a turn boundary — never
// between tool_use and tool_result).
// If no boundary exists at or below want (short histories), it searches
// upward, still keeping at least the current turn (last two messages).
func (l *Loop) turnAlignedCut(want int) int {
	isBoundary := func(i int) bool {
		m := l.messages[i]
		return m.Role == "user" && len(m.Blocks) > 0 && m.Blocks[0].Type == "text"
	}
	for i := min(want, len(l.messages)-1); i > 0; i-- {
		if isBoundary(i) {
			return i
		}
	}
	for i := want + 1; i <= len(l.messages)-2; i++ {
		if isBoundary(i) {
			return i
		}
	}
	return 0
}

func renderForSummary(msgs []Message) string {
	var b strings.Builder
	for _, m := range msgs {
		for _, blk := range m.Blocks {
			switch blk.Type {
			case "text":
				fmt.Fprintf(&b, "%s: %s\n", m.Role, blk.Text)
			case "tool_use":
				fmt.Fprintf(&b, "[tool_use %s] %s\n", blk.Name, string(blk.Input))
			case "tool_result":
				fmt.Fprintf(&b, "[tool_result] %s\n", blk.Content)
			}
		}
	}
	return b.String()
}
