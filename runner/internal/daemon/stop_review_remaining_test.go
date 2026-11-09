package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestClaudeSecondStopDuringQueueWaitInterruptsCurrentInput(t *testing.T) {
	shrink(t, &ccInterruptKillAfter, 2*time.Second)
	shrink(t, &ccInterruptResultAfter, 2*time.Second)
	t.Setenv("BLERG_CLAUDE_STEERING", "1")
	_, log := installFakeClaude(t)
	for k, v := range map[string]string{"GO_FAKE_CLAUDE": "1", "FAKE_LOG": log, "FAKE_BACKGROUND": "1", "FAKE_QUEUE_DURING_TOOL": "1", "FAKE_TOOL_MS": "60000", "FAKE_NEXT_INIT_MS": "1100"} {
		t.Setenv(k, v)
	}
	host, sender, _ := newTestHost(t, nil, "")
	host.cfg.ClaudeCode = true
	host.Spawn(protocol.SpawnSession{SessionID: "second-queue-stop", Repo: "proj", Kind: "agent"})
	defer host.Kill("second-queue-stop")
	sess := host.get("second-queue-stop")
	d := sess.loop.(*claudeCodeDriver)
	host.UserInput("second-queue-stop", agent.Input{Text: "TOOL first"})
	waitUntil(t, "tool wait", func() bool { return sender.countKind("tool_call") == 2 })
	host.UserInput("second-queue-stop", agent.Input{Text: "DELAYINIT next", ClientID: "next"})
	host.Interrupt("second-queue-stop")
	waitUntil(t, "queued input waiting for init", func() bool {
		d.sm.Lock()
		defer d.sm.Unlock()
		p := d.steer.proc
		return p != nil && p.interruptQueueWait && len(d.steer.awaiting) == 1
	})
	time.Sleep(550 * time.Millisecond)
	host.Interrupt("second-queue-stop")
	waitUntil(t, "second turn ended", func() bool { return sender.countKind("turn_done") == 2 })
	interrupted := 0
	for _, ev := range sender.agentEvents() {
		if ev.Kind == "turn_done" {
			var p agent.TurnDonePayload
			_ = json.Unmarshal(ev.Payload, &p)
			if p.StopReason == "interrupted" {
				interrupted++
			}
		}
	}
	if interrupted != 2 {
		t.Fatalf("second Stop did not interrupt current input: interrupted=%d", interrupted)
	}
	if len(starts(logLines(t, log))) != 1 {
		t.Fatal("cooperative queue Stop restarted the engine")
	}
}

func TestClaudeWarmSlowInitDoesNotReuseExpiredStopDeadline(t *testing.T) {
	shrink(t, &ccInterruptKillAfter, 70*time.Millisecond)
	d, em, log := newSteeringDriver(t, "FAKE_NEXT_INIT_MS=260", "FAKE_ECHO_STEER=1")
	d.Enqueue("warmup", "chat")
	waitIdle(t, em, 1)
	d.Enqueue("DELAYINIT next", "chat")
	waitUntil(t, "warm input awaiting init", func() bool {
		d.sm.Lock()
		defer d.sm.Unlock()
		return len(d.steer.awaiting) == 1 && !d.steer.proc.inTurn
	})
	d.Interrupt()
	waitIdle(t, em, 2)
	if len(starts(logLines(t, log))) != 1 || em.count("user:DELAYINIT next") != 1 || em.count("turn_done:interrupted") != 1 {
		t.Fatalf("warm Stop killed and replayed input: starts=%v events=%v", starts(logLines(t, log)), em.outline())
	}
}

func TestClaudeEngineExitClosesOwnedBackgroundWork(t *testing.T) {
	for _, mode := range []string{"streaming", "per-turn"} {
		t.Run(mode, func(t *testing.T) {
			env, _ := installFakeClaude(t)
			em := &collectEmitter{}
			d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, env)
			d.steer.legacy = mode == "per-turn"
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { d.Run(ctx); close(done) }()
			defer func() { cancel(); <-done }()
			d.Enqueue("MIXED work", "chat")
			waitIdle(t, em, 1)
			turns := 1
			if mode == "streaming" {
				d.sm.Lock()
				p := d.steer.proc
				d.sm.Unlock()
				if err := p.cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				<-p.done
				// An end nobody asked for wakes the agent with the news first.
				waitIdle(t, em, turns+1)
				turns++
				if !strings.Contains(strings.Join(em.outline(), "\n"), "background work was running") {
					t.Fatalf("the engine's end was not told: %v", em.outline())
				}
			}
			d.subMu.Lock()
			bg, subs, calls := len(d.bgTasks), len(d.subagents), len(d.bgCalls)
			bashOnly := bg == 1 && d.bgTasks["mix-bash-task"] != nil
			d.subMu.Unlock()
			if !bashOnly || subs != 0 || calls != 0 {
				t.Fatalf("engine-owned work survived exit: tasks=%d subagents=%d pending=%d", bg, subs, calls)
			}
			if active := d.backgroundRunning(); active != 0 {
				t.Fatalf("ended engine's retained Bash blocks admission: active=%d", active)
			}
			d.Enqueue("replacement work", "chat")
			waitIdle(t, em, turns+1)
			applied := make(chan struct{})
			if !d.queuePluginUpdate(func(context.Context) { close(applied) }) {
				t.Fatal("plugin update rejected")
			}
			select {
			case <-applied:
			case <-time.After(3 * time.Second):
				t.Fatal("retained Bash blocked plugin update after replacement turn")
			}
			d.sm.Lock()
			turn := d.usageTurnID
			d.sm.Unlock()
			if turn == "" {
				t.Fatal("replacement turn has no resume identity")
			}
			d.EnqueueInput(agent.Input{Text: "resume work", Source: "limit_resume", ExpectedTurnID: turn})
			waitIdle(t, em, turns+2)
			if em.count("user:resume work") != 1 {
				t.Fatalf("limit resume was not consumed once: %v", em.outline())
			}
			d.subMu.Lock()
			retained := d.bgTasks["mix-bash-task"] != nil
			d.subMu.Unlock()
			if !retained {
				t.Fatal("uncertain Bash work disappeared from the listing")
			}
			if countKind(em, "background_task_done") != 2 {
				t.Fatalf("engine-owned background tasks not closed: %v", em.outline())
			}
			ended := map[string]bool{}
			for _, ev := range em.snapshot() {
				if ev.Kind == "subagent_done" {
					p := ev.Payload.(agent.SubagentPayload)
					if strings.Contains(p.Summary, "engine process ended") {
						ended[p.ChildID] = true
					}
				}
			}
			if !ended["mix-agent"] || !ended["mix-foreground"] {
				t.Fatalf("subagent runs not closed: %v", ended)
			}
		})
	}
}

func TestCodexChildOnlyStopHasCompletionDeadline(t *testing.T) {
	for _, refusal := range []bool{false, true} {
		t.Run(map[bool]string{false: "fallback", true: "refusal-retry"}[refusal], func(t *testing.T) {
			shrink(t, &codexInterruptResultAfter, 70*time.Millisecond)
			bin := fakeCodex(t)
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv("FAKE_CODEX_CHILD_ONLY", "1")
			t.Setenv("FAKE_CODEX_STUCK_STOP", filepath.Join(t.TempDir(), "interrupted"))
			host, sender, _ := newTestHost(t, nil, "")
			var refused atomic.Bool
			refused.Store(refusal)
			host.cfg.StartCommand = func(cmd *exec.Cmd) error {
				cmd.Cancel = func() error {
					if refused.Load() {
						return errors.New("fixture cleanup refusal")
					}
					return cmd.Process.Kill()
				}
				return cmd.Start()
			}
			host.Spawn(protocol.SpawnSession{SessionID: "silent-child", Repo: "proj", Kind: "agent", Engine: "codex"})
			defer host.Kill("silent-child")
			sess := host.get("silent-child")
			d := sess.loop.(*codexDriver)
			defer refused.Store(false)
			host.UserInput("silent-child", agent.Input{Text: "start"})
			waitUntil(t, "owned silent child", func() bool {
				if sess.status.Load() != "idle" {
					return false
				}
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
			host.Interrupt("silent-child")
			if refusal {
				waitUntil(t, "child Stop refusal", func() bool {
					for _, ev := range sender.agentEvents() {
						if ev.Kind == "error" {
							var p agent.ErrorPayload
							_ = json.Unmarshal(ev.Payload, &p)
							if p.Code == agent.ErrorCodeStopFailed {
								return true
							}
						}
					}
					return false
				})
				sess.stopMu.Lock()
				stuck := sess.stopInProgress
				sess.stopMu.Unlock()
				if stuck {
					t.Fatal("cleanup refusal kept Stop latched")
				}
				refused.Store(false)
				host.Interrupt("silent-child")
			}
			waitUntil(t, "child ended after fallback", func() bool { return sender.countKind("subagent_done") > 0 })
			sess.stopMu.Lock()
			stuck := sess.stopInProgress
			sess.stopMu.Unlock()
			if stuck {
				t.Fatal("child-only Stop never completed")
			}
			for _, ev := range sender.agentEvents() {
				if ev.Kind == "error" && strings.Contains(string(ev.Payload), "disconnected") {
					t.Fatal("planned child Stop reported a disconnection")
				}
			}
		})
	}
}

func TestCodexChildOnlyStopHoldsQueuedInput(t *testing.T) {
	shrink(t, &codexInterruptResultAfter, 250*time.Millisecond)
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_CHILD_ONLY", "1")
	t.Setenv("FAKE_CODEX_STUCK_STOP", filepath.Join(t.TempDir(), "interrupted"))
	host, sender, _ := newTestHost(t, nil, "")
	host.Spawn(protocol.SpawnSession{SessionID: "child-queue", Repo: "proj", Kind: "agent", Engine: "codex"})
	defer host.Kill("child-queue")
	sess := host.get("child-queue")
	d := sess.loop.(*codexDriver)
	waitChild := func() bool {
		if sess.status.Load() != "idle" {
			return false
		}
		d.mu.Lock()
		a := d.app
		d.mu.Unlock()
		if a == nil {
			return false
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.childTurns["owned-child"] == "child-live"
	}
	host.UserInput("child-queue", agent.Input{Text: "start"})
	waitUntil(t, "owned child", waitChild)
	host.Interrupt("child-queue")
	host.UserInput("child-queue", agent.Input{Text: "next", ClientID: "next"})
	time.Sleep(100 * time.Millisecond)
	if sender.countKind("user_message") != 1 {
		t.Fatal("queued input reached the root while its child Stop was still pending")
	}
	waitUntil(t, "queued input after child Stop", func() bool { return sender.countKind("subagent_done") > 0 && sender.countKind("user_message") == 2 })
}

func TestHostStopReportsNaturalCompletionHonestly(t *testing.T) {
	shrink(t, &ccInterruptKillAfter, 2*time.Second)
	t.Setenv("BLERG_CLAUDE_STEERING", "1")
	_, log := installFakeClaude(t)
	for k, v := range map[string]string{"GO_FAKE_CLAUDE": "1", "FAKE_LOG": log, "FAKE_IGNORE_INTERRUPT": "1", "FAKE_TOOL_MS": "350"} {
		t.Setenv(k, v)
	}
	host, sender, _ := newTestHost(t, nil, "")
	host.cfg.ClaudeCode = true
	host.Spawn(protocol.SpawnSession{SessionID: "natural-stop", Repo: "proj", Kind: "agent"})
	defer host.Kill("natural-stop")
	host.UserInput("natural-stop", agent.Input{Text: "TOOL finishes naturally"})
	waitUntil(t, "tool", func() bool { return sender.countKind("tool_call") == 1 })
	host.Interrupt("natural-stop")
	waitUntil(t, "natural result", func() bool { return sender.countKind("turn_done") == 1 })
	for _, ev := range sender.agentEvents() {
		if ev.Kind == "stop_result" {
			var p map[string]string
			_ = json.Unmarshal(ev.Payload, &p)
			if p["state"] == "stopped" {
				t.Fatal("Stop claimed it canceled work that ended naturally")
			}
			if p["message"] == "The current work ended before Stop took effect." {
				return
			}
		}
	}
	t.Fatal("natural completion had no truthful Stop outcome")
}

func TestCodexChildStopMissingAckWaitsForCompletion(t *testing.T) {
	shrink(t, &codexInterruptResultAfter, 1500*time.Millisecond)
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_CHILD_ONLY", "1")
	t.Setenv("FAKE_CODEX_STOP_NO_ACK", "1")
	t.Setenv("FAKE_CODEX_CHILD_COMPLETION_MS", "900")
	t.Setenv("FAKE_CODEX_STUCK_STOP", filepath.Join(t.TempDir(), "interrupt"))
	host, sender, _ := newTestHost(t, nil, "")
	host.Spawn(protocol.SpawnSession{SessionID: "child-no-ack", Repo: "proj", Kind: "agent", Engine: "codex"})
	defer host.Kill("child-no-ack")
	sess := host.get("child-no-ack")
	d := sess.loop.(*codexDriver)
	host.UserInput("child-no-ack", agent.Input{Text: "start"})
	var app *codexApp
	waitUntil(t, "owned child", func() bool {
		if sess.status.Load() != "idle" {
			return false
		}
		d.mu.Lock()
		app = d.app
		d.mu.Unlock()
		if app == nil {
			return false
		}
		app.mu.Lock()
		defer app.mu.Unlock()
		return app.childTurns["owned-child"] == "child-live"
	})
	app.mu.Lock()
	app.rpcTimeout = 300 * time.Millisecond
	app.mu.Unlock()
	host.Interrupt("child-no-ack")
	waitUntil(t, "child completion", func() bool { return sender.countKind("subagent_done") == 1 })
	select {
	case <-app.done:
		t.Fatal("missing ACK killed a child still within its completion deadline")
	default:
	}
	if sender.countKind("error") != 0 {
		t.Fatal("missing ACK was reported as a failed Stop")
	}
}
