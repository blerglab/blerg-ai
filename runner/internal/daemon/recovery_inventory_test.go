package daemon

import (
	"slices"
	"testing"
)

func TestRecoveryRequiredInventoryIncludesLiveAndUnadoptedRecords(t *testing.T) {
	h := NewAgentHost(&agentTestSender{}, AgentHostConfig{})
	h.sessions["marked-live"] = &agentSession{requireRecoveryReady: true}
	h.sessions["legacy-live"] = &agentSession{}
	h.adopting["marked-pending"] = &adoptingSession{rec: AgentRecord{RequireRecoveryReady: true}}
	h.adopting["marked-live"] = &adoptingSession{rec: AgentRecord{RequireRecoveryReady: true}}
	h.adopting["legacy-pending"] = &adoptingSession{}
	got := h.RecoveryRequiredSessions()
	slices.Sort(got)
	if !slices.Equal(got, []string{"marked-live", "marked-pending"}) {
		t.Fatalf("marked recovery inventory = %v", got)
	}
}
