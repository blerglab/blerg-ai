package daemon

// BLG-447, real-CLI proof. The fake-CLI tests (backgroundwake_child_test.go, clauderesume_test.go)
// prove the wiring with a scripted stand-in; they cannot tell a genuine wake-up apart from a CLI
// that was never actually idle. These tests drive the real `claude` binary through the full
// AgentHost.Spawn wiring a real worker gets (no FAKE_CLAUDE/GO_FAKE_CLAUDE override: buildSessionEnv's
// real PATH resolves the real binary), end a turn waiting on a background job, and time how long it
// takes for visible activity to resume once the job finishes. Opt-in like claudestream_real_test.go:
// BLERG_REAL_CLAUDE=1 (a few cents of Haiku/Sonnet per run).
//
// A real `claude -p` process often holds its own "result" line open while a background shell it
// started is still running, then folds the <task-notification> straight into the SAME exchange once
// the job ends (one continuous "turn" from the driver's point of view, no idle gap at all — the
// genuine CLI self-wake TestChildBackgroundTaskSelfWakeIsNotDuplicatedByFallback already names). So
// the proof here is not "did a second turn_done appear": it is "did any activity (an assistant reply,
// a tool call, or a turn_done) land within 20s of background_task_done" — whichever mechanism
// produced it, self-wake or Blerg's own fallback.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// realCLIHost starts an AgentHost wired to the real claude binary (ClaudeCode: true, no
// FAKE_CLAUDE/GO_FAKE_CLAUDE in the environment to intercept it).
func realCLIHost(t *testing.T) (*AgentHost, *agentTestSender, string) {
	t.Helper()
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
	return host, sender, reposRoot
}

// waitUntilRealCLI polls cond with a budget long enough for a real model turn plus a real sleep.
func waitUntilRealCLI(t *testing.T, budget time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(budget)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// eventTime parses an AgentEvent's Ts (daemon wall clock).
func eventTime(t *testing.T, ev protocol.AgentEvent) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339Nano, ev.Ts)
	if err != nil {
		ts, err = time.Parse(time.RFC3339, ev.Ts)
	}
	if err != nil {
		t.Fatalf("event %q has an unparseable ts %q: %v", ev.Kind, ev.Ts, err)
	}
	return ts
}

// firstEventAfter returns the earliest event of one of the given kinds timestamped strictly after
// `after`, and whether one was found yet.
func firstEventAfter(t *testing.T, s *agentTestSender, after time.Time, kinds ...string) (protocol.AgentEvent, bool) {
	t.Helper()
	want := make(map[string]bool, len(kinds))
	for _, k := range kinds {
		want[k] = true
	}
	var best protocol.AgentEvent
	found := false
	for _, ev := range s.agentEvents() {
		if !want[ev.Kind] {
			continue
		}
		ts := eventTime(t, ev)
		if !ts.After(after) {
			continue
		}
		if !found || ts.Before(eventTime(t, best)) {
			best, found = ev, true
		}
	}
	return best, found
}

// firstEventOfKind returns the earliest event of one of the given kinds, and whether one exists.
func firstEventOfKind(t *testing.T, s *agentTestSender, kinds ...string) (protocol.AgentEvent, bool) {
	t.Helper()
	return firstEventAfter(t, s, time.Unix(0, 0), kinds...)
}

// assertWakeWithin asserts that activity (an assistant reply, a tool call, or the turn's end)
// shows up within budget of doneAt, logs the latency and returns it.
func assertWakeWithin(t *testing.T, sender *agentTestSender, label string, doneAt time.Time, budget time.Duration) time.Duration {
	t.Helper()
	waitUntilRealCLI(t, budget, "activity after the job ended", func() bool {
		_, ok := firstEventAfter(t, sender, doneAt, "assistant_text", "tool_call", "turn_done", "subagent_done")
		return ok
	})
	ev, _ := firstEventAfter(t, sender, doneAt, "assistant_text", "tool_call", "turn_done", "subagent_done")
	latency := eventTime(t, ev).Sub(doneAt)
	t.Logf("BLG-447 %s: background_task_done at %s -> %s (%s) at +%s", label, doneAt.Format(time.RFC3339Nano), ev.Kind, ev.Ts, latency)
	if latency < 0 || latency > budget {
		t.Fatalf("%s: woke %s after the job ended, want within %s", label, latency, budget)
	}
	return latency
}

// TestRealCLIBashBackgroundJobWakesTheIdleWorker (BLG-447, scenario a): a real worker starts a Bash
// background job (`sleep 20`) and ends its turn to wait for it; activity must resume within 20s of
// the job ending, exactly as it must for an interactive Claude Code session.
func TestRealCLIBashBackgroundJobWakesTheIdleWorker(t *testing.T) {
	if os.Getenv("BLERG_REAL_CLAUDE") != "1" {
		t.Skip("set BLERG_REAL_CLAUDE=1 to run against the real claude CLI")
	}
	host, sender, reposRoot := realCLIHost(t)
	marker := filepath.Join(reposRoot, "bash-job-done")

	const sid = "s-real-bash-bg"
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: sid, Repo: "proj", Kind: "agent", Model: "claude-haiku-4-5",
		Interaction: protocol.InteractionUnattended,
		InitialPrompt: "Call the Bash tool exactly once with run_in_background set to true and this " +
			"exact command: sleep 20 && touch " + marker + " . Then, without calling any more tools and " +
			"without waiting for that command, reply with the single word STARTED and end your turn.",
	})

	waitUntilRealCLI(t, 30*time.Second, "the background task to start", func() bool {
		return sender.countKind("background_task_started") >= 1
	})
	waitUntilRealCLI(t, 30*time.Second, "sleep 20 to finish (the marker file)", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
	waitUntilRealCLI(t, 10*time.Second, "its background_task_done", func() bool {
		_, ok := firstEventOfKind(t, sender, "background_task_done")
		return ok
	})
	doneEv, _ := firstEventOfKind(t, sender, "background_task_done")
	assertWakeWithin(t, sender, "scenario (a) Bash background job", eventTime(t, doneEv), 20*time.Second)
}

// TestRealCLIBackgroundSubagentWakesTheIdleWorker (BLG-447, scenario b): a real worker launches an
// Agent-tool subagent in the background (the subagent itself runs `sleep 20` via Bash) and ends its
// turn; activity must resume within 20s of the subagent ending.
func TestRealCLIBackgroundSubagentWakesTheIdleWorker(t *testing.T) {
	if os.Getenv("BLERG_REAL_CLAUDE") != "1" {
		t.Skip("set BLERG_REAL_CLAUDE=1 to run against the real claude CLI")
	}
	host, sender, reposRoot := realCLIHost(t)
	marker := filepath.Join(reposRoot, "subagent-job-done")

	const sid = "s-real-subagent-bg"
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: sid, Repo: "proj", Kind: "agent", Model: "claude-haiku-4-5",
		Interaction: protocol.InteractionUnattended,
		InitialPrompt: "Call the Agent tool exactly once, with subagent_type general-purpose and " +
			"run_in_background set to true, and this exact prompt for the subagent: 'Use the Bash " +
			"tool to run exactly: sleep 20 . Then touch " + marker + " . Then reply DONE.' Then, " +
			"without calling any more tools and without waiting for the subagent, reply with the " +
			"single word STARTED and end your turn.",
	})

	waitUntilRealCLI(t, 30*time.Second, "the subagent's background task to start", func() bool {
		return sender.countKind("background_task_started") >= 1
	})
	waitUntilRealCLI(t, 30*time.Second, "the subagent's sleep 20 to finish (the marker file)", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
	waitUntilRealCLI(t, 10*time.Second, "its background_task_done", func() bool {
		_, ok := firstEventOfKind(t, sender, "background_task_done")
		return ok
	})
	doneEv, _ := firstEventOfKind(t, sender, "background_task_done")
	assertWakeWithin(t, sender, "scenario (b) background Agent subagent", eventTime(t, doneEv), 20*time.Second)
}

// TestRealCLIResumedForegroundSubagentWakesTheIdleWorker (BLG-447, scenario c): the fix itself. A
// real worker launches a subagent in the FOREGROUND, gets its report and agentId, then resumes it
// with SendMessage detached (not waiting for the resumed run's result), and ends its turn. Activity
// must resume within 20s of the resumed run ending.
func TestRealCLIResumedForegroundSubagentWakesTheIdleWorker(t *testing.T) {
	if os.Getenv("BLERG_REAL_CLAUDE") != "1" {
		t.Skip("set BLERG_REAL_CLAUDE=1 to run against the real claude CLI")
	}
	host, sender, reposRoot := realCLIHost(t)
	marker := filepath.Join(reposRoot, "resumed-job-done")

	const sid = "s-real-resumed-subagent"
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: sid, Repo: "proj", Kind: "agent", Model: "claude-sonnet-5",
		Interaction: protocol.InteractionUnattended,
		InitialPrompt: "Step 1: call the Agent tool once, in the foreground (do not set run_in_background), " +
			"with subagent_type general-purpose and this exact prompt for the subagent: 'Reply with exactly " +
			"the word READY and stop.' Wait for its result and note the agentId it reports. " +
			"Step 2: call SendMessage to that agentId with this exact message: 'Use the Bash tool to run " +
			"exactly: sleep 20 . Then touch " + marker + " . Then reply DONE.' Send it so it resumes the " +
			"agent and runs in the background — do not wait for the resumed run's report. " +
			"Step 3: as soon as SendMessage returns, without calling any more tools and without waiting " +
			"for the resumed agent, reply with the single word STARTED and end your turn.",
	})

	defer func() {
		if !t.Failed() {
			return
		}
		t.Logf("=== full event dump (failure) ===")
		for _, ev := range sender.agentEvents() {
			t.Logf("DEBUG event: kind=%s ts=%s payload=%s", ev.Kind, ev.Ts, string(ev.Payload))
		}
	}()

	waitUntilRealCLI(t, 60*time.Second, "the placeholder subagent to finish", func() bool {
		return sender.countKind("subagent_done") >= 1
	})
	waitUntilRealCLI(t, 60*time.Second, "sleep 20 to finish (the marker file)", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
	// The resumed run's own final subagent_done (Resumed: true); the placeholder's own is
	// Resumed: false, so this is unambiguous even though there is no background_task_started/done
	// pair for it (by design — trackResumedRunLocked only tracks a resumed run as a bgTasks entry
	// when its ORIGINAL ran in the background; this one's did not, which is exactly the BLG-447 gap).
	waitUntilRealCLI(t, 10*time.Second, "the resumed run's own subagent_done", func() bool {
		_, ok := resumedSubagentDone(t, sender)
		return ok
	})
	doneEv, _ := resumedSubagentDone(t, sender)
	latency := assertWakeWithin(t, sender, "scenario (c) resumed foreground subagent", eventTime(t, doneEv), 20*time.Second)

	// Documented, not asserted: at 20s the real CLI's own process held this exchange open as one
	// continuous turn (no second status_changed:running, no intermediate turn_done) for all three
	// scenarios (a)/(b)/(c) — the engine's self-sustain, not Blerg's armWakeFallback, carried this
	// particular run. That fallback's own code path is deterministically exercised by the fake-CLI
	// TestResumedForegroundSubagentEndWakesAnIdleSession, which simulates the genuine idle gap this
	// 20s real run never reaches. See this file's package comment and the test report.
	t.Logf("scenario (c): wake latency %s; turn_done count so far: %d", latency, sender.countKind("turn_done"))
}

// resumedSubagentDone finds the subagent_done event of a resumed run (payload.Resumed == true),
// distinct from the original (foreground, non-resumed) subagent's own subagent_done.
func resumedSubagentDone(t *testing.T, s *agentTestSender) (protocol.AgentEvent, bool) {
	t.Helper()
	for _, ev := range s.agentEvents() {
		if ev.Kind != "subagent_done" {
			continue
		}
		var p struct {
			Resumed bool `json:"resumed"`
		}
		if json.Unmarshal(ev.Payload, &p) == nil && p.Resumed {
			return ev, true
		}
	}
	return protocol.AgentEvent{}, false
}

// checkGenuineIdle reports (without failing the test) whether turn_done fired while the job's
// marker does not exist yet — a genuine idle gap, the overnight incidents' own shape — within
// budget. It never fails: a false result is itself the finding worth reporting, not a defect in
// the test. The 20s scenarios above never reach this; this test exists to see whether a much
// longer job (240s) does.
func checkGenuineIdle(t *testing.T, sender *agentTestSender, marker string, budget time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(budget)
	for time.Now().Before(deadline) {
		if sender.countKind("turn_done") >= 1 {
			if _, err := os.Stat(marker); err == nil {
				t.Logf("turn_done fired, but the job's marker already existed too — inconclusive, not a genuine pre-completion idle gap")
				return false
			}
			t.Logf("genuine idle confirmed: turn_done fired at %s while the background job is still running", time.Now().Format(time.RFC3339))
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("no turn_done within %s of the job starting, with the job still running: the real CLI is holding this turn open rather than going idle (as in the 20s scenarios)", budget)
	return false
}

// TestRealCLILongBashJobGoesIdleThenWakes (BLG-447): a real worker starts a Bash background job
// long enough (sleep 240) that the real CLI actually ends its turn (turn_done) and goes idle
// before the job finishes — the overnight incidents' own shape, not the 20s scenarios' one
// continuous turn. The daemon's fallback (or the CLI resuming a fresh turn) must then start a new
// turn within 30s of the job ending.
func TestRealCLILongBashJobGoesIdleThenWakes(t *testing.T) {
	if os.Getenv("BLERG_REAL_CLAUDE") != "1" {
		t.Skip("set BLERG_REAL_CLAUDE=1 to run against the real claude CLI")
	}
	host, sender, reposRoot := realCLIHost(t)
	marker := filepath.Join(reposRoot, "long-bash-job-done")

	const sid = "s-real-long-bash-bg"
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: sid, Repo: "proj", Kind: "agent", Model: "claude-haiku-4-5",
		Interaction: protocol.InteractionUnattended,
		InitialPrompt: "Call the Bash tool exactly once with run_in_background set to true and this " +
			"exact command: sleep 240 && touch " + marker + " . Then, without calling any more tools and " +
			"without waiting for that command, reply with the single word STARTED and end your turn.",
	})

	waitUntilRealCLI(t, 30*time.Second, "the background task to start", func() bool {
		return sender.countKind("background_task_started") >= 1
	})
	wentIdle := checkGenuineIdle(t, sender, marker, 220*time.Second)

	waitUntilRealCLI(t, 60*time.Second, "sleep 240 to finish (the marker file)", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
	waitUntilRealCLI(t, 10*time.Second, "its background_task_done", func() bool {
		_, ok := firstEventOfKind(t, sender, "background_task_done")
		return ok
	})
	doneEv, _ := firstEventOfKind(t, sender, "background_task_done")
	label := "long Bash job (sleep 240)"
	if wentIdle {
		label += ", genuine idle"
	} else {
		label += ", CLI held the turn open (self-sustain, like the 20s scenarios)"
	}
	assertWakeWithin(t, sender, label, eventTime(t, doneEv), 30*time.Second)
	t.Logf("went idle before the job finished: %v; turn_done count by the end: %d", wentIdle, sender.countKind("turn_done"))
}

// TestRealCLILongBackgroundSubagentGoesIdleThenWakes (BLG-447): same proof, with the background
// work being an Agent-tool subagent (sleep 240 inside it) instead of a direct Bash call.
func TestRealCLILongBackgroundSubagentGoesIdleThenWakes(t *testing.T) {
	if os.Getenv("BLERG_REAL_CLAUDE") != "1" {
		t.Skip("set BLERG_REAL_CLAUDE=1 to run against the real claude CLI")
	}
	host, sender, reposRoot := realCLIHost(t)
	marker := filepath.Join(reposRoot, "long-subagent-job-done")

	const sid = "s-real-long-subagent-bg"
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: sid, Repo: "proj", Kind: "agent", Model: "claude-haiku-4-5",
		Interaction: protocol.InteractionUnattended,
		InitialPrompt: "Call the Agent tool exactly once, with subagent_type general-purpose and " +
			"run_in_background set to true, and this exact prompt for the subagent: 'Use the Bash " +
			"tool to run exactly: sleep 240 . Then touch " + marker + " . Then reply DONE.' Then, " +
			"without calling any more tools and without waiting for the subagent, reply with the " +
			"single word STARTED and end your turn.",
	})

	waitUntilRealCLI(t, 30*time.Second, "the subagent's background task to start", func() bool {
		return sender.countKind("background_task_started") >= 1
	})
	wentIdle := checkGenuineIdle(t, sender, marker, 220*time.Second)

	waitUntilRealCLI(t, 60*time.Second, "the subagent's sleep 240 to finish (the marker file)", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
	waitUntilRealCLI(t, 10*time.Second, "its background_task_done", func() bool {
		_, ok := firstEventOfKind(t, sender, "background_task_done")
		return ok
	})
	doneEv, _ := firstEventOfKind(t, sender, "background_task_done")
	label := "long background Agent subagent (sleep 240)"
	if wentIdle {
		label += ", genuine idle"
	} else {
		label += ", CLI held the turn open (self-sustain, like the 20s scenarios)"
	}
	assertWakeWithin(t, sender, label, eventTime(t, doneEv), 30*time.Second)
	t.Logf("went idle before the job finished: %v; turn_done count by the end: %d", wentIdle, sender.countKind("turn_done"))
}
