package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
)

func TestCodexConnectionFailureRetainsCauseOnInterruptedTurnAndExit(t *testing.T) {
	for _, mode := range []string{"interrupted", "exit", "requested"} {
		t.Run(mode, func(t *testing.T) {
			dir := fakeCodex(t)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("FAKE_CODEX_CONNECTION_FAILURE", mode)
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			em := &collectEmitter{}
			d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
			stop := startTestCodexDriver(t.Context(), t, d)
			d.Enqueue("Reply without tools", "chat")
			if mode == "requested" {
				waitUntil(t, "connection retry", func() bool { return countKind(em, "error") > 0 })
				d.Interrupt()
			}
			waitUntil(t, "failed turn", func() bool { return countKind(em, "turn_done") > 0 })
			stop()
			var finalCause bool
			for _, ev := range em.snapshot() {
				if ev.Kind == "error" {
					p, _ := ev.Payload.(agent.ErrorPayload)
					if p.Code != agent.ErrorCodeAPIRetry && strings.Contains(p.Message, "TLS handshake failed") {
						finalCause = true
					}
				}
				if ev.Kind == "turn_done" {
					p, _ := ev.Payload.(agent.TurnDonePayload)
					want := "error"
					if mode == "requested" {
						want = "interrupted"
					}
					if p.StopReason != want {
						t.Error("connection failure or requested interruption recorded incorrectly")
					}
				}
			}
			if (mode != "requested" && !finalCause) || !strings.Contains(logs.String(), "TLS handshake failed") {
				t.Error("engine connection cause missing from final error or pod log")
			}
			all, _ := json.Marshal(em.snapshot())
			for _, secret := range []string{"fixture-private-token", "fixture-password", "fixture-query"} {
				if bytes.Contains(all, []byte(secret)) || strings.Contains(logs.String(), secret) {
					t.Error("connection diagnostic disclosed a credential")
				}
			}
		})
	}
}

// startTestCodexDriver owns the runtime until it has stopped, including runApp's
// deferred process/PID cleanup. Callers defer the returned stop function so it
// runs before any t.Cleanup restores environment or sandbox globals; Cleanup is
// also registered as a fallback. A completed model turn is not a stopped runtime.
func startTestCodexDriver(parent context.Context, t *testing.T, driver *codexDriver) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			timer := time.NewTimer(15 * time.Second)
			defer timer.Stop()
			select {
			case <-done:
			case <-timer.C:
				t.Error("Codex fixture runtime did not stop after cancellation")
			}
		})
	}
	t.Cleanup(stop)
	go func() { defer close(done); driver.Run(ctx) }()
	return stop
}

// fakeCodex implements the installed Codex app-server RPC shapes without
// requiring a login or running a model.
func fakeCodex(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/usr/bin/env python3
import json, os, sys, time, subprocess
if "--version" in sys.argv:
 print(os.environ.get("FAKE_CODEX_VERSION","codex-cli 0.162.1"));sys.exit(0)
if os.environ.get("FAKE_CODEX_ENV_DUMP"):
 with open(os.environ["FAKE_CODEX_ENV_DUMP"],"w") as dump: json.dump(dict(os.environ),dump)
with open(os.environ.get("FAKE_CODEX_LOG", os.devnull), "a") as log: log.write("codex " + " ".join(sys.argv[1:]) + "\n")
if os.environ.get("FAKE_CODEX_HELPERS"):
 helper=subprocess.Popen(["sleep", "300"],stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 with open(os.environ["FAKE_CODEX_HELPERS"],"a") as log: log.write(str(helper.pid)+"\n")
mcp_reads=0
turn_starts=0
for line in sys.stdin:
 r=json.loads(line); method=r.get("method", ""); ident=r.get("id")
 with open(os.environ.get("FAKE_CODEX_LOG", os.devnull), "a") as log: log.write(method + " " + json.dumps(r.get("params",{})) + "\n")
 def send(v): print(json.dumps(v), flush=True)
 if ident is None: continue
 if os.environ.get("FAKE_CODEX_REJECT_PLUGIN")==method and any("selected@market" in arg for arg in sys.argv):
  gate=os.environ.get("FAKE_CODEX_PLUGIN_GATE")
  if gate:
   with open(gate+".waiting","w") as f: f.write("ready")
   while not os.path.exists(gate): time.sleep(0.005)
  send({"id":ident,"error":{"code":-32603,"message":"fixture plugin load failed"}})
  sys.exit(1)
 if method=="" and ("result" in r or "error" in r):
  if ident==2000:
   send({"method":"turn/completed","params":{"threadId":"cx-thread-1","turn":{"id":"turn-1","status":"completed"}}})
  if ident==1000:
   send({"method":"item/completed","params":{"threadId":"cx-thread-1","item":{"type":"dynamicToolCall","id":"dynamic1","tool":"read_file","status":"completed","success":r["result"].get("success"),"contentItems":r["result"].get("contentItems")}}})
   send({"method":"turn/completed","params":{"threadId":"cx-thread-1","turn":{"id":"turn-1","status":"completed"}}})
  continue
 result={}
 if method=="mcpServerStatus/list": result={"data":[],"nextCursor":None}
 if method=="mcpServerStatus/list" and os.environ.get("FAKE_CODEX_MCP_STATUS"): result={"data":[{"name":"fixture-server","runtimeStatus":os.environ["FAKE_CODEX_MCP_STATUS"],"tools":{}}],"nextCursor":None}
 if method=="mcpServerStatus/list" and os.environ.get("FAKE_CODEX_MCP_TRANSITION"):
  mcp_reads += 1
  if mcp_reads > 1: time.sleep(0.1)
  send({"id":ident,"result":{"data":[{"name":"fixture-server","runtimeStatus":"starting" if mcp_reads==1 else "connected","tools":{}}],"nextCursor":None}})
  if mcp_reads==1:
   for _ in range(100): send({"method":os.environ["FAKE_CODEX_MCP_TRANSITION"],"params":{"threadId":"cx-thread-1","name":"fixture-server","status":"ready","success":True}})
  continue
 if method=="mcpServerStatus/list" and os.environ.get("FAKE_CODEX_MCP_FAILURE"):
  if os.environ["FAKE_CODEX_MCP_FAILURE"]=="partial" and not r.get("params",{}).get("cursor"):
   send({"id":ident,"result":{"data":[{"name":"fixture-server","runtimeStatus":"ready","tools":{}}],"nextCursor":"second"}})
  elif os.environ["FAKE_CODEX_MCP_FAILURE"]=="missing-data": send({"id":ident,"result":{}})
  elif os.environ["FAKE_CODEX_MCP_FAILURE"]=="null-data": send({"id":ident,"result":{"data":None}})
  else: send({"id":ident,"error":{"code":-32603,"message":"fixture failure"}})
  continue
 if method=="thread/read" and r.get("params",{}).get("threadId")=="cx-thread-1": result={"thread":{"id":"cx-thread-1","status":{"type":"idle"},"turns":[]}}
 if method=="thread/read" and not os.environ.get("FAKE_CODEX_CHILD_READ"):
  child_id=r.get("params",{}).get("threadId")
  if (child_id=="owned-child" and os.environ.get("FAKE_CODEX_CHILD_QUESTION") in ("owned","stale")) or (child_id=="child-thread" and (os.environ.get("FAKE_CODEX_CHILD_FOLLOWUP") or os.environ.get("FAKE_CODEX_CHILD_REROUTE"))):
   result={"thread":{"id":child_id,"parentThreadId":"cx-thread-1"}}
 if method=="thread/read" and os.environ.get("FAKE_CODEX_CHILD_READ"):
  time.sleep(0.03)
  result={"thread":json.loads(os.environ["FAKE_CODEX_CHILD_READ"]).get(r.get("params",{}).get("threadId"),{})}
 if method=="thread/list" and os.environ.get("FAKE_CODEX_CHILD_CATALOG"): result=json.loads(os.environ["FAKE_CODEX_CHILD_CATALOG"])
 if method=="model/list": result=json.loads(os.environ.get("FAKE_CODEX_MODELS",'{"data":[],"nextCursor":null}'))
 if method=="config/read": result={"config":json.loads(os.environ.get("FAKE_CODEX_CONFIG","{}"))}
 if method in ("thread/start", "thread/resume"): result={"thread":{"id":"cx-thread-1"},"model":"gpt-5-codex","reasoningEffort":"high"}
 if method in ("thread/start", "thread/resume") and os.environ.get("FAKE_CODEX_OMIT_INITIAL_SETTINGS"):
  result={"thread":{"id":"cx-thread-1"}}
 if method=="skills/list" and os.environ.get("FAKE_CODEX_SKILL"): result={"data":[{"skills":[{"name":"review","path":"/opt/skills/review/SKILL.md","description":"Review code","scope":"project","enabled":True}]}]}
 if method=="turn/start":
  turn_starts += 1
  result={"turn":{"id":"turn-"+str(turn_starts) if os.environ.get("FAKE_CODEX_STEER_RACE") else "turn-1"}}
 if method=="turn/start" and os.environ.get("FAKE_CODEX_MISSING_TURN_ID"): result={}
 if method=="turn/steer" and os.environ.get("FAKE_CODEX_STEER_RACE"):
  completion={"method":"turn/completed","params":{"threadId":"cx-thread-1","turn":{"id":"turn-1","status":"completed"}}}
  mode=os.environ["FAKE_CODEX_STEER_RACE"]
  if mode=="before": send(completion)
  send({"id":ident,"error":{"code":-32600,"message":"no active turn to steer" if mode!="other-error" else "fixture validation rejected"}})
  if mode=="after":
   time.sleep(0.02)
   send(completion)
  continue
 send({"id":ident,"result":result})
 if method=="turn/start" and os.environ.get("FAKE_CODEX_ROOT_USAGE_EVENTS"):
  gate=os.environ.get("FAKE_CODEX_ROOT_USAGE_GATE")
  if gate:
   send({"method":"item/agentMessage/delta","params":{"threadId":"cx-thread-1","delta":"Fixture awaiting usage"}})
   while not os.path.exists(gate): time.sleep(0.005)
  for notification in json.loads(os.environ["FAKE_CODEX_ROOT_USAGE_EVENTS"]): send(notification)
  continue
 if method=="turn/start" and os.environ.get("FAKE_CODEX_CHILD_FOLLOWUP"):
  send({"method":"thread/started","params":{"thread":{"id":"child-thread","parentThreadId":"cx-thread-1","model":"fixture-child-model","preview":"First task"}}})
  send({"method":"turn/started","params":{"threadId":"child-thread","turn":{"id":"child-turn-1","status":"inProgress"}}})
  send({"method":"turn/completed","params":{"threadId":"child-thread","turn":{"id":"child-turn-1","status":"completed"}}})
  send({"method":"turn/started","params":{"threadId":"child-thread","turn":{"id":"child-turn-2","status":"inProgress"}}})
  send({"method":"item/completed","params":{"threadId":"child-thread","item":{"id":"child-message-2","type":"agentMessage","text":"Follow-up result"}}})
  send({"method":"turn/completed","params":{"threadId":"child-thread","turn":{"id":"child-turn-2","status":"completed"}}})
 if method=="turn/start" and os.environ.get("FAKE_CODEX_ROOT_REROUTE"):
  gate=os.environ.get("FAKE_CODEX_ROOT_REROUTE_GATE")
  if gate:
   send({"method":"item/agentMessage/delta","params":{"threadId":"cx-thread-1","delta":"Fixture waiting before reroute"}})
   until=time.monotonic()+5
   while not os.path.exists(gate) and time.monotonic()<until: time.sleep(0.005)
  send({"method":"model/rerouted","params":{"threadId":"cx-thread-1","toModel":"fixture-root-rerouted"}})
  if os.environ["FAKE_CODEX_ROOT_REROUTE"]=="settings":
   send({"method":"thread/settings/updated","params":{"threadId":"cx-thread-1","threadSettings":{"model":"fixture-root-settings","effort":"low"}}})
 if method=="turn/start" and os.environ.get("FAKE_CODEX_CHILD_REROUTE"):
  send({"method":"thread/started","params":{"thread":{"id":"child-thread","parentThreadId":"cx-thread-1","model":"fixture-original","preview":"Review"}}})
  send({"method":"turn/started","params":{"threadId":"child-thread","turn":{"id":"child-turn","status":"inProgress"}}})
  if os.environ.get("FAKE_CODEX_CHILD_EFFORT"):
   send({"method":"thread/settings/updated","params":{"threadId":"child-thread","threadSettings":{"model":"fixture-original","effort":"high"}}})
   send({"method":"thread/settings/updated","params":{"threadId":"child-thread","threadSettings":{"model":"fixture-original"}}})
   send({"method":"thread/started","params":{"thread":{"id":"child-thread","parentThreadId":"cx-thread-1","model":"fixture-original","preview":"Review"}}})
   send({"method":"thread/settings/updated","params":{"threadId":"child-thread","threadSettings":{"model":"fixture-original","effort":None}}})
  send({"method":"thread/tokenUsage/updated","params":{"threadId":"child-thread","tokenUsage":{"total":{"inputTokens":20,"cachedInputTokens":5,"outputTokens":3}}}})
  send({"method":"thread/tokenUsage/updated","params":{"threadId":"child-thread","turnId":"old-child-turn","tokenUsage":{"total":{"inputTokens":9999,"outputTokens":9999}}}})
  send({"method":"model/rerouted","params":{"threadId":"child-thread","toModel":"fixture-rerouted"}})
  send({"method":"thread/tokenUsage/updated","params":{"threadId":"child-thread","tokenUsage":{"total":{"inputTokens":50,"cachedInputTokens":15,"outputTokens":10}}}})
  send({"method":"turn/completed","params":{"threadId":"child-thread","turn":{"id":"child-turn","status":"completed"}}})
 if method=="turn/start" and os.environ.get("FAKE_CODEX_AUTH_DIAGNOSTIC"):
  sys.stderr.write("Third-party tool secret: sample-private-value\n")
  sys.stderr.write("auth: Failed to refresh token: Your access token could not be refreshed. Please log out and sign in again.\n")
  sys.stderr.flush()
  time.sleep(0.1)
 if method=="turn/start" and os.environ.get("FAKE_CODEX_RECOVERED_CHILD"):
  send({"method":"turn/started","params":{"threadId":"historical-child","turn":{"id":"recovered-turn","status":"inProgress"}}})
  send({"method":"item/completed","params":{"threadId":"historical-child","item":{"type":"agentMessage","id":"recovered-message","text":"Recovered child progress"}}})
  send({"method":"turn/completed","params":{"threadId":"historical-child","turn":{"id":"recovered-turn","status":"completed"}}})
 if method=="turn/start" and os.environ.get("FAKE_CODEX_CHILD_QUESTION"):
  mode=os.environ["FAKE_CODEX_CHILD_QUESTION"]
  if mode=="native-source" or mode=="unproven-source":
   send({"method":"item/completed","params":{"threadId":"cx-thread-1","item":{"type":"subAgentActivity","id":"native-start","kind":"started","agentThreadId":"owned-child","model":"fixture-child-model"}}})
  else:
   send({"method":"thread/started","params":{"thread":{"id":"owned-child","parentThreadId":"foreign-root" if mode in ("foreign","foreign-item") else "cx-thread-1","model":"fixture-child-model","preview":"Review"}}})
  if mode=="foreign-item": send({"method":"item/completed","params":{"threadId":"cx-thread-1","item":{"type":"collabAgentToolCall","id":"foreign-claim","tool":"sendInput","agentsStates":{"owned-child":{"status":"running","message":"Foreign metadata"}}}}})
  send({"method":"turn/started","params":{"threadId":"owned-child","turn":{"id":"child-turn","status":"inProgress"}}})
  send({"id":2000,"method":"item/tool/requestUserInput","params":{"threadId":"owned-child","turnId":"stale-turn" if mode=="stale" else "child-turn","isBlocking":True,"questions":[{"id":"choice","question":"Child question","options":[{"label":"Blue","description":"Cool tone"}]}]}})
  continue
 if method=="turn/start" and os.environ.get("FAKE_CODEX_QUESTION"):
  send({"id":2000,"method":"item/tool/requestUserInput","params":{"threadId":"cx-thread-1","turnId":"turn-1","isBlocking":not bool(os.environ.get("FAKE_CODEX_NONBLOCKING_QUESTION")),"questions":[{"id":"color","header":"Color","question":"Choose a color","isSecret":bool(os.environ.get("FAKE_CODEX_SECRET")),"options":[{"label":"Blue","description":"Cool tone"},{"label":"Green","description":"Natural tone"}]}]}})
  continue
 if method=="turn/interrupt":
  send({"method":"turn/completed","params":{"threadId":"cx-thread-1","turn":{"id":"turn-1","status":"interrupted"}}})
 if method=="turn/start" and os.environ.get("FAKE_CODEX_DYNAMIC"):
  send({"method":"item/started","params":{"threadId":"cx-thread-1","item":{"type":"dynamicToolCall","id":"dynamic1","tool":"read_file","arguments":{"path":"sample.txt"},"status":"inProgress"}}})
  send({"id":1000,"method":"item/tool/call","params":{"threadId":"cx-thread-1","turnId":"turn-1","tool":"read_file","arguments":{"path":"sample.txt"}}})
  continue
 if method=="turn/start" and os.environ.get("FAKE_CODEX_EXIT_ONCE") and not os.path.exists(os.environ["FAKE_CODEX_EXIT_ONCE"]):
  open(os.environ["FAKE_CODEX_EXIT_ONCE"], "w").close()
  time.sleep(0.1)
  sys.exit(0)
 if method=="turn/start" and os.environ.get("FAKE_CODEX_LIMIT"):
  message="You've hit your usage limit. Try again at 2030-01-02T03:04:05Z."
  send({"method":"error","params":{"threadId":"cx-thread-1","error":{"message":message},"willRetry":False}})
  send({"method":"turn/completed","params":{"threadId":"cx-thread-1","turn":{"id":"turn-1","status":"failed","error":{"message":message}}}})
  continue
 if method=="turn/start" and os.environ.get("FAKE_CODEX_CONNECTION_FAILURE"):
  send({"method":"error","params":{"threadId":"cx-thread-1","error":{"message":"Reconnecting... 5/5","additionalDetails":"TLS handshake failed; Authorization: Bearer fixture-private-token; https://user:fixture-password@example.test/responses?token=fixture-query"},"willRetry":True}})
  if os.environ["FAKE_CODEX_CONNECTION_FAILURE"]=="exit": sys.exit(1)
  if os.environ["FAKE_CODEX_CONNECTION_FAILURE"]=="requested": continue
  send({"method":"turn/completed","params":{"threadId":"cx-thread-1","turn":{"id":"turn-1","status":"interrupted"}}})
  continue
 if method=="turn/start" and os.environ.get("FAKE_CODEX_STREAM_FIRST"):
  for _ in range(1000): send({"method":"item/agentMessage/delta","params":{"threadId":"cx-thread-1","delta":"progress"}})
  continue
 if method=="turn/start" and (os.environ.get("FAKE_CODEX_HOLD_FIRST") or (os.environ.get("FAKE_CODEX_STEER_RACE") and turn_starts==1)): continue
 if method in ("turn/start", "turn/steer"):
  if os.environ.get("FAKE_CODEX_REASONING_PROGRESS"):
   # The real order: a request's reasoning item closes, then its usage arrives once the response
   # completes. A usage update that lands while the next request's reasoning item is open still
   # carries the previous request's counts.
   send({"method":"item/started","params":{"threadId":"cx-thread-1","item":{"id":"reasoning-fixture","type":"reasoning"}}})
   send({"method":"item/completed","params":{"threadId":"cx-thread-1","item":{"id":"reasoning-fixture","type":"reasoning","summary":[]}}})
   send({"method":"thread/tokenUsage/updated","params":{"threadId":"cx-thread-1","tokenUsage":{"last":{"inputTokens":100,"outputTokens":20,"reasoningOutputTokens":13}}}})
   send({"method":"item/started","params":{"threadId":"cx-thread-1","item":{"id":"reasoning-fixture-2","type":"reasoning"}}})
   send({"method":"thread/tokenUsage/updated","params":{"threadId":"cx-thread-1","tokenUsage":{"last":{"inputTokens":100,"outputTokens":20,"reasoningOutputTokens":13}}}})
   send({"method":"item/completed","params":{"threadId":"cx-thread-1","item":{"id":"reasoning-fixture-2","type":"reasoning","summary":[]}}})
  for name,item in [("item/completed",{"id":"i0","type":"agentMessage","text":"Reading the card."}), ("item/started",{"id":"i1","type":"commandExecution","command":"ls","status":"inProgress"}), ("item/completed",{"id":"i1","type":"commandExecution","command":"ls","status":"completed","aggregatedOutput":"README.md\n","exitCode":127 if os.environ.get("FAKE_CODEX_COMMAND_FAIL") else 0}), ("item/completed",{"id":"i2","type":"agentMessage","text":"Done."})]: send({"method":name,"params":{"threadId":"cx-thread-1","item":item}})
  send({"method":"thread/tokenUsage/updated","params":{"threadId":"cx-thread-1","tokenUsage":{"last":{"inputTokens":100,"cachedInputTokens":10,"outputTokens":20}}}})
  send({"method":"turn/completed","params":{"threadId":"cx-thread-1","turn":{"id":"turn-"+str(turn_starts) if os.environ.get("FAKE_CODEX_STEER_RACE") else "turn-1","status":"completed"}}})
`
	path := filepath.Join(dir, "codex")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCodexDriverTurn(t *testing.T) {
	binDir := fakeCodex(t)
	logPath := filepath.Join(binDir, "calls.log")
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_LOG", logPath)

	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Model: "gpt-5-codex", Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopDriver := startTestCodexDriver(ctx, t, d)
	defer stopDriver()

	d.Enqueue("work the card", "chat")

	deadline := time.After(15 * time.Second)
	for {
		kinds := em.kinds()
		if contains(kinds, "turn_done") {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("no turn_done; kinds so far: %v", kinds)
		case <-time.After(50 * time.Millisecond):
		}
	}

	kinds := em.kinds()
	for _, want := range []string{"user_message", "status_changed", "assistant_text", "tool_call", "tool_result", "turn_done"} {
		if !contains(kinds, want) {
			t.Errorf("missing event kind %q in %v", want, kinds)
		}
	}

	// Second turn resumes the codex thread.
	d.Enqueue("continue", "chat")
	deadline = time.After(15 * time.Second)
	for countKind(em, "turn_done") < 2 {
		select {
		case <-deadline:
			t.Fatal("second turn_done never arrived")
		case <-time.After(50 * time.Millisecond):
		}
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !containsStr(string(raw), "app-server --listen stdio://") {
		t.Errorf("turns must share a persistent app-server; calls:\n%s", raw)
	}
}

// TestCodexDriverFailedCommandMarksToolResultError verifies that a command
// exiting non-zero (status "failed", not "completed") is surfaced as an
// error tool_result — mirrors what codex-cli 0.154.0 actually emits for a
// failing shell command (see codexdriver_test.go's sibling fixture and the
// real capture in codexdriver.go's doc comment).
func TestCodexDriverFailedCommandMarksToolResultError(t *testing.T) {
	dir := fakeCodex(t)
	t.Setenv("FAKE_CODEX_COMMAND_FAIL", "1")
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopDriver := startTestCodexDriver(ctx, t, d)
	defer stopDriver()
	d.Enqueue("do the thing", "chat")

	deadline := time.After(15 * time.Second)
	for !contains(em.kinds(), "turn_done") {
		select {
		case <-deadline:
			t.Fatal("no turn_done")
		case <-time.After(50 * time.Millisecond):
		}
	}

	em.mu.Lock()
	defer em.mu.Unlock()
	found := false
	for _, ev := range em.events {
		if ev.Kind != "tool_result" {
			continue
		}
		payload, ok := ev.Payload.(agent.ToolResultPayload)
		if !ok {
			t.Fatalf("tool_result payload has unexpected type %T", ev.Payload)
		}
		found = true
		if !payload.IsError {
			t.Errorf("tool_result.IsError = false for a failed (exit 127) command, want true")
		}
	}
	if !found {
		t.Fatal("no tool_result event emitted")
	}
}

func TestCodexDriverReportsExpiredAuthWithoutForwardingStderr(t *testing.T) {
	t.Setenv("CODEX_AUTH_JSON", `{"OPENAI_API_KEY":"fixture-api-private"}`)
	t.Setenv("PATH", fakeCodex(t)+":"+os.Getenv("PATH"))
	t.Setenv("FAKE_CODEX_AUTH_DIAGNOSTIC", "1")
	em := &collectEmitter{}
	d := newCodexDriver(AgentDriverOpts{Spec: engineRegistry["codex"], WorkDir: t.TempDir(), Emitter: em})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopDriver := startTestCodexDriver(ctx, t, d)
	defer stopDriver()
	d.Enqueue("work", "chat")
	deadline := time.Now().Add(15 * time.Second)
	for !contains(em.kinds(), "turn_done") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	found := false
	for _, ev := range em.snapshot() {
		if ev.Kind != "error" {
			continue
		}
		p, ok := ev.Payload.(agent.ErrorPayload)
		if !ok {
			continue
		}
		if strings.Contains(p.Message, "Settings") && strings.Contains(p.Message, "new person-owned cluster session") && strings.Contains(p.Message, "API key") {
			found = true
		}
		if strings.Contains(p.Message, "sample-private-value") {
			t.Fatal("stderr secret reached error event")
		}
	}
	if !found {
		t.Fatal("expired Codex login was hidden from the session")
	}
}

func TestCodexAuthDiagnosticDiscardsRawOutput(t *testing.T) {
	var events []agent.ErrorPayload
	w := &codexAuthDiagnostic{emit: func(_ string, p any) { events = append(events, p.(agent.ErrorPayload)) }}
	for _, chunk := range []string{strings.Repeat("sample-private-value ", 1000), "auth: Failed to refresh token: Your access token ", "could not be refreshed. Please log out and sign in again.\n", "auth: Failed to refresh token: Your access token could not be refreshed."} {
		n, err := w.Write([]byte(chunk))
		if n != len(chunk) || err != nil {
			t.Fatalf("Write = %d, %v; want %d, nil", n, err, len(chunk))
		}
	}
	if len(events) != 1 {
		t.Fatalf("got %d diagnostics; want exactly one", len(events))
	}
	if !events[0].Retryable {
		t.Fatal("login repair must leave the session available to retry")
	}
	if strings.Contains(events[0].Message, "sample-private-value") || w.window != "" {
		t.Fatal("raw stderr retained or forwarded")
	}
}
