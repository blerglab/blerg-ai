package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// The emitter takes the session's code out of every event before it leaves the daemon: a tool
// result (here a `ps` listing that shows an engine's command line), the model's text, a
// streamed delta, an error, a retyped variant.
func TestEmitterScrubsTheMarkerCode(t *testing.T) {
	sender := &agentTestSender{}
	e := newWSEmitter(sender, "s1", nil)
	e.markerNonce = testMarkerNonce
	ps := "  PID TTY      CMD\n 4242 ?        claude -p --append-system-prompt=This session's code is " + testMarkerNonce + ".\n"
	for _, ev := range []agent.Event{
		{ClientEventID: "e1", Kind: "tool_result", Payload: agent.ToolResultPayload{CallID: "t1", Output: ps}},
		{ClientEventID: "e2", Kind: "assistant_text", Payload: agent.AssistantTextPayload{Text: "The code is " + strings.ToUpper(testMarkerNonce), Done: true}},
		{ClientEventID: "e3", Kind: "assistant_text", Payload: agent.AssistantTextPayload{Text: interleaveCode(testMarkerNonce)}},
		{ClientEventID: "e4", Kind: "error", Payload: agent.ErrorPayload{Message: "failed: " + testMarkerNonce}},
		{ClientEventID: "e5", Kind: "user_message", Payload: agent.UserMessagePayload{Text: "plain", Source: "chat"}},
	} {
		ev.Ts = time.Now()
		e.Emit(ev)
	}
	evs := sender.agentEvents()
	if len(evs) != 5 {
		t.Fatalf("sent %d events", len(evs))
	}
	for _, ev := range evs {
		assertNoCode(t, string(ev.Payload))
	}
	if !strings.Contains(string(evs[0].Payload), "‹code›") || !strings.Contains(string(evs[0].Payload), "4242") {
		t.Fatalf("the tool result lost more than the code: %s", evs[0].Payload)
	}
}

func interleaveCode(s string) string { return strings.Join(strings.Split(s, ""), "\u200b") }

func assertNoCode(t *testing.T, s string) {
	t.Helper()
	var decoded any
	if json.Unmarshal([]byte(s), &decoded) == nil {
		raw, _ := json.Marshal(decoded)
		s = string(raw)
	}
	if strings.Contains(strings.ToLower(agent.NeutralizeMarkers(s, "")), testMarkerNonce) ||
		agent.ScrubCode(s, testMarkerNonce) != s {
		t.Fatalf("the marker code left the daemon: %s", s)
	}
}

// An echo of a relay that matched no message (claudestream's fallback) is shown as the relay, by
// its body and source, never as the marked block with the code.
func TestUnmatchedRelayEchoNeverCarriesTheCode(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil, withMarkerNonce(testMarkerNonce))
	d.steer.legacy = false
	p := &ccProc{}
	d.steer.proc = p
	relay := agent.Input{Text: "Review of plan.md v1: APPROVED", Source: "review"}.ModelText(testMarkerNonce)
	other := "<system-reminder>\nThe file changed. " + testMarkerNonce + "\n</system-reminder>"
	for i, text := range []string{relay, other, "hook said " + testMarkerNonce} {
		line, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}, "isReplay": true, "uuid": "u" + string(rune('a'+i))})
		d.onLine(p, line)
	}
	var got []agent.UserMessagePayload
	for _, ev := range em.snapshot() {
		if p, ok := ev.Payload.(agent.UserMessagePayload); ok {
			got = append(got, p)
		}
	}
	if len(got) != 3 || got[0].Text != "Review of plan.md v1: APPROVED" || got[0].Source != "review" || got[1].Source != "system" || got[2].Source != "chat" {
		t.Fatalf("echoes = %+v", got)
	}
	for _, p := range got {
		if strings.Contains(p.Text, testMarkerNonce) {
			t.Fatalf("an echo carried the code: %+v", p)
		}
	}
}

// The appended system prompt goes to Claude in a private file outside the workspace, not on the
// command line, and the file is removed when the session's driver stops.
func TestClaudeSystemPromptFileIsPrivateAndRemoved(t *testing.T) {
	env, logPath := installFakeClaude(t)
	em := &collectEmitter{}
	work := t.TempDir()
	d := newClaudeCodeDriver(work, "claude-opus-5", "", em, env, withMarkerNonce(testMarkerNonce))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	d.EnqueueInput(agent.Input{Text: "hello", Source: "chat"})
	waitIdle(t, em, 1)
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`--append-system-prompt-file=(\S+)`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatalf("no system prompt file: %s", raw)
	}
	path := m[1]
	if strings.HasPrefix(path, work) {
		t.Fatalf("the prompt file is in the workspace: %s", path)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("prompt file %v, err %v", st, err)
	}
	if dir, err := os.Stat(filepath.Dir(path)); err != nil || dir.Mode().Perm() != 0o700 {
		t.Fatalf("prompt directory %v, err %v", dir, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("driver did not stop")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("the prompt file outlived the session: %v", err)
	}
}

// End to end through the host: an engine whose tool runs `ps` (its own command line) and reads
// its system prompt file reports both as a tool result. The engine was told the code, and none
// of what reached the server carries it.
func TestHostKeepsTheCodeOffEverythingItSends(t *testing.T) {
	dir := t.TempDir()
	script := `#!/bin/bash
pf=""
for a in "$@"; do case "$a" in --append-system-prompt-file=*) pf="${a#*=}" ;; esac; done
content=""
[ -n "$pf" ] && content=$(cat "$pf")
python3 -I -c '
import json, sys
argv, content = sys.argv[1], sys.argv[2]
out = "  PID TTY      CMD\n 4242 ?        " + argv + "\n" + content
for line in [
  {"type":"system","subtype":"init","session_id":"cc-ps"},
  {"type":"assistant","message":{"content":[{"type":"tool_use","id":"tu_ps","name":"Bash","input":{"command":"ps -ef; cat prompt"}}]}},
  {"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu_ps","content":out,"is_error":False}]}},
  {"type":"assistant","message":{"content":[{"type":"text","text":"I read: " + content}]}},
  {"type":"result","subtype":"success","is_error":False,"session_id":"cc-ps","result":"done"},
]:
  print(json.dumps(line))
' "claude $*" "$content"
`
	if err := os.WriteFile(filepath.Join(dir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir(), ClaudeCode: true})
	host.Spawn(protocol.SpawnSession{Type: "spawn_session", SessionID: "s-ps", Repo: "proj", Kind: "agent", InitialPrompt: "hello"})
	sender.waitForKind(t, "turn_done")
	defer host.Kill("s-ps")
	code := sessionMarkerNonce(host.markerKeyDirFor(), "s-ps")
	sawResult := false
	sender.mu.Lock()
	msgs := append([]any(nil), sender.msgs...)
	sender.mu.Unlock()
	for _, m := range msgs {
		raw, _ := json.Marshal(m)
		if strings.Contains(strings.ToLower(string(raw)), code) {
			t.Fatalf("the marker code reached the server: %s", raw)
		}
		if ev, ok := m.(protocol.AgentEvent); ok && strings.Contains(string(ev.Payload), "This session's code is ‹code›") {
			sawResult = true
		}
	}
	if !sawResult {
		t.Fatal("no event showed the (scrubbed) announcement the engine read: the test proves nothing")
	}
}
