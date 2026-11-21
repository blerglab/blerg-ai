package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/persontime"

	"github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

type initialPlanSender struct {
	host      *AgentHost
	requests  int
	clockOnly bool
}

func (s *initialPlanSender) Send(v any) error {
	req := v.(protocol.PlanInputRequest)
	s.requests++
	s.clockOnly = req.ClockOnly
	raw, _ := json.Marshal(protocol.PlanInputResult{Type: "plan_input_result", SessionID: req.SessionID, RequestID: req.RequestID, PlanNudge: "Held fact", PersonTime: &persontime.Account{Timezone: "America/New_York"}})
	s.host.DeliverPlanInput(raw)
	return nil
}
func TestInitialPlanMetadataNeverQueuesAnotherInput(t *testing.T) {
	s := &initialPlanSender{}
	h := &AgentHost{sender: s, cfg: AgentHostConfig{PlanNudges: func() bool { return true }}}
	s.host = h
	if got := h.initialPlanNudge(context.Background(), "session", agent.Input{Text: "Resume", ClientID: "resume"}); got.PersonTime == nil || got.PersonTime.Timezone != "America/New_York" || got.PlanNudge != "Held fact" || s.requests != 1 {
		t.Fatalf("metadata=%+v requests=%d", got, s.requests)
	}
	if got := h.initialPlanNudge(t.Context(), "session", agent.Input{}, true); got.PersonTime == nil || !s.clockOnly || s.requests != 2 {
		t.Fatal("restart clock-only metadata was not requested")
	}
	h.cfg.PlanNudges = func() bool { return false }
	if got := h.initialPlanNudge(context.Background(), "session", agent.Input{Text: "Resume"}); got.PlanNudge != "" || s.requests != 2 {
		t.Fatal("old server received a metadata request")
	}
}
