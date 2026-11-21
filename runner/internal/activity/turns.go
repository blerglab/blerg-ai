package activity

import (
	"encoding/json"
	"sort"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/processtelemetry"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Turn contains derived counters only, never command input or tool output.
// Seq is the terminal event sequence, or zero for the current open turn.
// Idle gaps between turns are excluded. Model time is the residual running
// interval, including engine work that has no separate instrumentation.
type Turn struct {
	Seq             int64                   `json:"turn_seq"`
	StartedAt       time.Time               `json:"started_at"`
	EndedAt         time.Time               `json:"ended_at"`
	DurationSeconds float64                 `json:"duration_seconds"`
	Outcome         string                  `json:"outcome"`
	Timing          Timing                  `json:"timing"`
	ToolCounts      map[string]int          `json:"tool_counts"`
	Tokens          *protocol.UsageCounters `json:"tokens"`
	TurnEndBasis    string                  `json:"turn_end_basis"`
	TurnEndSeconds  map[string]float64      `json:"turn_end_seconds"`
	BackgroundWaits int                     `json:"background_waits"`
}

// Turns partitions a session by observed running transitions and turn_done.
// Events are copied before sorting; replay never mutates the caller's history.
func Turns(start, end time.Time, events []Event, waits []Interval) []Turn {
	events = append([]Event(nil), events...)
	var declared []Event
	filtered := events[:0]
	for _, e := range events {
		if e.Kind == "gate_interval" || e.Kind == "process_activity" {
			declared = append(declared, e)
		} else {
			filtered = append(filtered, e)
		}
	}
	events = filtered
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].Seq != 0 && events[j].Seq != 0 {
			return events[i].Seq < events[j].Seq
		}
		return events[i].At.Before(events[j].At)
	})
	scripts := scriptSignals{}
	for i := range events {
		if events[i].Kind == "tool_call" {
			var p struct {
				Input json.RawMessage `json:"input"`
			}
			_ = json.Unmarshal(events[i].Payload, &p)
			events[i].Class = scripts.classify(p.Input)
		}
	}
	out := []Turn{}
	processClasses := map[string]string{}
	for _, e := range declared {
		if e.Kind == "process_activity" {
			var batch processtelemetry.Batch
			if json.Unmarshal(e.Payload, &batch) == nil && len(batch.Spans) <= processtelemetry.MaxSpans {
				for _, span := range batch.Spans {
					if processtelemetry.Valid(span.Program, span.Class) {
						if processClasses[span.CallID] != "tests" {
							processClasses[span.CallID] = span.Class
						}
					}
				}
			}
		}
	}
	var active []Event
	var began *time.Time
	background := map[string][]Event{}
	original := map[string]Event{}
	finish := func(stop time.Time, seq int64, outcome string, tokens *protocol.UsageCounters) {
		if began == nil {
			return
		}
		for _, e := range active {
			if e.Kind == "turn_work" {
				var p struct {
					EndedAt time.Time `json:"ended_at"`
				}
				_ = json.Unmarshal(e.Payload, &p)
				if p.EndedAt.After(stop) && !p.EndedAt.After(end) {
					stop = p.EndedAt
				}
			}
		}
		if stop.Before(*began) {
			stop = *began
		}
		timingEvents := append(append([]Event(nil), active...), declared...)
		t := Turn{TurnEndBasis: "unknown", Seq: seq, StartedAt: *began, EndedAt: stop, DurationSeconds: stop.Sub(*began).Seconds(), Outcome: outcome, Timing: Split(*began, stop, timingEvents, waits), ToolCounts: map[string]int{}, Tokens: tokens, TurnEndSeconds: map[string]float64{}}
		for _, e := range active {
			var p struct {
				CallID  string             `json:"call_id"`
				Tool    string             `json:"tool"`
				Input   json.RawMessage    `json:"input"`
				Seconds map[string]float64 `json:"seconds"`
			}
			if json.Unmarshal(e.Payload, &p) != nil {
				continue
			}
			if e.Kind == "tool_call" {
				class := Classify(p.Tool, p.Input)
				if e.Class == "tests" || e.Class == "build_lint" {
					class = e.Class
				}
				if observed := processClasses[p.CallID]; observed != "" {
					class = observed
				}
				t.ToolCounts[class]++
				if Classify(p.Tool, p.Input) == "waiting" {
					t.BackgroundWaits++
				}
			}
			if e.Kind == "turn_work" {
				for _, key := range []string{"sync", "freeze", "thaw"} {
					if n, ok := p.Seconds[key]; ok && n >= 0 {
						t.TurnEndSeconds[key] += n
						t.TurnEndBasis = "measured"
					}
				}
			}
		}
		out = append(out, t)
		began = nil
		active = nil
	}
	for _, e := range events {
		if e.At.Before(start) || e.At.After(end) {
			continue
		}
		var p struct {
			Status     string                  `json:"status"`
			StopReason string                  `json:"stop_reason"`
			Usage      *protocol.UsageCounters `json:"usage"`
			Reported   *bool                   `json:"usage_reported"`
			CallID     string                  `json:"call_id"`
			TaskID     string                  `json:"task_id"`
		}
		_ = json.Unmarshal(e.Payload, &p)
		if began == nil && e.Kind == "turn_done" && len(out) > 0 && out[len(out)-1].Outcome == "completed" {
			out[len(out)-1].Seq = e.Seq
			if p.Usage != nil && (p.Reported == nil || *p.Reported) {
				out[len(out)-1].Tokens = p.Usage
			}
			switch p.StopReason {
			case "interrupted":
				out[len(out)-1].Outcome = "stopped"
			case "error":
				out[len(out)-1].Outcome = "failed"
			}
			continue
		}
		if began == nil && (e.Kind == "status_changed" && p.Status == "running" || e.Kind == "tool_call" || e.Kind == "turn_done") {
			at := e.At
			began = &at
			for _, seed := range background {
				active = append(active, seed...)
			}
			// A tool without a running transition provides only a lower bound.
		}
		if e.Kind == "tool_call" {
			seed := e
			seed.Kind = "background_call"
			original[p.CallID] = seed
		}
		if e.Kind == "background_task_started" {
			background[p.TaskID] = []Event{e}
			if call, ok := original[p.CallID]; ok {
				background[p.TaskID] = []Event{call, e}
			}
		}
		if e.Kind == "background_task_done" {
			delete(background, p.TaskID)
		}
		if began == nil {
			continue
		}
		active = append(active, e)
		if e.Kind == "status_changed" && p.Status == "idle" {
			finish(e.At, e.Seq, "completed", nil)
			continue
		}
		if e.Kind == "turn_done" {
			outcome := "completed"
			switch p.StopReason {
			case "interrupted":
				outcome = "stopped"
			case "error":
				outcome = "failed"
			}
			if p.Reported != nil && !*p.Reported {
				p.Usage = nil
			}
			finish(e.At, e.Seq, outcome, p.Usage)
		}
	}
	finish(end, 0, "running", nil)
	return out
}
