package topics

import (
	"encoding/json"
	"testing"
	"time"
)

func TestStructuredReviewMark(t *testing.T) {
	var in MarkInput
	_ = json.Unmarshal([]byte(`{"kind":"review","text":"Review round","by":"codex","card":368,"verdict":"changes","findings":["high","low"],"target":"abcdef1","round":2,"at":"2026-10-01T12:00:00Z"}`), &in)
	p, err := CleanMark(in, "agent", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	for _, k := range []string{"verdict", "findings", "target", "round"} {
		if got[k] == nil {
			t.Errorf("missing %s in %s", k, raw)
		}
	}
	id := MarkEventID("session", p)
	_ = json.Unmarshal([]byte(`{"round":3}`), &in)
	p2, err := CleanMark(in, "agent", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if id == MarkEventID("session", p2) {
		t.Fatal("distinct review rounds deduplicated")
	}
	_ = json.Unmarshal([]byte(`{"verdict":"maybe"}`), &in)
	if _, err := CleanMark(in, "agent", time.Now()); err == nil {
		t.Fatal("invalid verdict accepted")
	}
}
