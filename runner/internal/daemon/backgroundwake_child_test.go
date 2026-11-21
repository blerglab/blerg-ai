package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// turnDoneCount counts turn_done agent_events the sender has captured so far.
func turnDoneCount(sender *agentTestSender) int {
	n := 0
	for _, ev := range sender.agentEvents() {
		if ev.Kind == "turn_done" {
			n++
		}
	}
	return n
}

// TestChildBackgroundTaskEndWakesAnIdleSession (BLG-447): a delegation child (Interaction:
// unattended, what every Agent-tool/delegation worker gets — headlessSession in
// runner/internal/server/notices.go) runs a real background Bash job and ends its turn to wait
// for it, exactly the pattern the BLG-447 skill update told workers to stop using. The fake's
// NOWAKEBG command reports the job's end with only its notice, never an autonomous turn of the
// CLI's own, so a turn starting afterward can only be Blerg's own wake-up (claudebackground.go's
// wakeForFinishedTask) — not the CLI self-resuming, which TestBackgroundWorkOutlivesIdleRealCLI
// and TestIdleShutdownNeverEndsLiveBackgroundWork already cover. This drives it through the full
// agent-session wiring a real child gets (AgentHost.Spawn, not newClaudeCodeDriver directly), to
// prove the wake-up still reaches the long-lived process once that wiring is in the way.
func TestChildBackgroundTaskEndWakesAnIdleSession(t *testing.T) {
	t.Setenv("BLERG_CLAUDE_STEERING", "1")
	shrink(t, &ccIdleShutdown, 300*time.Millisecond)
	shrink(t, &ccWakeFallbackAfter, 50*time.Millisecond)
	_, logPath := installFakeClaude(t)
	t.Setenv("GO_FAKE_CLAUDE", "1")
	t.Setenv("FAKE_LOG", logPath)
	marker := filepath.Join(t.TempDir(), "gate-finished")
	t.Setenv("FAKE_BG_SECONDS", "1")
	t.Setenv("FAKE_BG_MARKER", marker)

	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir(), ClaudeCode: true})
	t.Cleanup(func() {
		host.Freeze()
		for _, id := range host.ActiveIDs() {
			host.Kill(id)
		}
	})

	const sid = "s-child-bg"
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: sid, Repo: "proj", Kind: "agent",
		InitialPrompt: "NOWAKEBG run the gate", Interaction: protocol.InteractionUnattended,
	})

	sender.waitForKind(t, "background_task_started")
	waitUntil(t, "the command to finish", func() bool { _, err := os.Stat(marker); return err == nil })
	sender.waitForKind(t, "background_task_done")
	waitUntil(t, "Blerg's own wake-up turn for the finished background job", func() bool { return turnDoneCount(sender) >= 2 })
}

// The REALBG ordering of the same scenario: the CLI reports the task's end and starts its own
// turn (self-wake) with no Blerg input ever sent. Through the full AgentHost.Spawn wiring, this
// must not ALSO trigger closeBackgroundTask's fallback — markRunning (fired by the CLI's own
// init) must cancel it first (BLG-447 code review, finding H). The fallback's window is longer
// than REALBG's self-wake takes, so passing proves the cancellation won.
func TestChildBackgroundTaskSelfWakeIsNotDuplicatedByFallback(t *testing.T) {
	t.Setenv("BLERG_CLAUDE_STEERING", "1")
	shrink(t, &ccIdleShutdown, 5*time.Second)
	shrink(t, &ccWakeFallbackAfter, time.Second)
	_, logPath := installFakeClaude(t)
	t.Setenv("GO_FAKE_CLAUDE", "1")
	t.Setenv("FAKE_LOG", logPath)
	marker := filepath.Join(t.TempDir(), "gate-finished")
	t.Setenv("FAKE_BG_SECONDS", "0.1")
	t.Setenv("FAKE_BG_MARKER", marker)

	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir(), ClaudeCode: true})
	t.Cleanup(func() {
		host.Freeze()
		for _, id := range host.ActiveIDs() {
			host.Kill(id)
		}
	})

	const sid = "s-child-bg-selfwake"
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: sid, Repo: "proj", Kind: "agent",
		InitialPrompt: "REALBG run the gate", Interaction: protocol.InteractionUnattended,
	})

	sender.waitForKind(t, "background_task_started")
	waitUntil(t, "the command to finish", func() bool { _, err := os.Stat(marker); return err == nil })
	sender.waitForKind(t, "background_task_done")
	waitUntil(t, "the CLI's own self-wake turn", func() bool { return turnDoneCount(sender) >= 2 })
	time.Sleep(2 * time.Second) // past the fallback's window, which must not have survived the self-wake
	if n := turnDoneCount(sender); n != 2 {
		t.Fatalf("the canceled fallback still started a turn: %d turn_done events", n)
	}
}
