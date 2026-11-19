package activity

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestRecordedGeneratedScriptGateShare(t *testing.T) {
	path := os.Getenv("BLERG_ACTIVITY_AUDIT")
	if path == "" {
		t.Skip("set BLERG_ACTIVITY_AUDIT to the private BLG-358 event fixture")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var input []struct {
		Seq     int64           `json:"seq"`
		At      time.Time       `json:"ts"`
		Kind    string          `json:"kind"`
		Payload json.RawMessage `json:"payload"`
	}
	if err = json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	events := []Event{}
	for _, r := range input {
		events = append(events, Event{Seq: r.Seq, At: r.At, Kind: r.Kind, Payload: r.Payload})
	}
	turns := Turns(events[0].At, events[len(events)-1].At, events, nil)
	var total, gates, model float64
	for _, turn := range turns {
		total += turn.DurationSeconds
		gates += turn.Timing.Phases["gates"]
		model += turn.Timing.Seconds["model"]
	}
	total = events[len(events)-1].At.Sub(events[0].At).Seconds()
	t.Logf("BLG-358 gates=%.2f%% model=%.2f%%", 100*gates/total, 100*model/total)
	if gates/total < .64 || gates/total > .74 {
		t.Fatalf("gate fraction %.4f; hand audit about .69", gates/total)
	}
}
