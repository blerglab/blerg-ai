// gate-timing measures two committed revisions in retained private worktrees.
// Setup is excluded from wall time. Normal tool caches remain shared; no tests
// are selected out, retried, or reclassified as passing after a failure.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type receipts map[string][]string
type comparison struct {
	Preserved          bool      `json:"preserved"`
	BeforeEvents       int       `json:"before_events"`
	AfterEvents        int       `json:"after_events"`
	Missing            []string  `json:"missing"`
	ChangedRepetitions []string  `json:"changed_repetitions"`
	NewSkips           []string  `json:"new_skips"`
	Added              []string  `json:"added"`
	MissingSuites      *[]string `json:"missing_suites,omitempty"`
	CommandsPassed     bool      `json:"commands_passed"`
}

func eventCount(r receipts) int {
	n := 0
	for _, outcomes := range r {
		n += len(outcomes)
	}
	return n
}
func skipCount(values []string) int {
	n := 0
	for _, v := range values {
		if v == "skip" {
			n++
		}
	}
	return n
}
func compareReceipts(before, after receipts) comparison {
	c := comparison{BeforeEvents: eventCount(before), AfterEvents: eventCount(after), Missing: []string{}, ChangedRepetitions: []string{}, NewSkips: []string{}, Added: []string{}}
	for key, values := range before {
		other, ok := after[key]
		if !ok {
			c.Missing = append(c.Missing, key)
			continue
		}
		if len(values) != len(other) {
			c.ChangedRepetitions = append(c.ChangedRepetitions, key)
		}
		if skipCount(values) < skipCount(other) {
			c.NewSkips = append(c.NewSkips, key)
		}
	}
	for key := range after {
		if _, ok := before[key]; !ok {
			c.Added = append(c.Added, key)
		}
	}
	for _, items := range [][]string{c.Missing, c.ChangedRepetitions, c.NewSkips, c.Added} {
		sort.Strings(items)
	}
	c.Preserved = len(before) > 0 && len(c.Missing)+len(c.ChangedRepetitions)+len(c.NewSkips) == 0
	return c
}
func goReceipts(output, suite string) receipts {
	result := receipts{}
	for _, line := range strings.Split(output, "\n") {
		var event struct{ Action, Package, Test string }
		if json.Unmarshal([]byte(line), &event) != nil || event.Test == "" {
			continue
		}
		if event.Action == "pass" || event.Action == "fail" || event.Action == "skip" {
			key := strings.Join([]string{suite, event.Package, event.Test}, "|")
			result[key] = append(result[key], event.Action)
		}
	}
	return result
}

type gateSummary struct {
	Complete   bool             `json:"complete"`
	FullMatrix bool             `json:"full_matrix"`
	Suites     []map[string]any `json:"suites"`
}

func gateReceipts(logs, checkout string) (*gateSummary, receipts, error) {
	data, err := os.ReadFile(filepath.Join(logs, "summary.json")) //nolint:gosec // G304: logs is the explicitly selected local evidence directory.
	if err != nil {
		return nil, nil, err
	}
	var summary gateSummary
	if err = json.Unmarshal(data, &summary); err != nil {
		return nil, nil, err
	}
	result := receipts{}
	for _, suite := range summary.Suites {
		name, _ := suite["suite"].(string)
		log, _ := suite["log"].(string)
		text, err := os.ReadFile(filepath.Join(logs, log)) //nolint:gosec // G304: reads the suite log recorded by the benchmarked local gate.
		if err != nil {
			return nil, nil, err
		}
		for key, value := range goReceipts(string(text), name) {
			result[key] = value
		}
		data, err := os.ReadFile(filepath.Join(logs, name+".vitest.json")) //nolint:gosec // G304: reads the local gate's named Vitest receipt artifact.
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		var report struct {
			TestResults []struct {
				Name             string
				AssertionResults []struct {
					FullName      *string
					Title, Status string
				}
			}
		}
		if err = json.Unmarshal(data, &report); err != nil {
			return nil, nil, err
		}
		for _, file := range report.TestResults {
			path, err := filepath.Rel(checkout, file.Name)
			if err != nil {
				return nil, nil, err
			}
			for _, test := range file.AssertionResults {
				title := test.Title
				if test.FullName != nil {
					title = *test.FullName
				}
				key := strings.Join([]string{name, filepath.ToSlash(path), title}, "|")
				status := test.Status
				switch status {
				case "passed":
					status = "pass"
				case "failed":
					status = "fail"
				case "pending", "skipped", "todo":
					status = "skip"
				}
				result[key] = append(result[key], status)
			}
		}
	}
	return &summary, result, nil
}

var errHelp = errors.New("help requested")

type options struct{ Base, Head, Output, Mode string }

func parseOptions(args []string) (options, error) {
	o := options{Mode: "both"}
	positional := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "-h" || arg == "--help" {
			return o, errHelp
		}
		key, value, equals := strings.Cut(arg, "=")
		if key == "--output" || key == "--mode" {
			if !equals {
				i++
				if i >= len(args) {
					return o, fmt.Errorf("%s requires a value", key)
				}
				value = args[i] //nolint:gosec // G602: the preceding i >= len(args) guard returns before this indexed access.
			}
			if key == "--output" {
				o.Output = value
			} else {
				o.Mode = value
			}
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return o, fmt.Errorf("unknown option %s", arg)
		}
		positional = append(positional, arg)
	}
	if len(positional) != 2 || o.Output == "" {
		return o, fmt.Errorf("usage: gate-timing BASE HEAD --output NEW_DIRECTORY [--mode both|gate|make]")
	}
	if o.Mode != "both" && o.Mode != "gate" && o.Mode != "make" {
		return o, fmt.Errorf("mode must be both, gate, or make")
	}
	o.Base, o.Head = positional[0], positional[1]
	return o, nil
}
func environment(values map[string]string) []string {
	env := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, ok := values[key]; !ok {
			env = append(env, entry)
		}
	}
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	return env
}
func capture(dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(context.Background(), args[0], args[1:]...) //nolint:gosec // G204: benchmark commands and the configured Go tool are explicitly authorized local executables.
	cmd.Dir = dir
	cmd.Stderr = os.Stderr
	data, err := cmd.Output()
	return strings.TrimSpace(string(data)), err
}
func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0600) //nolint:gosec // G703: the caller explicitly selects the private benchmark output directory.
}
func loadAverage() []float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return nil
	}
	values := []float64{}
	for _, field := range fields[:3] {
		n, err := strconv.ParseFloat(field, 64)
		if err != nil {
			return nil
		}
		values = append(values, n)
	}
	return values
}
func command(dir string, env []string, output io.Writer, args ...string) (int, error) {
	cmd := exec.CommandContext(context.Background(), args[0], args[1:]...) //nolint:gosec // G204: benchmark commands and the configured Go tool are explicitly authorized local executables.
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout = output
	cmd.Stderr = output
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		return exited.ExitCode(), nil
	}
	return 0, err
}

// The retained Go executable acts as the legacy make reporter proxy. Exec keeps
// signal/process-group behavior and all flags except adding JSON reporting.
func proxyArgs(args []string) []string {
	result := append([]string{}, args...)
	if len(args) > 0 && args[0] == "test" {
		found := false
		for _, a := range args {
			found = found || a == "-json"
		}
		if !found {
			result = append([]string{"test", "-json"}, args[1:]...)
		}
	}
	return result
}
func runProxy() error {
	goTool := os.Getenv("BLERG_TIMING_REAL_GO")
	if goTool == "" {
		return fmt.Errorf("legacy Go proxy has no configured Go tool")
	}
	return syscall.Exec(goTool, append([]string{goTool}, proxyArgs(os.Args[1:])...), os.Environ()) //nolint:gosec // G204: the proxy replaces itself with the configured Go tool, preserving its arguments and signal handling.
}
func removeSuitePrefix(r receipts) receipts {
	result := receipts{}
	for key, value := range r {
		_, rest, _ := strings.Cut(key, "|")
		result[rest] = value
	}
	return result
}
func missingSuites(before, after []map[string]any) []string {
	present := map[string]bool{}
	for _, suite := range after {
		name, _ := suite["suite"].(string)
		present[name] = true
	}
	missing := []string{}
	for _, suite := range before {
		name, _ := suite["suite"].(string)
		if !present[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
}
func runTiming(o options, stdout io.Writer) (int, error) {
	for _, key := range []string{"DATABASE_URL", "TEST_DATABASE_URL"} {
		if os.Getenv(key) == "" {
			return 2, fmt.Errorf("%s must be configured privately", key)
		}
	}
	workers := 2
	if value := os.Getenv("BLERG_TOOL_WORKERS"); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			return 2, fmt.Errorf("BLERG_TOOL_WORKERS must be positive")
		}
		workers = n
	}
	root, err := capture("", "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return 1, fmt.Errorf("find repository: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return 1, err
	}
	goTool := os.Getenv("GO")
	if goTool == "" {
		goTool = filepath.Join(home, "go/bin/go")
	}
	if found, err := exec.LookPath(goTool); err == nil {
		goTool = found
	}
	goTool, err = filepath.Abs(goTool)
	if err != nil {
		return 1, err
	}
	revisions := map[string]string{}
	for _, entry := range []struct{ label, ref string }{{"before", o.Base}, {"after", o.Head}} {
		sha, err := capture(root, "git", "rev-parse", "--verify", entry.ref+"^{commit}")
		if err != nil {
			return 1, fmt.Errorf("resolve %s revision: %w", entry.label, err)
		}
		revisions[entry.label] = sha
	}
	output, err := filepath.Abs(o.Output)
	if err != nil {
		return 1, err
	}
	if err = os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		return 1, err
	}
	if err = os.Mkdir(output, 0700); err != nil {
		return 1, err
	}
	cache := os.Getenv("XDG_CACHE_HOME")
	if cache == "" {
		cache = filepath.Join(home, ".cache")
	}
	lockPath := filepath.Join(cache, "blerg/gate-timing.lock")
	if err = os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil { //nolint:gosec // G703: XDG_CACHE_HOME is the caller's explicit cache location.
		return 1, err
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY, 0600) //nolint:gosec // G304: opens only the benchmark lock in the configured user cache.
	if err != nil {
		return 1, err
	}
	defer func() { _ = lock.Close() }()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return 1, err
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	runs := []map[string]any{}
	comparisons := map[string]comparison{}
	report := map[string]any{"revisions": revisions, "requested_workers": workers, "runs": runs, "comparisons": comparisons, "cache_policy": "Normal shared Go/lint/Vitest caches retained; before runs first; not a cold-cache comparison."}
	save := func() error {
		report["runs"] = runs
		if err := writeJSON(filepath.Join(output, "results.tmp"), report); err != nil {
			return err
		}
		return os.Rename(filepath.Join(output, "results.tmp"), filepath.Join(output, "results.json"))
	}
	if err = save(); err != nil {
		return 1, err
	}
	tools := filepath.Join(output, "tools")
	if err = os.Mkdir(tools, 0700); err != nil {
		return 1, err
	}
	self, err := os.Executable()
	if err != nil {
		return 1, err
	}
	binary, err := os.ReadFile(self) //nolint:gosec // G304: self comes from os.Executable, not an evidence-file payload.
	if err != nil {
		return 1, err
	}
	if err = os.WriteFile(filepath.Join(tools, "go"), binary, 0700); err != nil { //nolint:gosec // G306: the owner-only legacy reporter proxy must be executable.
		return 1, err
	}
	values := map[string]string{"GO": goTool, "GOWORK": "off", "BLERG_TOOL_WORKERS": strconv.Itoa(workers), "GOMAXPROCS": strconv.Itoa(min(workers, 6)), "GOFLAGS": fmt.Sprintf("-p=%d", min(workers, 6)), "PATH": tools + string(os.PathListSeparator) + filepath.Dir(goTool) + string(os.PathListSeparator) + os.Getenv("PATH"), "BLERG_TIMING_REAL_GO": goTool}
	if os.Getenv("GOLANGCI_LINT") == "" {
		values["GOLANGCI_LINT"] = filepath.Join(home, "go/bin/golangci-lint")
	}
	inventories := map[string]receipts{}
	summaries := map[string]*gateSummary{}
	failed := false
	for _, label := range []string{"before", "after"} {
		checkout := filepath.Join(output, label)
		code, err := command(root, environment(values), stdout, "git", "worktree", "add", "--detach", checkout, revisions[label])
		if err != nil {
			return 1, fmt.Errorf("create %s worktree: %w", label, err)
		}
		if code != 0 {
			return 1, fmt.Errorf("create %s worktree: exit %d", label, code)
		}
		setup, err := os.Create(filepath.Join(output, label+"-setup.log")) //nolint:gosec // G304: output is the caller-selected, newly created private evidence directory.
		if err != nil {
			return 1, err
		}
		for _, dir := range []string{checkout, filepath.Join(checkout, "core/web")} {
			code, err = command(dir, environment(values), setup, "npm", "ci")
			if err != nil {
				_ = setup.Close()
				return 1, fmt.Errorf("install %s dependencies: %w", label, err)
			}
			if code != 0 {
				_ = setup.Close()
				return 1, fmt.Errorf("install %s dependencies: exit %d", label, code)
			}
		}
		if err = setup.Close(); err != nil {
			return 1, err
		}
		for _, mode := range []string{"gate", "make"} {
			if o.Mode != "both" && o.Mode != mode {
				continue
			}
			logs := filepath.Join(output, label+"-gate")
			consolePath := filepath.Join(output, label+"-"+mode+".log")
			argv := []string{"make", "test"}
			if mode == "gate" {
				argv = []string{"python3", "scripts/pre_merge.py", "--logs", logs}
			}
			values["BLERG_GATE_TELEMETRY"] = filepath.Join(output, label+"-history.jsonl")
			_, _ = fmt.Fprintf(stdout, "%s: running %s\n", label, mode)
			console, err := os.Create(consolePath) //nolint:gosec // G304: consolePath is a fixed run name beneath the private evidence directory.
			if err != nil {
				return 1, err
			}
			started := time.Now()
			loadStart := loadAverage()
			code, runErr := command(checkout, environment(values), console, argv...)
			seconds := math.Round(time.Since(started).Seconds()*1000) / 1000
			closeErr := console.Close()
			if runErr != nil {
				return 1, fmt.Errorf("run %s %s: %w", label, mode, runErr)
			}
			if closeErr != nil {
				return 1, closeErr
			}
			row := map[string]any{"revision": label, "mode": mode, "exit_code": code, "seconds": seconds, "load_start": loadStart, "load_end": loadAverage()}
			var summary *gateSummary
			r := receipts{}
			if mode == "gate" {
				if _, err = os.Stat(filepath.Join(logs, "summary.json")); err == nil {
					summary, r, err = gateReceipts(logs, checkout)
				} else if errors.Is(err, os.ErrNotExist) {
					err = nil
				}
			} else {
				var data []byte
				data, err = os.ReadFile(consolePath) //nolint:gosec // G304: reads the console log this benchmark command just wrote.
				if err == nil {
					found := ""
					for _, line := range strings.Split(string(data), "\n") {
						if strings.HasPrefix(line, "Logs: ") {
							found = strings.TrimPrefix(line, "Logs: ")
							break
						}
					}
					if found != "" {
						summary, r, err = gateReceipts(found, checkout)
					} else {
						r = goReceipts(string(data), "make")
					}
					r = removeSuitePrefix(r)
				}
			}
			if err != nil {
				return 1, fmt.Errorf("read %s %s receipts: %w", label, mode, err)
			}
			row["named_events"] = eventCount(r)
			if summary != nil {
				row["complete"] = summary.Complete
				row["full_matrix"] = summary.FullMatrix
				suites := []map[string]any{}
				for _, suite := range summary.Suites {
					entry := map[string]any{}
					for _, key := range []string{"suite", "seconds", "outcome", "tests"} {
						entry[key] = suite[key]
					}
					suites = append(suites, entry)
				}
				row["suites"] = suites
			}
			runs = append(runs, row)
			inventories[label+"|"+mode] = r
			summaries[label+"|"+mode] = summary
			if err = writeJSON(filepath.Join(output, label+"-"+mode+"-receipts.json"), r); err != nil {
				return 1, err
			}
			before, ok := inventories["before|"+mode]
			if ok && label == "after" {
				c := compareReceipts(before, r)
				if mode == "gate" {
					old := summaries["before|"+mode]
					beforeSuites, afterSuites := []map[string]any{}, []map[string]any{}
					if old != nil {
						beforeSuites = old.Suites
					}
					if summary != nil {
						afterSuites = summary.Suites
					}
					missing := missingSuites(beforeSuites, afterSuites)
					c.MissingSuites = &missing
					c.Preserved = c.Preserved && len(beforeSuites) > 0 && len(missing) == 0
				}
				c.CommandsPassed = true
				for _, run := range runs {
					if run["mode"] == mode && run["exit_code"] != 0 {
						c.CommandsPassed = false
					}
				}
				comparisons[mode] = c
			}
			if err = save(); err != nil {
				return 1, err
			}
			failed = failed || code != 0
			_, _ = fmt.Fprintf(stdout, "%s %s: exit %d, %g s, %d named events\n", label, mode, code, seconds, eventCount(r))
		}
	}
	for _, c := range comparisons {
		failed = failed || !c.Preserved
	}
	if failed {
		return 1, nil
	}
	return 0, nil
}
func main() {
	if filepath.Base(os.Args[0]) == "go" {
		if err := runProxy(); err != nil {
			fmt.Fprintf(os.Stderr, "Gate timing Go proxy failed: %v\n", err)
			os.Exit(1)
		}
		return
	}
	options, err := parseOptions(os.Args[1:])
	if errors.Is(err, errHelp) {
		fmt.Println("usage: gate-timing BASE HEAD --output NEW_DIRECTORY [--mode both|gate|make]")
		return
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Gate timing failed: %v\n", err)
		os.Exit(2)
	}
	code, err := runTiming(options, os.Stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Gate timing failed: %v\n", err)
	}
	os.Exit(code)
}
