package daemon

import (
	"context"
	"encoding/json"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Initial prompts bypass the normal server input queue (including replacement cluster pods).
// Fetch only metadata, just before queueing that same prompt; never queue another input.
func (h *AgentHost) initialPlanNudge(ctx context.Context, sessionID string, in agent.Input, clockOnly ...bool) protocol.PlanInputResult {
	if h.cfg.PlanNudges == nil || !h.cfg.PlanNudges() {
		return protocol.PlanInputResult{}
	}
	id := ccUUID()
	answer := make(chan protocol.PlanInputResult, 1)
	h.initialPlanMu.Lock()
	if h.initialPlans == nil {
		h.initialPlans = map[string]chan protocol.PlanInputResult{}
	}
	h.initialPlans[id] = answer
	h.initialPlanMu.Unlock()
	defer func() { h.initialPlanMu.Lock(); delete(h.initialPlans, id); h.initialPlanMu.Unlock() }()
	if err := h.sender.Send(protocol.PlanInputRequest{ClockOnly: len(clockOnly) > 0 && clockOnly[0], Type: "plan_input_request", SessionID: sessionID, RequestID: id, ClientID: in.ClientID, Source: in.Source}); err != nil {
		return protocol.PlanInputResult{}
	}
	timer := time.NewTimer(50 * time.Second)
	defer timer.Stop()
	select {
	case nudge := <-answer:
		return nudge
	case <-timer.C:
		return protocol.PlanInputResult{}
	case <-ctx.Done():
		return protocol.PlanInputResult{}
	}
}

func (h *AgentHost) DeliverPlanInput(raw []byte) {
	var res protocol.PlanInputResult
	if json.Unmarshal(raw, &res) != nil {
		return
	}
	h.initialPlanMu.Lock()
	ch := h.initialPlans[res.RequestID]
	h.initialPlanMu.Unlock()
	if ch != nil {
		select {
		case ch <- res:
		default:
		}
	}
}
