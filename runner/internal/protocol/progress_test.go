package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProgressBoundsAndSanitizedDisplayOnlyMetadata(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `{"completed":-1,"total":2,"label":"Work"}`, `{"completed":3,"total":2,"label":"Work"}`, `{"completed":0,"total":1000001,"label":"Work"}`, `{"completed":0,"total":2,"label":" "}`, `{"completed":0,"total":2,"label":"Work","child_id":"a","parent_call_id":"b"}`} {
		if _, ok := DecodeProgressUpdated(json.RawMessage(raw)); ok {
			t.Fatal("malformed progress accepted")
		}
	}
	p, ok := DecodeProgressUpdated(json.RawMessage(`{"completed":0,"total":1000000,"label":"Working\nnow","child_id":"child","secret":"private"}`))
	raw, _ := json.Marshal(p)
	if !ok || p.Label != "Working now" || p.ChildID != "child" || strings.Contains(string(raw), "private") {
		t.Fatal("progress bounds/attribution/sanitization lost")
	}
	if _, ok := DecodeProgressUpdated(json.RawMessage(strings.Repeat(" ", MaxProgressUpdatedBytes) + `{}`)); ok {
		t.Fatal("oversized progress decoded")
	}
}
