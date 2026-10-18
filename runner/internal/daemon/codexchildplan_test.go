package daemon

import (
	"context"
	"encoding/json"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func childPlanJSON(thread, turn string) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"threadId": thread, "turnId": turn, "plan": []map[string]string{{"step": "Review adapter", "status": "completed"}, {"step": "Test adapter", "status": "inProgress"}}})
	return raw
}

func TestCodexChildPlanUsesCurrentRunIdentityAfterRootTurn(t *testing.T) {
	a, em := codexPlanFixture()
	a.turn = ""
	a.d.activeTurn = false
	a.d.children = map[string]*codexChild{"fixture-child": {callID: "fixture-child-run"}}
	a.childTurns = map[string]string{"fixture-child": "child-turn"}
	a.observePlan(childPlanJSON("fixture-child", "child-turn"), 0)
	events := em.snapshot()
	if len(events) != 1 {
		t.Fatalf("child progress events = %d, want 1", len(events))
	}
	p := events[0].Payload.(protocol.PlanUpdatedPayload)
	if p.ChildID != "fixture-child-run" || p.ParentCallID != "" || len(p.Items) != 2 {
		t.Fatalf("child plan attribution: %+v", p)
	}
}

func TestCodexChildPlanRefusesUnownedStaleAndEndedRuns(t *testing.T) {
	for _, mode := range []string{"unknown", "stale turn", "missing turn", "done", "missing run", "old process", "closed process", "restricted"} {
		t.Run(mode, func(t *testing.T) {
			a, em := codexPlanFixture()
			a.d.children = map[string]*codexChild{"fixture-child": {callID: "fixture-child-run"}}
			a.childTurns = map[string]string{"fixture-child": "child-turn"}
			turn := "child-turn"
			switch mode {
			case "unknown":
				delete(a.d.children, "fixture-child")
			case "stale turn":
				turn = "old-turn"
			case "missing turn":
				delete(a.childTurns, "fixture-child")
			case "done":
				a.d.children["fixture-child"].done = true
			case "missing run":
				a.d.children["fixture-child"].callID = ""
			case "old process":
				a.d.app = &codexApp{}
			case "closed process":
				close(a.done)
			case "restricted":
				a.d.RestrictedRuntime = &codexRestrictedRuntime{Restricted: true}
			}
			a.observePlan(childPlanJSON("fixture-child", turn), 1)
			if len(em.snapshot()) != 0 {
				t.Fatal("unbound child plan escaped")
			}
		})
	}
}

func TestCodexChildResumePlanUsesFreshDisplayIdentity(t *testing.T) {
	a, em := codexPlanFixture()
	a.d.children = map[string]*codexChild{"fixture-child": {callID: "old-run", done: true}}
	a.d.childStatus("fixture-child", "", "", "running")
	a.childTurns = map[string]string{"fixture-child": "new-turn"}
	a.observePlan(childPlanJSON("fixture-child", "old-turn"), 1)
	a.observePlan(childPlanJSON("fixture-child", "new-turn"), 1)
	var plans []protocol.PlanUpdatedPayload
	for _, event := range em.snapshot() {
		if event.Kind == protocol.PlanUpdatedKind {
			plans = append(plans, event.Payload.(protocol.PlanUpdatedPayload))
		}
	}
	if len(plans) != 1 || plans[0].ChildID == "" || plans[0].ChildID == "old-run" {
		t.Fatalf("resumed plan: %+v", plans)
	}
}

// Exercise notification buffering, proof-of-ownership and resumed run mapping
// together, rather than only calling the plan adapter with a seeded child.
func TestCodexChildPlansFollowNativeOwnershipAndRunLifecycle(t *testing.T) {
	bin := fakeCodex(t)
	path := filepath.Join(bin, "codex")
	script, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range []string{"child-turn-1", "child-turn-2"} {
		needle := `  send({"method":"turn/started","params":{"threadId":"child-thread","turn":{"id":"` + turn + `","status":"inProgress"}}})`
		if strings.Count(string(script), needle) != 1 {
			t.Fatal("native child fixture changed")
		}
		script = []byte(strings.Replace(string(script), needle, needle+`
  send({"method":"turn/plan/updated","params":{"threadId":"child-thread","turnId":"`+turn+`","plan":[{"step":"Review run","status":"inProgress"}]}})`, 1))
	}
	if err := os.WriteFile(path, script, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_CHILD_FOLLOWUP", "1")
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := startTestCodexDriver(ctx, t, d)
	defer stop()
	d.Enqueue("Run children", "chat")
	waitUntil(t, "child plans and completions", func() bool {
		return countKind(em, protocol.PlanUpdatedKind) == 2 && countKind(em, "subagent_done") == 2
	})
	var runs, plans []string
	for _, event := range em.snapshot() {
		if event.Kind == "subagent_started" {
			runs = append(runs, event.Payload.(agent.SubagentPayload).ChildID)
		}
		if event.Kind == protocol.PlanUpdatedKind {
			p := event.Payload.(protocol.PlanUpdatedPayload)
			plans = append(plans, p.ChildID)
			if p.ParentCallID != "" {
				t.Fatal("child plan leaked root scope")
			}
		}
	}
	if len(runs) != 2 || len(plans) != 2 || runs[0] == runs[1] || runs[0] != plans[0] || runs[1] != plans[1] {
		t.Fatalf("runs=%v plans=%v", runs, plans)
	}
}
