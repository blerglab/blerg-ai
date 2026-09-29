package agent

import (
	"context"
	"encoding/json"
	"regexp"
	"sync"
)

// enforcer tracks check-in state within a single turn. It is a visibility
// mechanism (cannot be skipped accidentally), NOT a security boundary — see
// the design spec.
type enforcer struct {
	mu            sync.Mutex // check_in executes in the concurrent pass; guard all fields
	taskStartDone bool
	// riskyCovered is set when a pre_risky check-in occurs; it covers risky
	// calls in the same assistant message or the immediately following
	// executor step, then is consumed.
	riskyCovered bool
	mutated      bool // any mutating tool ran this turn
	reminded     bool // completion reminder already injected
}

// riskyRe is the small, high-precision risk list (spec: git push, kubectl
// apply/delete, package publish, DB migrate).
var riskyRe = regexp.MustCompile(`(?:^|\s|&&|\|\||;)\s*(git\s+push|kubectl\s+(apply|delete)|npm\s+publish|cargo\s+publish|goose\s+up|migrate\s+up)\b`)

func riskyCommand(command string) bool { return riskyRe.MatchString(command) }

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// gate returns a rejection message ("" = allowed) for a proposed tool call.
// sameMessagePhases lists check_in phases present in the same assistant
// message as the proposed call (same-message satisfaction).
func (e *enforcer) gate(t Tool, input json.RawMessage, sameMessagePhases []string) string {
	if !t.Mutating() {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.taskStartDone && !contains(sameMessagePhases, "task_start") {
		return `check_in required: call check_in(phase="task_start") with a brief plan before mutating tools`
	}
	if t.Def().Name == "bash" {
		var args struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal(input, &args)
		if riskyCommand(args.Command) && !e.riskyCovered && !contains(sameMessagePhases, "pre_risky") {
			return `check_in required: call check_in(phase="pre_risky") stating intent before risky commands`
		}
	}
	return ""
}

func newCheckInTool(e *enforcer) Tool {
	return checkInTool{e: e}
}

type checkInTool struct{ e *enforcer }

func (c checkInTool) Def() ToolDef {
	return ToolDef{
		Name:        "check_in",
		Description: "REQUIRED protocol tool. Phases: task_start (before any mutating work, with a brief plan), pre_risky (immediately before risky commands like git push), completion (summarize what was done and its verification status before ending a turn with mutating work).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"phase":{"type":"string","enum":["task_start","pre_risky","completion"]},"summary":{"type":"string"}},"required":["phase","summary"]}`),
	}
}
func (c checkInTool) Mutating() bool { return false }
func (c checkInTool) Execute(_ context.Context, in json.RawMessage) (string, error) {
	var args struct{ Phase, Summary string }
	if err := json.Unmarshal(in, &args); err != nil {
		return "", err
	}
	c.e.mu.Lock()
	switch args.Phase {
	case "task_start":
		c.e.taskStartDone = true
	case "pre_risky":
		c.e.riskyCovered = true
	}
	c.e.mu.Unlock()
	return "check-in recorded: " + args.Phase, nil
}
