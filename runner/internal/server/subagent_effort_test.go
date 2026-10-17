package server

import (
	"encoding/json"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestSubagentEffortSummaryUsesChildReportsAndUnknown(t *testing.T) {
	events := []db.InsightEvent{
		{Kind: "subagent_started", Payload: `{"child_id":"a","model":"child-a","effort":"low"}`},
		{Kind: "subagent_update", Payload: `{"child_id":"a","effort":"high"}`},
		{Kind: "subagent_update", Payload: `{"child_id":"a","model":"child-a"}`},
		{Kind: "subagent_done", Payload: `{"child_id":"a","model":"child-a"}`},
		{Kind: "subagent_started", Payload: `{"child_id":"b","model":"child-b","effort":"low"}`},
		{Kind: "subagent_update", Payload: `{"child_id":"b","model":"rerouted","effort":""}`},
		{Kind: "subagent_started", Payload: `{"child_id":"c","model":"legacy"}`},
		{Kind: "subagent_started", Payload: `{"child_id":"d","model":"child-d","effort":"high"}`},
		{Kind: "subagent_update", Payload: `{"child_id":"d","model":"child-d","effort":null}`},
	}
	raw, _ := json.Marshal(summariseSubagents(events))
	var out struct {
		ByEffort []countRow `json:"by_effort"`
	}
	_ = json.Unmarshal(raw, &out)
	if len(out.ByEffort) != 2 || out.ByEffort[0].Key != "unknown" || out.ByEffort[0].Count != 3 || out.ByEffort[1].Key != "high" || out.ByEffort[1].Count != 1 {
		t.Fatalf("effort summary=%s", raw)
	}
}
