package daemon

import (
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// ackingSender simulates the server side of the WS: it records agent events
// and immediately acks each non-transient one back into the host, like
// HandleAgentEvent does.
type ackingSender struct {
	mu   sync.Mutex
	host *AgentHost
	evs  []protocol.AgentEvent
	seq  int64
}

func (s *ackingSender) Send(msg any) error {
	ev, ok := msg.(protocol.AgentEvent)
	if !ok {
		return nil
	}
	s.mu.Lock()
	s.evs = append(s.evs, ev)
	host := s.host
	var seq int64
	if !ev.Transient {
		s.seq++
		seq = s.seq
	}
	s.mu.Unlock()
	if !ev.Transient && host != nil {
		// Ack from a separate goroutine like a real WS read loop would.
		go host.HandleAck(ev.SessionID, ev.ClientEventID)
		_ = seq
	}
	return nil
}

func (s *ackingSender) kindSeq(kind string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, ev := range s.evs {
		if ev.Kind == kind {
			n++
		}
	}
	return n
}

func TestEndToEndAgentSession(t *testing.T) {
	// Script: turn 1 = check_in + bash; then completion check-in; done.
	provider := &scriptedAgentProvider{scripts: [][]agent.StreamEvent{
		{
			{Kind: "block", Block: &agent.Block{Type: "tool_use", ID: "c1", Name: "check_in",
				Input: json.RawMessage(`{"phase":"task_start","summary":"listing files"}`)}},
			{Kind: "block", Block: &agent.Block{Type: "tool_use", ID: "b1", Name: "bash",
				Input: json.RawMessage(`{"command":"echo e2e-ok"}`)}},
			{Kind: "done", StopReason: "tool_use", Usage: &agent.Usage{InputTokens: 10, OutputTokens: 5}},
		},
		{
			{Kind: "block", Block: &agent.Block{Type: "tool_use", ID: "c2", Name: "check_in",
				Input: json.RawMessage(`{"phase":"completion","summary":"echoed"}`)}},
			{Kind: "done", StopReason: "tool_use", Usage: &agent.Usage{}},
		},
		{
			{Kind: "text_delta", TextDelta: "all done"},
			{Kind: "block", Block: &agent.Block{Type: "text", Text: "all done"}},
			{Kind: "done", StopReason: "end_turn", Usage: &agent.Usage{}},
		},
	}}

	sender := &ackingSender{}
	reposRoot := t.TempDir()
	if err := mkdirAllT(reposRoot + "/proj"); err != nil {
		t.Fatal(err)
	}
	host := NewAgentHost(sender, AgentHostConfig{
		ReposRoot: reposRoot, HomeDir: t.TempDir(), Provider: provider,
	})
	sender.host = host

	host.Spawn(protocol.SpawnSession{SessionID: "e2e", Repo: "proj", Kind: "agent", InitialPrompt: "run echo"})

	// Wait for the turn to complete.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if sender.kindSeq("turn_done") >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if sender.kindSeq("turn_done") == 0 {
		t.Fatal("turn never completed")
	}

	// Check-ins and tool activity made it onto the wire.
	if sender.kindSeq("check_in") != 2 {
		t.Fatalf("check_in events = %d", sender.kindSeq("check_in"))
	}
	if sender.kindSeq("tool_call") < 3 || sender.kindSeq("tool_result") < 3 {
		t.Fatalf("tool events: calls=%d results=%d", sender.kindSeq("tool_call"), sender.kindSeq("tool_result"))
	}

	// bash actually ran in the workspace.
	sender.mu.Lock()
	sawEcho := false
	for _, ev := range sender.evs {
		if ev.Kind == "tool_result" {
			var p agent.ToolResultPayload
			if json.Unmarshal(ev.Payload, &p) == nil && containsStr(p.Output, "e2e-ok") {
				sawEcho = true
			}
		}
	}
	sender.mu.Unlock()
	if !sawEcho {
		t.Fatal("bash output not in transcript")
	}

	// All non-transient events acked → pending drains to zero.
	sess := host.get("e2e")
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if sess.emitter.pendingCount() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := sess.emitter.pendingCount(); got != 0 {
		t.Fatalf("pending after acks = %d", got)
	}

	// Status ends idle.
	if host.States()["e2e"] != "idle" {
		t.Fatalf("state = %q", host.States()["e2e"])
	}

	// Kill tears down and reports session_ended.
	host.Kill("e2e")
	if host.Has("e2e") {
		t.Fatal("session survived Kill")
	}
}

func mkdirAllT(dir string) error { return os.MkdirAll(dir, 0o755) }
