//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

type dependencies struct {
	root        string
	nice        func() (int, error)
	load        func() ([]float64, error)
	affinity    func() ([]int, error)
	getenv      func(string) string
	revision    func(context.Context) (string, error)
	execute     func(context.Context, step, string, []string, []int) (int, error)
	startLoad   func(context.Context, []int) (func(), error)
	daemonAlive func(int) bool
}

func realDependencies(root string) dependencies {
	return dependencies{root: root, nice: func() (int, error) { n, err := syscall.Getpriority(syscall.PRIO_PROCESS, 0); return 20 - n, err }, load: hostLoad, affinity: allowedCPUs, getenv: os.Getenv, revision: func(ctx context.Context) (string, error) {
		b, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD").Output() //nolint:gosec // G204: fixed Git operation in the explicitly selected checkout.
		return strings.TrimSpace(string(b)), err
	}, execute: execute, startLoad: startLoad, daemonAlive: func(pid int) bool { _, err := os.Stat(fmt.Sprintf("/proc/%d/stat", pid)); return pid > 0 && err == nil }}
}
func hostLoad() ([]float64, error) {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(b))
	if len(fields) < 3 {
		return nil, errors.New("invalid_host_load")
	}
	v := []float64{}
	for _, s := range fields[:3] {
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil, err
		}
		v = append(v, n)
	}
	return v, nil
}
func allowedCPUs() ([]int, error) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if list, ok := strings.CutPrefix(line, "Cpus_allowed_list:"); ok {
			return parseCPUList(strings.TrimSpace(list))
		}
	}
	return nil, errors.New("missing_cpu_affinity")
}
func parseCPUList(value string) ([]int, error) {
	cpus := []int{}
	seen := map[int]bool{}
	for _, part := range strings.Split(value, ",") {
		bounds := strings.Split(part, "-")
		if len(bounds) > 2 {
			return nil, errors.New("invalid_cpus")
		}
		lo, err := strconv.Atoi(bounds[0])
		if err != nil || lo < 0 {
			return nil, errors.New("invalid_cpus")
		}
		hi := lo
		if len(bounds) == 2 {
			hi, err = strconv.Atoi(bounds[1])
			if err != nil || hi < lo || hi-lo > 65536 {
				return nil, errors.New("invalid_cpus")
			}
		}
		for n := lo; n <= hi; n++ {
			if !seen[n] {
				cpus = append(cpus, n)
				seen[n] = true
			}
		}
	}
	return cpus, nil
}
func cpuArgument(cpus []int) string {
	parts := []string{}
	for _, n := range cpus {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, ",")
}
func commandFor(ctx context.Context, args []string, cpus []int) *exec.Cmd {
	if len(cpus) > 0 {
		args = append([]string{"taskset", "-c", cpuArgument(cpus)}, args...)
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // G204: commands come from the fixed benchmark plan or this executable; no shell.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	// Force-kill the whole owned group if graceful cleanup exceeds its deadline.
	cmd.WaitDelay = 30 * time.Second
	return cmd
}
func execute(parent context.Context, s step, log string, env []string, cpus []int) (int, error) {
	ctx, cancel := context.WithTimeout(parent, s.Timeout)
	defer cancel()
	f, err := os.OpenFile(log, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // G304: exclusive file in this run's private evidence directory.
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	cmd := commandFor(ctx, s.Command, cpus)
	cmd.Dir = s.Cwd
	cmd.Env = env
	cmd.Stdout = f
	cmd.Stderr = f
	if err = cmd.Start(); err != nil {
		return 0, err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-ctx.Done():
		// CommandContext sends TERM first; release-perf owns service/database cleanup.
		timer := time.NewTimer(30 * time.Second)
		select {
		case err = <-done:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			err = <-done
		}
	}
	if ctx.Err() != nil {
		return cmd.ProcessState.ExitCode(), errors.New("benchmark_interrupted_or_timed_out")
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 0, err
}
func startLoad(ctx context.Context, cpus []int) (func(), error) {
	path, err := os.Executable()
	if err != nil {
		return nil, err
	}
	workers := []*exec.Cmd{}
	cleanup := func() {
		for _, cmd := range workers {
			_ = cmd.Process.Signal(syscall.SIGTERM)
		}
		for _, cmd := range workers {
			_ = cmd.Wait()
		}
	}
	for range cpus {
		cmd := commandFor(ctx, []string{"nice", "-n", "5", path, "--cpu-load-worker"}, cpus)
		pipe, e := cmd.StdoutPipe()
		if e != nil {
			cleanup()
			return nil, e
		}
		cmd.Stderr = io.Discard
		if err = cmd.Start(); err != nil {
			cleanup()
			return nil, err
		}
		workers = append(workers, cmd)
		ready := make(chan error, 1)
		go func() {
			b := make([]byte, 6)
			_, e := io.ReadFull(pipe, b)
			if e == nil && string(b) != "ready\n" {
				e = errors.New("load_worker_not_ready")
			}
			ready <- e
		}()
		timer := time.NewTimer(10 * time.Second)
		select {
		case e = <-ready:
		case <-ctx.Done():
			e = ctx.Err()
		case <-timer.C:
			e = errors.New("load_worker_start_timeout")
		}
		timer.Stop()
		_ = pipe.Close()
		if e != nil {
			cleanup()
			return nil, errors.New("load_worker_start_failed")
		}
	}
	return cleanup, nil
}
func worker() {
	// Atomic increments keep the synthetic CPU loop observable to the compiler.
	// One goroutine produces one busy thread; nice is applied before Go starts.
	fmt.Println("ready")
	var counter atomic.Uint64
	for {
		counter.Add(1)
	}
}
func environment(goBinary string, getenv func(string) string) []string {
	env := os.Environ()
	overrides := map[string]string{"GOWORK": "off", "DATABASE_URL": getenv("TEST_DATABASE_URL"), "PATH": filepath.Dir(goBinary) + string(os.PathListSeparator) + getenv("PATH")}
	for _, key := range []string{"GOWORK", "DATABASE_URL", "PATH"} {
		prefix := key + "="
		filtered := env[:0]
		for _, entry := range env {
			if !strings.HasPrefix(entry, prefix) {
				filtered = append(filtered, entry)
			}
		}
		filtered = append(filtered, prefix+overrides[key])
		env = filtered
	}
	return env
}
