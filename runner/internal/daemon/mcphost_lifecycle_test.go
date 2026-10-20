package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func hostMCPFixture(t *testing.T) (*AgentHost, *agentTestSender, string) {
	t.Helper()
	dir := fakeCodex(t)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_LOG", filepath.Join(dir, "calls.log"))
	t.Setenv("FAKE_CODEX_MCP_STATUS", "starting")
	t.Setenv("BLERG_RUNNER_MCP_PROMPT_WAIT_S", "30")
	host, sender, _ := newTestHost(t, nil, "")
	t.Cleanup(func() { host.Kill("fixture") })
	return host, sender, filepath.Join(dir, "calls.log")
}

func latestHostMCP(sender *agentTestSender) protocol.MCPStatusPayload {
	events := sender.agentEvents()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == protocol.MCPStatusKind {
			var p protocol.MCPStatusPayload
			_ = json.Unmarshal(events[i].Payload, &p)
			return p
		}
	}
	return protocol.MCPStatusPayload{}
}

func TestAgentHostMCPDiscoversBeforePromptAndManagerReleases(t *testing.T) {
	host, sender, logPath := hostMCPFixture(t)
	host.Spawn(protocol.SpawnSession{Type: "spawn_session", SessionID: "fixture", Repo: "proj", Kind: "agent", Engine: "codex"})
	deadline := time.Now().Add(15 * time.Second)
	for len(latestHostMCP(sender).Servers) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("host did not discover native MCP without a prompt")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !host.Has("fixture") || host.get("fixture").mcpState.Load() == nil {
		t.Fatal("host did not retain live tracker")
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "mcpServerStatus/list ") || strings.Contains(string(raw), "turn/start ") {
		t.Fatal("discovery must be a native status RPC without a prompt")
	}
	host.UserMessage("fixture", "first fixture prompt", "chat")
	deadline = time.Now().Add(15 * time.Second)
	for latestHostMCP(sender).Hold == nil {
		if time.Now().After(deadline) {
			t.Fatal("host first prompt bypassed MCP hold")
		}
		time.Sleep(5 * time.Millisecond)
	}
	mgr := &Manager{agents: host}
	for _, invalid := range []string{`{`, `{}`, `{"session_id":"missing"}`} {
		mgr.handleReleaseHeldPrompt([]byte(invalid))
	}
	if latestHostMCP(sender).Hold == nil {
		t.Fatal("invalid release affected a live session")
	}
	for range 2 {
		mgr.handleReleaseHeldPrompt([]byte(`{"type":"release_held_prompt","session_id":"fixture"}`))
	}
	sender.waitForKind(t, "turn_done")
	p := latestHostMCP(sender)
	if p.Hold != nil || p.Released != "person" {
		t.Fatalf("released state: %+v", p)
	}
	tracker := host.get("fixture").mcpState.Load()
	host.Kill("fixture")
	select {
	case <-tracker.done:
	case <-time.After(15 * time.Second):
		t.Fatal("killed host left MCP tracking running")
	}
	mgr.handleReleaseHeldPrompt([]byte(`{"session_id":"fixture"}`))
	if host.Has("fixture") {
		t.Fatal("release resurrected a killed session")
	}
}

func TestAgentHostMCPRestoredMetadataDoesNotConsumeFirstPrompt(t *testing.T) {
	host, sender, _ := hostMCPFixture(t)
	host.SpawnResumed(protocol.SpawnSession{Type: "spawn_session", SessionID: "fixture", Repo: "proj", Kind: "agent", Engine: "codex", InitialPrompt: "first after restore"}, []agent.RestoredEvent{{Kind: "model_changed", Payload: json.RawMessage(`{"model":"gpt-test"}`)}}, "resumed-from-wip")
	deadline := time.Now().Add(15 * time.Second)
	for latestHostMCP(sender).Hold == nil {
		for _, event := range sender.agentEvents() {
			if event.Kind == "user_message" {
				t.Fatal("metadata-only restore bypassed first-prompt hold")
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("restored unused session never entered hold")
		}
		time.Sleep(5 * time.Millisecond)
	}
	host.ReleaseHeldPrompt("fixture")
	sender.waitForKind(t, "turn_done")
}

func TestAgentHostMCPUsedRestoreDoesNotHoldLaterPrompt(t *testing.T) {
	host, sender, _ := hostMCPFixture(t)
	host.SpawnResumed(protocol.SpawnSession{Type: "spawn_session", SessionID: "fixture", Repo: "proj", Kind: "agent", Engine: "codex", InitialPrompt: "later after restore"}, []agent.RestoredEvent{{Kind: "user_message", Payload: json.RawMessage(`{"text":"earlier"}`)}, {Kind: "turn_done", Payload: json.RawMessage(`{}`)}}, "resumed-from-wip")
	sender.waitForKind(t, "turn_done")
	for _, event := range sender.agentEvents() {
		if event.Kind == protocol.MCPStatusKind {
			var p protocol.MCPStatusPayload
			_ = json.Unmarshal(event.Payload, &p)
			if p.Hold != nil {
				t.Fatal("restored used session held a later prompt")
			}
		}
	}
}

func TestMCPFirstPromptUsedNeedsConversationEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []agent.RestoredEvent
		record *adoptRequest
		want   bool
	}{
		{name: "fresh"},
		{name: "settings only", events: []agent.RestoredEvent{{Kind: "model_changed"}, {Kind: "capabilities"}, {Kind: protocol.MCPStatusKind}}},
		{name: "empty adoption", record: &adoptRequest{}},
		{name: "Codex thread allocated before input", record: &adoptRequest{rec: AgentRecord{CodexThreadID: "fixture-thread"}}},
		{name: "Claude native warmup identity only", record: &adoptRequest{rec: AgentRecord{ClaudeSessionID: "fixture-session"}}},
		{name: "native turn identity", record: &adoptRequest{rec: AgentRecord{CurrentTurnID: "fixture-turn"}}, want: true},
		{name: "restored first message", events: []agent.RestoredEvent{{Kind: "user_message"}}, want: true},
		{name: "partial restored window with assistant output", events: []agent.RestoredEvent{{Kind: "assistant_text"}}, want: true},
		{name: "restored completed turn", events: []agent.RestoredEvent{{Kind: "turn_done"}}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := mcpFirstPromptUsed(tc.events, tc.record); got != tc.want {
				t.Fatalf("first prompt used = %v, want %v", got, tc.want)
			}
		})
	}
}
