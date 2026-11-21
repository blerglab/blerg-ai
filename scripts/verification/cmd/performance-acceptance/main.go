//go:build linux

// performance-acceptance drives BLG-431 foreground benchmarks and retains JSON.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type step struct {
	Name    string        `json:"name"`
	Cwd     string        `json:"cwd"`
	Command []string      `json:"command"`
	Timeout time.Duration `json:"-"`
}

func benchmarkPlan(goBinary string, soak, reps, daemon int) []step {
	browser := []string{"npm", "run", "test:performance", "-w", "runner/frontend", "--"}
	for _, name := range []string{"LongSession", "SessionSwitch", "SessionTabSwitch", "BoardGraphInsights"} {
		browser = append(browser, "src/components/"+name+".performance.browser.test.tsx")
	}
	filter := "budgets typing in a long live workspace|budgets switching into a long live workspace|reuses finished Markdown|measures opening|defers tab strip|measures polling demand|keeps the 400-card board asleep"
	browser = append(browser, "--maxWorkers=1", "-t", filter, "--reporter=default", "--reporter=json")
	return []step{{"quiet", ".", slices.Clone(browser), 20 * time.Minute}, {"loaded", ".", slices.Clone(browser), 20 * time.Minute}, {"routes", "runner", []string{goBinary, "test", "./internal/server", "./internal/daemon", "-run", "^(" + strings.Join(routeNames, "|") + ")$", "-json", "-count=1", "-timeout", "10m"}, 20 * time.Minute}, {"production", "runner", []string{goBinary, "run", "./cmd/release-perf", "-version", "blg431", "-go", goBinary, "-n", strconv.Itoa(reps), "-acceptance-soak", strconv.Itoa(soak) + "s", "-acceptance-daemon-pid", strconv.Itoa(daemon)}, time.Duration(soak)*time.Second + time.Hour}}
}
func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".acceptance-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err = f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
func readJSON(path string) (map[string]any, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is an explicit private evidence file selected by this runner.
	if err != nil {
		return nil, err
	}
	var m map[string]any
	err = json.Unmarshal(data, &m)
	return m, err
}
func safeCause(err error) string {
	// Stable codes only: OS/subprocess errors can contain private environment data.
	code := err.Error()
	if regexp.MustCompile(`^[a-z0-9_]+$`).MatchString(code) {
		return code
	}
	return "measurement_error"
}
func run(ctx context.Context, args []string, d dependencies) int {
	fs := flag.NewFlagSet("performance-acceptance", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	output := fs.String("output", "", "new results JSON path (required)")
	planOnly := fs.Bool("plan", false, "write plan JSON without executing benchmarks")
	daemon := fs.Int("daemon-pid", 0, "existing daemon PID sampled read-only")
	soak := fs.Int("soak-seconds", 1800, "sustained soak seconds; acceptance requires at least 1800")
	reps := fs.Int("reps", 11, "production repetitions per scenario")
	typing := fs.Float64("typing-budget-ms", 70, "native key-to-paint median budget")
	boot := fs.Float64("boot-budget-ms", 3000, "informational maximum boot target (not a gate without a measured baseline)")
	bootBaseline := fs.String("boot-baseline", "", "completed historical boot JSON from this machine; gate on a slower measured median")
	bundle := fs.Int("bundle-budget-bytes", 1_100_000, "entry JavaScript gzip budget")
	cpusFlag := fs.String("cpus", "", "comma-separated allowed CPUs (default first four, bounded by BLERG_TOOL_WORKERS)")
	home, _ := os.UserHomeDir()
	defaultGo := filepath.Join(home, "go", "bin", "go")
	if _, err := os.Stat(defaultGo); err != nil {
		defaultGo = "go"
	}
	goBinary := fs.String("go", defaultGo, "Go executable")
	if slices.Contains(args, "--help") || slices.Contains(args, "-h") {
		fs.SetOutput(os.Stdout)
		fs.PrintDefaults()
		return 0
	}
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || *output == "" || *soak <= 0 || *reps <= 0 || *bundle <= 0 || !finite(*typing) || !finite(*boot) || *typing <= 0 || *boot <= 0 {
		fmt.Fprintln(os.Stderr, "Invalid arguments; use --help.")
		return 2
	}
	if path, err := exec.LookPath(*goBinary); err == nil {
		*goBinary = path
	}
	if path, err := filepath.Abs(*goBinary); err == nil {
		*goBinary = path
	}
	path, err := filepath.Abs(*output)
	if err != nil {
		return failure("invalid_output")
	}
	*output = path
	if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return failure("output_exists_or_unreadable")
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return failure("output_directory_unavailable")
	}
	allowed, err := d.affinity()
	if err != nil {
		return failure("cpu_affinity_unavailable")
	}
	slices.Sort(allowed)
	cpus := slices.Clone(allowed)
	if *cpusFlag != "" {
		cpus, err = parseCPUList(*cpusFlag)
		if err != nil {
			return failure("invalid_cpus")
		}
	} else {
		limit := 4
		if text := d.getenv("BLERG_TOOL_WORKERS"); text != "" {
			n, e := strconv.Atoi(text)
			if e != nil || n < 1 {
				return failure("invalid_worker_limit")
			}
			limit = min(limit, n)
		}
		cpus = cpus[:min(limit, len(cpus))]
	}
	if len(cpus) == 0 {
		return failure("invalid_cpus")
	}
	for _, n := range cpus {
		if !slices.Contains(allowed, n) {
			return failure("invalid_cpus")
		}
	}
	nice, err := d.nice()
	if err != nil {
		return failure("priority_unavailable")
	}
	load, err := d.load()
	if err != nil || len(load) != 3 {
		return failure("host_load_unavailable")
	}
	revision, err := d.revision(ctx)
	if err != nil {
		return failure("revision_unavailable")
	}
	report := map[string]any{"schema_version": 1, "status": "planned", "revision": revision, "started_utc": time.Now().UTC().Format(time.RFC3339Nano), "environment": map[string]any{"nice": nice, "load": load, "cpus": cpus}, "config": map[string]any{"soak_seconds": *soak, "repetitions": *reps, "typing_budget_ms": *typing, "boot_budget_ms": *boot, "entry_js_gzip_budget_bytes": *bundle}, "estimated_seconds": map[string]any{"min": *soak + 600, "max": *soak + 1800}, "steps": map[string]any{}}
	if *bootBaseline != "" {
		previous, e := readJSON(*bootBaseline)
		if e != nil || previous["status"] != "complete" || compareBoot(obj(previous, "boot"), nil, *boot)["valid"] != true {
			return failure("invalid_boot_baseline")
		}
		report["boot_baseline"] = map[string]any{"samplesMs": array(obj(previous, "boot"), "samplesMs")}
	}
	plan := benchmarkPlan(*goBinary, *soak, *reps, *daemon)
	if *planOnly {
		report["plan"] = plan
		if err = writeJSON(path, report); err != nil {
			return failure("json_write_failed")
		}
		return 0
	}
	evidence := filepath.Join(filepath.Dir(path), "evidence")
	if err = os.Mkdir(evidence, 0o700); err != nil {
		return failure("evidence_exists_or_unavailable")
	}
	report["status"] = "running"
	if err = writeJSON(path, report); err != nil {
		return failure("json_write_failed")
	}
	err = measure(ctx, d, report, plan, path, evidence, *goBinary, cpus, *daemon, nice, load)
	if err != nil {
		report["status"] = "incomplete"
		report["cause"] = safeCause(err)
		fmt.Fprintln(os.Stderr, "Acceptance incomplete:", safeCause(err))
	} else {
		report["boot_comparison"] = compareBoot(obj(obj(report, "production"), "boot"), obj(report, "boot_baseline"), *boot)
		failures := assess(report, *typing, *boot, float64(*bundle))
		report["failures"] = failures
		report["status"] = "passed"
		if len(failures) > 0 {
			report["status"] = "failed"
			fmt.Fprintln(os.Stderr, "Acceptance failed:", strings.Join(failures, ", "))
		}
	}
	report["finished_utc"] = time.Now().UTC().Format(time.RFC3339Nano)
	if err = writeJSON(path, report); err != nil {
		return failure("json_write_failed")
	}
	switch report["status"] {
	case "passed":
		return 0
	case "failed":
		return 1
	default:
		return 2
	}
}
func failure(code string) int { fmt.Fprintln(os.Stderr, "Acceptance refused:", code); return 2 }
func measure(ctx context.Context, d dependencies, r map[string]any, plan []step, output, evidence, goBinary string, cpus []int, daemon, nice int, load []float64) error {
	if nice != 0 {
		return errors.New("foreground_priority_required")
	}
	if load[0] >= 4 {
		return errors.New("quiet_host_required_load_below_4")
	}
	if d.getenv("TEST_DATABASE_URL") == "" {
		return errors.New("test_database_required")
	}
	if !d.daemonAlive(daemon) {
		return errors.New("explicit_live_daemon_pid_required")
	}
	env := environment(goBinary, d.getenv)
	for _, s := range plan {
		s.Cwd = filepath.Join(d.root, s.Cwd)
		s.Command = slices.Clone(s.Command)
		browser := s.Name == "quiet" || s.Name == "loaded"
		if browser {
			s.Command = append(s.Command, "--outputFile.json="+filepath.Join(evidence, s.Name+".vitest.json"))
		}
		if s.Name == "production" {
			s.Command = append(s.Command, "-logs", filepath.Join(evidence, "production"))
		}
		started := time.Now()
		affinity := []int(nil)
		if browser {
			affinity = cpus
		}
		code, err := func() (int, error) {
			if s.Name == "loaded" {
				stop, e := d.startLoad(ctx, cpus)
				if e != nil {
					return 0, e
				}
				defer stop()
			}
			return d.execute(ctx, s, filepath.Join(evidence, s.Name+".log"), env, affinity)
		}()
		entry := map[string]any{"exit_code": code, "elapsed_seconds": time.Since(started).Seconds(), "log": s.Name + ".log"}
		obj(r, "steps")[s.Name] = entry
		if e := writeJSON(output, r); e != nil {
			return e
		}
		if err != nil {
			return err
		}
		if browser {
			raw, e := os.ReadFile(filepath.Join(evidence, s.Name+".log")) //nolint:gosec // G304: evidence and fixed step names belong to this run.
			if e != nil {
				return e
			}
			metrics := map[string]any{}
			for key, values := range extractMetrics(string(raw)) {
				metrics[key] = values
			}
			entry["metrics"] = metrics
			result, e := readJSON(filepath.Join(evidence, s.Name+".vitest.json"))
			if e != nil {
				return e
			}
			tests := map[string]any{}
			for _, key := range []string{"numPassedTests", "numFailedTests", "numPendingTests"} {
				v, ok := result[key]
				if !ok {
					return errors.New("missing_vitest_counts")
				}
				tests[key] = v
			}
			entry["tests"] = tests
		}
		if s.Name == "routes" {
			raw, e := os.ReadFile(filepath.Join(evidence, "routes.log")) //nolint:gosec // G304: fixed file in this run's private evidence directory.
			if e != nil {
				return e
			}
			routes := map[string]any{}
			for _, line := range strings.Split(string(raw), "\n") {
				var event struct{ Test, Action string }
				if json.Unmarshal([]byte(line), &event) == nil && slices.Contains(routeNames, event.Test) && slices.Contains([]string{"pass", "fail", "skip"}, event.Action) {
					routes[event.Test] = event.Action
				}
			}
			r["routes"] = routes
		}
		if e := writeJSON(output, r); e != nil {
			return e
		}
	}
	production, err := readJSON(filepath.Join(evidence, "production", "acceptance.json"))
	if err != nil {
		return err
	}
	r["production"] = production
	scenarios, err := productionScenarios(filepath.Join(evidence, "production"))
	if err != nil {
		return err
	}
	r["production_scenarios"] = scenarios
	bundle, err := bundleSize(filepath.Join(evidence, "production", "dist", "runner-frontend"))
	if err != nil {
		return err
	}
	r["bundle"] = bundle
	return nil
}
func entry() int {
	if len(os.Args) == 2 && os.Args[1] == "--cpu-load-worker" {
		worker()
		return 0
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel")
	root, err := cmd.Output()
	if err != nil {
		return failure("repository_root_unavailable")
	}
	return run(ctx, os.Args[1:], realDependencies(strings.TrimSpace(string(root))))
}

func main() { os.Exit(entry()) }
