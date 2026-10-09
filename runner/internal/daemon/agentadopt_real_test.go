package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// TestAdoptRealCLI is a daemon restart against the real `claude` binary (a few cents of Haiku).
// Opt-in: BLERG_REAL_CLAUDE=1. One host starts a session and is "killed" mid-tool the way a
// daemon is (records frozen, the engine SIGKILLed); a second host adopts the record, and the
// same Claude conversation answers the next message knowing it was interrupted.
//
// It is how the assumptions recovery rests on are re-checked against a new CLI version: that a
// conversation killed mid-tool resumes at all, that it keeps its session id, and that its
// transcript is ~/.claude/projects/*/<id>.jsonl.
func TestAdoptRealCLI(t *testing.T) {
	if os.Getenv("BLERG_REAL_CLAUDE") != "1" {
		t.Skip("set BLERG_REAL_CLAUDE=1 to run against the real claude CLI")
	}
	t.Setenv("BLERG_CLAUDE_STEERING", "1") // the streaming engine, as a real daemon runs
	reposRoot := t.TempDir()
	workDir := filepath.Join(reposRoot, "proj")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	recDir := filepath.Join(t.TempDir(), "agents")
	newHost := func() (*AgentHost, *agentTestSender) {
		s := &agentTestSender{}
		return NewAgentHost(s, AgentHostConfig{ReposRoot: reposRoot, ClaudeCode: true, RecordsDir: recDir}), s
	}
	dump := func() string { return "" }
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s\n%s", what, dump())
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	host1, sender1 := newHost()
	host1.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "real-1", Repo: "proj", Kind: "agent", Model: "claude-haiku-4-5",
		Interaction:   protocol.InteractionInteractive,
		InitialPrompt: "Use the Bash tool to run exactly: sleep 300 . The secret word for this conversation is PLATYPUS; remember it.",
	})
	dump = func() string { return fmt.Sprintf("events: %v errors: %v", sender1.kinds(), errorTexts(sender1)) }
	waitFor("the tool call", func() bool { return sender1.countKind("tool_call") >= 1 })
	var before AgentRecord
	waitFor("the record to name the engine and the conversation", func() bool {
		rec, ok := readRecord(t, recDir, "real-1")
		before = rec
		return ok && rec.ClaudeSessionID != "" && rec.EnginePID > 0 && rec.TurnActive
	})
	if !transcriptExists(before.ClaudeSessionID) {
		t.Fatalf("no transcript named after the reported session id %s", before.ClaudeSessionID)
	}
	// The daemon dies: nothing more is recorded, and its engine goes with it.
	host1.Freeze()
	if err := syscall.Kill(before.EnginePID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if rec, _ := readRecord(t, recDir, "real-1"); !rec.TurnActive {
		t.Fatal("the engine's death cleared the cut-off turn from a frozen record")
	}

	host2, sender2 := newHost()
	if n := host2.AdoptRecorded(); n != 1 {
		t.Fatalf("adopted %d sessions", n)
	}
	t.Cleanup(func() { host2.Kill("real-1") })
	host2.UserMessage("real-1", "Do not run anything. What was the secret word, and were you interrupted? Answer in one line.", "chat")
	waitFor("the resumed turn", func() bool { return sender2.countKind("turn_done") >= 1 })

	answer := ""
	for _, ev := range sender2.agentEvents() {
		if ev.Kind == "assistant_text" {
			var p agent.AssistantTextPayload
			_ = json.Unmarshal(ev.Payload, &p)
			answer += p.Text
		}
	}
	if !strings.Contains(strings.ToUpper(answer), "PLATYPUS") {
		t.Fatalf("the resumed conversation does not remember the first one: %q (errors: %v)", answer, errorTexts(sender2))
	}
	t.Logf("resumed answer: %s", answer)
	if got := userTexts(sender2); len(got) != 1 || strings.Contains(got[0], "[system]") {
		t.Errorf("the note leaked into the transcript: %v", got)
	}
	after := waitRecord(t, recDir, "real-1", "the record after the resumed turn", func(r AgentRecord) bool { return !r.TurnActive })
	if after.ClaudeSessionID != before.ClaudeSessionID {
		t.Logf("note: the resumed conversation has a new id (%s -> %s); the record follows it", before.ClaudeSessionID, after.ClaudeSessionID)
	}
}
