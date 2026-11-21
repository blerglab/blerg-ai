package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/db"
)

func TestChildTelemetryLastTurnEnd(t *testing.T) {
	end := time.Date(2026, 10, 17, 10, 1, 0, 0, time.UTC)
	c := liveChild{Status: "idle"}
	childTelemetry(&c, db.ChildTelemetry{Events: []db.AgentEventRow{
		{Seq: 1, Kind: "turn_done", Ts: end, Payload: `{}`},
		{Seq: 2, Kind: "status_changed", Ts: end.Add(time.Minute), Payload: `{}`},
	}})
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["last_turn_ended_at"] != end.Format(time.RFC3339) {
		t.Fatalf("last turn end = %v, want %s (not latest activity)", fields["last_turn_ended_at"], end.Format(time.RFC3339))
	}
}
