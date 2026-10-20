package daemon

import (
	"encoding/json"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"testing"
)

// Sanitized native gateway shape: cumulative lifetime counters, not turn deltas.
func TestHermesLiveUsageReportsCurrentTurnWithoutBillingOrLateResurrection(t *testing.T) {
	r, em, _ := hermesSecurityRun(t)
	r.actualModel = "fixture-model"
	r.observeUsage(hermesUsage{Model: "fixture-model", Input: 100, Output: 10})
	r.begin()
	tick := hermesSecurityEvent(r.sid, "session.usage", map[string]any{"usage": map[string]any{"model": "fixture-model", "input": 150, "output": 15, "total": 165, "calls": 2}})
	r.handle(tick)
	r.handle(tick)
	var snapshots []map[string]any
	for _, e := range em.snapshot() {
		if e.Kind == "usage_updated" {
			raw, _ := json.Marshal(e.Payload)
			var p map[string]any
			_ = json.Unmarshal(raw, &p)
			snapshots = append(snapshots, p)
		}
	}
	if len(snapshots) != 2 || snapshots[0]["usage"] != nil {
		t.Fatalf("expected unknown start and one deduplicated report, got %+v", snapshots)
	}
	u, _ := snapshots[1]["usage"].(map[string]any)
	if u["input_tokens"] != float64(50) || u["output_tokens"] != float64(5) || snapshots[1]["scope"] != "turn" {
		t.Fatalf("not turn-relative: %+v", snapshots[1])
	}
	if countKind(em, "turn_done") != 0 {
		t.Fatal("progress billed a completion")
	}
	r.finish("end_turn", "")
	before := countKind(em, "usage_updated")
	r.handle(tick)
	if countKind(em, "usage_updated") != before {
		t.Fatal("late idle usage resurrected turn")
	}
	r.begin()
	if countKind(em, "usage_updated") != before+1 {
		t.Fatal("new turn did not clear snapshot")
	}
	for _, e := range em.snapshot() {
		if e.Kind == "turn_done" {
			u := e.Payload.(agent.TurnDonePayload).Usage
			if u.InputTokens != 50 || u.OutputTokens != 5 {
				t.Fatalf("double counted billing: %+v", u)
			}
		}
	}
}

func TestHermesUsageRequiresNativeReportAndPreservesIdleBaseline(t *testing.T) {
	for _, contract := range []int{6, 8} {
		t.Run(string(rune('0'+contract)), func(t *testing.T) {
			r, em, _ := hermesSecurityRun(t)
			r.contract = contract
			r.actualModel = "fixture-model"
			r.begin()
			before := countKind(em, "usage_updated")
			for _, p := range []map[string]any{{}, {"usage": nil}, {"usage": map[string]any{}}, {"usage": map[string]any{"input": nil, "output": 0}}, {"usage": map[string]any{"input": 0}}} {
				r.handle(hermesSecurityEvent(r.sid, "session.usage", p))
			}
			if countKind(em, "usage_updated") != before {
				t.Fatal("missing native counters invented reported zero")
			}
			r.handle(hermesSecurityEvent(r.sid, "session.usage", map[string]any{"usage": map[string]any{"input": 0, "output": 0}}))
			if countKind(em, "usage_updated") != before+1 {
				t.Fatal("explicit native zero not reported")
			}
			r.finish("interrupted", "")
			r.handle(hermesSecurityEvent(r.sid, "session.usage", map[string]any{"usage": map[string]any{"input": 100, "output": 10}}))
			r.begin()
			r.handle(hermesSecurityEvent(r.sid, "session.usage", map[string]any{"usage": map[string]any{"input": 120, "output": 12}}))
			r.finish("end_turn", "")
			events := em.snapshot()
			for i := len(events) - 1; i >= 0; i-- {
				if events[i].Kind == "turn_done" {
					u := events[i].Payload.(agent.TurnDonePayload).Usage
					if u.InputTokens != 20 || u.OutputTokens != 2 {
						t.Fatalf("idle lifetime report charged to next turn: %+v", u)
					}
					break
				}
			}
		})
	}
}

func TestHermesUnknownFinalUsageIsNotAReportedZero(t *testing.T) {
	for _, raw := range []string{`{"status":"complete"}`, `{"status":"complete","usage":{}}`, `{"status":"complete","usage":{"input":null,"output":0}}`} {
		r, em, _ := hermesSecurityRun(t)
		r.begin()
		r.handle(hermesSecurityEvent(r.sid, "message.complete", json.RawMessage(raw)))
		for _, e := range em.snapshot() {
			if e.Kind == "turn_done" {
				p := e.Payload.(agent.TurnDonePayload)
				if p.UsageReported == nil || *p.UsageReported {
					t.Fatal("missing final native counters claimed reported zero")
				}
			}
		}
	}
}

func TestHermesInfoRejectsMissingCountersAndReportsRealZero(t *testing.T) {
	for _, raw := range []string{`{"usage":{}}`, `{"usage":null}`, `{}`} {
		r, em, _ := hermesSecurityRun(t)
		r.begin()
		r.info(json.RawMessage(raw))
		r.finish("end_turn", "")
		for _, e := range em.snapshot() {
			if e.Kind == "turn_done" {
				p := e.Payload.(agent.TurnDonePayload)
				if p.UsageReported == nil || *p.UsageReported {
					t.Fatal("empty info invented reported usage")
				}
			}
		}
	}
	r, em, _ := hermesSecurityRun(t)
	r.begin()
	r.info(json.RawMessage(`{"usage":{"input":0,"output":0}}`))
	if countKind(em, "usage_updated") != 2 {
		t.Fatal("valid active info report was lost")
	}
}
