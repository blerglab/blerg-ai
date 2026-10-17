package daemon

import (
	"context"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

func TestCodexChildOwnershipSurvivesRecordAndStartsClosed(t *testing.T) {
	dir := t.TempDir()
	keeper := newRecordKeeper(dir, &atomic.Bool{}, AgentRecord{SessionID: "fixture-session", Engine: "codex", CodexThreadID: "root-thread"})
	d := newCodexDriver(AgentDriverOpts{Emitter: &collectEmitter{}})
	d.threadID = "root-thread"
	d.onKnownChildren = keeper.setKnownSubagents
	d.onOpenWork = keeper.setOpenWork
	d.childStatus("owned-child", "fixture-model", strings.Repeat("x", 500), "running")
	d.childUsage("owned-child", agent.Usage{InputTokens: 100}, true)
	d.childStatus("owned-child", "", "Done", "completed")
	record, ok := readRecord(t, dir, "fixture-session")
	if !ok || len(record.KnownSubagents) != 1 || len(record.OpenSubagents) != 0 {
		t.Fatalf("ownership record missing after completion: %+v", record.KnownSubagents)
	}
	em := &collectEmitter{}
	restored := newCodexDriver(AgentDriverOpts{Emitter: em})
	restored.threadID = "root-thread"
	restored.seedKnownChildren(record.KnownSubagents)
	if !restored.childKnown("owned-child") || !restored.children["owned-child"].done || len(em.events) != 0 {
		t.Fatal("restoration invented active child execution")
	}
	restored.childStatus("owned-child", "", "Next task", "running")
	restored.childUsage("owned-child", agent.Usage{InputTokens: 125}, true)
	if child := restored.children["owned-child"]; child.usage.InputTokens != 25 || child.callID == record.KnownSubagents[0].ChildID {
		t.Fatalf("restored child reused identity/usage: %+v", child)
	}
}

func TestCodexNativeChildDiscoveryRequiresRootedAncestry(t *testing.T) {
	bin := fakeCodex(t)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_CHILD_CATALOG", `{"data":[{"id":"grandchild","parentThreadId":"historical-child","model":"fixture-model","preview":"Nested work"},{"id":"foreign-child","parentThreadId":"foreign-root","preview":"Private sibling"},{"id":"historical-child","parentThreadId":"cx-thread-1","model":"fixture-model","preview":"Owned work"},{"id":"cycle-one","parentThreadId":"cycle-two"},{"id":"cycle-two","parentThreadId":"cycle-one"}],"nextCursor":null}`)
	t.Setenv("FAKE_CODEX_RECOVERED_CHILD", "1")
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopDriver := startTestCodexDriver(ctx, t, d)
	defer stopDriver()
	waitUntil(t, "owned child discovery", func() bool { return d.childKnown("historical-child") && d.childKnown("grandchild") })
	if d.childKnown("foreign-child") || d.childKnown("cycle-one") || d.childKnown("cycle-two") {
		t.Fatal("unrooted history authorized as owned")
	}
	if countKind(em, "subagent_started") != 0 {
		t.Fatal("historical metadata invented active execution")
	}
	d.Enqueue("Resume child", "chat")
	waitUntil(t, "main turn completion", func() bool { return countKind(em, "turn_done") == 1 })
	if countKind(em, "subagent_started") != 1 || countKind(em, "subagent_done") != 1 {
		t.Fatalf("recovered direct child notifications lost: %v", em.kinds())
	}
	em.mu.Lock()
	defer em.mu.Unlock()
	found := false
	for _, event := range em.events {
		if p, ok := event.Payload.(agent.AssistantTextPayload); ok && p.Text == "Recovered child progress" && p.ParentCallID != "" {
			found = true
		}
	}
	if !found {
		t.Fatal("recovered child output was not routed")
	}
}

func TestCodexRecoveredChildAllowlistIsBoundedAndRejectsRoot(t *testing.T) {
	d := newCodexDriver(AgentDriverOpts{Emitter: &collectEmitter{}})
	d.threadID = "root"
	entries := []OpenSubagent{{AgentID: "root"}, {AgentID: "../foreign"}, {AgentID: ""}}
	for i := 0; i < codexMaxKnownChildren+20; i++ {
		entries = append(entries, OpenSubagent{AgentID: ccUUID(), Summary: strings.Repeat("s", 500)})
	}
	d.seedKnownChildren(entries)
	if len(d.children) != codexMaxKnownChildren || d.childKnown("root") || d.childKnown("../foreign") {
		t.Fatal("unsafe or unbounded recovered ownership")
	}
	for _, child := range d.children {
		if len(child.summary) > 200 || !child.done {
			t.Fatal("recovered metadata unbounded or active")
		}
	}
}
