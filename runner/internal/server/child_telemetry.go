package server

import (
	"encoding/json"

	"github.com/blerglab/blerg-ai/runner/internal/db"
	"github.com/blerglab/blerg-ai/runner/internal/livestate"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func childTelemetry(c *liveChild, snapshot db.ChildTelemetry) *protocol.UsageUpdatedPayload {
	c.ToolCalls = snapshot.ToolCalls
	active := c.EndedAt == nil && (c.Status == "running" || c.Status == "waiting")
	if active {
		c.Activity = "Working…"
		c.ActivitySince = snapshot.ResetAt
	}
	if c.Status == "waiting" {
		c.Activity = "Waiting for an answer"
	}
	var completed int64
	var latestTool *db.AgentEventRow
	var usage *protocol.UsageUpdatedPayload
	for i := range snapshot.Events {
		e := &snapshot.Events[i]
		at := e.Ts
		c.LastActivityAt = &at
		switch e.Kind {
		case "turn_done":
			completed = e.Seq
			usage = nil
			var report struct {
				Reported *bool `json:"usage_reported"`
			}
			_ = json.Unmarshal([]byte(e.Payload), &report)
			if report.Reported != nil && !*report.Reported {
				usage = &protocol.UsageUpdatedPayload{Scope: "turn"}
			}
			if !active {
				var p struct {
					Stop string `json:"stop_reason"`
				}
				_ = json.Unmarshal([]byte(e.Payload), &p)
				switch p.Stop {
				case "error":
					c.Activity = "Turn failed"
				case "interrupted":
					c.Activity = "Turn interrupted"
				default:
					c.Activity = "Turn complete"
				}
			}
		case "tool_call":
			latestTool = e
		case protocol.UsageUpdatedKind:
			if active && e.Seq > completed {
				if p, ok := protocol.DecodeUsageUpdated([]byte(e.Payload)); ok {
					usage = &p
				}
			}
		}
	}
	if active && c.Status != "waiting" && latestTool != nil && latestTool.Seq > max(completed, snapshot.ResetSeq) && snapshot.ToolPending {
		c.Activity = livestate.ToolActivity(json.RawMessage(latestTool.Payload))
		at := latestTool.Ts
		c.ActivitySince = &at
	}
	return usage
}
