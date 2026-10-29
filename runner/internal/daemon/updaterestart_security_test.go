package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// Even a receipt passing all of the old mode, hash, age and call checks is
// session-writable evidence on a host. It must never change the pause decision.
func TestSessionReceiptCannotAuthorizeRecovery(t *testing.T) {
	update := openCallOf("install-1", "Bash", bashIn("install/desktop/daemon/install.sh install"))
	deploy := openCallOf("deploy-1", "Bash", bashIn("make deploy"))
	for _, tc := range []struct {
		name        string
		session     string
		calls       []OpenCall
		receiptCall OpenCall
		replay      bool
	}{
		{name: "session planted receipt", session: "s1", calls: []OpenCall{update}, receiptCall: update},
		{name: "replayed receipt", session: "s1", calls: []OpenCall{update}, receiptCall: update, replay: true},
		{name: "another session", session: "s2", calls: []OpenCall{update}, receiptCall: update},
		{name: "different destructive command with valid update receipt", session: "s1", calls: []OpenCall{deploy}, receiptCall: update},
		{name: "different destructive command with matching receipt", session: "s1", calls: []OpenCall{deploy}, receiptCall: deploy},
		{name: "different tool call id", session: "s1", calls: []OpenCall{openCallOf("install-2", "Bash", bashIn("install/desktop/daemon/install.sh install"))}, receiptCall: update},
		{name: "two cut off calls", session: "s1", calls: []OpenCall{update, deploy}, receiptCall: update},
		{name: "combined deployment and update", session: "s1", calls: []OpenCall{openCallOf("combined-1", "Bash", bashIn("make deploy & install/desktop/daemon/install.sh install"))}, receiptCall: openCallOf("combined-1", "Bash", bashIn("make deploy & install/desktop/daemon/install.sh install"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			updateMarker(t, state, func(m map[string]any) { m["call"] = tc.receiptCall })
			marker := filepath.Join(state, "restart-update.json")
			saved, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			if tc.replay {
				_ = ReadRestartCause(state)
				if err := os.WriteFile(marker, saved, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(state, plannedRestartFile), []byte("update\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cause := ReadRestartCause(state)
			rec := AgentRecord{SessionID: tc.session, TurnActive: true, OpenCalls: tc.calls}
			factory, records, work, _ := recoveryHostAfter(t, cause)
			stubTranscripts(t, "conv-1")
			rec.WorkDir, rec.ClaudeSessionID, rec.Interaction = work, "conv-1", protocol.InteractionUnattended
			if err := writeAgentRecord(records, rec); err != nil {
				t.Fatal(err)
			}
			host, sender := factory()
			if n := host.AdoptRecorded(); n != 1 {
				t.Fatalf("adopted %d", n)
			}
			host.NoteConnected()
			waitUntil(t, "receipt must not authorize recovery", func() bool {
				return countRecoveryPauses(sender) != 0 || sender.countKind("turn_done") != 0
			})
			if countRecoveryPauses(sender) != 1 || sender.countKind("turn_done") != 0 {
				t.Fatal("session-writable receipt authorized destructive recovery")
			}
			if words := (&AgentHost{cfg: AgentHostConfig{RestartCause: cause}}).restartCauseWords(rec); words == "" {
				t.Fatal("missing ordinary restart explanation")
			} else if strings.Contains(words, "completed (now on") {
				t.Fatalf("unverified completion claim: %s", words)
			}
		})
	}
}

func countRecoveryPauses(sender *agentTestSender) int {
	sender.mu.Lock()
	defer sender.mu.Unlock()
	count := 0
	for _, msg := range sender.msgs {
		if _, ok := msg.(protocol.SessionPaused); ok {
			count++
		}
	}
	return count
}
