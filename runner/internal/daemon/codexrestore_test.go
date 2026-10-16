package daemon

import (
	"encoding/json"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"strings"
	"testing"
)

func TestCodexRestoreRetainsArchivedMessagesAndSettings(t *testing.T) {
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], Emitter: em})
	d.RestoreContext([]agent.RestoredEvent{{Kind: "user_message", Payload: json.RawMessage(`{"text":"original-task","source":"chat"}`)}, {Kind: "assistant_text", Payload: json.RawMessage(`{"text":"working-note","done":true}`)}, {Kind: "tool_result", Payload: json.RawMessage(`{"call_id":"call","output":"result-marker","is_error":false}`)}, {Kind: "model_changed", Payload: json.RawMessage(`{"model":"gpt-5-codex","effort":"high"}`)}})
	for _, want := range []string{"original-task", "working-note", "result-marker", `"role":"tool"`, `"source":"chat"`} {
		if !strings.Contains(d.adoptionNote, want) {
			t.Fatalf("missing archived context %q", want)
		}
	}
	if d.model != "gpt-5-codex" || d.effort != "high" {
		t.Fatal("settings were lost on pod replacement")
	}
	if !contains(em.kinds(), "error") {
		t.Fatal("new-thread context restoration must be explicit")
	}
}
func TestCodexRestoreBoundsHistoryAndPreservesInitialGoal(t *testing.T) {
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], Emitter: &collectEmitter{}})
	events := []agent.RestoredEvent{{Kind: "user_message", Payload: json.RawMessage(`{"text":"initial-goal","source":"chat"}`)}}
	for range 100 {
		raw, _ := json.Marshal(agent.AssistantTextPayload{Text: strings.Repeat("x", 32000), Done: true})
		events = append(events, agent.RestoredEvent{Kind: "assistant_text", Payload: raw})
	}
	last, _ := json.Marshal(agent.UserMessagePayload{Text: "latest-request", Source: "chat"})
	events = append(events, agent.RestoredEvent{Kind: "user_message", Payload: last})
	// A long run of tool output after the request must not evict that request.
	for range 100 {
		raw, _ := json.Marshal(agent.ToolResultPayload{Output: strings.Repeat("x", 32000), CallID: "recent-tool"})
		events = append(events, agent.RestoredEvent{Kind: "tool_result", Payload: raw})
	}
	d.RestoreContext(events)
	if len(d.adoptionNote) > codexRestoredContextLimit+2048 {
		t.Fatal("archived history exceeds bound")
	}
	if !strings.Contains(d.adoptionNote, "initial-goal") || !strings.Contains(d.adoptionNote, "latest-request") {
		t.Fatal("context limit lost initial goal or latest request")
	}
}
