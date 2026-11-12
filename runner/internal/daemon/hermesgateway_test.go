package daemon

import (
	"context"
	"errors"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fakeHermesGateway(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HERMES_HOME", filepath.Join(os.Getenv("HOME"), ".hermes"))
	if err := os.Mkdir(filepath.Join(root, "tui_gateway"), 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"hermes": "#!/bin/sh\nprintf 'legacy-final\\n'\n", "hermes_bootstrap.py": "", "tui_gateway/__init__.py": "", "tui_gateway/server.py": "DESKTOP_BACKEND_CONTRACT=8\ndef _startup_system_prompt(cfg,task_id):return 'native instructions'\ndef _maybe_schedule_auto_continue(sid,session,session_key):return None\n",
		"tui_gateway/entry.py": "def main():\n    " + strings.ReplaceAll(strings.ReplaceAll(fakeHermesGatewaySource, "global calls", "nonlocal calls"), "\n", "\n    ")} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0755); err != nil {
			t.Fatal(err)
		}
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HERMES_PYTHON_SRC_ROOT", root)
	t.Setenv("HERMES_PYTHON", python)
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	return root
}

const fakeHermesGatewaySource = `import json,sys,os
sid='runtime-owned';key='durable-owned';calls=0
stop_marker=os.getenv('FAKE_HERMES_STOP_MARKER','')
stuck_stop=bool(stop_marker and not os.path.exists(stop_marker))

def send(x): print(json.dumps(x),flush=True)
def event(kind,payload=None,session=sid):send({'jsonrpc':'2.0','method':'event','params':{'type':kind,'session_id':session,'payload':payload or {}}})
def done():
 global calls
 calls+=1
 if os.getenv('FAKE_HERMES_FAIL'):
  event('error',{'message':'PRIVATE_SECRET'})
  event('message.complete',{'text':'','status':'error'})
  return
 event('tool.start',{'tool_id':'tool-'+str(calls),'name':'read_file','args':{'path':'example.txt'}})
 event('tool.complete',{'tool_id':'tool-'+str(calls),'name':'read_file','result':{'content':'fixture-result'}})
 event('message.delta',{'text':'streamed '})
 event('message.delta',{'text':'answer'})
 event('message.complete',{'text':os.getenv('FAKE_HERMES_FINAL','streamed answer'),'status':'complete','usage':{'model':'fixture-model','input':calls*10,'output':calls*3,'total':calls*13,'calls':calls}})
if os.getenv('FAKE_HERMES_STUBBORN'):
 import signal,time;signal.signal(signal.SIGTERM,signal.SIG_IGN)
if os.getenv('FAKE_HERMES_BRIDGE_ERROR'):
 event('blerg.bridge_error',{},'');sys.stdin.read()
 if os.getenv('FAKE_HERMES_STUBBORN'):time.sleep(30)
 sys.exit(0)
if os.getenv('FAKE_HERMES_ENV_LOG'):
 with open(os.environ['FAKE_HERMES_ENV_LOG'],'w') as f:f.write(os.environ.get('HERMES_YOLO_MODE',''))
event('gateway.ready',{},'')
for line in sys.stdin:
 try:r=json.loads(line)
 except:continue
 method=r.get('method','');p=r.get('params',{});rid=r.get('id')
 if os.getenv('FAKE_HERMES_RPC_LOG') or os.getenv('FAKE_HERMES_LOG'):
  with open(os.getenv('FAKE_HERMES_RPC_LOG') or os.environ['FAKE_HERMES_LOG'],'a') as f:f.write(method+'\n')
 if not method:continue
 if method=='client.capabilities':result={'server_requests':True}
 elif method=='mcp.servers.status':result={'servers':[]}
 elif method=='session.create':result={'session_id':sid,'stored_session_id':key,'info':{'model':'fixture-model','reasoning_effort':'low','desktop_contract':8,'tools':{'file':['read_file']},'skills':{},'usage':{'input':0,'output':0}}}
 elif method=='session.resume':
  if set(p)-{'session_id','source','eager_build','omit_messages'}:send({'id':rid,'error':{'code':4000,'message':'invalid parameters'}});continue
  if p.get('session_id')!=key:send({'id':rid,'error':{'code':4007,'message':'missing PRIVATE_SECRET'}});continue
  result={'session_id':sid,'stored_session_id':key,'info':{'model':'fixture-model','desktop_contract':8,'usage':{'input':0,'output':0}}}
 elif method=='prompt.submit':
  if p.get('text')=='reject' and os.getenv('FAKE_HERMES_REJECT_MARKER'):
   import time;open(os.environ['FAKE_HERMES_REJECT_MARKER'],'w').close();time.sleep(0.15)
   send({'id':rid,'result':{'status':'rejected'}} if os.getenv('FAKE_HERMES_REJECT_STATUS') else {'id':rid,'error':{'code':4000,'message':'fixture rejected prompt'}});continue
  send({'id':rid,'result':{'status':'streaming'}});event('message.start')
  if not stuck_stop and not os.getenv('FAKE_HERMES_HOLD') and (not os.getenv('FAKE_HERMES_HOLD_AFTER_FIRST') or calls==0):done()
  continue
 elif method=='session.redirect':
  send({'id':rid,'result':{'status':'redirected'}})
  if not os.getenv('FAKE_HERMES_HOLD_REDIRECT'):done()
  continue
 elif method=='session.interrupt':
  if os.getenv('FAKE_HERMES_NATIVE_ACK_MARKER') and not os.path.exists(os.environ['FAKE_HERMES_NATIVE_ACK_MARKER']):
   import time;open(os.environ['FAKE_HERMES_NATIVE_ACK_MARKER'],'w').close();event('message.complete',{'status':'interrupted'});event('message.start');time.sleep(0.08);send({'id':rid,'result':{'status':'interrupted'}});continue
  if stuck_stop:
   with open(stop_marker,'w') as f:f.write('stopped')
   send({'id':rid,'result':{'status':'interrupting'}});continue
  send({'id':rid,'result':{'status':'interrupted'}});event('message.complete',{'text':'','status':'interrupted'});continue
 else:result={}
 send({'id':rid,'result':result})
if os.getenv('FAKE_HERMES_STUBBORN'):time.sleep(30)
`

func TestHermesGatewayStreamsToolsAndUsage(t *testing.T) {
	fakeHermesGateway(t)
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), SessionID: "blerg", Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	d.EnqueueInput(agent.Input{Text: "first", Source: "chat", ClientID: "first-id"})
	waitUntil(t, "Hermes structured completion", func() bool { return countKind(em, "turn_done") == 1 })
	if countKind(em, "tool_call") != 1 || countKind(em, "tool_result") != 1 {
		t.Fatal("Hermes native tool events were lost")
	}
	d.Enqueue("second", "chat")
	waitUntil(t, "second Hermes completion", func() bool { return countKind(em, "turn_done") == 2 })
	em.mu.Lock()
	defer em.mu.Unlock()
	for _, ev := range em.events {
		if ev.Kind == "turn_done" {
			p := ev.Payload.(agent.TurnDonePayload)
			if p.Usage.InputTokens != 10 || p.Usage.OutputTokens != 3 {
				t.Fatalf("cumulative usage counted as a turn: %+v", p.Usage)
			}
		}
	}
}
func TestHermesGatewayMidTurnSteering(t *testing.T) {
	fakeHermesGateway(t)
	t.Setenv("FAKE_HERMES_HOLD", "1")
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), SessionID: "blerg", Emitter: em})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go d.Run(ctx)
	d.Enqueue("first", "chat")
	waitUntil(t, "Hermes first acceptance", func() bool { return countKind(em, "user_message") == 1 })
	d.EnqueueInput(agent.Input{Text: "correction", Source: "chat", ClientID: "steer-id"})
	waitUntil(t, "Hermes native steer completion", func() bool { return countKind(em, "turn_done") == 1 })
	if countKind(em, "user_message") != 2 {
		t.Fatal("mid-turn correction was not acknowledged")
	}
}

func TestHermesResumeWaitsForRecoveryAuthority(t *testing.T) {
	root := fakeHermesGateway(t)
	log := filepath.Join(root, "rpc.log")
	t.Setenv("FAKE_HERMES_RPC_LOG", log)
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), Emitter: &collectEmitter{}})
	d.nativeID = "durable-owned"
	blocked := make(chan struct{})
	entered := make(chan struct{})
	d.recoveryInputGate = func(ctx context.Context) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		select {
		case <-blocked:
		case <-ctx.Done():
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.Run(ctx)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("native resume bypassed recovery admission gate")
	}
	raw, _ := os.ReadFile(log)
	if strings.Contains(string(raw), "session.resume") {
		t.Fatal("native auto-continuation was authorized before recovery readiness")
	}
	close(blocked)
	waitUntil(t, "native resume after authorization", func() bool { b, _ := os.ReadFile(log); return strings.Contains(string(b), "session.resume") })
}

func TestHermesStopFallbackHoldsQueuedInput(t *testing.T) {
	shrink(t, &hermesInterruptResultAfter, 100*time.Millisecond)
	fakeHermesGateway(t)
	marker := filepath.Join(t.TempDir(), "stopped")
	logPath := filepath.Join(t.TempDir(), "calls")
	t.Setenv("FAKE_HERMES_STOP_MARKER", marker)
	t.Setenv("FAKE_HERMES_RPC_LOG", logPath)
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	defer func() { cancel(); <-done }()
	d.Enqueue("first", "chat")
	waitUntil(t, "first Hermes turn", func() bool { return countKind(em, "user_message") == 1 })
	d.Interrupt()
	d.Enqueue("next", "chat")
	d.Interrupt()
	waitUntil(t, "Hermes Stop fallback and queue", func() bool { return countKind(em, "turn_done") == 2 })
	if em.count("turn_done:interrupted") != 1 || em.count("user:next") != 1 || countKind(em, "error") != 0 {
		t.Fatal(em.outline())
	}
	raw, _ := os.ReadFile(logPath)
	if strings.Contains(string(raw), "session.redirect") {
		t.Fatal("queued input steered the stopped Hermes turn")
	}
	if !strings.Contains(string(raw), "session.resume") {
		t.Fatal("fallback did not resume saved conversation")
	}
}

func TestHermesStopReportsRestartRefusal(t *testing.T) {
	shrink(t, &hermesInterruptResultAfter, 200*time.Millisecond)
	fakeHermesGateway(t)
	t.Setenv("FAKE_HERMES_STOP_MARKER", filepath.Join(t.TempDir(), "stopped"))
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), Emitter: em})
	starts := 0
	d.startCommand = func(cmd *exec.Cmd) error {
		starts++
		if starts > 1 {
			return errors.New("fixture containment refusal")
		}
		return cmd.Start()
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	defer func() { cancel(); <-done }()
	d.Enqueue("first", "chat")
	waitUntil(t, "Hermes turn", func() bool { return countKind(em, "user_message") == 1 })
	entered, release := make(chan struct{}), make(chan struct{})
	d.mu.Lock()
	d.recoveryInputGate = func(ctx context.Context) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	d.mu.Unlock()
	d.EnqueueInput(agent.Input{Text: "next", ClientID: "held"})
	<-entered
	d.Interrupt()
	close(release)
	waitUntil(t, "clear restart refusal", func() bool {
		for _, e := range em.snapshot() {
			if p, ok := e.Payload.(agent.ErrorPayload); ok && p.Code == agent.ErrorCodeRuntimeStopped && strings.Contains(p.Message, "fixture containment refusal") {
				return true
			}
		}
		return false
	})
	if em.count("user:next") != 0 {
		t.Fatal("refused restart recorded undelivered input")
	}
	waitUntil(t, "undelivered receipt", func() bool {
		for _, e := range em.snapshot() {
			if p, ok := e.Payload.(agent.ErrorPayload); ok && p.ClientID == "held" {
				return true
			}
		}
		return false
	})
}

func TestHermesStopRefusalKeepsQueueForRetry(t *testing.T) {
	shrink(t, &hermesInterruptResultAfter, 60*time.Millisecond)
	fakeHermesGateway(t)
	t.Setenv("FAKE_HERMES_STOP_MARKER", filepath.Join(t.TempDir(), "stopped"))
	em := &collectEmitter{}
	d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], WorkDir: t.TempDir(), Emitter: em})
	var refuse atomic.Bool
	refuse.Store(true)
	d.startCommand = func(cmd *exec.Cmd) error {
		cancel := cmd.Cancel
		cmd.Cancel = func() error {
			if refuse.CompareAndSwap(true, false) {
				return errors.New("fixture stop refusal")
			}
			return cancel()
		}
		return cmd.Start()
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	defer func() { refuse.Store(false); cancel(); <-done }()
	d.Enqueue("first", "chat")
	waitUntil(t, "Hermes turn", func() bool { return countKind(em, "user_message") == 1 })
	d.Interrupt()
	d.EnqueueInput(agent.Input{Text: "next", ClientID: "held"})
	waitUntil(t, "visible Stop refusal", func() bool {
		for _, e := range em.snapshot() {
			if p, ok := e.Payload.(agent.ErrorPayload); ok && strings.Contains(p.Message, "fixture stop refusal") {
				return true
			}
		}
		return false
	})
	if countKind(em, "turn_done") != 0 || em.count("user:next") != 0 {
		t.Fatal("refused Stop reported success")
	}
	d.Interrupt()
	waitIdle(t, em, 2)
	if em.count("user:next") != 1 || em.count("turn_done:interrupted") != 1 {
		t.Fatal(em.outline())
	}
}
