package daemon

import (
	"context"
	"encoding/json"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"testing"
	"time"
)

func TestHermesMCPStatusHonestProjection(t *testing.T) {
	raw := json.RawMessage(`{"servers":[{"name":"pending","status":"connecting","tools":0,"url":"PRIVATE_SECRET"},{"name":"on-demand","status":"lazy","source":"plugin","tools":4},{"name":"online","status":"connected","tools":3},{"name":"disabled","disabled":true,"status":"configured"}]}`)
	got, err := hermesMCPRows(raw, 7, true)
	if err != nil || !got.Ready || got.Generation != 7 || len(got.Servers) != 4 {
		t.Fatalf("invalid inventory: %+v %v", got, err)
	}
	statuses := map[string]string{}
	for _, s := range got.Servers {
		statuses[s.Name] = s.Status
	}
	if statuses["pending"] != "pending" || statuses["on-demand"] != "lazy" || statuses["online"] != "connected" || statuses["disabled"] != "disabled" {
		t.Fatalf("untruthful statuses: %v", statuses)
	}
	for _, bad := range []string{`{}`, `{"servers":[{"name":"same"},{"name":"same"}]}`, `{"servers":[{"name":"https://invalid.example"}]}`} {
		if _, err := hermesMCPRows(json.RawMessage(bad), 7, true); err == nil {
			t.Fatal("invalid inventory settled")
		}
	}
}

func TestHermesMCPGateAndGeneration(t *testing.T) {
	fakeHermesGateway(t)
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), Emitter: em})
	entered, release := make(chan struct{}), make(chan struct{})
	d.SetMCPInputGate(func(ctx context.Context) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	d.EnqueueInput(agent.Input{Text: "first", ClientID: "gated"})
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("MCP gate not reached")
	}
	if countKind(em, "user_message") != 0 {
		t.Fatal("accepted before MCP admission")
	}
	probe, err := d.MCPStatus(ctx)
	if err != nil || !probe.Ready || probe.Generation != 1 || len(probe.Servers) != 0 {
		t.Fatalf("native empty status %+v %v", probe, err)
	}
	close(release)
	waitUntil(t, "MCP-released input", func() bool { return countKind(em, "turn_done") == 1 })
}
