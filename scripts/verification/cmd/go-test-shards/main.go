// go-test-shards runs ordinary packages, then disjoint server test shards.
// It preserves test flags, all subtests/examples/fuzz seeds, and normal Go caching.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type plan struct {
	Command []string
	Workers int
}
type discoverFunc func([]string) (string, error)

func budgetFlags(flags []string, workers int) []string {
	result := []string{}
	for _, flag := range flags {
		if !strings.HasPrefix(flag, "-p=") && !strings.HasPrefix(flag, "-parallel=") {
			result = append(result, flag)
		}
	}
	return append(result, fmt.Sprintf("-p=%d", workers), fmt.Sprintf("-parallel=%d", min(4, workers)))
}
func plans(command []string, workers int, discover discoverFunc) ([]plan, error) {
	if len(command) < 3 || command[1] != "test" {
		return nil, fmt.Errorf("expected Go test command with trailing packages")
	}
	index := len(command)
	for index > 2 && !strings.HasPrefix(command[index-1], "-") {
		index--
	}
	flags, packages := command[2:index], command[index:]
	single := []plan{{command, workers}}
	if workers == 1 {
		return single, nil
	}
	for _, flag := range flags {
		for _, prefix := range []string{"-run", "-list", "-bench", "-fuzz"} {
			if strings.HasPrefix(flag, prefix) {
				return single, nil
			}
		}
	}
	listed, err := discover(append([]string{command[0], "list"}, packages...))
	if err != nil {
		return nil, fmt.Errorf("list packages: %w", err)
	}
	all := strings.Fields(listed)
	servers := []string{}
	for _, p := range all {
		if strings.HasSuffix(p, "/internal/server") {
			servers = append(servers, p)
		}
	}
	if len(servers) != 1 {
		return single, nil
	}
	server := servers[0]
	listing := []string{command[0], "test"}
	for _, f := range flags {
		if f != "-json" {
			listing = append(listing, f)
		}
	}
	output, err := discover(append(listing, "-list=^(Test|Example|Fuzz)", server))
	if err != nil {
		return nil, fmt.Errorf("list server tests: %w", err)
	}
	valid := regexp.MustCompile(`^(Test|Example|Fuzz)[\pL\pN_]*$`)
	unique := map[string]bool{}
	for _, line := range strings.Split(output, "\n") {
		if valid.MatchString(line) {
			unique[line] = true
		}
	}
	names := []string{}
	for n := range unique {
		names = append(names, n)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("server test discovery returned no test names")
	}
	count := min(workers, 4, len(names))
	allowance := max(1, workers/count)
	rest := []string{}
	for _, p := range all {
		if p != server {
			rest = append(rest, p)
		}
	}
	result := []plan{}
	if len(rest) > 0 {
		args := append([]string{command[0], "test"}, budgetFlags(flags, workers)...)
		result = append(result, plan{append(args, rest...), workers})
	}
	for shard := 0; shard < count; shard++ {
		selected := []string{}
		for i := shard; i < len(names); i += count {
			selected = append(selected, regexp.QuoteMeta(names[i]))
		}
		args := append([]string{command[0], "test"}, budgetFlags(flags, allowance)...)
		result = append(result, plan{append(args, "-run=^("+strings.Join(selected, "|")+")$", server), allowance})
	}
	return result, nil
}
func environment(values map[string]string) []string {
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := values[key]; !replaced {
			env = append(env, entry)
		}
	}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}

// Each process buffers partial lines independently; only complete JSON lines
// enter the shared stream. This retains Go JSON receipts under concurrent writes.
type lineWriter struct {
	pending []byte
	output  io.Writer
	mutex   *sync.Mutex
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.pending = append(w.pending, p...)
	for {
		i := bytes.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		w.mutex.Lock()
		_, err := w.output.Write(w.pending[:i+1])
		w.mutex.Unlock()
		if err != nil {
			return 0, err
		}
		w.pending = w.pending[i+1:]
	}
	return len(p), nil
}
func (w *lineWriter) flush() {
	if len(w.pending) > 0 {
		w.mutex.Lock()
		_, _ = w.output.Write(w.pending)
		w.mutex.Unlock()
		w.pending = nil
	}
}
func executePlans(ps []plan, workers int, run func(plan) int) int {
	if len(ps) == 1 {
		if run(ps[0]) != 0 {
			return 1
		}
		return 0
	}
	failed := false
	if ps[0].Workers == workers {
		failed = run(ps[0]) != 0
		ps = ps[1:]
	}
	var wg sync.WaitGroup
	var mutex sync.Mutex
	for _, p := range ps {
		wg.Go(func() {
			if run(p) != 0 {
				mutex.Lock()
				failed = true
				mutex.Unlock()
			}
		})
	}
	wg.Wait()
	if failed {
		return 1
	}
	return 0
}
func run(command []string, stdout, stderr io.Writer) int {
	workers := 2
	if value := os.Getenv("BLERG_TOOL_WORKERS"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			_, _ = fmt.Fprintln(stderr, "Go test shard discovery failed: worker budget must be positive")
			return 1
		}
		workers = n
	}
	workers = min(workers, 6)
	discover := func(argv []string) (string, error) {
		cmd := exec.CommandContext(context.Background(), argv[0], argv[1:]...) //nolint:gosec // G204: executes the explicitly supplied Go tool for package/test discovery.
		cmd.Env = environment(map[string]string{"GOWORK": "off"})
		cmd.Stderr = stderr
		out, err := cmd.Output()
		return string(out), err
	}
	ps, err := plans(command, workers, discover)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Go test shard discovery failed: %v\n", err)
		return 1
	}
	var outputMutex, errorMutex sync.Mutex
	execute := func(p plan) int {
		writer := &lineWriter{output: stdout, mutex: &outputMutex}
		cmd := exec.CommandContext(context.Background(), p.Command[0], p.Command[1:]...) //nolint:gosec // G204: executes the same trusted Go tool with the planned test flags.
		cmd.Env = environment(map[string]string{"GOMAXPROCS": strconv.Itoa(p.Workers), "GOFLAGS": fmt.Sprintf("-p=%d", p.Workers)})
		cmd.Stdout = writer
		cmd.Stderr = writer
		err := cmd.Run()
		writer.flush()
		if err == nil {
			return 0
		}
		errorMutex.Lock()
		defer errorMutex.Unlock()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			_, _ = fmt.Fprintf(stderr, "Go test shard failed: exit %d\n", exit.ExitCode())
		} else {
			_, _ = fmt.Fprintf(stderr, "Go test shard could not start: %T\n", err)
		}
		return 1
	}
	return executePlans(ps, workers, execute)
}
func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
