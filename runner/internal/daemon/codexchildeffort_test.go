package daemon

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

func TestCodexChildEffortRecoveryAndModelBoundary(t *testing.T) {
	var records []OpenSubagent
	if err := json.Unmarshal([]byte(`[{"agent_id":"child","child_id":"run","model":"fixture-child","effort":"low"}]`), &records); err != nil {
		t.Fatal(err)
	}
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Emitter: em, Effort: "high"})
	d.seedKnownChildren(records)
	d.childStatus("child", "", "review", "running")
	assertEffort := func(want string) {
		t.Helper()
		em.mu.Lock()
		raw, _ := json.Marshal(em.events[len(em.events)-1].Payload)
		em.mu.Unlock()
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		if p["effort"] != want {
			t.Fatalf("effort=%v want %q (root effort must not be inferred): %s", p["effort"], want, raw)
		}
	}
	assertEffort("low")
	d.childStatus("child", "fixture-rerouted", "", "")
	assertEffort("")
}
func TestClaudeChildEffortRemainsUnknown(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "sonnet", "high", em, nil)
	d.startSubagent("call", json.RawMessage(`{"subagent_type":"Explore","model":"haiku","description":"Review"}`))
	em.mu.Lock()
	defer em.mu.Unlock()
	p := em.events[len(em.events)-1].Payload.(agent.SubagentPayload)
	b, _ := json.Marshal(p)
	var fields map[string]any
	_ = json.Unmarshal(b, &fields)
	if fields["effort"] != "" {
		t.Fatalf("unknown child effort should be explicit, without inheriting root: %s", b)
	}
}

func TestCodexChildReportedEffortResetSnapshots(t *testing.T) {
	em := &collectEmitter{}
	var saved []OpenSubagent
	d := newCodexDriver(AgentDriverOpts{Emitter: em, Effort: "high"})
	d.onKnownChildren = func(records []OpenSubagent) { saved = records }
	low := "low"
	d.childStatus("child", "fixture-child", "review", "running", &low)
	d.childStatus("child", "fixture-child", "", "", nil) // authoritative null/reset, same model
	if d.children["child"].effort != "" {
		t.Fatal("null native effort preserved stale observation")
	}
	d.updateOpenWork("subagent_update", nil)
	if len(saved) != 1 || saved[0].Effort != "" {
		t.Fatalf("snapshot resurrected stale effort: %+v", saved)
	}
	restored := newCodexDriver(AgentDriverOpts{Emitter: &collectEmitter{}, Effort: "high"})
	restored.seedKnownChildren(saved)
	if restored.children["child"].effort != "" {
		t.Fatal("recovery inferred parent effort")
	}
	invalid := "unbounded bogus effort"
	d.childStatus("child", "fixture-child", "", "", &invalid)
	if d.children["child"].effort != "" {
		t.Fatal("invalid effort accepted")
	}
}

func TestCodexNativeChildConfiguredEffortAndNullReset(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_CHILD_REROUTE", "1")
	t.Setenv("FAKE_CODEX_CHILD_EFFORT", "1")
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em, Effort: "low"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopDriver := startTestCodexDriver(ctx, t, d)
	defer stopDriver()
	d.Enqueue("Review the fixture", "chat")
	waitUntil(t, "child completed", func() bool { return countKind(em, "subagent_done") == 1 })
	em.mu.Lock()
	defer em.mu.Unlock()
	seenHigh, cleared := false, false
	for _, e := range em.events {
		if e.Kind != "subagent_update" && e.Kind != "subagent_done" {
			continue
		}
		p := e.Payload.(agent.SubagentPayload)
		if p.Model == "fixture-original" && p.Effort == "high" {
			seenHigh = true
		}
		if seenHigh && p.Model == "fixture-original" && p.Effort == "" {
			cleared = true
		}
		if e.Kind == "subagent_done" && p.Effort != "" {
			t.Fatal("unset effort returned after completion")
		}
	}
	if !seenHigh || !cleared {
		t.Fatalf("native child effort observations missing: high=%v null-reset=%v", seenHigh, cleared)
	}
}

func TestCodexNativeChildPartialSettingsPreserveEffort(t *testing.T) {
	d := newCodexDriver(AgentDriverOpts{Emitter: &collectEmitter{}})
	d.childNativeStatus("child", "a", "", "running", json.RawMessage(`"high"`))
	d.childNativeStatus("child", "a", "", "", nil)
	if d.children["child"].effort != "high" {
		t.Fatal("omitted same-model setting erased observed effort")
	}
	d.childNativeStatus("child", "a", "", "", json.RawMessage(`null`))
	if d.children["child"].effort != "" {
		t.Fatal("explicit null did not clear effort")
	}
}
