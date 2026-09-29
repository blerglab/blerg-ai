package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// AgentTypeDef is a discovered agent type (.claude/agents/*.md frontmatter:
// name, description, model; body = system prompt).
type AgentTypeDef struct {
	Name, Description, Model, SystemPrompt string
	ReadOnly                               bool
}

var builtinTypes = []AgentTypeDef{
	{Name: "general-purpose", Description: "General agent for research and multi-step tasks.",
		SystemPrompt: "You are a capable subagent. Complete the task and reply with your findings — your final text is returned to the caller as data."},
	{Name: "explore", Description: "Read-only exploration agent.", ReadOnly: true,
		SystemPrompt: "You are a read-only exploration subagent. Investigate and report findings as data. Never modify anything."},
}

// DiscoverAgentTypes returns built-ins plus definitions from
// <projectDir>/.claude/agents/*.md and <homeDir>/.claude/agents/*.md.
func DiscoverAgentTypes(projectDir, homeDir string) []AgentTypeDef {
	out := append([]AgentTypeDef{}, builtinTypes...)
	seen := map[string]bool{"general-purpose": true, "explore": true}
	for _, root := range []string{filepath.Join(projectDir, ".claude/agents"), filepath.Join(homeDir, ".claude/agents")} {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(root, e.Name())) //nolint:gosec // *.md entry listed from the fixed .claude/agents roots of the project and home dir
			if err != nil {
				continue
			}
			parts := strings.SplitN(string(data), "---", 3)
			if len(parts) < 3 {
				continue
			}
			var def AgentTypeDef
			for _, line := range strings.Split(parts[1], "\n") {
				line = strings.TrimSpace(line)
				if v, ok := strings.CutPrefix(line, "name:"); ok {
					def.Name = strings.TrimSpace(v)
				}
				if v, ok := strings.CutPrefix(line, "description:"); ok {
					def.Description = strings.TrimSpace(v)
				}
				if v, ok := strings.CutPrefix(line, "model:"); ok {
					def.Model = strings.TrimSpace(v)
				}
			}
			def.SystemPrompt = strings.TrimSpace(parts[2])
			if def.Name != "" && !seen[def.Name] {
				seen[def.Name] = true
				out = append(out, def)
			}
		}
	}
	return out
}

// SpawnFunc runs one child turn to completion and returns its final text.
// Provided by the host so children get their own event stream; tests and
// the default use an in-process child loop.
type SpawnFunc func(ctx context.Context, childID string, cfg Config, prompt string) (string, error)

// DefaultSpawn runs a child loop in-process until its first turn ends and
// returns the final assistant text. Terminal error events (which end a turn
// without turn_done) also unblock the parent instead of hanging it.
func DefaultSpawn(ctx context.Context, childID string, cfg Config, prompt string) (string, error) {
	done := make(chan struct{})
	var mu sync.Mutex
	var final, errMsg string
	finish := func() {
		select {
		case <-done:
		default:
			close(done)
		}
	}
	inner := cfg.Emitter
	cfg.Emitter = emitterFunc(func(ev Event) {
		if inner != nil {
			inner.Emit(ev)
		}
		switch ev.Kind {
		case "assistant_text":
			if p, ok := ev.Payload.(AssistantTextPayload); ok && p.Done {
				mu.Lock()
				final = p.Text
				mu.Unlock()
			}
		case "error":
			if p, ok := ev.Payload.(ErrorPayload); ok {
				mu.Lock()
				errMsg = p.Message
				mu.Unlock()
			}
			finish()
		case "turn_done":
			finish()
		}
	})
	child := NewLoop(cfg)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go child.Run(cctx)
	child.Enqueue(prompt, "chat")
	select {
	case <-done:
		mu.Lock()
		defer mu.Unlock()
		if errMsg != "" {
			return "", fmt.Errorf("subagent error: %s", errMsg)
		}
		return final, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type emitterFunc func(Event)

func (f emitterFunc) Emit(ev Event) { f(ev) }

// AgentTool returns the "agent" tool: spawn a subagent with its own context,
// system prompt, and model. Not mutating — parallel agent calls in one
// message run concurrently (the superpowers parallel-dispatch pattern).
func AgentTool(l *Loop, types []AgentTypeDef, spawn SpawnFunc) Tool {
	return agentTool{l: l, types: types, spawn: spawn}
}

type agentTool struct {
	l     *Loop
	types []AgentTypeDef
	spawn SpawnFunc
}

func (a agentTool) Def() ToolDef {
	var names []string
	for _, t := range a.types {
		names = append(names, t.Name)
	}
	return ToolDef{Name: "agent",
		Description: "Spawn a subagent for an independent task. Multiple agent calls in one message run in parallel. Types: " + strings.Join(names, ", "),
		InputSchema: json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string"},"agent_type":{"type":"string"},"model":{"type":"string"}},"required":["prompt"]}`)}
}
func (a agentTool) Mutating() bool { return false }

func (a agentTool) Execute(ctx context.Context, in json.RawMessage) (string, error) {
	var args struct {
		Prompt    string `json:"prompt"`
		AgentType string `json:"agent_type"`
		Model     string `json:"model"`
	}
	if err := json.Unmarshal(in, &args); err != nil {
		return "", err
	}
	typeName := args.AgentType
	if typeName == "" {
		typeName = "general-purpose"
	}
	var def AgentTypeDef
	found := false
	for _, t := range a.types {
		if t.Name == typeName {
			def, found = t, true
			break
		}
	}
	if !found {
		if args.AgentType != "" {
			return "", fmt.Errorf("unknown agent_type: %s", args.AgentType)
		}
		def = builtinTypes[0] // general-purpose fallback, never a zero-value def
	}
	// Model resolution: explicit > typedef > parent current.
	model := a.l.cfg.Model
	if def.Model != "" {
		model = def.Model
	}
	if args.Model != "" {
		model = args.Model
	}
	// One nesting level: children get no agent tool, and no ask (child asks
	// arrive with parent proxying in Phase 1b).
	reg := a.l.cfg.Registry.Without("agent", "ask")
	if def.ReadOnly {
		var ro []Tool
		for _, t := range registryTools(reg) {
			if !t.Mutating() {
				ro = append(ro, t)
			}
		}
		reg = NewRegistry(ro...)
	}
	childID := newUUID()
	a.l.emit("subagent_started", SubagentPayload{ChildID: childID, AgentType: typeName, Model: model, Summary: truncate(args.Prompt, 120)})
	// Children do NOT emit into the parent's transcript stream (their
	// turn_done/status events would corrupt parent status derivation and
	// crash-recovery replay). The host routes child streams in Phase 1b;
	// here we only fold child spend back into the parent's session budget.
	childEmitter := emitterFunc(func(ev Event) {
		if ev.Kind == "turn_done" {
			if p, ok := ev.Payload.(TurnDonePayload); ok {
				a.l.addCostForModel(p.Model, p.Usage)
			}
		}
	})
	remaining := a.l.budget() - a.l.SpentUSD()
	if remaining <= 0 {
		remaining = 0.01 // budget already spent; effectively cap the child out
	}
	cfg := Config{
		Provider: a.l.cfg.Provider, Emitter: childEmitter, Registry: reg,
		System: def.SystemPrompt, Model: model, Effort: a.l.cfg.Effort,
		Messenger: a.l.cfg.Messenger, Pricing: a.l.cfg.Pricing,
		BudgetUSD: remaining, RetryBase: a.l.cfg.RetryBase,
	}
	result, err := a.spawn(ctx, childID, cfg, args.Prompt)
	if err != nil {
		a.l.emit("subagent_done", SubagentPayload{ChildID: childID, AgentType: typeName, Model: model, Summary: "error: " + err.Error()})
		return "", err
	}
	a.l.emit("subagent_done", SubagentPayload{ChildID: childID, AgentType: typeName, Model: model, Summary: truncate(result, 120)})
	return result, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
