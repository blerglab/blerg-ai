package daemon

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func codexPlanFixture() (*codexApp, *collectEmitter) {
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], Emitter: em})
	d.threadID = "fixture-root"
	d.activeTurn = true
	a := &codexApp{d: d, ctx: context.Background(), done: make(chan struct{}), turn: "fixture-turn", planEpoch: 1}
	d.app = a
	return a, em
}
func codexPlanJSON(plan string) json.RawMessage {
	return json.RawMessage(`{"threadId":"fixture-root","turnId":"fixture-turn","explanation":"not part of the shared plan","plan":` + plan + `}`)
}
func TestCodexPlanNativeStatuses(t *testing.T) {
	a, em := codexPlanFixture()
	a.observePlan(codexPlanJSON(`[{"step":"Inspect code","status":"completed"},{"step":"Implement adapter","status":"inProgress"},{"step":"Run tests","status":"pending"}]`), 1)
	events := em.snapshot()
	want := protocol.PlanUpdatedPayload{Items: []protocol.PlanItem{{Text: "Inspect code", Status: "completed"}, {Text: "Implement adapter", Status: "in_progress"}, {Text: "Run tests", Status: "pending"}}}
	if len(events) != 1 || events[0].Kind != protocol.PlanUpdatedKind || events[0].ClientEventID == "" || events[0].Ts.IsZero() || !reflect.DeepEqual(events[0].Payload, want) {
		t.Fatalf("native plan: %+v", events)
	}
	raw, err := json.Marshal(events[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "explanation") || strings.Contains(string(raw), "not part") || strings.Contains(string(raw), "active") {
		t.Fatal("invented/native-only plan metadata escaped")
	}
}
func TestCodexPlanExplicitEmptyOnlyClears(t *testing.T) {
	a, em := codexPlanFixture()
	a.observePlan(codexPlanJSON(`[{"step":"Keep this plan","status":"pending"}]`), 1)
	oversized := make([]map[string]string, protocol.MaxPlanItems+1)
	for i := range oversized {
		oversized[i] = map[string]string{"step": "Step", "status": "pending"}
	}
	tooMany, _ := json.Marshal(oversized)
	for _, raw := range []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{"threadId":"fixture-root","turnId":"fixture-turn"}`), codexPlanJSON("null"), codexPlanJSON(`{}`), codexPlanJSON(`[{"step":"","status":"pending"}]`), codexPlanJSON(`[{"step":"Something","status":"future"}]`), codexPlanJSON(`[{"step":42,"status":"pending"}]`), codexPlanJSON(string(tooMany)), json.RawMessage(`{broken`)} {
		a.observePlan(raw, 1)
		if len(em.snapshot()) != 1 {
			t.Fatalf("invalid plan replaced previous snapshot: %s", raw)
		}
	}
	a.observePlan(codexPlanJSON(`[{"step":"`+strings.Repeat("x", protocol.MaxPlanUpdatedBytes)+`","status":"pending"}]`), 1)
	if len(em.snapshot()) != 1 {
		t.Fatal("oversized native body replaced the plan")
	}
	a.observePlan(codexPlanJSON(`[]`), 1)
	events := em.snapshot()
	if len(events) != 2 {
		t.Fatal("explicit clear missing")
	}
	got := events[1].Payload.(protocol.PlanUpdatedPayload)
	if got.Items == nil || len(got.Items) != 0 {
		t.Fatalf("explicit clear must be []: %+v", got)
	}
}
func TestCodexPlanBoundedText(t *testing.T) {
	a, em := codexPlanFixture()
	raw, _ := json.Marshal(map[string]any{"threadId": "fixture-root", "turnId": "fixture-turn", "plan": []map[string]string{{"step": strings.Repeat("界", 250), "status": "inProgress"}}})
	a.observePlan(raw, 1)
	events := em.snapshot()
	if len(events) != 1 {
		t.Fatal("bounded plan missing")
	}
	got := events[0].Payload.(protocol.PlanUpdatedPayload)
	if len([]rune(got.Items[0].Text)) > protocol.MaxPlanText || got.Items[0].Status != "in_progress" {
		t.Fatalf("unbounded plan: %+v", got)
	}
}
func TestCodexPlanRequiresCurrentRootProcessAndTurn(t *testing.T) {
	for _, kind := range []string{"child", "owned child", "foreign", "missing thread", "missing turn", "stale turn", "idle", "inactive driver", "old process", "closed process", "canceled process", "old epoch", "unstamped", "restricted"} {
		t.Run(kind, func(t *testing.T) {
			a, em := codexPlanFixture()
			thread, turn, epoch := "fixture-root", "fixture-turn", uint64(1)
			switch kind {
			case "child":
				thread = "fixture-child"
			case "owned child":
				thread = "fixture-child"
				a.d.children = map[string]*codexChild{"fixture-child": {}}
			case "foreign":
				thread = "foreign-root"
			case "missing thread":
				thread = ""
			case "missing turn":
				turn = ""
			case "stale turn":
				turn = "old-turn"
			case "idle":
				a.turn = ""
			case "inactive driver":
				a.d.activeTurn = false
			case "old process":
				a.d.app = &codexApp{}
			case "closed process":
				close(a.done)
			case "canceled process":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				a.ctx = ctx
			case "old epoch":
				a.planEpoch = 2
			case "unstamped":
				epoch = 0
			case "restricted":
				a.d.RestrictedRuntime = &codexRestrictedRuntime{Restricted: true}
			}
			raw, _ := json.Marshal(map[string]any{"threadId": thread, "turnId": turn, "plan": []map[string]string{{"step": "Must not appear", "status": "pending"}}})
			a.observePlan(raw, epoch)
			if len(em.snapshot()) != 0 {
				t.Fatal("unbound plan updated main-agent plan")
			}
		})
	}
	if codexRestrictedConfig("fixture-model", "", nil)["tools.update_plan.enabled"] != false {
		t.Fatal("restricted update_plan must remain disabled")
	}
}
func TestCodexPlanReaderBindsOnlyActiveOrStartingEpoch(t *testing.T) {
	for _, phase := range []string{"idle", "active", "starting", "accepted but not bound"} {
		t.Run(phase, func(t *testing.T) {
			a, _ := codexPlanFixture()
			a.turn = ""
			a.planEpoch = 7
			if phase == "active" {
				a.turn = "fixture-turn"
			}
			if phase == "starting" {
				a.starting = true
			}
			if phase == "accepted but not bound" {
				a.planStarting = true
			}
			a.events = make(chan struct{}, 1)
			reader, writer := io.Pipe()
			finished := make(chan struct{})
			go func() { a.read(reader); close(finished) }()
			defer func() { _ = writer.Close(); _ = reader.Close(); <-finished }()
			raw, _ := json.Marshal(map[string]any{"method": "turn/plan/updated", "params": codexPlanJSON(`[]`)})
			if _, err := writer.Write(append(raw, '\n')); err != nil {
				t.Fatal(err)
			}
			select {
			case <-a.events:
			case <-time.After(15 * time.Second):
				t.Fatal("native plan notification did not reach backlog")
			}
			a.mu.Lock()
			got := a.backlog[0].planEpoch
			a.mu.Unlock()
			want := uint64(7)
			if phase == "idle" {
				want = 0
			}
			if got != want {
				t.Fatalf("queued epoch=%d want%d", got, want)
			}
		})
	}
}
func TestCodexPlanBeforeTurnStartResponse(t *testing.T) {
	dir := fakeCodex(t)
	path := filepath.Join(dir, "codex")
	script, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	needle := ` send({"id":ident,"result":result})`
	replacement := ` if method=="turn/start": send({"method":"turn/plan/updated","params":{"threadId":"cx-thread-1","turnId":"turn-1","explanation":None,"plan":[{"step":"Check early native plan","status":"inProgress"}]}})
` + needle
	if strings.Count(string(script), needle) != 1 {
		t.Fatal("fake RPC response fixture changed")
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(script), needle, replacement, 1)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := startTestCodexDriver(ctx, t, d)
	defer stop()
	d.Enqueue("fixture input", "chat")
	waitUntil(t, "native plan and turn complete", func() bool { return countKind(em, protocol.PlanUpdatedKind) == 1 && countKind(em, "turn_done") == 1 })
	for _, event := range em.snapshot() {
		if event.Kind == protocol.PlanUpdatedKind {
			got := event.Payload.(protocol.PlanUpdatedPayload)
			if len(got.Items) != 1 || got.Items[0].Status != "in_progress" {
				t.Fatalf("early native plan lost: %+v", got)
			}
		}
	}
}
