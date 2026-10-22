package daemon

import (
	"context"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
  send({'id':rid,'result':{'status':'streaming'}});event('message.start')
  if not os.getenv('FAKE_HERMES_HOLD') and (not os.getenv('FAKE_HERMES_HOLD_AFTER_FIRST') or calls==0):done()
  continue
 elif method=='session.redirect':
  send({'id':rid,'result':{'status':'redirected'}});done();continue
 elif method=='session.interrupt':
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
