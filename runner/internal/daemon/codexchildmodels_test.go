package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

func TestCodexChildReroutePreservesKnownPrefixAndUnknownRemainder(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_CHILD_REROUTE", "1")
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopDriver := startTestCodexDriver(ctx, t, d)
	defer stopDriver()
	d.Enqueue("Review the fixture", "chat")
	waitUntil(t, "rerouted child completed", func() bool { return countKind(em, "subagent_done") == 1 })
	em.mu.Lock()
	defer em.mu.Unlock()
	for _, ev := range em.events {
		if ev.Kind != "subagent_done" {
			continue
		}
		raw, err := json.Marshal(ev.Payload)
		if err != nil {
			t.Fatal(err)
		}
		var p struct {
			Model   string                 `json:"model"`
			Usage   agent.Usage            `json:"usage"`
			ByModel map[string]agent.Usage `json:"usage_by_model"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		if p.Model != "fixture-rerouted" {
			t.Fatalf("child model %q ignored native reroute", p.Model)
		}
		if p.Usage != (agent.Usage{InputTokens: 35, CacheReadTokens: 15, OutputTokens: 10}) {
			t.Fatalf("aggregate usage changed: %+v", p.Usage)
		}
		if len(p.ByModel) != 2 || p.ByModel["fixture-original"] != (agent.Usage{InputTokens: 15, CacheReadTokens: 5, OutputTokens: 3}) || p.ByModel[""] != (agent.Usage{InputTokens: 20, CacheReadTokens: 10, OutputTokens: 7}) {
			t.Fatalf("model usage attribution: %+v", p.ByModel)
		}
	}
}

func TestCodexChildModelPartitionsSurviveSnapshotsAndResetOnResume(t *testing.T) {
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Emitter: em})
	d.childStatus("child", "fixture-original", "Review", "running")
	d.childUsage("child", agent.Usage{InputTokens: 10}, true)
	em.mu.Lock()
	snapshot := em.events[len(em.events)-1].Payload.(agent.SubagentPayload)
	em.mu.Unlock()
	d.childStatus("child", "fixture-next", "", "")
	d.childUsage("child", agent.Usage{InputTokens: 25}, true)
	d.childUsage("child", agent.Usage{InputTokens: 25}, true) // duplicate
	d.childUsage("child", agent.Usage{InputTokens: 12}, true) // stale
	if snapshot.UsageByModel["fixture-original"].InputTokens != 10 || len(snapshot.UsageByModel) != 1 {
		t.Fatal("later attribution mutated an emitted snapshot")
	}
	c := d.children["child"]
	if c.usage.InputTokens != 25 || c.usageByModel["fixture-original"].InputTokens != 10 || c.usageByModel[""].InputTokens != 15 {
		t.Fatalf("duplicates/stale totals changed attribution: %+v", c)
	}
	d.childStatus("child", "", "", "completed")
	d.childStatus("child", "", "Follow up", "running")
	d.childUsage("child", agent.Usage{InputTokens: 30}, true)
	if c.usage.InputTokens != 5 || len(c.usageByModel) != 1 || c.usageByModel["fixture-next"].InputTokens != 5 {
		t.Fatal("resumed run included previously settled spend")
	}
}

func TestCodexChildModelAttributionKeepsUnknownPrefixAndBoundsMetadata(t *testing.T) {
	d := newCodexDriver(AgentDriverOpts{Emitter: &collectEmitter{}})
	d.childStatus("child", "invalid model", "Review", "running")
	d.childUsage("child", agent.Usage{InputTokens: 1}, true)
	d.childStatus("child", "fixture-first", "", "")
	if d.children["child"].usageByModel[""].InputTokens != 1 {
		t.Fatal("later model retroactively relabeled unknown usage")
	}
	d.childUsage("child", agent.Usage{InputTokens: 2}, true)
	if d.children["child"].usageByModel[""].InputTokens != 2 || len(d.children["child"].usageByModel) != 1 {
		t.Fatal("first known model invented precision for unsampled suffix")
	}
	for n := 3; n <= 100; n++ {
		d.childStatus("child", fmt.Sprintf("fixture-%d", n), "", "")
		d.childUsage("child", agent.Usage{InputTokens: n}, true)
	}
	c := d.children["child"]
	if len(c.usageByModel) > 32 || c.usageByModel[""].InputTokens == 0 {
		t.Fatal("unbounded model partitions or dropped unknown spend")
	}
	var total agent.Usage
	for _, usage := range c.usageByModel {
		total = addNativeUsage(total, usage)
	}
	if total != c.usage || total.InputTokens != 100 {
		t.Fatal("partition does not reconcile with aggregate")
	}
}

func TestCodexChildLaunchHintDoesNotOverwriteEffectiveModel(t *testing.T) {
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Emitter: em})
	d.childStatus("child", "fixture-original", "Review", "running")
	d.childStatus("child", "fixture-rerouted", "", "")
	// A collaboration tool can finish after its child rerouted; its model
	// argument is the original launch request, not the child's effective model.
	d.appItem([]byte(`{"type":"collabAgentToolCall","id":"fixture-call","tool":"spawnAgent","model":"fixture-original","agentsStates":{"child":{"status":"completed"}}}`), true, "")
	em.mu.Lock()
	defer em.mu.Unlock()
	for _, ev := range em.events {
		if p, ok := ev.Payload.(agent.SubagentPayload); ok && ev.Kind == "subagent_done" && p.Model != "fixture-rerouted" {
			t.Fatalf("launch hint overwrote effective child model: %s", p.Model)
		}
	}
}

func TestCodexChildPartitionBoundReservesUnknownRemainder(t *testing.T) {
	d := newCodexDriver(AgentDriverOpts{Emitter: &collectEmitter{}})
	d.childStatus("child", "fixture-next", "Review", "running")
	c := d.children["child"]
	c.usageByModel = map[string]agent.Usage{}
	for i := 0; i < 31; i++ {
		c.usageByModel[fmt.Sprintf("fixture-%d", i)] = agent.Usage{InputTokens: 1}
	}
	c.usage = agent.Usage{InputTokens: 31}
	c.lastTotal = c.usage
	d.childUsage("child", agent.Usage{InputTokens: 32}, true)
	if len(c.usageByModel) != 32 || c.usageByModel[""].InputTokens != 1 {
		t.Fatalf("unknown remainder exceeded complete-partition bound: %+v", c.usageByModel)
	}
}
