//go:build linux || darwin

package daemon

import (
	"encoding/json"
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// Both restart modes retain private state but lose all in-memory sessions.
// A failed recovery must release each pending bubble through its own receipt.
func TestHostRestartFailedRecoveryReturnsHeldInput(t *testing.T) {
	for _, planned := range []bool{true, false} {
		name := "host reboot"
		if planned {
			name = "daemon restart"
		}
		t.Run(name, func(t *testing.T) {
			newHost, records, work, _ := recoveryHostAfter(t, RestartCause{Planned: planned})
			if err := writeAgentRecord(records, AgentRecord{SessionID: "lost", Repo: "proj", WorkDir: filepath.Join(work, "missing")}); err != nil {
				t.Fatal(err)
			}
			host, sender := newHost()
			host.NoteConnected()
			host.BeginAdoption()
			host.UserInput("lost", agent.Input{Text: "keep my work", Source: "chat", ClientID: "pending-1"})
			host.RunAdoption()
			returned := false
			for _, event := range sender.agentEvents() {
				var p agent.ErrorPayload
				if event.Kind == "error" && json.Unmarshal(event.Payload, &p) == nil && p.ClientID == "pending-1" && p.Retryable && p.InputText == "keep my work" {
					returned = true
				}
			}
			if !returned {
				t.Fatal("failed recovery left the held input without a retryable client receipt")
			}
			if err := os.Mkdir(filepath.Join(work, "missing"), 0755); err != nil {
				t.Fatal(err)
			}
			host.UserInput("lost", agent.Input{Text: "retry once", Source: "chat", ClientID: "pending-2"})
			sender.waitForKind(t, "turn_done")
			got := userTexts(sender)
			if len(got) != 1 || got[0] != "chat:retry once" {
				t.Fatalf("recovery replayed returned input: %v", got)
			}
		})
	}
}

// A pre-restart session may have no usable record or process. The manager must
// still acknowledge End rather than leaving the browser waiting for an exit.
func TestHostRestartEndMissingProcessAcknowledged(t *testing.T) {
	sender := newRecordingSender(8)
	manager := newManagerWithSender(sender, ManagerConfig{ReposRoot: t.TempDir()})
	manager.killSession("lost")
	for _, raw := range sender.messages() {
		var msg struct {
			Type      string `json:"type"`
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal([]byte(raw), &msg) == nil && msg.Type == "session_ended" && msg.SessionID == "lost" {
			return
		}
	}
	t.Fatal("End of a missing process had no server acknowledgement")
}

// Run actual disposable host engine processes, stop the old host, retain its
// private state, and construct a new host exactly as daemon startup does.
func TestHostRestartRetainedStateDeliversInput(t *testing.T) {
	for _, planned := range []bool{true, false} {
		for _, midTurn := range []bool{false, true} {
			name := "reboot/idle"
			if planned {
				name = "daemon/idle"
			}
			if midTurn {
				name = strings.ReplaceAll(name, "idle", "mid-turn")
			}
			t.Run(name, func(t *testing.T) {
				newHost, records, _, _ := recoveryHostAfter(t, RestartCause{Planned: planned})
				t.Setenv("BLERG_CLAUDE_STEERING", "0")
				stubTranscripts(t, "cc-sess-1")
				if midTurn {
					fifo := filepath.Join(t.TempDir(), "engine-gate")
					if err := syscall.Mkfifo(fifo, 0600); err != nil {
						t.Fatal(err)
					}
					t.Setenv("FAKE_RESTART_GATE", fifo)
					bin := strings.Split(os.Getenv("PATH"), ":")[0]
					engine := filepath.Join(bin, "claude")
					raw, err := os.ReadFile(engine)
					if err != nil {
						t.Fatal(err)
					}
					block := `if [ -n "$FAKE_RESTART_GATE" ]; then
 echo '{"type":"system","subtype":"init","session_id":"cc-sess-1"}'
 echo '{"type":"assistant","message":{"content":[{"type":"tool_use","id":"blocked","name":"Bash","input":{"command":"fixture work"}}]}}'
 exec 3<> "$FAKE_RESTART_GATE"
 read -r line <&3
fi
`
					if err := os.WriteFile(engine, []byte(strings.Replace(string(raw), "cat <<'EOF'", block+"cat <<'EOF'", 1)), 0755); err != nil {
						t.Fatal(err)
					}
				}
				before, oldSender := newHost()
				before.Spawn(protocol.SpawnSession{SessionID: "restart", Kind: "agent", Repo: "proj", InitialPrompt: "initial work", Interaction: protocol.InteractionInteractive})
				var pid int
				if midTurn {
					rec := waitRecord(t, records, "restart", "blocked native engine", func(r AgentRecord) bool { return r.EnginePID != 0 && r.ToolRunning != nil && *r.ToolRunning })
					pid = rec.EnginePID
				} else {
					oldSender.waitForKind(t, "turn_done")
					waitRecord(t, records, "restart", "idle record", func(r AgentRecord) bool { return r.ClaudeSessionID != "" && !r.TurnActive && r.EnginePID == 0 })
				}
				before.Freeze()
				previous := before.get("restart")
				previous.cancel()
				select {
				case <-previous.runDone:
				case <-time.After(10 * time.Second):
					t.Fatal("old process did not stop")
				}
				if pid != 0 && !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
					t.Fatal("simulated reboot retained a live engine")
				}
				t.Setenv("FAKE_RESTART_GATE", "")
				after, sender := newHost()
				sender.notify = make(chan struct{}, 1)
				after.NoteConnected()
				if after.BeginAdoption() != 1 {
					t.Fatal("restart did not retain its session")
				}
				after.UserInput("restart", agent.Input{Text: "continue after restart", Source: "chat", ClientID: "after-restart"})
				after.RunAdoption()
				deadline := time.NewTimer(15 * time.Second)
				defer deadline.Stop()
				// Mid-turn recovery finishes its own turn before it consumes the
				// held input. That first turn_done is not a delivery receipt.
				for {
					for _, event := range sender.agentEvents() {
						var p agent.UserMessagePayload
						if event.Kind == "user_message" && json.Unmarshal(event.Payload, &p) == nil && p.ClientID == "after-restart" && p.Text == "continue after restart" {
							return
						}
					}
					select {
					case <-sender.notify:
					case <-deadline.C:
						t.Fatal("retained session did not deliver its held input")
					}
				}
			})
		}
	}
}

func TestRecoveryRefusedRelayDoesNotPromiseComposerReturn(t *testing.T) {
	newHost, records, work, _ := recoveryHost(t)
	if err := writeAgentRecord(records, AgentRecord{SessionID: "relay", Repo: "proj", WorkDir: filepath.Join(work, "missing")}); err != nil {
		t.Fatal(err)
	}
	host, sender := newHost()
	host.NoteConnected()
	host.BeginAdoption()
	host.UserInput("relay", agent.Input{Text: "relayed answer", Source: "ask_answer", ClientID: "relay-id"})
	host.RunAdoption()
	for _, event := range sender.agentEvents() {
		var p agent.ErrorPayload
		if event.Kind == "error" && json.Unmarshal(event.Payload, &p) == nil && p.ClientID == "relay-id" {
			if p.InputText != "" || strings.Contains(p.Message, "composer") {
				t.Fatal("relay promised an input return")
			}
			return
		}
	}
	t.Fatal("relay refusal receipt missing")
}
