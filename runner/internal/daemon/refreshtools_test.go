package daemon

import (
	"context"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
	"sync/atomic"
	"testing"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

// fakeRefreshDriver is a sessionDriver that counts refreshTools calls.
type fakeRefreshDriver struct {
	refreshed atomic.Int32
}

func (f *fakeRefreshDriver) Start()                               {}
func (f *fakeRefreshDriver) Run(context.Context)                  {}
func (f *fakeRefreshDriver) EnqueueInput(agent.Input)             {}
func (f *fakeRefreshDriver) SetModel(string, string, string)      {}
func (f *fakeRefreshDriver) Interrupt()                           {}
func (f *fakeRefreshDriver) RestoreContext([]agent.RestoredEvent) {}
func (f *fakeRefreshDriver) refreshTools()                        { f.refreshed.Add(1) }

func TestAgentHostRefreshToolsReachesTheDriverOrIsIgnored(t *testing.T) {
	h := &AgentHost{sessions: map[string]*agentSession{}}
	d := &fakeRefreshDriver{}
	h.sessions["s1"] = &agentSession{loop: d}
	h.RefreshTools("s1")
	if d.refreshed.Load() != 1 {
		t.Fatalf("refreshTools calls = %d", d.refreshed.Load())
	}
	h.RefreshTools("unknown") // no panic
}

// *claudeCodeDriver must satisfy toolRefresher (compile-time), and its refresh must bump the
// steering generation the way a model change does.
var _ toolRefresher = (*claudeCodeDriver)(nil)

func TestClaudeDriverRefreshToolsMarksStale(t *testing.T) {
	em := &collectEmitter{}
	d := newClaudeCodeDriver(t.TempDir(), "claude-opus-5", "", em, nil)
	d.sm.Lock()
	before := d.steer.cfgGen
	d.sm.Unlock()
	d.refreshTools()
	d.sm.Lock()
	after := d.steer.cfgGen
	d.sm.Unlock()
	if after != before+1 {
		t.Fatalf("cfgGen %d -> %d, want +1", before, after)
	}
}

func TestActualAdaptersReportToolSchemaReloadPolicy(t *testing.T) {
	claude, ok := claudeInitCapabilities([]byte(`{"tools":["Read"]}`), nil, t.TempDir(), t.TempDir())
	if !ok || claude.ToolSchemaRefresh != protocol.ToolSchemaRefreshNextTurn {
		t.Fatal("Claude CLI must report its idle/next-turn refresh")
	}
	native := nativeCapabilities("fixture", nil, nil, nil, t.TempDir(), t.TempDir())
	if native.ToolSchemaRefresh != protocol.ToolSchemaRefreshNewSession {
		t.Fatal("native API loop cannot promise reload just because its provider is Claude")
	}
	codex := (&codexRestrictedRuntime{}).capabilities("fixture")
	if codex.ToolSchemaRefresh != protocol.ToolSchemaRefreshNewSession {
		t.Fatal("Codex native schema requires a fresh conversation")
	}
	generic, ok := engineCapabilities(EngineSpec{ID: "fixture-engine", DisplayName: "Fixture", Capabilities: func(CapabilityContext) []protocol.CapabilityGroup {
		return []protocol.CapabilityGroup{{ID: protocol.CapGroupTools, Items: []protocol.CapabilityItem{{Name: "fixture"}}}}
	}}, "fixture", CapabilityContext{})
	if !ok || generic.ToolSchemaRefresh != protocol.ToolSchemaRefreshNewSession {
		t.Fatal("generic CLI must not promise an unsupported refresh")
	}
}
