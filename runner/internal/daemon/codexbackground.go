package daemon

import (
	"encoding/json"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"time"
)

type codexTerminal struct{ ItemID, ProcessID, Command string }

// The runtime's terminal inventory is authoritative: a process id on a tool
// item alone does not mean the process was left running in the background.
func (a *codexApp) backgroundTerminals() {
	interval := a.terminalPollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	known := map[string]codexTerminal{}
	defer func() {
		for id, t := range known {
			a.d.emit("background_task_done", agent.BackgroundTaskDonePayload{TaskID: id, CallID: t.ItemID, Kind: "bash", Status: "stopped", Description: t.Command, Summary: "Codex runtime disconnected"})
		}
	}()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-a.done:
			return
		case <-ticker.C:
			current := map[string]codexTerminal{}
			cursor := ""
			complete := false
			for page := 0; page < 20; page++ {
				params := map[string]any{"threadId": a.threadID(), "limit": 100}
				if cursor != "" {
					params["cursor"] = cursor
				}
				raw, err := a.call("thread/backgroundTerminals/list", params)
				if err != nil {
					// An unavailable inventory is not evidence that any process
					// stopped. Keep the last complete snapshot and retry next tick.
					break
				}
				var r struct {
					Data       []codexTerminal
					NextCursor string
				}
				if json.Unmarshal(raw, &r) != nil || r.Data == nil {
					break
				}
				for _, t := range r.Data {
					if t.ProcessID != "" {
						current[t.ProcessID] = t
					}
				}
				if r.NextCursor == "" {
					complete = true
					break
				}
				if r.NextCursor == cursor {
					break
				}
				cursor = r.NextCursor
			}
			if !complete {
				continue
			}
			for id, t := range current {
				if _, exists := known[id]; !exists {
					a.d.emit("background_task_started", agent.BackgroundTaskStartedPayload{TaskID: id, CallID: t.ItemID, Kind: "bash", Description: t.Command, Command: cutRunes(t.Command, 500), StartedAt: time.Now().Format(time.RFC3339)})
				}
			}
			for id, t := range known {
				if _, exists := current[id]; !exists {
					a.d.emit("background_task_done", agent.BackgroundTaskDonePayload{TaskID: id, CallID: t.ItemID, Kind: "bash", Status: "stopped", Description: t.Command, Summary: "No longer listed as running by Codex"})
				}
			}
			known = current
		}
	}
}
