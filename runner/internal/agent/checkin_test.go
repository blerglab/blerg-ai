package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// stubTool is a minimal Tool for gate tests.
type stubTool struct {
	name     string
	mutating bool
}

func (s stubTool) Def() ToolDef                                             { return ToolDef{Name: s.name, InputSchema: json.RawMessage(`{}`)} }
func (s stubTool) Mutating() bool                                           { return s.mutating }
func (s stubTool) Execute(context.Context, json.RawMessage) (string, error) { return "ok", nil }

func TestGateRejectsMutationBeforeTaskStart(t *testing.T) {
	e := &enforcer{}
	msg := e.gate(stubTool{name: "write_file", mutating: true}, nil, nil)
	if !strings.Contains(msg, "task_start") {
		t.Fatalf("want task_start rejection, got %q", msg)
	}
	// read-only passes
	if msg := e.gate(stubTool{name: "read_file"}, nil, nil); msg != "" {
		t.Fatalf("read-only should pass, got %q", msg)
	}
}

func TestGateSameMessageCheckInSatisfies(t *testing.T) {
	e := &enforcer{}
	msg := e.gate(stubTool{name: "write_file", mutating: true}, nil, []string{"task_start"})
	if msg != "" {
		t.Fatalf("same-message task_start should satisfy, got %q", msg)
	}
}

func TestCheckInToolRecordsPhase(t *testing.T) {
	e := &enforcer{}
	tool := newCheckInTool(e)
	if tool.Mutating() {
		t.Fatal("check_in must not be mutating")
	}
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"phase":"task_start","summary":"doing X"}`))
	if err != nil || out == "" {
		t.Fatalf("out=%q err=%v", out, err)
	}
	if !e.taskStartDone {
		t.Fatal("taskStartDone not set")
	}
	if msg := e.gate(stubTool{name: "bash", mutating: true}, json.RawMessage(`{"command":"ls"}`), nil); msg != "" {
		t.Fatalf("after task_start, plain bash should pass: %q", msg)
	}
}

func TestGateRejectsRiskyWithoutPreRisky(t *testing.T) {
	e := &enforcer{taskStartDone: true}
	in := json.RawMessage(`{"command":"git push origin main"}`)
	if msg := e.gate(stubTool{name: "bash", mutating: true}, in, nil); !strings.Contains(msg, "pre_risky") {
		t.Fatalf("want pre_risky rejection, got %q", msg)
	}
	// covered by same-message check-in
	if msg := e.gate(stubTool{name: "bash", mutating: true}, in, []string{"pre_risky"}); msg != "" {
		t.Fatalf("same-message pre_risky should cover, got %q", msg)
	}
	// covered by previous executor step
	e.riskyCovered = true
	if msg := e.gate(stubTool{name: "bash", mutating: true}, in, nil); msg != "" {
		t.Fatalf("riskyCovered should cover, got %q", msg)
	}
}

func TestRiskyCommandList(t *testing.T) {
	risky := []string{
		"git push origin main",
		"kubectl apply -f x.yaml",
		"kubectl delete pod foo",
		"npm publish",
		"cargo publish",
		"goose up",
		"migrate up",
	}
	for _, c := range risky {
		if !riskyCommand(c) {
			t.Errorf("want risky: %q", c)
		}
	}
	safe := []string{"git status", "git commit -m x", "ls", "kubectl get pods", "npm test"}
	for _, c := range safe {
		if riskyCommand(c) {
			t.Errorf("want safe: %q", c)
		}
	}
}
