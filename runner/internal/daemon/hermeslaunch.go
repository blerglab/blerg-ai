package daemon

import (
	"context"
	"errors"
	"fmt"
	agent "github.com/blerglab/blerg-ai/runner/internal/agent"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

// Match the native TUI's source/venv layout, but exclude the workspace and
// PYTHONPATH from Python imports. No profile or provider settings are edited.
// HERMES_YOLO_MODE is set here, before Hermes imports its approval module
// (which reads it once), because every Blerg agent session runs without
// permission prompts, like Claude and Codex, and a sandboxed gateway started
// through docker exec does not inherit the daemon's environment.
const hermesGatewayBootstrap = `import os
os.environ['HERMES_YOLO_MODE']='1'
import sys,json,inspect
sys.path.insert(0,sys.argv[1])
try:
 import tui_gateway.server as server
 from tui_gateway import entry
 if server.DESKTOP_BACKEND_CONTRACT not in (6,8):raise RuntimeError()
 original=server._startup_system_prompt
 if tuple(inspect.signature(original).parameters)!=('cfg','task_id'):raise RuntimeError()
 if tuple(inspect.signature(server._maybe_schedule_auto_continue).parameters)!=('sid','session','session_key'):raise RuntimeError()
 guide=sys.argv[2]
 if len(guide.encode('utf-8'))>65536:raise RuntimeError()
 def startup(cfg,task_id):
  native=original(cfg,task_id)
  return '\n\n'.join(part for part in (native,guide) if part)
 server._startup_system_prompt=startup
 server._maybe_schedule_auto_continue=lambda sid,session,session_key:None
except Exception:
 print(json.dumps({'jsonrpc':'2.0','method':'event','params':{'type':'blerg.bridge_error','payload':{'code':'unsupported_native_api'}}}),flush=True)
 raise SystemExit(1)
entry.main()
`

const hermesGatewayPIDFile = "/tmp/blerg-hermes-gateway.pid"

func (d *hermesDriver) gatewayCommand(ctx context.Context) (*exec.Cmd, error) {
	if len(d.DeveloperInstructions) > 65536 {
		return nil, errors.New("hermes session guidance exceeds its safety limit")
	}
	env := d.env
	value := func(key string) string {
		for _, v := range env {
			if x, ok := strings.CutPrefix(v, key+"="); ok {
				return x
			}
		}
		return ""
	}
	home := value("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	source := value("HERMES_PYTHON_SRC_ROOT")
	profile := value("HERMES_HOME")
	if profile == "" {
		profile = filepath.Join(home, ".hermes")
	}
	if source == "" {
		source = filepath.Join(profile, "hermes-agent")
	}
	python := value("HERMES_PYTHON")
	if d.prefix.enabled() {
		// The installed image owns its Python/source; host-only overrides cannot
		// point a sandbox at paths that exist only on the workstation.
		source = "/home/agent/.hermes/hermes-agent"
		python = source + "/venv/bin/python"
		cmd := d.prefix.commandPID(ctx, d.workDir, python, hermesGatewayPIDFile, "-I", "-u", "-c", "import os\nif os.getpgrp()!=os.getpid(): os.setsid()\n"+hermesGatewayBootstrap, source, d.DeveloperInstructions)
		cmd.Env = env
		d.configureGatewayCancellation(ctx, cmd)
		return cmd, nil
	}
	if !filepath.IsAbs(source) {
		return nil, errors.New("hermes source directory must be absolute")
	}
	source, err := filepath.EvalSymlinks(source)
	if err != nil {
		return nil, errors.New("hermes native gateway installation is unavailable")
	}
	for _, p := range []string{"hermes_bootstrap.py", "tui_gateway/entry.py"} {
		if st, e := os.Stat(filepath.Join(source, p)); e != nil || !st.Mode().IsRegular() {
			return nil, errors.New("hermes native gateway installation is unavailable")
		}
	}
	if python == "" {
		for _, p := range []string{".venv/bin/python", ".venv/bin/python3", "venv/bin/python", "venv/bin/python3"} {
			candidate := filepath.Join(source, p)
			if st, e := os.Stat(candidate); e == nil && st.Mode().IsRegular() {
				python = candidate
				break
			}
		}
	}
	if python == "" {
		return nil, errors.New("hermes Python environment is unavailable; install the native gateway dependencies")
	}
	cmd := d.prefix.commandPID(ctx, d.workDir, python, hermesGatewayPIDFile, "-I", "-u", "-c", hermesGatewayBootstrap, source, d.DeveloperInstructions)
	cmd.Env = env
	d.configureGatewayCancellation(ctx, cmd)
	return cmd, nil
}

// Killing docker exec alone leaves its in-container command running. Complete
// the bounded inner kill before returning cancellation, so no replacement can
// start while the old gateway still owns the native conversation.
func (d *hermesDriver) configureGatewayCancellation(parent context.Context, cmd *exec.Cmd) {
	cmd.WaitDelay = 3 * time.Second
	if d.prefix.enabled() {
		cmd.Cancel = func() error {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 3*time.Second)
			defer cancel()
			//nolint:gosec // daemon-built Docker executable/container and fixed cleanup script; no user input.
			cleanup := exec.CommandContext(ctx, d.prefix[0], "exec", d.prefix.container(), "sh", "-c", "if test -f "+hermesGatewayPIDFile+"; then p=$(cat "+hermesGatewayPIDFile+"); kill -TERM \"$p\" 2>/dev/null; n=0; while kill -0 \"$p\" 2>/dev/null; do n=$((n+1)); if test \"$n\" -ge 60; then kill -KILL -\"$p\" 2>/dev/null; exit 1; fi; sleep 0.025; done; fi")
			cleanup.Stdout = nil
			cleanup.Stderr = nil
			if err := cleanup.Run(); err != nil {
				d.mu.Lock()
				d.cleanupUncertain = true
				d.mu.Unlock()
				d.emit("error", agent.ErrorPayload{Message: "Hermes sandbox shutdown could not be verified; automatic restart is disabled", Retryable: false})
			}
			return cmd.Process.Kill()
		}
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGTERM)
		deadline := time.Now().Add(1500 * time.Millisecond)
		for time.Now().Before(deadline) {
			if err := cmd.Process.Signal(syscall.Signal(0)); errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
				return nil
			}
			if runtime.GOOS == "linux" {
				state, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", cmd.Process.Pid))
				if err == nil && strings.Contains(string(state), ") Z ") {
					return nil
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
		d.mu.Lock()
		d.cleanupUncertain = true
		d.mu.Unlock()
		d.emit("error", agent.ErrorPayload{Message: "Hermes shutdown did not finish gracefully; automatic restart is disabled", Retryable: false})
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
