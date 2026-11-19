package topics

import (
	"encoding/json"
	"testing"
	"time"
)

func TestStructuredFailureMark(t *testing.T) {
	var in MarkInput
	if err := json.Unmarshal([]byte(`{"kind":"failure","text":"Gate failed","card":368,"type":"gate","severity":"high","suite":"board tests","rerun_passed":true,"at":"2026-10-01T12:00:00Z"}`), &in); err != nil {
		t.Fatal(err)
	}
	p, err := CleanMark(in, "agent", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	var got map[string]any
	_ = json.Unmarshal(raw, &got)
	for _, k := range []string{"type", "severity", "suite", "rerun_passed"} {
		if got[k] == nil {
			t.Errorf("missing %s", k)
		}
	}
	id := MarkEventID("s", p)
	_ = json.Unmarshal([]byte(`{"rerun_passed":false}`), &in)
	p2, err := CleanMark(in, "agent", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if id == MarkEventID("s", p2) {
		t.Fatal("rerun result deduplicated")
	}
	for _, bad := range []string{`{"type":"unknown"}`, `{"type":"gate","severity":"urgent"}`, `{"type":"gate","severity":"low","findings":["high"]}`} {
		var v MarkInput
		_ = json.Unmarshal([]byte(`{"kind":"failure","text":"Failed","card":368,"suite":"checks"}`), &v)
		_ = json.Unmarshal([]byte(bad), &v)
		if _, err := CleanMark(v, "agent", time.Now()); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}
