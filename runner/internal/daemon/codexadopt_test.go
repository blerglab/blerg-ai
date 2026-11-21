package daemon

import (
	"encoding/json"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestCodexHostRecovery(t *testing.T) {
	oldTranscriptExists := codexTranscriptExists
	codexTranscriptExists = func(string) bool { return true }
	t.Cleanup(func() { codexTranscriptExists = oldTranscriptExists })
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	calls := filepath.Join(bin, "calls.log")
	t.Setenv("FAKE_CODEX_LOG", calls)
	root := t.TempDir()
	records := t.TempDir()
	newHost := func() *AgentHost {
		h := NewAgentHost(&agentTestSender{}, AgentHostConfig{ReposRoot: root, HomeDir: t.TempDir(), RecordsDir: records})
		t.Cleanup(func() {
			h.Freeze()
			for _, id := range h.ActiveIDs() {
				h.Kill(id)
			}
		})
		return h
	}
	h := newHost()
	h.Spawn(protocol.SpawnSession{SessionID: "cx1", Kind: "agent", Engine: "codex", ProjectPath: root, InitialPrompt: "first task"})
	waitUntil(t, "first Codex turn", func() bool { b, _ := os.ReadFile(calls); return len(b) > 0 })
	rec := waitRecord(t, records, "cx1", "Codex thread recorded", func(r AgentRecord) bool { return r.CodexThreadID == "cx-thread-1" && !r.TurnActive })
	h.Freeze()
	h.Kill("cx1")
	if err := writeAgentRecord(records, rec); err != nil {
		t.Fatal(err)
	}
	next := newHost()
	if n := next.AdoptRecorded(); n != 1 {
		t.Fatalf("adopted %d sessions", n)
	}
	next.UserInput("cx1", heldMessage{Text: "second task", Source: "chat"})
	waitUntil(t, "resumed Codex turn", func() bool {
		b, _ := os.ReadFile(calls)
		r, ok := readRecord(t, records, "cx1")
		return strings.Contains(string(b), "thread/resume") && ok && !r.TurnActive
	})
}

func TestCodexRecoverySaysWhenConversationIsMissing(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("CODEX_HOME", t.TempDir())
	root, records := t.TempDir(), t.TempDir()
	rec := AgentRecord{SessionID: "missing", Engine: "codex", CodexThreadID: "missing-thread", WorkDir: root, Model: "gpt-6.1-sol"}
	if err := writeAgentRecord(records, rec); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	h := NewAgentHost(sender, AgentHostConfig{ReposRoot: root, HomeDir: t.TempDir(), RecordsDir: records})
	defer func() { h.Freeze(); h.Kill("missing") }()
	if got := h.AdoptRecorded(); got != 1 {
		t.Fatalf("adopted %d sessions", got)
	}
	found := false
	for _, text := range errorTexts(sender) {
		if strings.Contains(text, "could not be restored") {
			found = true
		}
	}
	if !found {
		t.Fatal("missing conversation was reported as restored")
	}
}

func TestCodexCutoffCommandSafety(t *testing.T) {
	for _, cmd := range []string{"git push --force origin main", "rm -rf build", "systemctl --user restart example.service"} {
		call := openCallOf("command-1", "bash", []byte(`{"command":`+strconv.Quote(cmd)+`}`))
		rec := AgentRecord{Engine: "codex", TurnActive: true, OpenCalls: []OpenCall{call}}
		if cont, _ := adoptPlan(rec, true, "interactive"); cont {
			t.Fatalf("continued destructive Codex command %q", cmd)
		}
	}
	call := openCallOf("question-1", "bash", []byte(`{"command":"blerg-runner ask 'Choose a branch'"}`))
	if !call.asksPerson() {
		t.Fatal("Codex ask command not recognized as waiting on the person")
	}
}

func TestCodexRestartDuringStopKeepsQueuedInputAndDoesNotContinueStoppedTurn(t *testing.T) {
	shrink(t, &codexInterruptResultAfter, 100*time.Millisecond)
	for _, before := range []bool{false, true} {
		name := "Stop before freeze"
		if before {
			name = "Stop after freeze"
		}
		t.Run(name, func(t *testing.T) { testCodexRestartDuringStop(t, before) })
	}
}

func testCodexRestartDuringStop(t *testing.T, freezeBefore bool) {
	oldExists := codexTranscriptExists
	codexTranscriptExists = func(string) bool { return true }
	t.Cleanup(func() { codexTranscriptExists = oldExists })
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_STUCK_STOP", filepath.Join(bin, "stopped"))
	root, records := t.TempDir(), t.TempDir()
	makeHost := func() (*AgentHost, *agentTestSender) {
		sender := &agentTestSender{}
		h := NewAgentHost(sender, AgentHostConfig{ReposRoot: root, HomeDir: t.TempDir(), RecordsDir: records})
		t.Cleanup(func() { h.Freeze(); h.Kill("stopping") })
		return h, sender
	}
	h, _ := makeHost()
	h.Spawn(protocol.SpawnSession{SessionID: "stopping", Kind: "agent", Engine: "codex", ProjectPath: root, InitialPrompt: "wait for background work"})
	waitRecord(t, records, "stopping", "active turn", func(r AgentRecord) bool { return r.TurnActive && r.CodexThreadID != "" })
	if freezeBefore {
		h.Freeze()
	}
	h.Interrupt("stopping")
	in := heldMessage{Text: "queued after Stop", Source: "chat", ClientID: "queued-after-stop"}
	h.UserInput("stopping", in)
	h.Freeze()
	old := h.get("stopping")
	old.cancel()
	select {
	case <-old.runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("old worker did not stop")
	}
	raw, err := os.ReadFile(agentRecordPath(records, "stopping"))
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state["stop_requested"] != true {
		t.Fatal("Stop intent was not retained before interrupting the engine")
	}
	next, sender := makeHost()
	if n := next.AdoptRecorded(); n != 1 {
		t.Fatalf("adopted %d workers", n)
	}
	waitUntil(t, "recovery notice", func() bool { return len(userTexts(sender)) > 0 })
	rec, _ := readRecord(t, records, "stopping")
	if rec.AutoContinues != 0 {
		t.Fatal("restart automatically continued the stopped turn")
	}
	// The server resends its durable outbox after reconnect. Repeated delivery
	// of the same client ID must execute once, even across Stop and restart.
	next.UserInput("stopping", in)
	next.UserInput("stopping", in)
	waitUntil(t, "queued input delivered", func() bool {
		for _, ev := range sender.agentEvents() {
			var p agent.UserMessagePayload
			if ev.Kind == "user_message" && json.Unmarshal(ev.Payload, &p) == nil && p.ClientID == in.ClientID {
				return true
			}
		}
		return false
	})
	n := 0
	for _, ev := range sender.agentEvents() {
		var p agent.UserMessagePayload
		if ev.Kind == "user_message" && json.Unmarshal(ev.Payload, &p) == nil && p.ClientID == in.ClientID {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("queued input delivered %d times", n)
	}
}
