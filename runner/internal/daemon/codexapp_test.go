package daemon

import (
	"context"
	"encoding/json"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/livestate"
	"github.com/blerglab/blerg-ai/runner/internal/models"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCodexSettingsAcknowledged(t *testing.T) {
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], Model: "gpt-5-codex", Emitter: em})
	d.SetModel("gpt-5-codex", "high", "ui")
	if !contains(em.kinds(), "model_changed") {
		t.Fatal("accepted settings must update saved and displayed model/effort")
	}
}
func TestCodexAppItems(t *testing.T) {
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], Emitter: em})
	d.appItem([]byte(`{"type":"mcpToolCall","id":"m1","server":"docs","tool":"read","arguments":{"q":"hello"},"status":"inProgress"}`), false, "")
	d.appItem([]byte(`{"type":"reasoning","id":"r1","summary":["Checking assumptions"],"content":[]}`), true, "")
	d.appItem([]byte(`{"type":"contextCompaction","id":"c1"}`), true, "")
	for _, k := range []string{"tool_call", "assistant_thinking", "compaction"} {
		if !contains(em.kinds(), k) {
			t.Errorf("missing %s", k)
		}
	}
	em.mu.Lock()
	defer em.mu.Unlock()
	p := em.events[0].Payload.(agent.ToolCallPayload)
	if p.Tool != "mcp__docs__read" {
		t.Fatalf("tool = %q", p.Tool)
	}
}

func TestCodexMidTurnSteering(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_HOLD_FIRST", "1")
	log := filepath.Join(bin, "calls.log")
	t.Setenv("FAKE_CODEX_LOG", log)
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	d.EnqueueInput(agent.Input{Text: "start", ClientID: "first"})
	waitUntil(t, "accepted first message", func() bool { return countKind(em, "user_message") == 1 })
	d.EnqueueInput(agent.Input{Text: "change direction", ClientID: "steering"})
	waitUntil(t, "steered turn completes", func() bool { return countKind(em, "turn_done") == 1 })
	raw, _ := os.ReadFile(log)
	if !strings.Contains(string(raw), `turn/steer`) || !strings.Contains(string(raw), `"expectedTurnId": "turn-1"`) {
		t.Fatalf("no guarded steering request: %s", raw)
	}
	if countKind(em, "user_message") != 2 {
		t.Fatal("accepted steering must be persisted")
	}
	d.mu.Lock()
	active := d.activeTurn
	d.mu.Unlock()
	if active {
		t.Fatal("completed persistent process must not count as active turn")
	}
}

func TestCodexAppServerReconnectsSavedThread(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	log := filepath.Join(bin, "calls.log")
	t.Setenv("FAKE_CODEX_LOG", log)
	t.Setenv("FAKE_CODEX_EXIT_ONCE", filepath.Join(bin, "exit-marker"))
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	d.Enqueue("first", "chat")
	waitUntil(t, "reconnect with saved thread", func() bool { raw, _ := os.ReadFile(log); return strings.Contains(string(raw), "thread/resume") })
	d.Enqueue("continue", "chat")
	waitUntil(t, "new turn after reconnect", func() bool { return countKind(em, "turn_done") >= 2 })
	if countKind(em, "user_message") != 2 {
		t.Fatal("reconnect lost an accepted input")
	}
}

func TestCodexChildLifecycleAndMetrics(t *testing.T) {
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], Emitter: em})
	d.childStatus("child", "gpt-5-codex", "Review", "started")
	d.appItem([]byte(`{"type":"collabAgentToolCall","id":"spawn","tool":"spawnAgent","model":"gpt-5-codex","agentsStates":{"child":{"status":"running","message":"Review"}}}`), true, "")
	d.appItem([]byte(`{"type":"commandExecution","id":"c1","command":"ls","status":"inProgress"}`), false, "child")
	d.childUsage("child", agent.Usage{InputTokens: 12, OutputTokens: 8}, true)
	d.childStatus("child", "", "Done", "completed")
	d.childStatus("child", "", "Done", "completed")
	if countKind(em, "subagent_started") != 1 || countKind(em, "subagent_done") != 1 {
		t.Fatalf("duplicate lifecycle: %v", em.kinds())
	}
	em.mu.Lock()
	defer em.mu.Unlock()
	for _, e := range em.events {
		if e.Kind == "subagent_done" {
			p := e.Payload.(agent.SubagentPayload)
			if p.ToolUses != 1 || p.Usage == nil || p.Usage.InputTokens != 12 {
				t.Fatalf("missing child metrics: %+v", p)
			}
		}
	}
}

func TestCodexTurnUsageKeepsOriginalModel(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_HOLD_FIRST", "1")
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], Model: "gpt-5-codex", WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	d.Enqueue("start", "chat")
	waitUntil(t, "first turn accepted", func() bool { return countKind(em, "user_message") == 1 })
	d.SetModel("gpt-5.6-terra", "high", "ui")
	d.Enqueue("finish", "chat")
	waitUntil(t, "turn complete", func() bool { return countKind(em, "turn_done") == 1 })
	em.mu.Lock()
	defer em.mu.Unlock()
	for _, e := range em.events {
		if e.Kind == "turn_done" && e.Payload.(agent.TurnDonePayload).Model != "gpt-5-codex" {
			t.Fatal("model change relabeled prior turn usage")
		}
	}
}

func TestCodexSlashSkillUsesRuntimeNativeInput(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_SKILL", "1")
	log := filepath.Join(bin, "calls.log")
	t.Setenv("FAKE_CODEX_LOG", log)
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	d.adoptionNote = "The host restarted."
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	d.EnqueueInput(agent.Input{Text: "/review inspect changes", ClientID: "skill"})
	waitUntil(t, "native skill turn", func() bool { return countKind(em, "turn_done") == 1 })
	raw, _ := os.ReadFile(log)
	if !strings.Contains(string(raw), `"type": "skill"`) || !strings.Contains(string(raw), `"path": "/opt/skills/review/SKILL.md"`) {
		t.Fatal("enabled runtime skill was not invoked natively")
	}
	em.mu.Lock()
	defer em.mu.Unlock()
	for _, e := range em.events {
		if p, ok := e.Payload.(agent.UserMessagePayload); ok && p.Text != "/review inspect changes" {
			t.Fatal("native skill path or recovery note leaked into user history")
		}
	}
}

func TestCodexAdoptionPersonTakesPrecedence(t *testing.T) {
	d := newCodexDriver(AgentDriverOpts{Emitter: &collectEmitter{}})
	d.Enqueue("do this instead", "chat")
	if d.continueAdopted("continue", "system continue") {
		t.Fatal("recovery must not race ahead of an accepted person message")
	}
	called := false
	if d.unlessHeardFromPerson(func() { called = true }) || called {
		t.Fatal("must not report pause after person message")
	}
}

func TestCodexOpenWorkRecoverySnapshots(t *testing.T) {
	d := newCodexDriver(AgentDriverOpts{Emitter: &collectEmitter{}})
	var tasks []agent.BackgroundTaskStartedPayload
	var subs []OpenSubagent
	d.onOpenWork = func(t []agent.BackgroundTaskStartedPayload, s []OpenSubagent) { tasks = t; subs = s }
	d.childStatus("child", "gpt-5-codex", "Review", "running")
	if len(tasks) != 1 || len(subs) != 1 || subs[0].AgentID != "child" {
		t.Fatal("live child was not recorded for recovery")
	}
	d.childStatus("child", "", "Done", "completed")
	if len(tasks) != 0 || len(subs) != 0 {
		t.Fatal("finished child remained in recovery snapshot")
	}
}

func TestCodexAdoptionQueuedContinueSupersededBeforeConsumption(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	if !d.continueAdopted("recovery continue", "system continue") {
		t.Fatal("expected queued recovery continuation")
	}
	d.Enqueue("person says stop the old task", "chat")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	waitUntil(t, "person turn", func() bool { return countKind(em, "turn_done") == 1 })
	em.mu.Lock()
	defer em.mu.Unlock()
	for _, e := range em.events {
		if p, ok := e.Payload.(agent.UserMessagePayload); ok && p.Source == "system" {
			t.Fatal("superseded recovery continuation reached Codex")
		}
	}
}

func TestCodexChildResumesAfterCompletion(t *testing.T) {
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Emitter: em})
	var tasks []agent.BackgroundTaskStartedPayload
	var subs []OpenSubagent
	d.onOpenWork = func(t []agent.BackgroundTaskStartedPayload, s []OpenSubagent) { tasks = t; subs = s }
	d.childStatus("child", "fixture-model", "First task", "running")
	d.appItem([]byte(`{"type":"commandExecution","id":"tool-first","command":"ls"}`), false, "child")
	d.childStatus("child", "", "First result", "completed")
	// A late usage notification is metadata, not evidence of a new run.
	d.childUsage("child", agent.Usage{InputTokens: 10}, true)
	if countKind(em, "subagent_started") != 1 || len(subs) != 0 {
		t.Fatal("late metadata reopened completed child")
	}
	d.childStatus("child", "", "Follow-up task", "running")
	if countKind(em, "subagent_started") != 2 || len(tasks) != 1 || len(subs) != 1 {
		t.Fatalf("follow-up did not reopen child: %v", em.kinds())
	}
	em.mu.Lock()
	for _, e := range em.events {
		if p, ok := e.Payload.(agent.SubagentPayload); ok && e.Kind == "subagent_started" && p.Summary == "Follow-up task" {
			if !p.Resumed || !p.Background {
				t.Errorf("follow-up lifecycle flags = %+v", p)
			}
		}
	}
	em.mu.Unlock()

	// The canonical reducer must see a distinct live run despite the earlier end.
	em.mu.Lock()
	events := []livestate.Event{}
	for i, event := range em.events {
		payload, _ := json.Marshal(event.Payload)
		events = append(events, livestate.Event{Seq: int64(i + 1), ID: event.ClientEventID, Kind: event.Kind, Payload: payload, Ts: event.Ts})
	}
	em.mu.Unlock()
	state := livestate.Build(events, livestate.Options{Now: time.Now(), Status: "idle", ShowTokens: true})
	if len(state.Subagents) != 1 || !state.Subagents[0].Resumed || state.Subagents[0].Key == "child" {
		t.Fatalf("canonical Live reducer lost follow-up: %+v", state)
	}
	resumedID := state.Subagents[0].Key
	if subs[0].ChildID != resumedID || subs[0].AgentID != "child" {
		t.Fatalf("recovery lost native/run mapping: %+v", subs)
	}
	d.appItem([]byte(`{"type":"agentMessage","id":"message-followup","text":"Follow-up progress"}`), true, "child")
	em.mu.Lock()
	last := em.events[len(em.events)-1].Payload.(agent.AssistantTextPayload)
	em.mu.Unlock()
	if last.ParentCallID != resumedID {
		t.Fatalf("follow-up text parent = %q, want %q", last.ParentCallID, resumedID)
	}

	d.appItem([]byte(`{"type":"commandExecution","id":"tool-followup","command":"pwd"}`), false, "child")
	d.childUsage("child", agent.Usage{InputTokens: 35}, true)
	if child := d.children["child"]; child.tools != 1 || child.usage.InputTokens != 25 {
		t.Fatalf("resumed counters include previous run: tools=%d usage=%+v", child.tools, child.usage)
	}
	d.childStatus("child", "", "Second result", "completed")
	d.childStatus("child", "", "Second result", "completed")
	if countKind(em, "subagent_done") != 2 || len(tasks) != 0 || len(subs) != 0 {
		t.Fatalf("second completion did not close child exactly once: %v", em.kinds())
	}
	d.childStatus("child", "", "Third task", "running")
	third := d.children["child"]
	if third.resumedFrom != resumedID || third.callID == resumedID || third.tools != 0 || third.usage != (agent.Usage{}) {
		t.Fatalf("third run did not chain/reset: %+v", third)
	}
	d.childUsage("child", agent.Usage{InputTokens: 40}, true)
	if third.usage.InputTokens != 5 {
		t.Fatalf("third run token delta = %+v", third.usage)
	}

}

func TestCodexChildNotFoundClosesLiveWork(t *testing.T) {
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Emitter: em})
	d.childStatus("child", "fixture-model", "Task", "running")
	d.childStatus("child", "", "", "notFound")
	if countKind(em, "subagent_done") != 1 || !d.children["child"].done {
		t.Fatalf("missing child remains live: %v", em.kinds())
	}
}

func TestCodexTokenUsageSeparatesCachedInput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		native codexTokenUsage
		want   agent.Usage
	}{
		{"uncached", codexTokenUsage{Input: 100, Output: 20}, agent.Usage{InputTokens: 100, OutputTokens: 20}},
		{"cached-subset", codexTokenUsage{Input: 100, Cached: 80, Output: 20}, agent.Usage{InputTokens: 20, CacheReadTokens: 80, OutputTokens: 20}},
		{"all-cached", codexTokenUsage{Input: 100, Cached: 100}, agent.Usage{CacheReadTokens: 100}},
		{"inconsistent-cache", codexTokenUsage{Input: 10, Cached: 20}, agent.Usage{CacheReadTokens: 20}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.native.usage(); got != tc.want {
				t.Fatalf("usage = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestCodexCapabilitiesUnavailableMCPIsNotEmptyInventory(t *testing.T) {
	for _, mode := range []string{"complete", "partial", "missing-data", "null-data"} {
		t.Run(mode, func(t *testing.T) {
			bin := fakeCodex(t)
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv("FAKE_CODEX_MCP_FAILURE", mode)
			em := &collectEmitter{}
			d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go d.Run(ctx)
			waitUntil(t, "capability inventory", func() bool { return countKind(em, "capabilities") > 0 })
			em.mu.Lock()
			defer em.mu.Unlock()
			for _, e := range em.events {
				if p, ok := e.Payload.(protocol.CapabilitiesPayload); ok {
					for _, g := range p.Groups {
						if g.ID == protocol.CapGroupMCP {
							t.Fatalf("failed MCP inventory advertised as authoritative: %+v", g)
						}
					}
				}
			}
		})
	}
}

func TestCodexModelSwitchReconcilesReportedEffort(t *testing.T) {
	for _, tc := range []struct {
		name, old, requested, want string
		accepted                   bool
	}{
		{"preserve-supported", "high", "", "high", true},
		{"reset-unsupported", "ultra", "", "medium", true},
		{"explicit-supported", "ultra", "low", "low", true},
		{"reject-unsupported", "low", "ultra", "low", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			em := &collectEmitter{}
			d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], Model: "fixture-original", Effort: tc.old, Emitter: em})
			d.modelCatalog = map[string]models.Model{"fixture-next": {ID: "fixture-next", Efforts: []string{"low", "medium", "high"}, DefaultEffort: "medium"}}
			d.SetModel("fixture-next", tc.requested, "ui")
			if d.effort != tc.want || (d.model == "fixture-next") != tc.accepted {
				t.Fatalf("model=%q effort=%q", d.model, d.effort)
			}
		})
	}
}

func TestCodexCapabilitiesRefreshWhenMCPChanges(t *testing.T) {
	for _, notification := range []string{"mcpServer/startupStatus/updated", "mcpServer/oauthLogin/completed"} {
		t.Run(notification, func(t *testing.T) {
			bin := fakeCodex(t)
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv("FAKE_CODEX_MCP_TRANSITION", notification)
			log := filepath.Join(bin, "calls.log")
			t.Setenv("FAKE_CODEX_LOG", log)
			em := &collectEmitter{}
			d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go d.Run(ctx)
			waitUntil(t, "MCP connection state refreshed", func() bool {
				em.mu.Lock()
				defer em.mu.Unlock()
				for _, e := range em.events {
					if p, ok := e.Payload.(protocol.CapabilitiesPayload); ok {
						for _, g := range p.Groups {
							if g.ID == protocol.CapGroupMCP && len(g.Items) == 1 && g.Items[0].Status == "connected" {
								return true
							}
						}
					}
				}
				return false
			})
			raw, _ := os.ReadFile(log)
			if calls := strings.Count(string(raw), "mcpServerStatus/list "); calls > 3 {
				t.Fatalf("notification burst triggered %d inventory reads", calls)
			}
		})
	}
}

func TestCodexNativeChildFollowupHasDistinctLifecycle(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_CHILD_FOLLOWUP", "1")
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	d.Enqueue("Run child tasks", "chat")
	waitUntil(t, "main turn finished", func() bool { return countKind(em, "turn_done") == 1 })
	waitUntil(t, "child follow-up runs finished", func() bool { return countKind(em, "subagent_done") == 2 })
	if countKind(em, "subagent_started") != 2 || countKind(em, "subagent_done") != 2 {
		t.Fatalf("missing follow-up lifecycle: %v", em.kinds())
	}
	em.mu.Lock()
	defer em.mu.Unlock()
	ids := []string{}
	for _, e := range em.events {
		if e.Kind == "subagent_started" {
			ids = append(ids, e.Payload.(agent.SubagentPayload).ChildID)
		}
	}
	if ids[0] == ids[1] {
		t.Fatal("distinct runs share a canonical child ID")
	}
	for _, e := range em.events {
		if p, ok := e.Payload.(agent.AssistantTextPayload); ok && p.Text == "Follow-up result" && p.ParentCallID != ids[1] {
			t.Fatalf("follow-up text routed to wrong run: %+v", p)
		}
	}
}

func TestCodexNativeModelCatalogConstrainsNextTurn(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_MODELS", `{"data":[{"id":"gpt-5-codex","model":"gpt-5-codex","defaultReasoningEffort":"high","supportedReasoningEfforts":[{"reasoningEffort":"high"},{"reasoningEffort":"ultra"}]},{"id":"fixture-next","model":"fixture-next","defaultReasoningEffort":"medium","supportedReasoningEfforts":[{"reasoningEffort":"low"},{"reasoningEffort":"medium"}]}],"nextCursor":null}`)
	log := filepath.Join(bin, "calls.log")
	t.Setenv("FAKE_CODEX_LOG", log)
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	waitUntil(t, "initialized runtime", func() bool { return countKind(em, "model_changed") > 0 })
	d.SetModel("gpt-5-codex", "ultra", "ui")
	d.SetModel("fixture-next", "", "ui")
	d.Enqueue("Use next model", "chat")
	waitUntil(t, "next model turn", func() bool { return countKind(em, "turn_done") == 1 })
	raw, _ := os.ReadFile(log)
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "turn/start ") {
			var p struct{ Model, Effort string }
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "turn/start ")), &p); err != nil {
				t.Fatal(err)
			}
			if p.Model != "fixture-next" || p.Effort != "medium" {
				t.Fatalf("next-turn config: %+v", p)
			}
			return
		}
	}
	t.Fatal("no native turn start")
}

func TestCodexSteeringCompletionRaceKeepsMessage(t *testing.T) {
	for _, mode := range []string{"before", "after", "other-error"} {
		t.Run(mode, func(t *testing.T) {
			bin := fakeCodex(t)
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			t.Setenv("FAKE_CODEX_STEER_RACE", mode)
			log := filepath.Join(bin, "calls.log")
			t.Setenv("FAKE_CODEX_LOG", log)
			em := &collectEmitter{}
			d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go d.Run(ctx)
			d.EnqueueInput(agent.Input{Text: "First", ClientID: "first"})
			waitUntil(t, "first accepted", func() bool { return countKind(em, "user_message") == 1 })
			d.EnqueueInput(agent.Input{Text: "Queued follow-up", ClientID: "followup"})
			if mode == "other-error" {
				waitUntil(t, "explicit rejection", func() bool { return countKind(em, "error") > 0 })
				if countKind(em, "user_message") != 1 {
					t.Fatal("unrecognized rejection was retried")
				}
				return
			}
			waitUntil(t, "follow-up completion", func() bool { return countKind(em, "turn_done") == 2 || countKind(em, "error") > 0 })
			if countKind(em, "turn_done") != 2 {
				t.Fatalf("follow-up was rejected instead of retained: %v", em.kinds())
			}
			em.mu.Lock()
			accepted := 0
			for _, e := range em.events {
				if p, ok := e.Payload.(agent.UserMessagePayload); ok && p.ClientID == "followup" {
					accepted++
				}
				if p, ok := e.Payload.(agent.ErrorPayload); ok && p.ClientID == "followup" {
					t.Errorf("safe steer retry emitted delivery failure: %+v", p)
				}
			}
			em.mu.Unlock()
			if accepted != 1 {
				t.Fatalf("follow-up accepted %d times", accepted)
			}
			raw, _ := os.ReadFile(log)
			if starts, steers := strings.Count(string(raw), "turn/start "), strings.Count(string(raw), "turn/steer "); starts != 2 || steers != 1 {
				t.Fatalf("start/steer counts = %d/%d", starts, steers)
			}
		})
	}
}

type codexSlowTextEmitter struct{ *collectEmitter }

func (e codexSlowTextEmitter) Emit(event agent.Event) {
	if event.Kind == "assistant_text" {
		time.Sleep(time.Millisecond)
	}
	e.collectEmitter.Emit(event)
}

func TestCodexStreamingBacklogDoesNotStarveSteering(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_STREAM_FIRST", "1")
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: codexSlowTextEmitter{em}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	d.EnqueueInput(agent.Input{Text: "Start streaming", ClientID: "first"})
	waitUntil(t, "streamed output", func() bool { return countKind(em, "assistant_text") > 0 })
	d.EnqueueInput(agent.Input{Text: "Change direction", ClientID: "steer"})
	waitUntil(t, "accepted steering", func() bool { return countKind(em, "user_message") == 2 })
	if countKind(em, "assistant_text") >= 1000 {
		t.Fatal("steering waited for the entire streamed-output backlog")
	}
}

func TestCodexRejectedSteerSurvivesMissingCompletion(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_STEER_RACE", "missing-completion")
	log := filepath.Join(bin, "calls.log")
	t.Setenv("FAKE_CODEX_LOG", log)
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	d.EnqueueInput(agent.Input{Text: "First", ClientID: "first"})
	waitUntil(t, "first accepted", func() bool { return countKind(em, "user_message") == 1 })
	d.EnqueueInput(agent.Input{Text: "Preserve this input", ClientID: "followup"})
	d.EnqueueInput(agent.Input{Text: "Later input", ClientID: "later"})
	waitUntil(t, "retained input accepted after reconnect", func() bool { return countKind(em, "user_message") >= 2 })
	em.mu.Lock()
	messages := []string{}
	for _, e := range em.events {
		if p, ok := e.Payload.(agent.UserMessagePayload); ok {
			messages = append(messages, p.ClientID)
		}
	}
	em.mu.Unlock()
	if len(messages) < 2 || messages[0] != "first" || messages[1] != "followup" {
		t.Fatalf("input order after reconnect: %v", messages)
	}
	raw, _ := os.ReadFile(log)
	if !strings.Contains(string(raw), "thread/resume ") {
		t.Fatal("missing-completion recovery did not resume saved native thread")
	}
}

func TestCodexMCPAuthStatusUsesSharedContract(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_MCP_STATUS", "authenticationRequired")
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	waitUntil(t, "capabilities", func() bool { return countKind(em, "capabilities") > 0 })
	em.mu.Lock()
	defer em.mu.Unlock()
	for _, e := range em.events {
		if p, ok := e.Payload.(protocol.CapabilitiesPayload); ok {
			for _, g := range p.Groups {
				if g.ID == protocol.CapGroupMCP && len(g.Items) == 1 {
					if g.Items[0].Status != "needs-auth" {
						t.Fatalf("noncanonical MCP auth status: %q", g.Items[0].Status)
					}
					return
				}
			}
		}
	}
	t.Fatal("MCP inventory missing")
}

func TestCodexMissingAcceptedTurnIdentityStopsUnguardedWork(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_MISSING_TURN_ID", "1")
	log := filepath.Join(bin, "calls.log")
	t.Setenv("FAKE_CODEX_LOG", log)
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	d.EnqueueInput(agent.Input{Text: "Unconfirmed work", ClientID: "first"})
	d.EnqueueInput(agent.Input{Text: "Next work", ClientID: "next"})
	waitUntil(t, "both inputs settled", func() bool { return countKind(em, "error") >= 2 })
	raw, _ := os.ReadFile(log)
	apps := strings.Split(string(raw), "codex app-server")
	if len(apps) < 3 || strings.Count(apps[1], "turn/start ") != 1 {
		t.Fatal("missing accepted turn ID allowed another unguarded start on the same app-server")
	}
	if countKind(em, "user_message") != 0 {
		t.Fatal("ambiguous acceptance was reported as confirmed")
	}
}

func TestCodexFreshDriverDoesNotReuseEndedNativeChildID(t *testing.T) {
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Emitter: em})
	var recovery []OpenSubagent
	d.onOpenWork = func(_ []agent.BackgroundTaskStartedPayload, subs []OpenSubagent) { recovery = subs }
	// Older daemons used the native thread ID as the canonical first-run ID.
	// A fresh driver may rediscover that same child through a resumed tool call.
	now := time.Now()
	events := []livestate.Event{
		{Seq: 1, ID: "old-start", Kind: "subagent_started", Ts: now.Add(-2 * time.Minute), Payload: json.RawMessage(`{"child_id":"native-child","agent_type":"Codex","summary":"Old task"}`)},
		{Seq: 2, ID: "old-end", Kind: "subagent_done", Ts: now.Add(-time.Minute), Payload: json.RawMessage(`{"child_id":"native-child","agent_type":"Codex","summary":"Old result"}`)},
	}
	d.childStatus("native-child", "fixture-model", "New task", "running")
	for i, event := range em.events {
		payload, _ := json.Marshal(event.Payload)
		events = append(events, livestate.Event{Seq: int64(i + 3), ID: event.ClientEventID, Kind: event.Kind, Ts: event.Ts, Payload: payload})
	}
	state := livestate.Build(events, livestate.Options{Now: time.Now(), Status: "running", ShowTokens: true})
	if len(state.Subagents) != 1 || state.Subagents[0].Key == "native-child" {
		t.Fatalf("fresh run collided with previous ended native identity: %+v", state)
	}
	if len(recovery) != 1 || recovery[0].AgentID != "native-child" || recovery[0].ChildID != state.Subagents[0].Key {
		t.Fatalf("native recovery identity lost: %+v", recovery)
	}
}
