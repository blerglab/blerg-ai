// session-suite runs the credential-free session regression proofs, preserving
// raw Go test events and refusing to count missing capabilities as passes.
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
	"runtime"
	"strconv"
	"strings"
	"time"
)

type scenario struct{ Name, Package, Pattern, Layer string }

var scenarios = []scenario{
	{"process-shapes", "daemon", "TestSessionEndToEndProcessShapes", "2"},
	{"parallel-commits", "daemon", "TestHostChildrenConcurrentCommits", "2"},
	{"idle-respawn", "daemon", "TestHostSteeringIdleRespawnFreeze", "2"},
	{"restart-failed-freeze", "daemon", "TestHostRestartResumesWorkspaceAfterFailedFreeze", "2"},
	{"restart-child", "daemon", "TestHostChildRestartRecoveryUsesAllocationJournal", "2"},
	{"discard-cleanup", "daemon", "TestHostChildWorktreeLifecycle", "2"},
	{"discard-delivery", "server", "TestParentDiscardAttemptsEveryReachableMemberBeforeRetry", "3b"},
	{"child-attention", "server", "TestChildQuestionLifecyclePaths", "3b"},
	{"child-escalation-restart", "server", "TestChildQuestionEscalationSurvivesRestartBeforeNotification", "3b"},
}

type result struct {
	Name    string
	Passed  bool
	Seconds float64
	Error   string
}

func receipts(r io.Reader, required string) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 16*1024*1024)
	passed := false
	for scanner.Scan() {
		var e struct{ Action, Test string }
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			return err
		}
		if e.Action == "skip" || e.Action == "fail" {
			return fmt.Errorf("%s: %s", e.Action, e.Test)
		}
		if e.Test == required && e.Action == "pass" {
			passed = true
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !passed {
		return fmt.Errorf("missing pass receipt for %s", required)
	}
	return nil
}

func main() { os.Exit(run()) }
func run() int {
	goTool := flag.String("go", "go", "Go executable")
	logs := flag.String("logs", "", "new private evidence directory (required)")
	flag.Parse()
	if runtime.GOOS != "linux" || *logs == "" {
		fmt.Fprintln(os.Stderr, "Linux and -logs NEW_DIRECTORY required; run from runner/")
		return 2
	}
	if err := os.Mkdir(*logs, 0700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	scratch, err := os.MkdirTemp("", "session-suite-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	defer func() {
		if err := os.RemoveAll(scratch); err != nil {
			fmt.Fprintln(os.Stderr, "scratch cleanup:", err)
		}
	}()
	workers := 2
	if n, err := strconv.Atoi(os.Getenv("BLERG_TOOL_WORKERS")); err == nil && n > 0 {
		workers = n
	}
	helper := filepath.Join(scratch, "daemon")
	buildLog, err := os.Create(filepath.Join(*logs, "build.log"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	//nolint:gosec // The operator explicitly selects the Go executable; arguments are fixed.
	build := exec.CommandContext(ctx, *goTool, "build", fmt.Sprintf("-p=%d", workers), "-o", helper, "./cmd/daemon")
	build.Stdout, build.Stderr = buildLog, buildLog
	err = build.Run()
	cancel()
	_ = buildLog.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper build:", err)
		return 1
	}
	var results []result
	failed := false
	fmt.Println("scenario\tlayer\tresult\tseconds")
	for _, s := range scenarios {
		started := time.Now()
		r := result{Name: s.Name}
		log, openErr := os.Create(filepath.Join(*logs, s.Name+".jsonl"))
		if openErr != nil {
			fmt.Fprintln(os.Stderr, openErr)
			return 1
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		//nolint:gosec // Operator-selected Go executable and repository-owned scenario table.
		cmd := exec.CommandContext(ctx, *goTool, "test", "-json", fmt.Sprintf("-p=%d", workers), fmt.Sprintf("-parallel=%d", workers), "-timeout=4m", "-run=^"+s.Pattern+"$", "./internal/"+s.Package)
		cmd.Env = append(os.Environ(), "GOWORK=off", "BLERG_HOST_HELPER_TEST_BINARY="+helper)
		cmd.Stdout = log
		var stderr strings.Builder
		cmd.Stderr = &stderr
		runErr := cmd.Run()
		cancel()
		_ = log.Close()
		_ = os.WriteFile(filepath.Join(*logs, s.Name+".stderr"), []byte(stderr.String()), 0600)
		if runErr == nil {
			f, e := os.Open(log.Name())
			if e != nil {
				runErr = e
			} else {
				runErr = receipts(f, s.Pattern)
				_ = f.Close()
			}
		}
		r.Passed = runErr == nil
		if runErr != nil {
			r.Error = runErr.Error()
			failed = true
		}
		r.Seconds = time.Since(started).Seconds()
		results = append(results, r)
		status := "PASS"
		if !r.Passed {
			status = "FAIL"
		}
		fmt.Printf("%s\t%s\t%s\t%.3f\n", s.Name, s.Layer, status, r.Seconds)
	}
	data, err := json.MarshalIndent(results, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(*logs, "summary.json"), data, 0600)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if failed {
		return 1
	}
	return 0
}
