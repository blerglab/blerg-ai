package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// WorkerPriority belongs to a desktop, never a cluster pod. Only child launch
// argv changes; the daemon's threads and request handlers retain their priority.
type WorkerPriority struct {
	mu        sync.Mutex
	enabled   atomic.Bool
	stateFile string
	prefix    []string
}

var priorityBackendOnce sync.Once
var priorityBackend []string

func NewWorkerPriority(stateFile string) *WorkerPriority {
	priorityBackendOnce.Do(func() {
		priorityBackend = detectWorkerPriority(runtime.GOOS, exec.LookPath, func(args []string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			return exec.CommandContext(ctx, args[0], args[1:]...).Run() //nolint:gosec // fixed scheduler executable and arguments, no session data
		})
	})
	p := &WorkerPriority{stateFile: stateFile, prefix: priorityBackend}
	p.enabled.Store(true)
	if stateFile != "" {
		data, err := os.ReadFile(stateFile) //nolint:gosec // daemon-owned settings path
		if err == nil {
			var s daemonSettings
			if err = json.Unmarshal(data, &s); err == nil && s.YieldToPerson != nil {
				p.enabled.Store(*s.YieldToPerson)
			}
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			logDaemonFailure("worker priority settings read", err)
		}
	}
	return p
}

func (p *WorkerPriority) Enabled() bool { return p != nil && p.enabled.Load() }

func (p *WorkerPriority) Set(enabled bool) error {
	if p == nil || p.stateFile == "" {
		err := errors.New("this desktop cannot save worker priority settings")
		logDaemonFailure("worker priority settings save", err)
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := saveDaemonSettings(p.stateFile, func(s *daemonSettings) { s.YieldToPerson = &enabled }); err != nil {
		logDaemonFailure("worker priority settings save", err)
		return errors.New("could not save worker priority settings")
	}
	p.enabled.Store(enabled)
	return nil
}

// Probe only harmless executables. A failed launch is never retried with the
// payload: it might already have run. Unsupported scheduling degrades once,
// with a safe cause, before any session command is started.
func detectWorkerPriority(goos string, lookup func(string) (string, error), probe func([]string) error) []string {
	var prefix []string
	add := func(name string, args ...string) {
		path, err := lookup(name)
		if err != nil {
			logDaemonFailure("worker priority "+name, err)
			return
		}
		candidate := append([]string{path}, args...)
		if err = probe(append(append([]string{}, candidate...), "true")); err != nil {
			logDaemonFailure("worker priority "+name, err)
			return
		}
		prefix = append(prefix, candidate...)
	}
	switch goos {
	case "linux":
		add("systemd-run", "--user", "--scope", "--quiet", "--collect", "--description=Blerg agent work", "-p", "CPUWeight=10", "-p", "IOWeight=10")
		add("nice", "-n", "5")
		add("ionice", "-c", "3")
	case "darwin":
		add("taskpolicy", "-b")
		add("nice", "-n", "5")
	default:
		logDaemonFailure("worker priority unsupported", errors.New("unsupported operating system"))
	}
	return prefix
}

func wrapWorkerCommand(cmd *exec.Cmd, prefix []string) {
	if len(prefix) == 0 {
		return
	}
	original := append([]string{cmd.Path}, cmd.Args[1:]...)
	cmd.Path = prefix[0]
	cmd.Args = append(append([]string{}, prefix...), original...)
}

// Docker's server creates the container processes, so host client priority is
// insufficient. The image supplies nice/ionice; every exec starts them anew
// because Docker exec does not inherit the container init's scheduling state.
func wrapSandboxWorkerCommand(cmd *exec.Cmd) {
	if len(cmd.Args) < 7 || cmd.Args[1] != "exec" {
		return
	}
	inner := append([]string{"nice", "-n", "5", "ionice", "-c", "3"}, cmd.Args[6:]...)
	cmd.Args = append(append([]string{}, cmd.Args[:6]...), inner...)
}

func (m *Manager) handleWorkerPriority(raw []byte) {
	var req protocol.WorkerPriorityRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		logDaemonFailure("worker priority request", err)
		return
	}
	go func() {
		p := m.agents.cfg.WorkerPriority
		res := protocol.WorkerPriorityResult{Type: "worker_priority_result", RequestID: req.RequestID}
		if p != nil {
			err := error(nil)
			if req.Enabled != nil {
				err = p.Set(*req.Enabled)
			}
			res.OK = err == nil
			res.Enabled = p.Enabled()
			names := []string{}
			for _, name := range []string{"systemd-run", "nice", "ionice", "taskpolicy"} {
				for _, arg := range p.prefix {
					if strings.HasSuffix(arg, "/"+name) {
						names = append(names, name)
					}
				}
			}
			res.Backend = strings.Join(names, " + ")
			if res.Backend == "" {
				res.Backend = "unavailable"
			}
		}
		if err := m.client.Send(res); err != nil {
			logDaemonFailure("worker priority reply", err)
		}
	}()
}
