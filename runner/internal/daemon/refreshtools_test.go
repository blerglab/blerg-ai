package daemon

import (
	"context"
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
