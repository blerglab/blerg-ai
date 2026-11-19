package daemon

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// A recorded session whose host workspace journal cannot be resumed stays paused, and its
// reason says which case it is: a launch that was already closed and delivered (an older
// daemon did that while the conversation went on), a stop that did not finish, a journal of
// another session, or one that cannot be verified. None of them is hosted, and the record
// stays for the person.
func TestAdoptRefusesUnresumableHostWorkspaceWithItsReason(t *testing.T) {
	binding := protocol.WorkspaceRuntimeBinding{WorkspaceRuntimeRef: protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}, ManifestDigest: "digest"}
	finalized := func(id string) workspacePrivateManifest {
		ended := protocol.SessionEnded{Type: "session_ended", SessionID: id}
		state := workspacePrivateManifest{Host: true, Session: id, Binding: &binding, StopRequested: true, Stopped: true, HostDriverStopped: true, FinalDelivery: &ended, ResultsAcknowledged: true, TerminalAcknowledged: true}
		state.TerminalReceipt = WorkspaceFinalization{Binding: binding, Message: ended, Results: state.FinalResults}.Digest()
		return state
	}
	for _, tc := range []struct {
		name   string
		state  func(id string) workspacePrivateManifest
		raw    string
		reason string
	}{
		{name: "finalized", state: finalized, reason: adoptWorkspaceClosed},
		{name: "stopping", state: func(id string) workspacePrivateManifest {
			return workspacePrivateManifest{Host: true, Session: id, Binding: &binding, StopRequested: true}
		}, reason: adoptWorkspaceStopping},
		{name: "undelivered", state: func(id string) workspacePrivateManifest {
			state := finalized(id)
			state.TerminalAcknowledged, state.TerminalReceipt = false, ""
			return state
		}, reason: adoptWorkspaceStopping},
		{name: "other-session", state: func(string) workspacePrivateManifest { return finalized("someone-else") }, reason: adoptWorkspaceForeign},
		{name: "not-host", state: func(id string) workspacePrivateManifest {
			return workspacePrivateManifest{Session: id}
		}, reason: adoptWorkspaceForeign},
		{name: "tampered", raw: `{"state":{"host":true},"mac":"00"}`, reason: adoptWorkspaceUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(DaemonStateDirEnv, t.TempDir())
			_, records, work, _ := recoveryHost(t)
			id := "ws-" + tc.name
			rec := AgentRecord{SessionID: id, Repo: "proj", WorkDir: work, Interaction: protocol.InteractionInteractive}
			if err := writeAgentRecord(records, rec); err != nil {
				t.Fatal(err)
			}
			path, err := workspaceRecord(id)
			if err != nil {
				t.Fatal(err)
			}
			if tc.raw != "" {
				if err = os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
					err = os.WriteFile(path, []byte(tc.raw), 0o600)
				}
			} else {
				err = (&LocalWorkspace{mu: new(sync.Mutex), record: path, state: tc.state(id)}).save()
			}
			if err != nil {
				t.Fatal(err)
			}
			sender := &agentTestSender{}
			host := NewAgentHost(sender, AgentHostConfig{ReposRoot: filepath.Dir(work), HomeDir: t.TempDir(), ClaudeCode: true, RecordsDir: records, Workspaces: &LocalWorkspaceService{}})
			if got := host.adoptOne(rec); got != tc.reason {
				t.Fatalf("reason %q, want %q", got, tc.reason)
			}
			var logs bytes.Buffer
			old := failurelog.Default
			failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
			defer func() { failurelog.Default = old }()
			host.NoteConnected()
			host.AdoptRecorded()
			var refusal map[string]any
			for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
				var entry map[string]any
				if err := json.Unmarshal([]byte(line), &entry); err != nil {
					t.Fatal(err)
				}
				if entry["stage"] == "adoption refused" {
					refusal = entry
				}
			}
			if refusal["session_id"] != id || refusal["reason"] != tc.reason || refusal["cause"] != "safety_check" {
				t.Fatalf("refusal must identify the session and reason: %s", logs.String())
			}
			foundPause := false
			sender.mu.Lock()
			messages := append([]any(nil), sender.msgs...)
			sender.mu.Unlock()
			for _, msg := range messages {
				if pause, ok := msg.(protocol.SessionPaused); ok {
					foundPause = pause.SessionID == id && pause.Reason == protocol.PausedByRestart && strings.Contains(pause.Detail, tc.reason)
				}
			}
			if !foundPause {
				t.Fatal("refusal did not reach the restart pause banner path")
			}
			if tc.name == "stopping" {
				raw, err := json.Marshal(protocol.InterruptSession{Type: "interrupt_session", SessionID: id})
				if err != nil {
					t.Fatal(err)
				}
				(&Manager{agents: host}).handleInterruptSession(raw)
				found := false
				for _, ev := range sender.agentEvents() {
					if ev.Kind == "stop_result" {
						var payload map[string]string
						if err := json.Unmarshal(ev.Payload, &payload); err != nil {
							t.Fatal(err)
						}
						found = payload["state"] == "failed" && strings.Contains(payload["message"], adoptWorkspaceStopping)
					}
				}
				if !found {
					t.Fatal("Stop on unrecovered workspace did not report retained cleanup reason")
				}
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("retained workspace journal missing: %v", err)
			}
			if host.Has(id) {
				t.Fatal("an unresumable workspace session was hosted")
			}
			if _, ok := readRecord(t, records, id); !ok {
				t.Fatal("the paused session's record was dropped")
			}
		})
	}
}
