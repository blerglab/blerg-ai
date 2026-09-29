package agent

import (
	"encoding/json"
)

// RestoredEvent is one persisted transcript event fed back into a fresh loop
// to rebuild provider context after a crash / pod resume. Payload is the raw
// JSON as stored in agent_events.
type RestoredEvent struct {
	Kind    string
	Payload json.RawMessage
}

// RestoreContext rebuilds the in-memory provider context from persisted
// events. Only complete turns are restored (everything after the last
// turn_done is the in-flight turn, discarded — the workspace may not match
// it). The rebuild base is the latest compaction summary, if any. Unpaired
// tool_use blocks (their tool_result event was unacked at crash time) get a
// synthesized error result so the API pairing invariant holds. The last
// model_changed event restores model/effort. Host-side setup only — call
// before Run.
func (l *Loop) RestoreContext(events []RestoredEvent) {
	// Trim to the last completed turn.
	lastDone := -1
	for i, ev := range events {
		if ev.Kind == "turn_done" {
			lastDone = i
		}
	}
	// Model/effort restore scans ALL events (a mid-in-flight model change is
	// still the session's current setting).
	for _, ev := range events {
		if ev.Kind == "model_changed" {
			var p ModelChangedPayload
			if json.Unmarshal(ev.Payload, &p) == nil {
				if p.Model != "" {
					l.cfg.Model = p.Model
				}
				if p.Effort != "" {
					l.cfg.Effort = p.Effort
				}
			}
		}
	}
	if lastDone < 0 {
		return // no completed turns; fresh context
	}
	events = events[:lastDone+1]

	// Full replay from the start; compaction events are applied exactly as
	// the live loop applied them ([summary] + messages[ThroughIndex:]), so a
	// MID-TURN compaction (between provider_blocks and its tool_result) never
	// orphans tool_result blocks and the retained tail is preserved.
	var msgs []Message
	var pendingResults []Block
	flushResults := func() {
		if len(pendingResults) > 0 {
			msgs = append(msgs, Message{Role: "user", Blocks: pendingResults})
			pendingResults = nil
		}
	}
	for _, ev := range events {
		switch ev.Kind {
		case "compaction":
			var p CompactionPayload
			if json.Unmarshal(ev.Payload, &p) != nil || p.Summary == "" {
				continue
			}
			flushResults()
			cut := p.ThroughIndex
			if cut < 0 {
				cut = 0
			}
			if cut > len(msgs) {
				cut = len(msgs)
			}
			replacement := Message{Role: "user", Blocks: []Block{{
				Type: "text", Text: "[Earlier conversation summary]\n" + p.Summary,
			}}}
			msgs = append([]Message{replacement}, msgs[cut:]...)
		case "user_message":
			var p UserMessagePayload
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			// A queued mid-turn message rides in the same user message as the
			// pending tool results (matching live-loop shape).
			if len(pendingResults) > 0 {
				pendingResults = append(pendingResults, Block{Type: "text", Text: p.Text})
			} else {
				msgs = append(msgs, Message{Role: "user", Blocks: []Block{{Type: "text", Text: p.Text}}})
			}
		case "provider_blocks":
			flushResults()
			var p ProviderBlocksPayload
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			msgs = append(msgs, Message{Role: "assistant", Blocks: p.Blocks})
		case "tool_result":
			var p ToolResultPayload
			if json.Unmarshal(ev.Payload, &p) != nil {
				continue
			}
			pendingResults = append(pendingResults, Block{
				Type: "tool_result", ToolUseID: p.CallID, Content: p.Output, IsError: p.IsError,
			})
		}
	}
	flushResults()

	// Repair pairing: any tool_use without a matching tool_result gets a
	// synthesized error result appended in a user message directly after its
	// assistant message.
	resultSeen := map[string]bool{}
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == "tool_result" {
				resultSeen[b.ToolUseID] = true
			}
		}
	}
	var repaired []Message
	for _, m := range msgs {
		repaired = append(repaired, m)
		if m.Role != "assistant" {
			continue
		}
		var missing []Block
		for _, b := range m.Blocks {
			if b.Type == "tool_use" && !resultSeen[b.ID] {
				missing = append(missing, Block{
					Type: "tool_result", ToolUseID: b.ID,
					Content: "tool result lost in daemon restart", IsError: true,
				})
			}
		}
		if len(missing) > 0 {
			repaired = append(repaired, Message{Role: "user", Blocks: missing})
		}
	}

	// Enforce role alternation: synthetic in-turn user messages (e.g. the
	// completion reminder) are not persisted as events, which can leave two
	// assistant messages adjacent. Insert a neutral user bridge.
	var alternated []Message
	for _, m := range repaired {
		if len(alternated) > 0 && alternated[len(alternated)-1].Role == m.Role && m.Role == "assistant" {
			alternated = append(alternated, Message{Role: "user", Blocks: []Block{{Type: "text", Text: "[continued]"}}})
		}
		alternated = append(alternated, m)
	}
	l.messages = alternated
}
