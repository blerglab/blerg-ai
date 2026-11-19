// runtime-smoke runs real runtime acceptance tests and refuses incomplete coverage.
// Run from the runner module. No model is contacted unless explicitly enabled.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	buildBudget   = 3 * time.Minute
	runtimeBudget = 5*time.Minute + 15*time.Second
	cleanupBudget = 30 * time.Second
)

var runtimeTests = map[string]string{
	"host":    "TestAcceptanceHostSmoke",
	"sandbox": "TestAcceptanceSandboxSmoke",
	"cluster": "TestAcceptanceClusterSmoke",
	"real":    "TestAcceptanceRealEngineSmoke",
}

type result struct {
	Runtime string  `json:"runtime"`
	Test    string  `json:"test"`
	Outcome string  `json:"outcome"`
	Seconds float64 `json:"seconds"`
}

func main() { os.Exit(run()) }

func run() int {
	runtimes := flag.String("runtimes", "host,sandbox,cluster", "comma-separated host,sandbox,cluster (real is an optional paid host smoke)")
	logs := flag.String("logs", "", "new private evidence directory (default: temporary directory)")
	goBinary := flag.String("go", "go", "Go executable")
	clusterContext := flag.String("cluster-context", "", "explicit kubectl context: run the existing isolated cluster controller")
	clusterNamespace := flag.String("cluster-namespace", "", "fresh acceptance- namespace for the cluster controller")
	clusterValues := flag.String("cluster-values", "", "private cluster controller values file; disposable images must match this checkout")
	flag.Parse()
	selected, err := selectRuntimes(*runtimes)
	if err != nil || flag.NArg() != 0 || !validClusterFlags(selected, *clusterContext, *clusterNamespace, *clusterValues) {
		fmt.Fprintln(os.Stderr, "invalid runtimes or unexpected arguments")
		return 1
	}
	executable, err := exec.LookPath(*goBinary)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Go executable unavailable")
		return 1
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return 1
	}
	*goBinary = executable
	goEnv := append(os.Environ(), "GOWORK=off", "PATH="+filepath.Dir(executable)+string(os.PathListSeparator)+os.Getenv("PATH"))
	if *logs == "" {
		*logs, err = os.MkdirTemp("", "blerg-runtime-smoke-")
	} else {
		err = os.Mkdir(*logs, 0700)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "cannot create new evidence directory")
		return 1
	}
	absolute, err := filepath.Abs(*logs)
	if err != nil {
		return 1
	}
	evidence, err := os.OpenRoot(absolute)
	if err != nil {
		return 1
	}
	defer func() { _ = evidence.Close() }()
	fmt.Println("Private evidence:", absolute)
	binary := filepath.Join(absolute, "daemon")
	buildLog, err := evidence.OpenFile("build.log", os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return 1
	}
	workers := 2
	if n, err := strconv.Atoi(os.Getenv("BLERG_TOOL_WORKERS")); err == nil && n > 0 {
		workers = min(n, 6)
	}
	buildCtx, buildCancel := context.WithTimeout(context.Background(), buildBudget)
	defer buildCancel()
	//nolint:gosec // The operator deliberately selects the Go executable; arguments bypass a shell.
	build := exec.CommandContext(buildCtx, *goBinary, "build", fmt.Sprintf("-p=%d", workers), "-o", binary, "./cmd/daemon")
	build.Env = slices.Concat(goEnv, []string{fmt.Sprintf("GOMAXPROCS=%d", workers)})
	build.Stdout, build.Stderr = buildLog, buildLog
	err = build.Run()
	_ = buildLog.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, "daemon build failed; see build.log")
		return 1
	}
	defer func() { _ = evidence.Remove("daemon") }()
	if slices.Contains(selected, "sandbox") {
		if err := sweepStaleContainers(time.Now()); err != nil {
			fmt.Fprintln(os.Stderr, "stale smoke container sweep failed")
			return 1
		}
	}
	rows := make([]result, 0, len(selected))
	code := 0
	for _, name := range selected {
		test := runtimeTests[name]
		start := time.Now()
		if name == "cluster" && *clusterContext != "" {
			outcome := runCluster(absolute, *clusterContext, *clusterNamespace, *clusterValues)
			rows = append(rows, result{name, test, outcome, time.Since(start).Seconds()})
			fmt.Printf("%s  %s  %.2fs  %s\n", name, outcome, time.Since(start).Seconds(), test)
			code = combineCode(code, outcomeCode(outcome))
			continue
		}
		path := name + ".jsonl"
		log, err := evidence.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return 1
		}
		// Fresh execution is required for live acceptance, never a cached receipt.
		ctx, cancel := context.WithTimeout(context.Background(), runtimeBudget)
		//nolint:gosec // The operator selects Go; the selector comes from the fixed runtime registry, without a shell.
		cmd := exec.CommandContext(ctx, *goBinary, "test", "-json", fmt.Sprintf("-p=%d", workers), fmt.Sprintf("-parallel=%d", min(workers, 4)), "-count=1", "-tags=blerg_workspace_integration", "-timeout=5m", "-run=^"+test+"$", "./internal/server")
		cmd.Env = slices.Concat(goEnv, []string{fmt.Sprintf("GOMAXPROCS=%d", workers), "BLERG_HOST_HELPER_TEST_BINARY=" + binary})
		cmd.Stdout, cmd.Stderr = log, log
		runErr := cmd.Run()
		cancel()
		_ = log.Close()
		input, err := evidence.Open(path)
		if err != nil {
			return 1
		}
		outcome := receipt(input, test)
		_ = input.Close()
		if runErr != nil {
			outcome = "FAIL"
		}
		code = combineCode(code, outcomeCode(outcome))
		row := result{name, test, outcome, time.Since(start).Seconds()}
		rows = append(rows, row)
		fmt.Printf("%s  %s  %.2fs  %s\n", name, outcome, row.Seconds, test)
	}
	raw, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return 1
	}
	if err := evidence.WriteFile("summary.json", append(raw, '\n'), 0600); err != nil {
		return 1
	}
	if code == 2 {
		fmt.Fprintln(os.Stderr, "INCOMPLETE: selected runtime skipped; see named test logs for prerequisites")
	}
	return code
}

func selectRuntimes(raw string) ([]string, error) {
	selected := strings.Split(raw, ",")
	seen := map[string]bool{}
	for _, name := range selected {
		if runtimeTests[name] == "" || seen[name] {
			return nil, fmt.Errorf("unknown or duplicate runtime")
		}
		seen[name] = true
	}
	return selected, nil
}

func receipt(input io.Reader, test string) string {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	outcome, count := "FAIL", 0
	for scanner.Scan() {
		var event struct{ Action, Test string }
		// Toolchain diagnostics can appear beside Go JSON; they do not prove a pass.
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Test != test {
			continue
		}
		switch event.Action {
		case "pass", "fail", "skip":
			count++
			outcome = strings.ToUpper(event.Action)
		}
	}
	if scanner.Err() != nil || count != 1 {
		return "FAIL"
	}
	return outcome
}

// The existing controller owns namespace safety, image configuration, exact Go
// test receipts and collection. Keep those protections rather than duplicating
// Kubernetes orchestration. Its Python implementation predates this Go command.
func runCluster(logs, kubeContext, namespace, values string) string {
	evidence, err := os.OpenRoot(logs)
	if err != nil {
		return "FAIL"
	}
	defer func() { _ = evidence.Close() }()
	log, err := evidence.OpenFile("cluster-controller.log", os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return "FAIL"
	}
	defer func() { _ = log.Close() }()
	state := filepath.Join(logs, "cluster-state")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	//nolint:gosec // Explicit operator cluster settings are separate argv entries to the existing controller, never shell input.
	cmd := exec.CommandContext(ctx, "python3", "../install/k8s/acceptance/acceptance.py", "run", "--context", kubeContext, "--namespace", namespace, "--values", values, "--state", state)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Run(); err != nil {
		return "FAIL"
	}
	raw, err := evidence.ReadFile("cluster-state/results.json")
	if err != nil {
		return "FAIL"
	}
	return clusterReceipt(raw)
}

func clusterReceipt(raw []byte) string {
	var report struct {
		Status string `json:"status"`
		Cases  []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"cases"`
	}
	if json.Unmarshal(raw, &report) != nil || len(report.Cases) != 1 || report.Cases[0].ID != "cluster-smoke" || report.Cases[0].Status != report.Status {
		return "FAIL"
	}
	switch report.Status {
	case "pass":
		return "PASS"
	case "pass-rbac-not-enforced":
		return "PASS-RBAC-NOT-ENFORCED"
	default:
		return "FAIL"
	}
}

func validClusterFlags(selected []string, context, namespace, values string) bool {
	if context == "" && namespace == "" && values == "" {
		return true
	}
	return slices.Contains(selected, "cluster") && context != "" && namespace != "" && values != ""
}

func outcomeCode(outcome string) int {
	switch outcome {
	case "PASS":
		return 0
	case "SKIP":
		return 2
	case "PASS-RBAC-NOT-ENFORCED":
		return 3
	default:
		return 1
	}
}

func combineCode(current, next int) int {
	if current == 1 || next == 1 {
		return 1
	}
	return max(current, next)
}

// Recover containers whose test process died before cleanup. The age threshold
// protects other active smoke runs; only explicitly labeled fixtures qualify.
func sweepStaleContainers(now time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupBudget)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--no-trunc", "--filter", "label=blerg.test.p6").Output()
	if err != nil {
		// Missing Docker remains a named skip in the sandbox test.
		return nil
	}
	for _, id := range strings.Fields(string(raw)) {
		//nolint:gosec // Container IDs come from the label-filtered Docker query and bypass a shell.
		raw, err := exec.CommandContext(ctx, "docker", "inspect", "--format", "{{.Created}}", id).Output()
		if err != nil {
			return err
		}
		created, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(raw)))
		if err != nil {
			return err
		}
		if now.Sub(created) < time.Hour {
			continue
		}
		//nolint:gosec // Remove only the exact labeled, aged container ID returned by Docker, without a shell.
		if err := exec.CommandContext(ctx, "docker", "rm", "-f", id).Run(); err != nil {
			return err
		}
	}
	return nil
}
