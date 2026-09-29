package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func writeAgentDef(projectDir, name, content string) error {
	dir := filepath.Join(projectDir, ".claude/agents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}

func TestAgentToolRunsChildAndReturnsResult(t *testing.T) {
	rec := &recorder{}
	childProvider := &fakeProvider{scripts: [][]StreamEvent{textResponse("child findings: all good")}}
	fp := &fakeProvider{scripts: [][]StreamEvent{
		toolCallResponse("agent", "tu_1", `{"prompt":"explore the code"}`),
		textResponse("done"),
	}}
	l := NewLoop(Config{Provider: fp, Emitter: rec, Registry: NewRegistry(), Model: "parent-model"})
	types := DiscoverAgentTypes(t.TempDir(), t.TempDir())
	spawn := func(ctx context.Context, childID string, cfg Config, prompt string) (string, error) {
		if cfg.Model != "parent-model" {
			t.Errorf("child should inherit parent model, got %q", cfg.Model)
		}
		cfg.Provider = childProvider
		return DefaultSpawn(ctx, childID, cfg, prompt)
	}
	l.cfg.Registry = NewRegistry(AgentTool(l, types, spawn))
	ctx, _ := contextWithCancel(t)
	go l.Run(ctx)
	l.Enqueue("go", "chat")
	waitFor(t, rec, "turn_done")

	if len(rec.byKind("subagent_started")) != 1 || len(rec.byKind("subagent_done")) != 1 {
		t.Fatalf("kinds = %v", rec.kinds())
	}
	// parent's second call must carry the child's answer as the tool result
	second := fp.Calls[1]
	last := second.Messages[len(second.Messages)-1]
	if !strings.Contains(last.Blocks[0].Content, "child findings: all good") {
		t.Fatalf("tool result = %+v", last.Blocks[0])
	}
}

func TestAgentModelResolutionOrder(t *testing.T) {
	types := []AgentTypeDef{{Name: "reviewer", Model: "typedef-model"}}
	var got atomic.Value
	spawn := func(ctx context.Context, childID string, cfg Config, prompt string) (string, error) {
		got.Store(cfg.Model)
		return "ok", nil
	}
	l := NewLoop(Config{Provider: &fakeProvider{}, Emitter: &recorder{}, Registry: NewRegistry(), Model: "parent-model"})
	tool := AgentTool(l, types, spawn)

	// explicit model wins
	_, err := tool.Execute(context.Background(), mustJSON(`{"prompt":"p","agent_type":"reviewer","model":"explicit-model"}`))
	if err != nil || got.Load().(string) != "explicit-model" {
		t.Fatalf("got %v err %v", got.Load(), err)
	}
	// typedef model next
	_, _ = tool.Execute(context.Background(), mustJSON(`{"prompt":"p","agent_type":"reviewer"}`))
	if got.Load().(string) != "typedef-model" {
		t.Fatalf("got %v", got.Load())
	}
	// parent model last
	_, _ = tool.Execute(context.Background(), mustJSON(`{"prompt":"p"}`))
	if got.Load().(string) != "parent-model" {
		t.Fatalf("got %v", got.Load())
	}
}

func TestParallelAgentCallsRunConcurrently(t *testing.T) {
	rec := &recorder{}
	var inFlight, maxInFlight atomic.Int32
	spawn := func(ctx context.Context, childID string, cfg Config, prompt string) (string, error) {
		cur := inFlight.Add(1)
		for {
			old := maxInFlight.Load()
			if cur <= old || maxInFlight.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
		inFlight.Add(-1)
		return "done " + prompt, nil
	}
	fp := &fakeProvider{scripts: [][]StreamEvent{
		{
			{Kind: "block", Block: &Block{Type: "tool_use", ID: "a1", Name: "agent", Input: mustJSON(`{"prompt":"one"}`)}},
			{Kind: "block", Block: &Block{Type: "tool_use", ID: "a2", Name: "agent", Input: mustJSON(`{"prompt":"two"}`)}},
			{Kind: "done", StopReason: "tool_use", Usage: &Usage{}},
		},
		textResponse("ok"),
	}}
	l := NewLoop(Config{Provider: fp, Emitter: rec, Registry: NewRegistry(), Model: "m"})
	l.cfg.Registry = NewRegistry(AgentTool(l, nil, spawn))
	ctx, _ := contextWithCancel(t)
	go l.Run(ctx)
	l.Enqueue("go", "chat")
	waitFor(t, rec, "turn_done")
	if maxInFlight.Load() < 2 {
		t.Fatalf("agent calls did not run concurrently (max in flight = %d)", maxInFlight.Load())
	}
}

func TestChildRegistryExcludesAgentAndAsk(t *testing.T) {
	reg := NewRegistry(stubTool{name: "ask"}, stubTool{name: "agent"}, stubTool{name: "read_file"})
	child := reg.Without("agent", "ask")
	if _, ok := child.Get("agent"); ok {
		t.Fatal("child must not have agent tool")
	}
	if _, ok := child.Get("ask"); ok {
		t.Fatal("child must not have ask tool")
	}
	if _, ok := child.Get("read_file"); !ok {
		t.Fatal("child lost read_file")
	}
}

func TestDiscoverAgentTypesIncludesBuiltinsAndFiles(t *testing.T) {
	project, home := t.TempDir(), t.TempDir()
	if err := writeAgentDef(project, "reviewer.md", "---\nname: reviewer\ndescription: reviews\nmodel: claude-haiku-4-5\n---\nYou review code."); err != nil {
		t.Fatal(err)
	}
	types := DiscoverAgentTypes(project, home)
	byName := map[string]AgentTypeDef{}
	for _, d := range types {
		byName[d.Name] = d
	}
	if _, ok := byName["general-purpose"]; !ok {
		t.Fatal("missing built-in general-purpose")
	}
	if !byName["explore"].ReadOnly {
		t.Fatal("explore must be read-only")
	}
	r := byName["reviewer"]
	if r.Model != "claude-haiku-4-5" || !strings.Contains(r.SystemPrompt, "You review code.") {
		t.Fatalf("reviewer = %+v", r)
	}
}
