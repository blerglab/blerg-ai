package daemon

import (
	"context"
	"errors"
	"fmt"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestClaudeQueueWaitRefusalCanRetryStop(t *testing.T) {
	shrink(t, &ccInterruptKillAfter, 60*time.Millisecond)
	shrink(t, &ccInterruptResultAfter, 80*time.Millisecond)
	gate := filepath.Join(t.TempDir(), "blocked")
	d, em, _ := newSteeringDriver(t, "FAKE_BACKGROUND=1", "FAKE_TOOL_MS=5000", "FAKE_STALL_INIT="+gate)
	var refuse atomic.Bool
	refuse.Store(true)
	d.startCommand = func(cmd *exec.Cmd) error {
		cmd.Cancel = func() error {
			if refuse.Load() {
				return errors.New("fixture cleanup refusal")
			}
			return cmd.Process.Kill()
		}
		return cmd.Start()
	}
	defer refuse.Store(false)
	d.Enqueue("TOOL wait", "chat")
	waitUntil(t, "tool", func() bool { return em.count("tool_call") == 2 })
	d.Enqueue("BLOCKINIT next", "chat")
	d.Interrupt()
	waitUntil(t, "cleanup refused", func() bool { return em.count("error:") > 0 })
	refuse.Store(false)
	d.Interrupt()
	waitUntil(t, "queued input resumed", func() bool { return em.count("user:BLOCKINIT next") == 1 })
}

func TestClaudeSlashDuringBackgroundWaitConsumedOnce(t *testing.T) {
	shrink(t, &ccSettleAfter, 40*time.Millisecond)
	shrink(t, &ccInterruptKillAfter, 60*time.Millisecond)
	d, em, log := newSteeringDriver(t, "FAKE_BACKGROUND=1")
	d.Enqueue("BGIDLE start", "chat")
	waitIdle(t, em, 1)
	d.Enqueue("/cost", "chat")
	waitUntil(t, "command result", func() bool { return em.count("turn_done:end_turn") == 2 })
	time.Sleep(100 * time.Millisecond)
	d.Interrupt()
	time.Sleep(160 * time.Millisecond)
	n := 0
	for _, l := range logLines(t, log) {
		if l == "command:/cost" {
			n++
		}
	}
	if n != 1 || em.count("user:/cost") != 1 || len(starts(logLines(t, log))) != 1 {
		t.Fatalf("consumed command lost or repeated: commands=%d events=%v", n, em.outline())
	}
}

func TestClaudeStopDuringNaturalTurnEndDoesNotKillReader(t *testing.T) {
	shrink(t, &ccInterruptKillAfter, 60*time.Millisecond)
	d, em, log := newSteeringDriver(t, "FAKE_BACKGROUND=1", "FAKE_TOOL_MS=200", "FAKE_QUEUE_DURING_TOOL=1")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	d.emitter = limitResumeEmitter(func(e agent.Event) {
		if e.Kind == "turn_done" {
			once.Do(func() { close(entered); <-release })
		}
		em.Emit(e)
	})
	d.Enqueue("TOOL start", "chat")
	waitUntil(t, "background tool", func() bool { return em.count("tool_call") == 2 })
	d.Enqueue("next", "chat")
	waitUntil(t, "held input", func() bool { d.sm.Lock(); defer d.sm.Unlock(); return len(d.steer.awaiting) > 0 })
	<-entered
	d.Interrupt()
	time.Sleep(160 * time.Millisecond)
	close(release)
	waitIdle(t, em, 2)
	if len(starts(logLines(t, log))) != 1 || countKind(em, "background_task_done") != 0 {
		t.Fatalf("healthy reader killed: %v", em.outline())
	}
}

func TestClaudeColdInitGetsFreshStopDeadline(t *testing.T) {
	shrink(t, &ccInterruptKillAfter, 60*time.Millisecond)
	d, em, log := newSteeringDriver(t, "FAKE_INIT_MS=180")
	d.Enqueue("TOOL cold", "chat")
	waitUntil(t, "input written", func() bool { d.sm.Lock(); defer d.sm.Unlock(); return len(d.steer.awaiting) == 1 })
	d.Interrupt()
	waitIdle(t, em, 1)
	if len(starts(logLines(t, log))) != 1 || em.count("user:TOOL cold") != 1 {
		t.Fatalf("cold input reran: %v", em.outline())
	}
}

func TestClaudeDetachedBackgroundRemainsListedAfterFallback(t *testing.T) {
	shrink(t, &ccInterruptKillAfter, 60*time.Millisecond)
	pidFile := filepath.Join(t.TempDir(), "child")
	d, em, _ := newSteeringDriver(t, "FAKE_BACKGROUND=1", "FAKE_CHILD_PID="+pidFile, "FAKE_CHILD_SETSID=1")
	d.Enqueue("BGIDLE start", "chat")
	waitIdle(t, em, 1)
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	var pid int
	if _, err = fmt.Sscan(string(raw), &pid); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()
	d.Enqueue("HANG wait", "chat")
	waitUntil(t, "hung tool", func() bool { return em.count("tool_call") == 2 })
	d.Interrupt()
	waitIdle(t, em, 2)
	waitUntil(t, "engine exited", func() bool { d.sm.Lock(); defer d.sm.Unlock(); return d.steer.proc == nil })
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatal("fixture detached task missing", err)
	}
	assertRetainedBackgroundTask(t, d, em, "bg-fixture")
}

func TestCancelEngineCommandTreatsESRCHAsDone(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	cmd.Cancel = func() error { return syscall.ESRCH }
	if err := cancelEngineCommand(cmd, true); err != nil {
		t.Fatal(err)
	}
}

func TestStopDuringRejectedSubmissionDoesNotPoisonNextInput(t *testing.T) {
	for _, engine := range []string{"hermes", "hermes-status", "codex"} {
		t.Run(engine, func(t *testing.T) {
			marker := filepath.Join(t.TempDir(), "rejected")
			em := &collectEmitter{}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var enqueue func(string, string)
			var interrupt func()
			var stop func()
			if strings.HasPrefix(engine, "hermes") {
				fakeHermesGateway(t)
				if engine == "hermes-status" {
					t.Setenv("FAKE_HERMES_REJECT_STATUS", "1")
				}
				t.Setenv("FAKE_HERMES_REJECT_MARKER", marker)
				d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry[strings.TrimSuffix(engine, "-status")], WorkDir: t.TempDir(), Emitter: em})
				done := make(chan struct{})
				go func() { defer close(done); d.Run(ctx) }()
				stop = func() { cancel(); <-done }
				enqueue = d.Enqueue
				interrupt = d.Interrupt
			} else {
				bin := fakeCodex(t)
				t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
				t.Setenv("FAKE_CODEX_REJECT_MARKER", marker)
				d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry[strings.TrimSuffix(engine, "-status")], WorkDir: t.TempDir(), Emitter: em})
				stop = startTestCodexDriver(ctx, t, d)
				enqueue = d.Enqueue
				interrupt = d.Interrupt
			}
			defer stop()
			enqueue("reject", "chat")
			waitUntil(t, "submission held", func() bool { _, err := os.Stat(marker); return err == nil })
			interrupt()
			waitUntil(t, "submission rejected", func() bool { return countKind(em, "error") > 0 })
			enqueue("next", "chat")
			waitUntil(t, "next input finishes", func() bool { return em.count("user:next") == 1 && em.count("turn_done:end_turn") == 1 })
		})
	}
}

func TestHermesNativeTurnBeforeStopAckCanBeStopped(t *testing.T) {
	fakeHermesGateway(t)
	t.Setenv("FAKE_HERMES_HOLD", "1")
	t.Setenv("FAKE_HERMES_NATIVE_ACK_MARKER", filepath.Join(t.TempDir(), "ack"))
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	defer func() { cancel(); <-done }()
	d.Enqueue("first", "chat")
	waitUntil(t, "first input", func() bool { return em.count("user:first") == 1 })
	d.Interrupt()
	waitUntil(t, "native turn starts", func() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.turnEpoch >= 2 })
	time.Sleep(150 * time.Millisecond)
	d.Enqueue("next", "chat")
	d.Interrupt()
	waitUntil(t, "native turn stopped and queue delivered", func() bool { return em.count("turn_done:interrupted") == 2 && em.count("user:next") == 1 })
}

func TestHostStopInterruptsCodexChildWithIdleRoot(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_CHILD_ONLY", "1")
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("FAKE_CODEX_LOG", log)
	host, sender, _ := newTestHost(t, nil, "")
	host.cfg.RecordsDir = t.TempDir()
	host.Spawn(protocol.SpawnSession{SessionID: "child-only", Repo: "proj", Kind: "agent", Engine: "codex"})
	defer host.Kill("child-only")
	host.UserInput("child-only", agent.Input{Text: "start"})
	waitUntil(t, "owned active child under idle root", func() bool {
		sess := host.get("child-only")
		if sess.status.Load() != "idle" {
			return false
		}
		d := sess.loop.(*codexDriver)
		d.mu.Lock()
		app := d.app
		d.mu.Unlock()
		if app == nil {
			return false
		}
		app.mu.Lock()
		defer app.mu.Unlock()
		return app.childTurns["owned-child"] == "child-live"
	})
	host.Interrupt("child-only")
	waitUntil(t, "child interrupt RPC", func() bool {
		raw, _ := os.ReadFile(log)
		return strings.Contains(string(raw), "turn/interrupt ") && strings.Contains(string(raw), "child-live")
	})
	waitUntil(t, "child completion", func() bool {
		for _, e := range sender.agentEvents() {
			if e.Kind == "subagent_done" {
				return true
			}
		}
		return false
	})
	host.UserInput("child-only", agent.Input{Text: "next", ClientID: "next"})
	waitUntil(t, "input after child Stop", func() bool {
		for _, e := range sender.agentEvents() {
			if e.Kind == "user_message" && strings.Contains(string(e.Payload), "next") {
				return true
			}
		}
		return false
	})
	sess := host.get("child-only")
	sess.record.mu.Lock()
	retained := sess.record.rec.StopRequested
	sess.record.mu.Unlock()
	if retained {
		t.Fatal("completed child-only Stop permanently disabled recovery")
	}

}

func TestHostStopIdleBackgroundWaitEmitsResult(t *testing.T) {
	t.Setenv("BLERG_CLAUDE_STEERING", "1")
	_, log := installFakeClaude(t)
	t.Setenv("GO_FAKE_CLAUDE", "1")
	t.Setenv("FAKE_LOG", log)
	t.Setenv("FAKE_BACKGROUND", "1")
	host, sender, _ := newTestHost(t, nil, "")
	host.cfg.ClaudeCode = true
	host.Spawn(protocol.SpawnSession{SessionID: "idle-bg", Repo: "proj", Kind: "agent"})
	defer host.Kill("idle-bg")
	host.UserInput("idle-bg", agent.Input{Text: "BGIDLE start"})
	waitUntil(t, "idle background", func() bool {
		for _, e := range sender.agentEvents() {
			if e.Kind == "turn_done" {
				return true
			}
		}
		return false
	})
	host.Interrupt("idle-bg")
	waitUntil(t, "background wait stopped", func() bool {
		for _, e := range sender.agentEvents() {
			if e.Kind == "background_wait_stopped" {
				return true
			}
		}
		return false
	})
	if len(starts(logLines(t, log))) != 1 {
		t.Fatal("idle Stop restarted healthy engine")
	}
}

func TestHostStopIdleCodexBackgroundWaitEmitsResult(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_BACKGROUND", "1")
	host, sender, _ := newTestHost(t, nil, "")
	host.cfg.RecordsDir = t.TempDir()
	host.Spawn(protocol.SpawnSession{SessionID: "idle-terminal", Repo: "proj", Kind: "agent", Engine: "codex"})
	defer host.Kill("idle-terminal")
	host.UserInput("idle-terminal", agent.Input{Text: "start"})
	waitUntil(t, "idle terminal inventory", func() bool {
		for _, e := range sender.agentEvents() {
			if e.Kind == "background_task_started" {
				return true
			}
		}
		return false
	})
	host.Interrupt("idle-terminal")
	waitUntil(t, "background wait acknowledged", func() bool {
		for _, e := range sender.agentEvents() {
			if e.Kind == "background_wait_stopped" {
				return true
			}
		}
		return false
	})
	sess := host.get("idle-terminal")
	sess.record.mu.Lock()
	retained := sess.record.rec.StopRequested
	sess.record.mu.Unlock()
	sess.stopMu.Lock()
	guarded := !sess.stopCompleted.IsZero()
	sess.stopMu.Unlock()
	if !retained || !guarded {
		t.Fatalf("idle background Stop lost restart intent or double-click guard: retained=%v guarded=%v", retained, guarded)
	}

}
func TestCodexFallbackRetainsUnconfirmedTerminalInventory(t *testing.T) {
	shrink(t, &codexInterruptResultAfter, 80*time.Millisecond)
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_BACKGROUND", "1")
	t.Setenv("FAKE_CODEX_STUCK_STOP", filepath.Join(t.TempDir(), "stopped"))
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stop := startTestCodexDriver(ctx, t, d)
	defer stop()
	d.Enqueue("start", "chat")
	waitUntil(t, "background inventory", func() bool { return countKind(em, "background_task_started") == 1 })
	d.Interrupt()
	d.Enqueue("next", "chat")
	waitUntil(t, "replacement delivers next input", func() bool { return em.count("user:next") == 1 })
	if countKind(em, "background_task_done") != 0 {
		t.Fatal("root replacement falsely completed tool children", em.outline())
	}
}

func TestHostStopCanStopSteeredTurnWithoutIdleTransition(t *testing.T) {
	shrink(t, &ccInterruptKillAfter, 80*time.Millisecond)
	t.Setenv("BLERG_CLAUDE_STEERING", "1")
	_, log := installFakeClaude(t)
	t.Setenv("GO_FAKE_CLAUDE", "1")
	t.Setenv("FAKE_LOG", log)
	t.Setenv("FAKE_BACKGROUND", "1")
	t.Setenv("FAKE_TOOL_MS", "5000")
	host, sender, _ := newTestHost(t, nil, "")
	host.cfg.ClaudeCode = true
	host.Spawn(protocol.SpawnSession{SessionID: "steered-stop", Repo: "proj", Kind: "agent"})
	defer host.Kill("steered-stop")
	host.UserInput("steered-stop", agent.Input{Text: "TOOL first"})
	waitUntil(t, "background wait", func() bool {
		n := 0
		for _, e := range sender.agentEvents() {
			if e.Kind == "tool_call" {
				n++
			}
		}
		return n == 2
	})
	host.Interrupt("steered-stop")
	host.UserInput("steered-stop", agent.Input{Text: "HANG next", ClientID: "next"})
	waitUntil(t, "steered next consumed", func() bool {
		for _, e := range sender.agentEvents() {
			if e.Kind == "user_message" && strings.Contains(string(e.Payload), "HANG next") {
				return true
			}
		}
		return false
	})
	time.Sleep(550 * time.Millisecond)
	host.Interrupt("steered-stop")
	waitUntil(t, "steered turn stopped", func() bool {
		n := 0
		for _, e := range sender.agentEvents() {
			if e.Kind == "turn_done" {
				n++
			}
		}
		return n == 2
	})
}
func TestStopAfterDaemonRecoveryIgnoresCachedBrowserIdentity(t *testing.T) {
	newHost, dir, work, _ := recoveryHost(t)
	stubTranscripts(t, "conv-1")
	t.Setenv("BLERG_CLAUDE_STEERING", "1")
	_, log := installFakeClaude(t)
	t.Setenv("GO_FAKE_CLAUDE", "1")
	t.Setenv("FAKE_LOG", log)
	if err := writeAgentRecord(dir, AgentRecord{SessionID: "recovered-stop", Repo: "proj", WorkDir: work, ClaudeSessionID: "conv-1", Model: "claude-opus-5", StopRequested: true}); err != nil {
		t.Fatal(err)
	}
	host, sender := newHost()
	host.NoteConnected()
	if host.AdoptRecorded() != 1 {
		t.Fatal("recovery failed")
	}
	host.UserInput("recovered-stop", agent.Input{Text: "TOOL next"})
	waitUntil(t, "recovered engine running", func() bool {
		for _, e := range sender.agentEvents() {
			if e.Kind == "tool_call" {
				return true
			}
		}
		return false
	})
	host.Interrupt("recovered-stop", "cached-before-restart")
	waitUntil(t, "recovered turn interrupted", func() bool {
		for _, e := range sender.agentEvents() {
			if e.Kind == "turn_done" && strings.Contains(string(e.Payload), "interrupted") {
				return true
			}
		}
		return false
	})
}

func TestClaudeCancelAfterProcessGroupExitReturnsDone(t *testing.T) {
	d, em, _ := newSteeringDriver(t)
	d.Enqueue("one", "chat")
	waitIdle(t, em, 1)
	d.sm.Lock()
	p := d.steer.proc
	d.sm.Unlock()
	d.shutdown()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("engine did not exit")
	}
	if err := p.cmd.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Cancel after exit = %v, want process done", err)
	}
}

func TestClaudeSlashRejectedResumeKeepsUnconsumedInput(t *testing.T) {
	shrink(t, &ccSettleAfter, 40*time.Millisecond)
	d, em, log := newSteeringDriver(t)
	d.setCCSessionID("bad-fixture")
	d.EnqueueInput(agent.Input{Text: "/cost", ClientID: "command"})
	waitUntil(t, "command consumed after fresh start", func() bool { return em.count("user:/cost") == 1 })
	if len(starts(logLines(t, log))) != 2 {
		t.Fatal("rejected resume was not retried exactly once")
	}
}

func TestHostStopBypassesFullPendingEventBuffer(t *testing.T) {
	t.Setenv("BLERG_CLAUDE_STEERING", "1")
	_, log := installFakeClaude(t)
	t.Setenv("GO_FAKE_CLAUDE", "1")
	t.Setenv("FAKE_LOG", log)
	t.Setenv("FAKE_BACKGROUND", "1")
	t.Setenv("FAKE_TOOL_MS", "60000")
	host, sender, _ := newTestHost(t, nil, "")
	host.cfg.ClaudeCode = true
	host.cfg.RecordsDir = t.TempDir()
	host.Spawn(protocol.SpawnSession{SessionID: "full-stop", Repo: "proj", Kind: "agent"})
	sess := host.get("full-stop")
	defer host.Kill("full-stop")
	defer sess.emitter.close()
	host.UserInput("full-stop", agent.Input{Text: "TOOL wait"})
	waitUntil(t, "tool", func() bool {
		n := 0
		for _, e := range sender.agentEvents() {
			if e.Kind == "tool_call" {
				n++
			}
		}
		return n == 2
	})
	for {
		sess.emitter.mu.Lock()
		n := len(sess.emitter.pending)
		sess.emitter.mu.Unlock()
		if n >= pendingCap {
			break
		}
		sess.emitter.Emit(agent.Event{ClientEventID: ccUUID(), Ts: time.Now(), Kind: "check_in", Payload: map[string]string{"message": "waiting"}})
	}
	returned := make(chan struct{})
	go func() { host.Interrupt("full-stop"); close(returned) }()
	select {
	case <-returned:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop blocked behind ordinary pending output")
	}
	sess.record.mu.Lock()
	requested := sess.record.rec.StopRequested
	sess.record.mu.Unlock()
	if !requested {
		t.Fatal("Stop intent was not durable before interrupting")
	}
}

func TestStopNoticesKeepPendingReserveBounded(t *testing.T) {
	sender := &agentTestSender{}
	em := newWSEmitter(sender, "reserve", nil)
	defer em.close()
	for i := 0; i < pendingCap; i++ {
		em.Emit(agent.Event{ClientEventID: ccUUID(), Ts: time.Now(), Kind: "check_in", Payload: map[string]string{"message": "waiting"}})
	}
	for i := 0; i < 100; i++ {
		for _, state := range []string{"stopping", "stopped", "idle"} {
			em.Emit(agent.Event{ClientEventID: ccUUID(), Ts: time.Now(), Kind: "stop_result", Payload: map[string]string{"state": state, "message": state}})
		}
	}
	em.mu.Lock()
	n := len(em.pending)
	em.mu.Unlock()
	if n != pendingCap+3 {
		t.Fatalf("Stop reserve grew with repeated presses: %d", n)
	}
	events := sender.agentEvents()
	if len(events) != pendingCap+300 {
		t.Fatalf("Stop presses lost their visible outcomes: %d", len(events))
	}
}

func TestStopCooldownNoticePreservesNextTurnsRecoveryCalls(t *testing.T) {
	t.Setenv("BLERG_CLAUDE_STEERING", "1")
	_, log := installFakeClaude(t)
	t.Setenv("GO_FAKE_CLAUDE", "1")
	t.Setenv("FAKE_LOG", log)
	t.Setenv("FAKE_BACKGROUND", "1")
	t.Setenv("FAKE_TOOL_MS", "60000")
	host, sender, _ := newTestHost(t, nil, "")
	host.cfg.ClaudeCode = true
	host.cfg.RecordsDir = t.TempDir()
	host.Spawn(protocol.SpawnSession{SessionID: "cooldown-record", Repo: "proj", Kind: "agent"})
	defer host.Kill("cooldown-record")
	sess := host.get("cooldown-record")
	calls := func() int {
		sess.record.mu.Lock()
		defer sess.record.mu.Unlock()
		return len(sess.record.rec.OpenCalls)
	}
	host.UserInput("cooldown-record", agent.Input{Text: "TOOL first"})
	waitUntil(t, "first tool recovery record", func() bool { return calls() > 0 })
	host.Interrupt("cooldown-record")
	waitUntil(t, "first Stop completed", func() bool { sess.stopMu.Lock(); defer sess.stopMu.Unlock(); return !sess.stopCompleted.IsZero() })
	host.UserInput("cooldown-record", agent.Input{Text: "TOOL next"})
	waitUntil(t, "next tool recovery record", func() bool {
		n := 0
		for _, ev := range sender.agentEvents() {
			if ev.Kind == "tool_call" {
				n++
			}
		}
		return n >= 4 && calls() > 0
	})
	sess.stopMu.Lock()
	elapsed := time.Since(sess.stopCompleted)
	sess.stopMu.Unlock()
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("fixture missed cooldown: %v", elapsed)
	}
	host.Interrupt("cooldown-record")
	if calls() == 0 {
		t.Fatal("informational cooldown notice erased the next turn's recovery calls")
	}
}
