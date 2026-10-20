package daemon

import (
	"context"
	"encoding/json"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"time"
)

// process.list is the native session-key-filtered inventory, unlike the global
// process/agent administration endpoints. Only this read-only API is polled.
func (r *hermesRun) processInventory(ctx context.Context) <-chan json.RawMessage {
	out := make(chan json.RawMessage, 1)
	go func() {
		defer close(out)
		timer := time.NewTicker(time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			raw, err := r.rpc.call(ctx, "process.list", map[string]any{"session_id": r.sid})
			if err != nil {
				return
			}
			if len(raw) > 1<<20 {
				return
			}
			select {
			case out <- raw:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}
func (r *hermesRun) background(raw json.RawMessage) {
	var p struct {
		Processes []struct {
			ID      string `json:"session_id"`
			Command string `json:"command"`
			Status  string `json:"status"`
			Exit    *int   `json:"exit_code"`
		}
	}
	if json.Unmarshal(raw, &p) != nil || len(p.Processes) > 256 {
		return
	}
	if r.tasks == nil {
		r.tasks = map[string]agent.BackgroundTaskStartedPayload{}
	}
	for _, item := range p.Processes {
		if !nativeSessionIDPattern.MatchString(item.ID) {
			continue
		}
		old, known := r.tasks[item.ID]
		if item.Status == "running" && !known {
			if len(r.tasks) >= 256 {
				r.rpc.stop()
				r.d.emit("error", agent.ErrorPayload{Message: "Hermes background inventory limit exceeded", Retryable: true})
				return
			}
			old = agent.BackgroundTaskStartedPayload{TaskID: item.ID, Kind: "bash", Description: hermesBoundedText(item.Command, 500), Command: hermesBoundedText(item.Command, 500), StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
			r.tasks[item.ID] = old
			r.d.emit("background_task_started", old)
		}
		if item.Status == "exited" && known {
			status := "completed"
			if item.Exit == nil {
				status = "stopped"
			} else if *item.Exit != 0 {
				status = "failed"
			}
			r.d.emit("background_task_done", agent.BackgroundTaskDonePayload{TaskID: item.ID, Kind: old.Kind, Status: status, Description: old.Description})
			delete(r.tasks, item.ID)
		}
	}
	r.reportOpenWork()
}
func (r *hermesRun) reportOpenWork() {
	if r.d.onOpenWork == nil {
		return
	}
	tasks := make([]agent.BackgroundTaskStartedPayload, 0, len(r.tasks))
	for _, t := range r.tasks {
		tasks = append(tasks, t)
	}
	subs := []OpenSubagent{}
	for id, p := range r.children {
		if r.childDone[id] || !r.childStarted[id] {
			continue
		}
		subs = append(subs, OpenSubagent{ChildID: p.ChildID, AgentID: id, AgentType: p.AgentType, Model: p.Model, Summary: p.Summary, CumulativeUsage: p.Usage})
	}
	r.d.onOpenWork(tasks, subs)
}
func (r *hermesRun) closeBackground() {
	for id, p := range r.tasks {
		r.d.emit("background_task_done", agent.BackgroundTaskDonePayload{TaskID: id, Kind: p.Kind, Status: "stopped", Description: p.Description})
		delete(r.tasks, id)
	}
	r.reportOpenWork()
}
