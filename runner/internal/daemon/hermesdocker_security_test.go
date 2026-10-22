package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This opt-in test uses a disposable, credential-free container, never an
// existing session container. The image must contain the native Python venv.
func TestHermesDockerSecurityStopsInnerGatewayBeforeReplacement(t *testing.T) {
	image := os.Getenv("BLERG_HERMES_DOCKER_SECURITY_IMAGE")
	if image == "" {
		t.Skip("set BLERG_HERMES_DOCKER_SECURITY_IMAGE for isolated Docker lifecycle proof")
	}
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal stop", true: "unconfirmed blocked write"}[uncertain], func(t *testing.T) {
			root := fakeHermesGateway(t)
			entry := `import json,sys,time,subprocess,signal,os

def main():
 children=[]
 def shutdown(signum,frame):
  for child in children:
   os.killpg(child.pid,signal.SIGKILL)
   child.wait()
  raise SystemExit(0)
 signal.signal(signal.SIGTERM,shutdown)
 def send(x): print(json.dumps(x),flush=True)
 send({'method':'event','params':{'type':'gateway.ready','payload':{}}})
 for line in sys.stdin:
  r=json.loads(line)
  if r.get('method') in ('session.create','session.resume'):
   child=subprocess.Popen(['sleep','infinity'],start_new_session=True)
   children.append(child)
   with open('/tmp/cx-hermes-child.pid','w') as f:f.write(str(child.pid))
   send({'id':r['id'],'result':{'session_id':'owned-runtime','stored_session_id':'owned-durable','info':{'model':'fixture','desktop_contract':8}}})
   while True: time.sleep(1)
  else: send({'id':r['id'],'result':{}})
`
			if err := os.WriteFile(filepath.Join(root, "tui_gateway", "entry.py"), []byte(entry), 0600); err != nil {
				t.Fatal(err)
			}
			name := "cx_hermes_security_" + strings.ReplaceAll(ccUUID(), "-", "")[:12]
			docker := func(args ...string) ([]byte, error) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				return exec.CommandContext(ctx, "docker", args...).Output()
			}
			label := labelTestContainers(t)
			_, err := docker("run", "--detach", "--name", name, "--label", label, "--label", sandboxTestLabels[0], "--user", strconv.Itoa(os.Getuid())+":"+strconv.Itoa(os.Getgid()), "--network", "none", "--mount", "type=bind,source="+filepath.Join(root, "tui_gateway")+",target=/home/agent/.hermes/hermes-agent/tui_gateway,readonly", "--entrypoint", "sleep", image, "infinity")
			if err != nil {
				t.Fatalf("disposable Docker fixture could not start: %v", err)
			}
			// Removed by its test label (labelTestContainers), never by name.
			d := newHermesDriver(AgentDriverOpts{Spec: engineRegistry["hermes"], Emitter: &collectEmitter{}, Env: []string{"PATH=/usr/bin:/bin", "HOME=/tmp"}})
			d.useSandbox(sandboxExec{"docker", "exec", "-i", name})
			probeCtx, probeCancel := context.WithTimeout(t.Context(), 5*time.Second)
			probe, probeErr := d.gatewayCommand(probeCtx)
			if probeErr != nil {
				probeCancel()
				t.Fatal(probeErr)
			}
			probeOutput, probeErr := probe.CombinedOutput()
			probeCancel()
			if probeErr != nil {
				t.Fatalf("credential-free fixture bootstrap failed: %v (%s)", probeErr, string(probeOutput))
			}

			start := func() (context.CancelFunc, <-chan struct{}, string) {
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan struct{})
				go func() { defer close(done); d.runGateway(ctx) }()
				t.Cleanup(func() {
					cancel()
					select {
					case <-done:
					case <-time.After(20 * time.Second):
						t.Error("disposable native worker did not stop during cleanup")
					}
				})
				waitUntil(t, "disposable native gateway identity", func() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.runtimeID != "" })
				raw, err := docker("exec", name, "cat", hermesGatewayPIDFile)
				if err != nil {
					cancel()
					t.Fatal("native gateway PID proof missing")
				}
				pid := strings.TrimSpace(string(raw))
				n, err := strconv.Atoi(pid)
				if err != nil || n <= 1 {
					cancel()
					t.Fatal("invalid disposable native gateway PID")
				}
				return cancel, done, pid
			}
			stop, done, pid := start()
			childRaw, childErr := docker("exec", name, "cat", "/tmp/cx-hermes-child.pid")
			if childErr != nil {
				t.Fatal("detached fixture child proof missing")
			}
			childPID := strings.TrimSpace(string(childRaw))
			if childN, err := strconv.Atoi(childPID); err != nil || childN <= 1 {
				t.Fatal("invalid detached child proof")
			}
			if uncertain {
				d.mu.Lock()
				rpc := d.rpc
				d.mu.Unlock()
				ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
				_, err := rpc.call(ctx, "session.usage", map[string]string{"payload": strings.Repeat("x", 2<<20)})
				cancel()
				if err == nil {
					t.Fatal("unread oversized native write was unexpectedly acknowledged")
				}
			} else {
				stop()
			}
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				stop()
				t.Fatal("native gateway cleanup did not complete")
			}
			stop()
			d.mu.Lock()
			cleanupUncertain := d.cleanupUncertain
			d.mu.Unlock()
			if cleanupUncertain {
				t.Fatal("ordinary disposable gateway shutdown could not be verified")
			}
			if _, err := docker("exec", name, "sh", "-c", "test ! -e /proc/"+pid); err != nil {
				t.Fatal("old in-container gateway survived cleanup")
			}
			if _, err := docker("exec", name, "sh", "-c", "test ! -e /proc/"+childPID); err != nil {
				t.Fatal("detached native tool survived gateway shutdown")
			}
			stop2, done2, pid2 := start()
			if pid2 == pid {
				stop2()
				t.Fatal("replacement reused old live native process")
			}
			stop2()
			select {
			case <-done2:
			case <-time.After(20 * time.Second):
				t.Fatal("replacement cleanup did not complete")
			}
			if _, err := docker("exec", name, "sh", "-c", "test ! -e /proc/"+pid2); err != nil {
				t.Fatal("replacement in-container gateway survived cleanup")
			}
		})
	}
}
