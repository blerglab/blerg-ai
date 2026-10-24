package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type initialPlanSender struct {
	host     *AgentHost
	requests int
}

func (s *initialPlanSender) Send(v any) error {
	req := v.(protocol.PlanInputRequest)
	s.requests++
	raw, _ := json.Marshal(protocol.PlanInputResult{Type: "plan_input_result", SessionID: req.SessionID, RequestID: req.RequestID, PlanNudge: "Held fact"})
	s.host.DeliverPlanInput(raw)
	return nil
}
func TestInitialPlanMetadataNeverQueuesAnotherInput(t *testing.T) {
	s := &initialPlanSender{}
	h := &AgentHost{sender: s, cfg: AgentHostConfig{PlanNudges: func() bool { return true }}}
	s.host = h
	if got := h.initialPlanNudge(context.Background(), "session", agent.Input{Text: "Resume", ClientID: "resume"}); got != "Held fact" || s.requests != 1 {
		t.Fatalf("metadata=%q requests=%d", got, s.requests)
	}
	h.cfg.PlanNudges = func() bool { return false }
	if got := h.initialPlanNudge(context.Background(), "session", agent.Input{Text: "Resume"}); got != "" || s.requests != 1 {
		t.Fatal("old server received a metadata request")
	}
}
