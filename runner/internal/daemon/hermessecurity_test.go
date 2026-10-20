package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

type hermesSecurityWriter struct {
	sync.Mutex
	bytes.Buffer
}

func (w *hermesSecurityWriter) Write(p []byte) (int, error) {
	w.Lock()
	defer w.Unlock()
	return w.Buffer.Write(p)
}
func (w *hermesSecurityWriter) Close() error { return nil }
func (w *hermesSecurityWriter) snapshot() string {
	w.Lock()
	defer w.Unlock()
	return w.String()
}
func hermesSecurityRun(t *testing.T) (*hermesRun, *collectEmitter, *hermesSecurityWriter) {
	t.Helper()
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], Emitter: em})
	writer := &hermesSecurityWriter{}
	rpc := &hermesRPC{input: writer, done: make(chan struct{}), wake: make(chan struct{}, 1), stop: func() {}}
	ctx, cancel := context.WithCancel(context.Background())
	r := newHermesRun(ctx, d, rpc)
	r.sid = "owned-runtime"
	t.Cleanup(func() { cancel(); r.cancelQuestions(); close(rpc.done) })
	return r, em, writer
}
func hermesSecurityEvent(sid, kind string, payload any) hermesFrame {
	raw, _ := json.Marshal(map[string]any{"session_id": sid, "type": kind, "payload": payload})
	return hermesFrame{Method: "event", Params: raw}
}
func TestHermesSecurityForeignEventsCannotMutateOwnedSession(t *testing.T) {
	r, em, _ := hermesSecurityRun(t)
	for _, kind := range []string{"message.start", "message.delta", "message.complete", "session.info", "session.usage", "tool.start", "todo.updated", "subagent.start", "clarify.request"} {
		r.handle(hermesSecurityEvent("foreign-runtime", kind, map[string]any{"text": "foreign", "tool_id": "foreign-tool", "name": "write_file", "subagent_id": "foreign-child", "model": "foreign-model", "usage": map[string]int{"input": 999}}))
	}
	if len(em.snapshot()) != 0 || r.active || r.total.Input != 0 || len(r.children) != 0 || len(r.tools) != 0 {
		t.Fatal("foreign native runtime changed owned session")
	}
}
func TestHermesSecurityRefusesNativePrivilegedRequests(t *testing.T) {
	for _, method := range []string{"secret", "sudo", "approval", "file.read", "browser.open", "clarify"} {
		t.Run(method, func(t *testing.T) {
			r, em, w := hermesSecurityRun(t)
			r.begin()
			before := len(em.snapshot()) // begin records only its explicit unknown usage snapshot.
			called := false
			r.d.AskUser = func(context.Context, string) (string, error) { called = true; return "should never answer", nil }
			params, _ := json.Marshal(map[string]string{"session_id": "foreign-runtime", "question": "credential request"})
			r.request(hermesFrame{ID: json.RawMessage(`"request-id"`), Method: method, Params: params})
			var response struct {
				Error  json.RawMessage `json:"error"`
				Result json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal([]byte(w.snapshot()), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Error) == 0 || len(response.Result) != 0 || called || len(em.snapshot()) != before {
				t.Fatal("privileged or foreign native request escaped refusal")
			}
		})
	}
}
func TestHermesSecurityPendingQuestionsAreBounded(t *testing.T) {
	r, _, _ := hermesSecurityRun(t)
	r.begin()
	r.d.AskUser = func(ctx context.Context, _ string) (string, error) { <-ctx.Done(); return "", ctx.Err() }
	for i := 0; i < 100; i++ {
		key := strings.Repeat("x", i+1)
		r.ask(hermesQuestionRequest{Question: "Wait for a person"}, key, func(map[string]any) {})
	}
	r.questionsMu.Lock()
	n := len(r.questions)
	r.questionsMu.Unlock()
	if n > 32 {
		t.Fatalf("unbounded pending native questions: got %d, safety maximum 32", n)
	}
}

type hermesSecurityBlockingWriter struct {
	pipe    *io.PipeWriter
	entered chan struct{}
	once    sync.Once
}

func (w *hermesSecurityBlockingWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	return w.pipe.Write(p)
}
func (w *hermesSecurityBlockingWriter) Close() error { return w.pipe.Close() }

func TestHermesSecurityBlockedWriteHonorsContext(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	defer inR.Close()
	defer outW.Close()
	stopped := make(chan struct{})
	var once sync.Once
	writer := &hermesSecurityBlockingWriter{pipe: inW, entered: make(chan struct{})}
	rpc := newHermesRPC(writer, outR, func() { once.Do(func() { close(stopped) }) })
	t.Cleanup(func() { inW.Close(); outR.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	completed := make(chan error, 1)
	go func() { _, err := rpc.call(ctx, "session.create", map[string]string{}); completed <- err }()
	// The pipe has no reader. Wait until the writer is actually inside Write,
	// then cancel independently of any wall-clock scheduling or native response.
	select {
	case <-writer.entered:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("protocol write did not begin")
	}
	cancel()
	select {
	case err := <-completed:
		if err == nil {
			t.Fatal("cancelled blocked write succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled call remained blocked in native stdin")
	}
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("unconfirmed write did not stop native process")
	}
}
func TestHermesSecurityOversizedEnvelopeFailsClosed(t *testing.T) {
	in := &hermesSecurityWriter{}
	raw, _ := json.Marshal(map[string]any{"method": strings.Repeat("m", 257), "params": map[string]string{}})
	stopped := make(chan struct{})
	var once sync.Once
	rpc := newHermesRPC(in, bytes.NewReader(append(raw, '\n')), func() { once.Do(func() { close(stopped) }) })
	select {
	case <-rpc.done:
	case <-time.After(5 * time.Second):
		t.Fatal("invalid native envelope did not terminate reader")
	}
	rpc.mu.Lock()
	failure, n := rpc.failure, len(rpc.backlog)
	rpc.mu.Unlock()
	if failure == "" || n != 0 {
		t.Fatalf("invalid envelope retained: failure=%q count=%d", failure, n)
	}
}
func TestHermesSecurityForeignChildAndDuplicateFinal(t *testing.T) {
	r, em, _ := hermesSecurityRun(t)
	r.actualModel = "model-a"
	r.begin()
	r.handle(hermesSecurityEvent(r.sid, "subagent.start", map[string]any{"subagent_id": "foreign-child", "parent_id": "foreign-parent", "model": "model-b"}))
	if len(r.children) != 0 {
		t.Fatal("foreign child accepted")
	}
	complete := hermesSecurityEvent(r.sid, "message.complete", map[string]any{"status": "complete", "usage": map[string]any{"model": "model-a", "input": 10, "output": 3}})
	r.handle(complete)
	r.handle(complete)
	if countKind(em, "turn_done") != 1 {
		t.Fatal("duplicate completion created extra turn")
	}
	r.begin()
	r.handle(hermesSecurityEvent(r.sid, "message.complete", map[string]any{"status": "complete", "usage": map[string]any{"model": "model-a", "input": 15, "output": 5}}))
	events := em.snapshot()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Kind == "turn_done" {
			u := events[i].Payload.(agent.TurnDonePayload).Usage
			if u.InputTokens != 5 || u.OutputTokens != 2 {
				t.Fatalf("duplicate cumulative usage corrupted next turn: %+v", u)
			}
			return
		}
	}
	t.Fatal("missing turn usage")
}

func TestHermesSecurityRetainedLifecycleFieldsAreBounded(t *testing.T) {
	t.Run("child summary", func(t *testing.T) {
		r, _, _ := hermesSecurityRun(t)
		r.handle(hermesSecurityEvent(r.sid, "subagent.start", map[string]any{"subagent_id": "owned-child", "parent_id": r.sid, "goal": strings.Repeat("g", 1<<20), "summary": strings.Repeat("s", 1<<20)}))
		if child, ok := r.children["owned-child"]; ok && len(child.Summary) > 65536 {
			t.Fatalf("retained child text not bounded: %d bytes", len(child.Summary))
		}
	})
	t.Run("child identifier", func(t *testing.T) {
		r, _, _ := hermesSecurityRun(t)
		r.handle(hermesSecurityEvent(r.sid, "subagent.start", map[string]any{"subagent_id": strings.Repeat("i", 65537), "parent_id": r.sid}))
		for id := range r.children {
			if len(id) > 1024 {
				t.Fatalf("retained child identifier not bounded: %d bytes", len(id))
			}
		}
	})
	t.Run("tool identifier", func(t *testing.T) {
		r, _, _ := hermesSecurityRun(t)
		r.begin()
		r.handle(hermesSecurityEvent(r.sid, "tool.start", map[string]any{"tool_id": strings.Repeat("i", 65537), "name": "read_file"}))
		for id := range r.tools {
			if len(id) > 1024 {
				t.Fatalf("retained tool identifier not bounded: %d bytes", len(id))
			}
		}
	})
}

func TestHermesSecurityGatewayCannotImportWorkspaceShadow(t *testing.T) {
	fakeHermesGateway(t)
	workspace := t.TempDir()
	for _, name := range []string{"json.py", "tui_gateway.py", "hermes_bootstrap.py"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte("raise RuntimeError('WORKSPACE_IMPORT_EXECUTED')\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: workspace, Emitter: em})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd, err := d.gatewayCommand(ctx)
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("trusted gateway failed in hostile workspace: %v", err)
	}
	if bytes.Contains(output, []byte("WORKSPACE_IMPORT_EXECUTED")) || !bytes.Contains(output, []byte("gateway.ready")) {
		t.Fatal("workspace shadow replaced trusted gateway")
	}
}

func TestHermesSecurityBootstrapPreservesPromptAndDisablesNativeRecovery(t *testing.T) {
	root := fakeHermesGateway(t)
	server := "DESKTOP_BACKEND_CONTRACT=8\ndef _startup_system_prompt(cfg,task_id):return 'original native prompt'\ndef _maybe_schedule_auto_continue(sid,session,session_key):return {'would_execute':True}\n"
	entry := "import json\nfrom tui_gateway import server\ndef main():\n print(json.dumps({'prompt':server._startup_system_prompt({},'task'),'recovery':server._maybe_schedule_auto_continue('sid',{},'key')}),flush=True)\n"
	for name, body := range map[string]string{"server.py": server, "entry.py": entry} {
		if err := os.WriteFile(filepath.Join(root, "tui_gateway", name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), Emitter: &collectEmitter{}})
	d.DeveloperInstructions = "bounded host instructions"
	cmd, err := d.gatewayCommand(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Prompt   string `json:"prompt"`
		Recovery any    `json:"recovery"`
	}
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatal(err)
	}
	if got.Prompt != "original native prompt\n\nbounded host instructions" || got.Recovery != nil {
		t.Fatalf("host shim changed prompt or allowed native recovery: %+v", got)
	}
}
func TestHermesSecurityBootstrapRejectsUnknownNativeAPI(t *testing.T) {
	for _, server := range []string{
		"DESKTOP_BACKEND_CONTRACT=9\ndef _startup_system_prompt(cfg,task_id):return ''\ndef _maybe_schedule_auto_continue(sid,session,session_key):return None\n",
		"DESKTOP_BACKEND_CONTRACT=8\ndef _startup_system_prompt(cfg,task_id,unsafe_extra=None):return ''\ndef _maybe_schedule_auto_continue(sid,session,session_key):return None\n",
		"DESKTOP_BACKEND_CONTRACT=8\ndef _startup_system_prompt(cfg,task_id):return ''\ndef _maybe_schedule_auto_continue(sid,session,session_key,unsafe_extra=None):return None\n",
	} {
		root := fakeHermesGateway(t)
		if err := os.WriteFile(filepath.Join(root, "tui_gateway", "server.py"), []byte(server), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "tui_gateway", "entry.py"), []byte("def main():\n raise RuntimeError('NATIVE_EXECUTION_MUST_NOT_START')\n"), 0600); err != nil {
			t.Fatal(err)
		}
		d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), Emitter: &collectEmitter{}})
		cmd, err := d.gatewayCommand(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		output, err := cmd.CombinedOutput()
		if err == nil || !bytes.Contains(output, []byte("unsupported_native_api")) || bytes.Contains(output, []byte("NATIVE_EXECUTION_MUST_NOT_START")) {
			t.Fatal("incompatible native API did not fail closed before entry.main")
		}
	}
}

func TestHermesSecurityResumeAliasesMustMatchRequestedIdentity(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "matching native6 response", true: "foreign response"}[foreign], func(t *testing.T) {
			root := fakeHermesGateway(t)
			path := filepath.Join(root, "tui_gateway", "entry.py")
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			expected := "result={'session_id':sid,'stored_session_id':key,'info':{'model':'fixture-model','desktop_contract':8,'usage':{'input':0,'output':0}}}"
			identity := "key"
			if foreign {
				identity = "'foreign-durable'"
			}
			replacement := "result={'session_id':sid,'session_key':" + identity + ",'resumed':" + identity + ",'info':{'model':'fixture-model','desktop_contract':6,'usage':{'input':0,'output':0}}}"
			changed := strings.Replace(string(source), expected, replacement, 1)
			if changed == string(source) {
				t.Fatal("native fixture resume shape changed; update explicit fixture")
			}
			if err := os.WriteFile(path, []byte(changed), 0600); err != nil {
				t.Fatal(err)
			}
			em := &collectEmitter{}
			d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), Emitter: em})
			d.nativeID = "durable-owned"
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})
			go func() { defer close(done); d.runGateway(ctx) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(15 * time.Second):
					t.Error("native fixture did not stop")
				}
			})
			d.EnqueueInput(agent.Input{Text: "continue exact saved thread", ClientID: "resume-input"})
			if foreign {
				waitUntil(t, "foreign Hermes native identity refusal", func() bool { return countKind(em, "error") > 0 })
				if countKind(em, "user_message") != 0 {
					t.Fatal("foreign native identity accepted pending input")
				}
			} else {
				waitUntil(t, "native6 exact resume acknowledgement", func() bool { return countKind(em, "turn_done") == 1 })
			}
		})
	}
}

type hermesSecurityFailedWriter struct{}

func (hermesSecurityFailedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func (hermesSecurityFailedWriter) Close() error              { return nil }
func TestHermesSecurityWriteFailureStopsUnconfirmedNativeOwner(t *testing.T) {
	outR, outW := io.Pipe()
	defer outR.Close()
	defer outW.Close()
	stopped := make(chan struct{})
	var once sync.Once
	rpc := newHermesRPC(hermesSecurityFailedWriter{}, outR, func() { once.Do(func() { close(stopped) }) })
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	_, err := rpc.call(ctx, "session.usage", map[string]string{})
	if err == nil {
		t.Fatal("failed native write unexpectedly acknowledged")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("actual native write failure returned while unconfirmed process retained ownership")
	}
}

func TestHermesSecurityLegacyBatchClarifyPreservesEachAnswer(t *testing.T) {
	r, _, _ := hermesSecurityRun(t)
	r.begin()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	t.Cleanup(func() { inR.Close(); inW.Close(); outR.Close(); outW.Close() })
	r.rpc = newHermesRPC(inW, outR, func() {})
	received := make(chan hermesFrame, 2)
	go func() {
		decoder := json.NewDecoder(inR)
		encoder := json.NewEncoder(outW)
		for {
			var frame hermesFrame
			if decoder.Decode(&frame) != nil {
				return
			}
			received <- frame
			if encoder.Encode(map[string]any{"id": frame.ID, "result": map[string]string{"status": "ok"}}) != nil {
				return
			}
		}
	}()
	r.d.AskUser = func(_ context.Context, q string) (string, error) {
		if strings.Contains(q, "First") {
			return "answer-a", nil
		}
		return "answer-b", nil
	}
	r.legacyQuestion(json.RawMessage(`{"session_id":"owned-runtime","request_id":"legacy-request","questions":[{"qid":"a","question":"First?"},{"qid":"b","question":"Second?"}]}`))
	answers := map[string]string{}
	for range 2 {
		select {
		case frame := <-received:
			var p struct {
				RequestID  string          `json:"request_id"`
				QuestionID string          `json:"question_id"`
				Answer     string          `json:"answer"`
				Answers    json.RawMessage `json:"answers"`
			}
			if err := json.Unmarshal(frame.Params, &p); err != nil {
				t.Fatal(err)
			}
			if frame.Method != "clarify.respond" || p.RequestID != "legacy-request" || p.QuestionID == "" || len(p.Answers) != 0 {
				t.Fatal("legacy native batch response cancelled all questions instead of locking each answer")
			}
			answers[p.QuestionID] = p.Answer
		case <-time.After(15 * time.Second):
			t.Fatal("native legacy batch did not receive both answer locks")
		}
	}
	if answers["a"] != "answer-a" || answers["b"] != "answer-b" {
		t.Fatalf("native batch answers changed: %+v", answers)
	}
}
