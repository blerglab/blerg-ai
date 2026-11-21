package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestTerminalSpawnRefusedBeforeWorkspace(t *testing.T) {
	sender := newRecordingSender(8)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: t.TempDir()})
	raw, err := json.Marshal(protocol.SpawnSession{SessionID: "legacy", Kind: "tmux", Repo: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	mgr.handleSpawnSession(raw)
	sender.mu.Lock()
	defer sender.mu.Unlock()
	encoded, _ := json.Marshal(sender.msgs)
	if !strings.Contains(string(encoded), "Terminal sessions have been removed") {
		t.Fatalf("expected Terminal refusal before workspace lookup, got %s", encoded)
	}
}

// A fresh manager sees both kinds of on-disk records after a daemon restart.
// Only the agent record is claimed; its next turn resumes the saved conversation
// through the CLI process, without replaying the original prompt.
func TestRestartResumesAgentAndIgnoresTerminalRecord(t *testing.T) {
	newHost, recDir, workDir, logPath := recoveryHost(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	transcriptDir := filepath.Join(home, ".claude", "projects", "fixture")
	if err := os.MkdirAll(transcriptDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(transcriptDir, "cc-sess-1.jsonl"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(workDir)
	if err := writeSessionRecord(root, SessionRecord{Version: recordSchemaVersion, SessionID: "legacy", Repo: "proj", ProjectPath: workDir}); err != nil {
		t.Fatal(err)
	}
	before, firstSender := newHost()
	before.Spawn(protocol.SpawnSession{SessionID: "agent", Kind: "agent", Repo: "proj", InitialPrompt: "original work", Interaction: protocol.InteractionInteractive})
	firstSender.waitForKind(t, "turn_done")
	waitRecord(t, recDir, "agent", "saved completed conversation", func(rec AgentRecord) bool { return rec.ClaudeSessionID == "cc-sess-1" && !rec.TurnActive })
	// Follow daemon shutdown: freeze disk records, cancel the engine, join it.
	before.Freeze()
	previous := before.get("agent")
	previous.cancel()
	select {
	case <-previous.runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("first engine did not stop")
	}
	host, sender := newHost()
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: root})
	mgr.agents = host
	done := make(chan struct{})
	if n := mgr.AdoptAgentSessions(func() { close(done) }); n != 1 {
		t.Fatalf("claimed %d sessions, want only agent", n)
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("agent adoption timed out")
	}
	oldOut := dockerOutput
	dockerOutput = func(...string) ([]byte, error) { return nil, nil }
	t.Cleanup(func() { dockerOutput = oldOut })
	mgr.ReconnectSessions()
	mgr.ReconnectSessions()
	if ids := mgr.ActiveSessionIDs(); !slices.Equal(ids, []string{"agent"}) {
		t.Fatalf("active sessions %v", ids)
	}
	host.UserMessage("agent", "continue saved work", "chat")
	sender.waitForKind(t, "turn_done")
	argv, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(argv), "--resume cc-sess-1") || !strings.Contains(string(argv), "continue saved work") || strings.Count(string(argv), "original work") != 1 {
		t.Fatalf("agent did not resume its saved conversation: %s", argv)
	}
}

func TestTerminalQueueRefusalReleasesClaim(t *testing.T) {
	sender := newRecordingSender(8)
	mgr := newManagerWithSender(sender, ManagerConfig{ReposRoot: t.TempDir()})
	t.Cleanup(func() { close(mgr.spawns) })
	for _, id := range []string{"legacy-first", "legacy-next"} {
		raw, err := json.Marshal(protocol.SpawnSession{SessionID: id, Kind: "tmux", Repo: "missing"})
		if err != nil {
			t.Fatal(err)
		}
		mgr.enqueueSpawn(raw)
	}
	// The next refusal proves the worker returned from the first handler.
	for i := 0; i < 2; i++ {
		select {
		case <-sender.ch:
		case <-time.After(5 * time.Second):
			t.Fatal("Terminal refusal timed out")
		}
	}
	if slices.Contains(mgr.QueuedSpawnIDs(), "legacy-first") {
		t.Fatal("refused Terminal launch still claimed in daemon hello")
	}
	if ids := mgr.ActiveSessionIDs(); len(ids) != 0 {
		t.Fatalf("refused Terminal launch still active: %v", ids)
	}
}

func TestReconnectUsesLegacyRecordOnlyForSandboxOwnership(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	root := t.TempDir()
	mgr := newManagerWithSender(newRecordingSender(8), ManagerConfig{ReposRoot: root})
	if err := writeSessionTokenFile(DaemonStateDir(), "legacy", "fixture-terminal-token"); err != nil {
		t.Fatal(err)
	}
	if err := writeSessionRecord(root, SessionRecord{Version: recordSchemaVersion, SessionID: "legacy", ProjectPath: root, Sandbox: true}); err != nil {
		t.Fatal(err)
	}
	oldOutput, oldRun := dockerOutput, dockerRun
	t.Cleanup(func() { dockerOutput, dockerRun = oldOutput, oldRun })
	dockerOutput = func(args ...string) ([]byte, error) {
		if len(args) > 1 && args[0] == "ps" && args[len(args)-1] != "{{.Names}}" {
			return []byte(dockerContainerName("legacy") + "\t\n"), nil
		}
		return nil, nil
	}
	var removed []string
	dockerRun = func(args ...string) error {
		if len(args) == 3 && args[0] == "rm" {
			removed = append(removed, args[2])
		}
		return nil
	}
	mgr.ReconnectSessions()
	if !slices.Equal(removed, []string{dockerContainerName("legacy")}) {
		t.Fatalf("owned legacy sandbox not cleaned up: %v", removed)
	}
	if ids := mgr.ActiveSessionIDs(); len(ids) != 0 {
		t.Fatalf("legacy record claimed for recovery: %v", ids)
	}
	if _, err := os.Stat(recordPath(root, "legacy")); !os.IsNotExist(err) {
		t.Fatalf("finished legacy record retained credentials: %v", err)
	}
	if _, err := os.Stat(SessionTokenPath(DaemonStateDir(), "legacy")); !os.IsNotExist(err) {
		t.Fatalf("finished legacy token file retained: %v", err)
	}
}

func TestReconnectRetiresUnclaimedHostRecord(t *testing.T) {
	root := t.TempDir()
	mgr := newManagerWithSender(newRecordingSender(8), ManagerConfig{ReposRoot: root})
	if err := writeSessionRecord(root, SessionRecord{SessionID: "old-host", ProjectPath: root, SessionToken: "fixture-retired-token"}); err != nil {
		t.Fatal(err)
	}
	mgr.ReconnectSessions()
	if _, err := os.Stat(recordPath(root, "old-host")); !os.IsNotExist(err) {
		t.Fatalf("unclaimed host record retained: %v", err)
	}
}

func TestSweepFinishesExitedLegacySandbox(t *testing.T) {
	const id = "exited-legacy"
	repo, clone := cloneFixture(t, id)
	childCommit(t, clone, "saved.txt")
	stubCloneProbe(t, true, true)
	oldOutput, oldRun := dockerOutput, dockerRun
	t.Cleanup(func() { dockerOutput, dockerRun = oldOutput, oldRun })
	removed := false
	dockerOutput = func(args ...string) ([]byte, error) {
		if args[0] == "ps" && slices.Contains(args, "-a") && !removed {
			return []byte(dockerContainerName(id) + "\t\n"), nil
		}
		return nil, nil
	}
	dockerRun = func(args ...string) error {
		if args[0] == "rm" {
			removed = true
		}
		return nil
	}
	sweepOrphanSandboxContainers(nil, func(s string) bool { return s == id })
	if !removed {
		t.Fatal("exited legacy container was not finished")
	}
	if _, err := os.Stat(clone); !os.IsNotExist(err) {
		t.Fatalf("finished clean clone stranded: %v", err)
	}
	if got := childRepoGit(t, repo, "log", "-1", "--format=%s", "wip/"+id); got != "child saved.txt" {
		t.Fatalf("legacy clone work lost: %s", got)
	}
}

func TestReconnectRetainsLegacyOwnershipWhenCleanupFails(t *testing.T) {
	t.Setenv(DaemonStateDirEnv, t.TempDir())
	root := t.TempDir()
	mgr := newManagerWithSender(newRecordingSender(8), ManagerConfig{ReposRoot: root})
	if err := writeSessionRecord(root, SessionRecord{SessionID: "unfinished", ProjectPath: root, Sandbox: true}); err != nil {
		t.Fatal(err)
	}
	oldOutput, oldRun := dockerOutput, dockerRun
	t.Cleanup(func() { dockerOutput, dockerRun = oldOutput, oldRun })
	dockerOutput = func(...string) ([]byte, error) { return []byte(dockerContainerName("unfinished") + "\t\n"), nil }
	dockerRun = func(...string) error { return os.ErrPermission }
	mgr.ReconnectSessions()
	if _, err := os.Stat(recordPath(root, "unfinished")); err != nil {
		t.Fatalf("unfinished cleanup lost ownership evidence: %v", err)
	}
}
