package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPlanUpdatedValidationAndExplicitClear(t *testing.T) {
	for _, raw := range []string{`{}`, `null`, `{"items":null}`, `{"items":"bad"}`, `{"items":[{"text":"x","status":"unknown"}]}`, `{"items":[{"text":"   ","status":"pending"}]}`, `{"items":[{"text":"x","status":"pending"}],"parent_call_id":"child"}`, `{"items":[],"child_id":"child"}`} {
		if _, ok := DecodePlanUpdated(json.RawMessage(raw)); ok {
			t.Fatalf("malformed/child accepted: %s", raw)
		}
	}
	p, ok := DecodePlanUpdated(json.RawMessage(`{"items":[]}`))
	if !ok || p.Items == nil || len(p.Items) != 0 {
		t.Fatalf("explicit clear %+v %v", p, ok)
	}
	items := make([]PlanItem, MaxPlanItems+1)
	for i := range items {
		items[i] = PlanItem{Text: "x", Status: "pending"}
	}
	if _, ok := SanitizePlanUpdated(PlanUpdatedPayload{Items: items}); ok {
		t.Fatal("oversized plan accepted")
	}
}

func TestPlanUpdatedSanitizesTextOrderAndActive(t *testing.T) {
	p, ok := SanitizePlanUpdated(PlanUpdatedPayload{Items: []PlanItem{{Text: "one\u202e " + strings.Repeat("界", 300), Status: "pending", Active: "must disappear"}, {Text: "two", Status: "in_progress", Active: "Doing\n two"}, {Text: "three", Status: "completed", Active: "must disappear"}}})
	if !ok || len(p.Items) != 3 || utf8.RuneCountInString(p.Items[0].Text) > MaxPlanText || strings.ContainsRune(p.Items[0].Text, 0x202e) || p.Items[0].Active != "" || p.Items[1].Text != "two" || p.Items[1].Active != "Doing two" || p.Items[2].Active != "" {
		t.Fatalf("sanitized %+v valid=%v", p, ok)
	}
}

func TestPlanUpdatedRejectsOversizedRawBeforeDecode(t *testing.T) {
	raw := json.RawMessage(`{"items":[{"text":"` + strings.Repeat("x", MaxPlanUpdatedBytes) + `","status":"pending"}]}`)
	if _, ok := DecodePlanUpdated(raw); ok {
		t.Fatal("oversized wire snapshot accepted")
	}
}
