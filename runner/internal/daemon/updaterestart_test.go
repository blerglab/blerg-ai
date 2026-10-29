package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Build an otherwise acceptable legacy receipt entirely from session-visible
// information. Its private file modes and matching executable hash add no trust.
func updateMarker(t *testing.T, dir string, change func(map[string]any)) {
	t.Helper()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	m := map[string]any{
		"session_id": "s1", "call": openCallOf("install-1", "Bash", bashIn("install/desktop/daemon/install.sh install")),
		"created_at": time.Now().UTC(), "from_commit": strings.Repeat("1", 40), "to_commit": strings.Repeat("2", 40),
		"to_binary_sha256": hex.EncodeToString(sum[:]), "result": "installed",
	}
	if change != nil {
		change(m)
	}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "restart-update.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, plannedRestartFile), []byte("update\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateReceiptDoesNotChangeEngineRecovery(t *testing.T) {
	for _, engine := range []string{"claude", "codex"} {
		for _, command := range []string{"install/desktop/daemon/install.sh install", "make restart-daemon", "make deploy", "go test ./..."} {
			t.Run(engine+"/"+command, func(t *testing.T) {
				call := openCallOf("call-1", "Bash", bashIn(command))
				if engine == "codex" {
					call.Tool = "bash"
				}
				state := t.TempDir()
				updateMarker(t, state, func(m map[string]any) { m["call"] = call })
				cause := ReadRestartCause(state)
				factory, records, work, log := recoveryHostAfter(t, cause)
				stubTranscripts(t, "conv-1")
				rec := AgentRecord{SessionID: "s1", WorkDir: work, ClaudeSessionID: "conv-1", Interaction: protocol.InteractionUnattended, TurnActive: true, OpenCalls: []OpenCall{call}}
				if engine == "codex" {
					oldTranscript := codexTranscriptExists
					codexTranscriptExists = func(string) bool { return true }
					t.Cleanup(func() { codexTranscriptExists = oldTranscript })
					bin := fakeCodex(t)
					t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
					log = filepath.Join(bin, "calls.log")
					t.Setenv("FAKE_CODEX_LOG", log)
					rec.Engine, rec.CodexThreadID, rec.ClaudeSessionID = "codex", "cx-thread-1", ""
					rec.Model = "gpt-6.1-sol"
				}
				if err := writeAgentRecord(records, rec); err != nil {
					t.Fatal(err)
				}
				host, sender := factory()
				if n := host.AdoptRecorded(); n != 1 {
					t.Fatalf("adopted %d", n)
				}
				host.NoteConnected()
				if command == "go test ./..." {
					sender.waitForKind(t, "turn_done")
				} else {
					waitUntil(t, "recovery decision", func() bool {
						return countRecoveryPauses(sender) != 0 || sender.countKind("turn_done") != 0
					})
					if countRecoveryPauses(sender) != 1 || sender.countKind("turn_done") != 0 {
						t.Fatal("destructive session ran")
					}
				}
				data, _ := os.ReadFile(log)
				if strings.Contains(string(data), "completed (now on") || strings.Contains(strings.Join(userTexts(sender), " "), "completed (now on") {
					t.Fatal("untrusted receipt produced a verified completion claim")
				}
			})
		}
	}
}
