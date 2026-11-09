package daemon

import (
	"encoding/json"
	"fmt"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStopTargetsCurrentWorkAcrossEngines(t *testing.T) {
	for _, engine := range []string{"native", "claude", "claude-legacy", "codex", "hermes", "openclaw"} {
		t.Run(engine, func(t *testing.T) {
			shrink(t, &ccInterruptKillAfter, 60*time.Millisecond)
			shrink(t, &codexInterruptResultAfter, 80*time.Millisecond)
			shrink(t, &hermesInterruptResultAfter, 80*time.Millisecond)
			provider := &scriptedAgentProvider{}
			host, sender, work := newTestHost(t, provider, "")
			selected := engine
			switch engine {
			case "native":
				selected = ""
				for i := 0; i < 2; i++ {
					input, _ := json.Marshal(map[string]string{"command": fmt.Sprintf("printf ready > ready-%d; sleep 60", i)})
					provider.scripts = append(provider.scripts, []agent.StreamEvent{
						{Kind: "block", Block: &agent.Block{Type: "tool_use", ID: fmt.Sprint("check", i), Name: "check_in", Input: json.RawMessage(`{"phase":"task_start","summary":"Wait"}`)}},
						{Kind: "block", Block: &agent.Block{Type: "tool_use", ID: fmt.Sprint("wait", i), Name: "bash", Input: input}},
						{Kind: "done", StopReason: "tool_use", Usage: &agent.Usage{}},
					})
				}
			case "claude", "claude-legacy":
				t.Setenv("BLERG_CLAUDE_STEERING", "1")
				if engine == "claude-legacy" {
					t.Setenv("BLERG_CLAUDE_STEERING", "0")
					t.Setenv("FAKE_LEGACY_MS", "60000")
				}
				selected = ""
				host.cfg.Provider = nil
				host.cfg.ClaudeCode = true
				_, log := installFakeClaude(t)
				t.Setenv("GO_FAKE_CLAUDE", "1")
				t.Setenv("FAKE_LOG", log)
			case "codex":
				bin := fakeCodex(t)
				t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
				t.Setenv("FAKE_CODEX_HOLD_FIRST", "1")
				t.Setenv("FAKE_CODEX_HOLD_STEER", "1")
			case "hermes":
				fakeHermesGateway(t)
				t.Setenv("FAKE_HERMES_HOLD", "1")
				t.Setenv("FAKE_HERMES_HOLD_REDIRECT", "1")
			case "openclaw":
				bin := t.TempDir()
				t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
				if err := os.WriteFile(filepath.Join(bin, "openclaw"), []byte("#!/usr/bin/env python3\nimport time\ntime.sleep(60)\n"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			host.Spawn(protocol.SpawnSession{SessionID: "scope", Repo: "proj", Kind: "agent", Engine: selected})
			defer host.Kill("scope")
			host.UserInput("scope", agent.Input{Text: "HANG first", ClientID: "first"})
			runningTarget := func() string {
				target := ""
				for _, e := range sender.agentEvents() {
					if e.Kind == "status_changed" {
						var p agent.StatusPayload
						_ = json.Unmarshal(e.Payload, &p)
						if p.Status == "running" {
							target = e.ClientEventID
						}
					}
				}
				return target
			}
			waitUntil(t, "first active turn", func() bool { return runningTarget() != "" })
			if engine == "native" {
				waitUntil(t, "first shell", func() bool { _, err := os.Stat(filepath.Join(work, "ready-0")); return err == nil })
			}
			if engine == "claude" {
				waitUntil(t, "first tool", func() bool {
					for _, e := range sender.agentEvents() {
						if e.Kind == "tool_call" {
							return true
						}
					}
					return false
				})
			}
			if engine == "claude-legacy" {
				waitUntil(t, "legacy engine started", func() bool { return len(starts(logLines(t, os.Getenv("FAKE_LOG")))) > 0 })
			}
			old := runningTarget()
			m := &Manager{agents: host}
			stop := func(target string) {
				raw, _ := json.Marshal(map[string]string{"session_id": "scope", "target_event_id": target})
				m.handleInterruptSession(raw)
			}
			stop(old)
			host.UserInput("scope", agent.Input{Text: "HANG next", ClientID: "next"})
			waitUntil(t, "next turn active", func() bool { return runningTarget() != "" && runningTarget() != old })
			if engine == "native" {
				waitUntil(t, "next shell", func() bool { _, err := os.Stat(filepath.Join(work, "ready-1")); return err == nil })
			}
			stop(old)
			host.UserInput("scope", agent.Input{Text: "queued third", ClientID: "third"})
			stop(old)
			time.Sleep(200 * time.Millisecond)
			turns := 0
			for _, e := range sender.agentEvents() {
				if e.Kind == "turn_done" {
					turns++
				}
			}
			if turns != 1 {
				t.Fatalf("late double-click canceled queued input: %d", turns)
			}
			time.Sleep(550 * time.Millisecond)
			// A stale browser event must no longer veto an intentional new Stop.
			stop("stale-after-restart")
			waitUntil(t, "current turn stopped", func() bool {
				n := 0
				for _, e := range sender.agentEvents() {
					if e.Kind == "turn_done" {
						n++
					}
				}
				return n >= 2
			})
			notices := 0
			for _, e := range sender.agentEvents() {
				if e.Kind == "stop_result" {
					notices++
				}
			}
			if notices < 4 {
				t.Fatalf("Stop presses lacked visible results: %d", notices)
			}
		})
	}
}
