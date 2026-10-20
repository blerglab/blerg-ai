package daemon

import (
	"context"
	"encoding/json"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHermesAdoptionContinuationYieldsToPersonAtConsumption(t *testing.T) {
	fakeHermesGateway(t)
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), Emitter: em})
	if !d.continueAdopted("do not execute stale continuation", "") {
		t.Fatal("not queued")
	}
	d.EnqueueInput(agent.Input{Text: "human-first", ClientID: "human"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	waitUntil(t, "human completion", func() bool { return countKind(em, "turn_done") == 1 })
	if countKind(em, "user_message") != 1 {
		t.Fatal("recovery continuation ran ahead of person")
	}
}
func TestHermesSparseChildrenAndTerminalDeduplication(t *testing.T) {
	r, em, _ := hermesSecurityRun(t)
	r.begin()
	for _, ev := range []struct {
		k string
		p map[string]any
	}{
		{"subagent.start", map[string]any{"subagent_id": "child", "model": "child-model", "input_tokens": 10, "output_tokens": 3, "tool_count": 2}},
		{"subagent.progress", map[string]any{"subagent_id": "child", "output_tokens": 8}},
		{"subagent.complete", map[string]any{"subagent_id": "child", "status": "complete"}},
		{"subagent.complete", map[string]any{"subagent_id": "child", "status": "complete"}},
	} {
		raw, _ := json.Marshal(ev.p)
		r.child(ev.k, raw)
	}
	child := r.children["child"]
	if child.Model != "child-model" || child.Usage.InputTokens != 10 || child.Usage.OutputTokens != 8 || child.ToolUses != 2 {
		t.Fatalf("sparse native child counters lost: %+v", child)
	}
	if countKind(em, "subagent_started") != 1 || countKind(em, "subagent_done") != 1 {
		t.Fatal("duplicate child lifecycle")
	}
}
func TestHermesBackgroundUsesNativeExitNotTerminalClose(t *testing.T) {
	r, em, _ := hermesSecurityRun(t)
	r.background(json.RawMessage(`{"processes":[{"session_id":"task","command":"fixture command","status":"running"}]}`))
	r.handle(hermesSecurityEvent(r.sid, "terminal.close", map[string]string{"process_id": "task"}))
	if countKind(em, "background_task_done") != 0 {
		t.Fatal("tab closure invented process exit")
	}
	r.background(json.RawMessage(`{"processes":[{"session_id":"task","status":"exited","exit_code":0}]}`))
	r.background(json.RawMessage(`{"processes":[{"session_id":"task","status":"exited","exit_code":0}]}`))
	if countKind(em, "background_task_started") != 1 || countKind(em, "background_task_done") != 1 {
		t.Fatal("background lifecycle duplicated")
	}
}
func TestHermesReasoningPreviewDoesNotDuplicateStream(t *testing.T) {
	r, em, _ := hermesSecurityRun(t)
	r.begin()
	r.handle(hermesSecurityEvent(r.sid, "reasoning.delta", map[string]string{"text": "native reasoning"}))
	r.handle(hermesSecurityEvent(r.sid, "reasoning.available", map[string]string{"text": "native reasoning"}))
	r.finish("complete", "")
	r.finish("complete", "")
	if countKind(em, "assistant_thinking") != 1 || countKind(em, "turn_done") != 1 {
		t.Fatal("duplicate final lifecycle")
	}
	for _, ev := range em.snapshot() {
		if ev.Kind == "assistant_thinking" && ev.Payload.(agent.AssistantThinkingPayload).Text != "native reasoning" {
			t.Fatal("preview duplicated reasoning")
		}
	}
}

func TestHermesUnverifiedCleanupDisablesReplacementAndRejectsInput(t *testing.T) {
	dir := t.TempDir()
	docker := filepath.Join(dir, "docker")
	if err := os.WriteFile(docker, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], Emitter: em})
	d.prefix = sandboxExec{docker, "exec", "fixture-container"}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "sleep", "30")
	d.configureGatewayCancellation(ctx, cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = cmd.Wait()
	d.mu.Lock()
	uncertain := d.cleanupUncertain
	d.mu.Unlock()
	if !uncertain {
		t.Fatal("unverified inner cleanup permitted replacement")
	}
	done := make(chan struct{})
	go func() { d.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("unsafe replacement attempted")
	}
	select {
	case err := <-d.EngineSessionLoaded():
		if err == nil {
			t.Fatal("failed startup reported loaded")
		}
	default:
		t.Fatal("adoption waiter was stranded")
	}
	d.EnqueueInput(agent.Input{Text: "retry", ClientID: "unaccepted"})
	for _, e := range em.snapshot() {
		if e.Kind == "error" && e.Payload.(agent.ErrorPayload).ClientID == "unaccepted" {
			return
		}
	}
	t.Fatal("closed input lacked correlated failure")
}

func TestHermesFinalFramesSurviveImmediateGatewayExit(t *testing.T) {
	root := fakeHermesGateway(t)
	path := filepath.Join(root, "tui_gateway", "entry.py")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := strings.Replace(string(raw), "if not os.getenv('FAKE_HERMES_HOLD'):done()", "if not os.getenv('FAKE_HERMES_HOLD'):done();os._exit(0)", 1)
	if err := os.WriteFile(path, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	d.EnqueueInput(agent.Input{Text: "finish then exit", ClientID: "fast-final"})
	waitUntil(t, "native final before exit", func() bool { return countKind(em, "turn_done") == 1 })
	if countKind(em, "user_message") != 1 {
		t.Fatal("accepted input acknowledgment lost at EOF")
	}
}

func TestHermesModelConfirmationDoesNotApproveOrRestart(t *testing.T) {
	root := fakeHermesGateway(t)
	path := filepath.Join(root, "tui_gateway", "entry.py")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := strings.Replace(string(raw), "else:result={}", "elif method=='config.set':result={'confirm_required':True}\n     else:result={}", 1)
	if err := os.WriteFile(path, []byte(source), 0700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "rpc.log")
	t.Setenv("FAKE_HERMES_RPC_LOG", logPath)
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	select {
	case err := <-d.EngineSessionLoaded():
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("not loaded")
	}
	d.SetModel("selected-model", "", "")
	waitUntil(t, "native model confirmation refusal", func() bool { return countKind(em, "error") > 0 })
	d.mu.Lock()
	model := d.model
	d.mu.Unlock()
	if model != "fixture-model" {
		t.Fatal("unconfirmed model retained")
	}
	d.EnqueueInput(agent.Input{Text: "keep working", ClientID: "after-refusal"})
	waitUntil(t, "native unchanged-model turn", func() bool { return countKind(em, "turn_done") == 1 })
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(calls), "session.create") != 1 || strings.Contains(string(calls), "approval.respond") {
		t.Fatal("native model confirmation bypassed")
	}
}

func TestHermesUnsupportedGatewayReportsPermanentErrorBeforeFastExit(t *testing.T) {
	root := fakeHermesGateway(t)
	path := filepath.Join(root, "tui_gateway", "server.py")
	if err := os.WriteFile(path, []byte("DESKTOP_BACKEND_CONTRACT=7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { d.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("unsupported gateway retried or hung")
	}
	if countKind(em, "error") != 1 {
		t.Fatal("permanent gateway failure was retried")
	}
	for _, ev := range em.snapshot() {
		if ev.Kind == "error" && !strings.Contains(ev.Payload.(agent.ErrorPayload).Message, "compatible Hermes native gateway") {
			t.Fatal("incompatible gateway error was misleading")
		}
	}
}

type hermesEOFAckWriter struct{ rpc *hermesRPC }

func (w hermesEOFAckWriter) Close() error { return nil }
func (w hermesEOFAckWriter) Write(raw []byte) (int, error) {
	var p struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return 0, err
	}
	w.rpc.mu.Lock()
	ch := w.rpc.pending[string(p.ID)]
	w.rpc.mu.Unlock()
	ch <- hermesFrame{Result: json.RawMessage(`{"status":"streaming"}`)}
	close(w.rpc.done)
	return len(raw), nil
}
func TestHermesAcknowledgedRPCSurvivesSimultaneousEOF(t *testing.T) {
	for i := 0; i < 100; i++ {
		rpc := &hermesRPC{pending: map[string]chan hermesFrame{}, done: make(chan struct{}), stop: func() {}}
		rpc.input = hermesEOFAckWriter{rpc}
		raw, err := rpc.call(context.Background(), "prompt.submit", map[string]any{"text": "fixture"})
		if err != nil || !strings.Contains(string(raw), "streaming") {
			t.Fatal("known native acknowledgment lost at EOF")
		}
	}
}
