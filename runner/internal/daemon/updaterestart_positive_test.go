package daemon

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"github.com/blerglab/blerg-ai/runner/internal/updatehandoff"
)

func observedUpdateCause(t *testing.T, call OpenCall) RestartCause {
	t.Helper()
	b, err := json.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	return WithUpdateReceipt(RestartCause{}, &updatehandoff.Receipt{Request: updatehandoff.Request{SessionID: "s1", Call: b}, CreatedAt: time.Now(), Succeeded: true, Commit: "1234567890abcdef"})
}

func assertUpdateRecovery(t *testing.T, engine string, rec AgentRecord, cause RestartCause, wantContinue bool) {
	t.Helper()
	factory, records, work, claudeLog := recoveryHostAfter(t, cause)
	stubTranscripts(t, "conv-1")
	rec.WorkDir, rec.ClaudeSessionID, rec.Interaction = work, "conv-1", protocol.InteractionUnattended
	if engine == "codex" {
		old := codexTranscriptExists
		codexTranscriptExists = func(string) bool { return true }
		t.Cleanup(func() { codexTranscriptExists = old })
		bin := fakeCodex(t)
		t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
		t.Setenv("FAKE_CODEX_LOG", bin+"/calls.log")
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
	waitUntil(t, "verified update recovery", func() bool { return countRecoveryPauses(sender) > 0 || sender.countKind("turn_done") > 0 })
	if got := sender.countKind("turn_done") > 0; got != wantContinue {
		t.Fatalf("continued=%v want=%v, paused=%d", got, wantContinue, countRecoveryPauses(sender))
	}
	if !wantContinue && countRecoveryPauses(sender) != 1 {
		t.Fatal("unsafe update did not pause")
	}
	if wantContinue && countRecoveryPauses(sender) != 0 {
		t.Fatal("exact recorded successful update paused")
	}
	words := strings.Join(userTexts(sender), " ")
	if wantContinue {
		logPath := claudeLog
		if engine == "codex" {
			logPath = os.Getenv("FAKE_CODEX_LOG")
		}
		input, err := os.ReadFile(logPath)
		if err != nil || !strings.Contains(string(input), "update you started succeeded (now on 1234567890abcdef)") || strings.Contains(string(input), "Nothing you ran caused it") || strings.Count(string(input), "Tool calls cut off before they returned") != 1 {
			t.Fatalf("agent update instructions missing, repeated, or misattributed: %s (%v)", input, err)
		}
		if !strings.Contains(words, "↻ Updated to 1234567890ab · continued") || strings.Contains(words, "Nothing you ran caused it") || len(userTexts(sender)) != 1 {
			t.Fatalf("requester notice: %s", words)
		}
	}
	if !wantContinue && !cause.updateSucceeded(rec) && strings.Contains(words, "update you started succeeded") {
		t.Fatal("unverified success claim")
	}
}

func TestObservedUpdateRequesterContinues(t *testing.T) {
	for _, engine := range []string{"claude", "codex"} {
		for _, command := range []string{
			"install/desktop/daemon/install.sh install", "make restart-daemon",
			"install/desktop/daemon/install.sh refresh-path", "./install/desktop/daemon/install.sh refresh-path --yes",
			"(setsid nohup ./install/desktop/daemon/install.sh install --yes >update.log 2>&1 &)\nwhile ! test -f ready; do sleep 1; done",
			"(setsid nohup ./install/desktop/daemon/install.sh install --yes >update.log 2>&1 &); while pgrep -f install.sh >/dev/null; do sleep 5; done",
			"(setsid nohup ./install/desktop/daemon/install.sh install --yes >update.log 2>&1 &); while ps aux | grep install.sh >/dev/null; do tail -n 5 install.sh.log; sleep 5; done",
		} {
			t.Run(engine+"/"+command, func(t *testing.T) {
				call := openCallOf("update-1", "Bash", bashIn(command))
				if engine == "codex" {
					call.Tool = "bash"
				}
				assertUpdateRecovery(t, engine, AgentRecord{SessionID: "s1", TurnActive: true, OpenCalls: []OpenCall{call}}, observedUpdateCause(t, call), true)
			})
		}
	}
}

func TestObservedUpdateStillPauses(t *testing.T) {
	for _, engine := range []string{"claude", "codex"} {
		for _, name := range []string{"foreign session", "different command", "different call id", "two calls", "combined command", "expired", "future", "failed", "already continued", "pending background work", "matching deployment receipt", "matching combined receipt", "nested destructive command"} {
			t.Run(engine+"/"+name, func(t *testing.T) {
				call := openCallOf("update-1", "Bash", bashIn("install/desktop/daemon/install.sh install"))
				if engine == "codex" {
					call.Tool = "bash"
				}
				cause := observedUpdateCause(t, call)
				rec := AgentRecord{SessionID: "s1", TurnActive: true, OpenCalls: []OpenCall{call}}
				switch name {
				case "foreign session":
					rec.SessionID = "s2"
				case "different command":
					rec.OpenCalls[0] = openCallOf(call.CallID, call.Tool, bashIn("make deploy"))
				case "matching deployment receipt":
					rec.OpenCalls[0] = openCallOf(call.CallID, call.Tool, bashIn("make deploy"))
					cause = observedUpdateCause(t, rec.OpenCalls[0])
				case "matching combined receipt":
					rec.OpenCalls[0] = openCallOf(call.CallID, call.Tool, bashIn("make deploy & install/desktop/daemon/install.sh install"))
					cause = observedUpdateCause(t, rec.OpenCalls[0])
				case "different call id":
					rec.OpenCalls[0].CallID = "update-2"
				case "two calls":
					rec.OpenCalls = append(rec.OpenCalls, openCallOf("other-1", call.Tool, bashIn("make deploy")))
				case "combined command":
					rec.OpenCalls[0] = openCallOf(call.CallID, call.Tool, bashIn("make deploy & install/desktop/daemon/install.sh install"))
				case "expired":
					cause.update.CreatedAt = time.Now().Add(-updatehandoff.MaxAge)
				case "future":
					cause.update.CreatedAt = time.Now().Add(time.Minute)
				case "failed":
					cause.update.Succeeded = false
				case "nested destructive command":
					rec.OpenCalls[0] = openCallOf(call.CallID, call.Tool, bashIn("make deploy $(make restart-daemon)"))
					cause = observedUpdateCause(t, rec.OpenCalls[0])
				case "already continued":
					rec.AutoContinues = 1
				case "pending background work":
					rec.OpenSubagents = []OpenSubagent{{ChildID: "child-1"}}
				}
				assertUpdateRecovery(t, engine, rec, cause, false)
			})
		}
	}
}

func TestFullUpdateInputIsBoundWithoutPersistingIt(t *testing.T) {
	command := "(setsid nohup ./install/desktop/daemon/install.sh install --yes &); " + strings.Repeat(" ", 300) + "make deploy"
	call := openCallOf("call-1", "Bash", bashIn(command))
	if call.CommandHash != fmt.Sprintf("%x", sha256.Sum256([]byte(command))) {
		t.Fatal("full input not bound")
	}
	stored, _ := json.Marshal(call)
	if strings.Contains(string(stored), command) {
		t.Fatal("full shell text persisted")
	}
	if call.UpdateSafe {
		t.Fatal("destructive work hidden beyond display truncation")
	}
}

func TestUpdateCandidateRequiresLiveObservation(t *testing.T) {
	call := openCallOf("update-1", "Bash", bashIn("make restart-daemon"))
	rec := AgentRecord{SessionID: "s1", TurnActive: true, OpenCalls: []OpenCall{call}, EnginePID: os.Getpid()}
	k := &recordKeeper{rec: rec}
	if len(k.updateCandidateLocked().Call) != 0 {
		t.Fatal("disk record certified without live tool event")
	}
	k.liveCalls = map[string]OpenCall{"other-call": openCallOf("other-call", "Bash", bashIn("go test ./..."))}
	if len(k.updateCandidateLocked().Call) != 0 {
		t.Fatal("an unrelated live event certified a disk call")
	}
	k.liveCalls[call.CallID] = openCallOf(call.CallID, "Bash", bashIn("make deploy"))
	if len(k.updateCandidateLocked().Call) != 0 {
		t.Fatal("reused ID certified a different live command")
	}
	k.liveCalls[call.CallID] = call
	if len(k.updateCandidateLocked().Call) == 0 {
		t.Fatal("live update not observed")
	}
	k.rec.OpenCalls = append(k.rec.OpenCalls, openCallOf("other-1", "Bash", bashIn("make deploy")))
	if len(k.updateCandidateLocked().Call) != 0 {
		t.Fatal("two calls certified")
	}
	k.rec.OpenCalls = []OpenCall{openCallOf("deploy-1", "Bash", bashIn("make deploy"))}
	if k.updateCandidateLocked().SessionID != "s1" {
		t.Fatal("session observation lost")
	}
}

func TestUpdateMonitorDoesNotHideDaemonInvocations(t *testing.T) {
	for _, suffix := range []string{
		"; ./install.sh uninstall",
		"; while pgrep -f install.sh; do ./install.sh uninstall; done",
		`; pgrep -f "$(./install.sh uninstall)"`,
		"; tail `./install.sh uninstall`",
		"; pgrep -f install.sh\n./install.sh uninstall",
		"; ps aux | grep install.sh; systemctl stop blerg-runner-daemon",
	} {
		t.Run(suffix, func(t *testing.T) {
			if safeUpdateCommand("make restart-daemon" + suffix) {
				t.Fatal("another daemon invocation was classified as monitoring")
			}
		})
	}
}

func TestRefreshPathIsAnUpdateCommandButNotACover(t *testing.T) {
	for command, want := range map[string]bool{
		"install/desktop/daemon/install.sh refresh-path":                                            true,
		"./install/desktop/daemon/install.sh refresh-path --yes":                                    true,
		"install/desktop/daemon/install.sh refresh-path; ./install.sh uninstall":                    false,
		"install/desktop/daemon/install.sh refresh-path; systemctl --user stop blerg-runner-daemon": false,
		"install/desktop/daemon/install.sh refreshpath":                                             false,
	} {
		if got := safeUpdateCommand(command); got != want {
			t.Errorf("%q: %v, want %v", command, got, want)
		}
	}
}
