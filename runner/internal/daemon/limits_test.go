package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// trackerHarness builds a tracker over a fake Codex source whose answers the
// test controls, collecting what the tracker sends.
type trackerHarness struct {
	t     *testing.T
	tr    *UsageLimitsTracker
	sent  chan protocol.UsageLimits
	reads atomic.Int32
	fail  atomic.Bool
	logs  atomic.Int32
}

func newTrackerHarness(t *testing.T) *trackerHarness {
	h := &trackerHarness{t: t, sent: make(chan protocol.UsageLimits, 32)}
	h.tr = newUsageLimitsTracker(func(msg any) error {
		h.sent <- msg.(protocol.UsageLimits)
		return nil
	}, []limitSource{
		{provider: "codex", read: func(context.Context) ([]protocol.UsageWindow, error) {
			n := h.reads.Add(1)
			if h.fail.Load() {
				return nil, errors.New("codex app-server: error -32600")
			}
			return []protocol.UsageWindow{{ID: "5h", UsedPercent: float64(n)}}, nil
		}},
		{provider: "claude", read: func(context.Context) ([]protocol.UsageWindow, error) { return nil, errNoUsageLimits }},
	})
	h.tr.interval = time.Hour
	h.tr.turnDelay = 50 * time.Millisecond
	h.tr.logf = func(string, ...any) { h.logs.Add(1) }
	return h
}

func (h *trackerHarness) next() protocol.UsageLimits {
	h.t.Helper()
	select {
	case m := <-h.sent:
		return m
	case <-time.After(2 * time.Second):
		h.t.Fatal("nothing sent")
	}
	return protocol.UsageLimits{}
}

func (h *trackerHarness) quiet(d time.Duration) {
	h.t.Helper()
	select {
	case m := <-h.sent:
		h.t.Fatalf("unexpected report %+v", m)
	case <-time.After(d):
	}
}

func (h *trackerHarness) run() {
	ctx, cancel := context.WithCancel(context.Background())
	h.t.Cleanup(cancel)
	go h.tr.Run(ctx)
}

func TestUsageLimitsTrackerReadsOnConnectAndAfterACodexTurn(t *testing.T) {
	h := newTrackerHarness(t)
	h.run()
	// Nothing at start: the first read waits for the connection (Kick from the connect callback),
	// so a report never races the daemon's hello.
	h.quiet(150 * time.Millisecond)
	h.tr.Kick()
	m := h.next()
	if m.Type != "usage_limits" || m.Provider != "codex" || len(m.Windows) != 1 || m.Windows[0].UsedPercent != 1 || m.FetchedAt.IsZero() {
		t.Fatalf("first report = %+v", m)
	}
	// The harness's Claude source has nothing to say: nothing is sent for it.
	h.quiet(100 * time.Millisecond)
	// A reconnect right after a read does not read again (a flapping link spawns no codex per try).
	h.tr.Kick()
	h.quiet(150 * time.Millisecond)

	// Turns ending in a burst make one read, a moment after the last.
	h.tr.CodexTurnEnded()
	h.tr.CodexTurnEnded()
	if m := h.next(); m.Windows[0].UsedPercent != 2 {
		t.Fatalf("after a turn = %+v", m)
	}
	h.quiet(200 * time.Millisecond)
	if n := h.reads.Load(); n != 2 {
		t.Fatalf("reads = %d, want 2", n)
	}
}

func TestUsageLimitsTrackerReportsNothingOnFailureAndLogsOnce(t *testing.T) {
	h := newTrackerHarness(t)
	h.fail.Store(true)
	h.run()
	h.tr.Kick()
	h.quiet(150 * time.Millisecond)
	h.tr.CodexTurnEnded()
	h.quiet(200 * time.Millisecond)
	if h.reads.Load() != 2 || h.logs.Load() != 1 {
		t.Fatalf("reads = %d, logs = %d; want 2 reads, one log line", h.reads.Load(), h.logs.Load())
	}
	// A success, then a failure again: that failure is new, and logged.
	h.fail.Store(false)
	h.tr.CodexTurnEnded()
	h.next()
	h.fail.Store(true)
	h.tr.CodexTurnEnded()
	h.quiet(200 * time.Millisecond)
	if h.logs.Load() != 2 {
		t.Fatalf("logs = %d, want 2", h.logs.Load())
	}
	// The last good report is what a reconnect says again, while it is not too old.
	h.tr.Resend()
	if m := h.next(); m.Provider != "codex" || m.Windows[0].UsedPercent != 3 {
		t.Fatalf("resent = %+v", m)
	}
	h.quiet(50 * time.Millisecond)
}

// Three failed reads in a row withdraw the provider's meters (an empty report): a logged-out
// Codex must not show its last percentages for days.
func TestUsageLimitsTrackerWithdrawsAfterThreeFailures(t *testing.T) {
	h := newTrackerHarness(t)
	h.run()
	h.tr.Kick()
	h.next()
	h.fail.Store(true)
	for i := 1; i <= 2; i++ {
		h.tr.CodexTurnEnded()
		h.quiet(200 * time.Millisecond)
	}
	h.tr.CodexTurnEnded()
	if m := h.next(); m.Provider != "codex" || len(m.Windows) != 0 {
		t.Fatalf("third failure = %+v, want codex withdrawn", m)
	}
	h.tr.CodexTurnEnded()
	h.quiet(200 * time.Millisecond) // withdrawn once, not again
	h.tr.Resend()
	h.quiet(50 * time.Millisecond)
	// A success brings it back.
	h.fail.Store(false)
	h.tr.CodexTurnEnded()
	if m := h.next(); len(m.Windows) != 1 {
		t.Fatalf("after recovery = %+v", m)
	}
}

func TestUsageLimitsTrackerClaudeCapUntilReset(t *testing.T) {
	h := newTrackerHarness(t)
	reset := time.Now().Add(300 * time.Millisecond)
	h.tr.RateLimited("claude", reset)
	m := h.next()
	if m.Provider != "claude" || len(m.Windows) != 1 || m.Windows[0] != (protocol.UsageWindow{ID: "capped", UsedPercent: 100, ResetsAt: reset.UTC()}) {
		t.Fatalf("cap report = %+v", m)
	}
	h.tr.Resend()
	if m := h.next(); m.Provider != "claude" || len(m.Windows) != 1 {
		t.Fatalf("resent cap = %+v", m)
	}
	// At the reset the cap is withdrawn: an empty report for claude.
	if m := h.next(); m.Provider != "claude" || len(m.Windows) != 0 {
		t.Fatalf("after the reset = %+v", m)
	}
	h.tr.Resend()
	h.quiet(30 * time.Millisecond)
	// A reset in the past says nothing.
	h.tr.RateLimited("claude", time.Now().Add(-time.Minute))
	h.quiet(30 * time.Millisecond)
}

func TestUsageLimitsTrackerSendErrorsAreIgnored(t *testing.T) {
	var calls atomic.Int32
	tr := newUsageLimitsTracker(func(any) error { calls.Add(1); return fmt.Errorf("not connected") }, nil)
	tr.RateLimited("claude", time.Now().Add(time.Hour))
	tr.Resend()
	if calls.Load() != 2 {
		t.Fatalf("send calls = %d", calls.Load())
	}
}

type recordingLimits struct {
	codexTurns  atomic.Int32
	claudeTurns atomic.Int32
	claudeCaps  atomic.Int32
}

func (r *recordingLimits) CodexTurnEnded()               { r.codexTurns.Add(1) }
func (r *recordingLimits) ClaudeTurnEnded()              { r.claudeTurns.Add(1) }
func (r *recordingLimits) ClaudeRateLimited(_ time.Time) { r.claudeCaps.Add(1) }

// A Codex agent session's finished turn reaches the usage-limits tracker.
func TestAgentHostTellsTheTrackerACodexTurnEnded(t *testing.T) {
	binDir := fakeCodex(t)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_LOG", filepath.Join(binDir, "calls.log"))
	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	limits := &recordingLimits{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir(), UsageLimits: limits})
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s1", Repo: "proj",
		InitialPrompt: "hello", Kind: "agent", Engine: "codex",
	})
	sender.waitForKind(t, "turn_done")
	deadline := time.Now().Add(2 * time.Second)
	for limits.codexTurns.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if limits.codexTurns.Load() != 1 || limits.claudeTurns.Load() != 0 || limits.claudeCaps.Load() != 0 {
		t.Fatalf("codex turns = %d, claude turns = %d, claude caps = %d", limits.codexTurns.Load(), limits.claudeTurns.Load(), limits.claudeCaps.Load())
	}
}

// A Claude Code agent session's finished turn reaches the tracker as a Claude turn.
func TestAgentHostTellsTheTrackerAClaudeTurnEnded(t *testing.T) {
	binDir := fakeClaude(t)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CLAUDE_LOG", filepath.Join(binDir, "calls.log"))
	reposRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(reposRoot, "proj"), 0o755); err != nil {
		t.Fatal(err)
	}
	sender := &agentTestSender{}
	limits := &recordingLimits{}
	host := NewAgentHost(sender, AgentHostConfig{ReposRoot: reposRoot, HomeDir: t.TempDir(), ClaudeCode: true, UsageLimits: limits})
	host.Spawn(protocol.SpawnSession{
		Type: "spawn_session", SessionID: "s1", Repo: "proj", InitialPrompt: "hello", Kind: "agent",
	})
	sender.waitForKind(t, "turn_done")
	deadline := time.Now().Add(2 * time.Second)
	for limits.claudeTurns.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if limits.claudeTurns.Load() != 1 || limits.codexTurns.Load() != 0 {
		t.Fatalf("claude turns = %d, codex turns = %d", limits.claudeTurns.Load(), limits.codexTurns.Load())
	}
	host.Kill("s1")
}

// A turn reads only its provider's source; a burst of both providers' turns reads each once.
func TestUsageLimitsTrackerTurnReadsOnlyItsProvider(t *testing.T) {
	var codexReads, claudeReads atomic.Int32
	sent := make(chan protocol.UsageLimits, 16)
	tr := newUsageLimitsTracker(func(msg any) error { sent <- msg.(protocol.UsageLimits); return nil }, []limitSource{
		{provider: "codex", read: func(context.Context) ([]protocol.UsageWindow, error) {
			codexReads.Add(1)
			return []protocol.UsageWindow{{ID: "5h"}}, nil
		}},
		{provider: "claude", read: func(context.Context) ([]protocol.UsageWindow, error) {
			claudeReads.Add(1)
			return []protocol.UsageWindow{{ID: "7d"}}, nil
		}},
	})
	tr.interval, tr.turnDelay = time.Hour, 30*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr.Run(ctx)
	tr.ClaudeTurnEnded()
	if m := <-sent; m.Provider != "claude" {
		t.Fatalf("after a Claude turn = %+v", m)
	}
	time.Sleep(100 * time.Millisecond)
	if codexReads.Load() != 0 || claudeReads.Load() != 1 {
		t.Fatalf("codex reads = %d, claude reads = %d", codexReads.Load(), claudeReads.Load())
	}
	tr.ClaudeTurnEnded()
	tr.CodexTurnEnded()
	<-sent
	<-sent
	time.Sleep(100 * time.Millisecond)
	if codexReads.Load() != 1 || claudeReads.Load() != 2 {
		t.Fatalf("codex reads = %d, claude reads = %d", codexReads.Load(), claudeReads.Load())
	}
}
