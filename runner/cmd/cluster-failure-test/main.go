// cluster-failure-test runs only local fixtures. It never invokes Kubernetes tools.
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
	"regexp"
	"runtime"
	"strings"
	"time"
)

type suite struct {
	Package string   `json:"package"`
	Tests   []string `json:"tests"`
}

// Explicit names make renames or build-tag exclusions fail instead of silently
// turning a green package into evidence for a case it did not exercise.
var suites = []suite{
	{"clusterworkspace", []string{
		"TestDiskGitTLSIsolationRecoveryAndTampering", "TestUnboundedStorageFailsClosed",
		"TestCloneAllBeforeReadyAndPartialFailure", "TestPartialPushResumePreservesSuccess",
		"TestPublicCloneDoesNotGrantPushAndStopSerializes", "TestPlanCredentialIsolation",
		"TestSupervisorAdmissionStopAndUncertainCleanup", "TestClusterWritersFailuresRetainAndFence",
		"TestClusterSnapshotThawsBeforeNetworkAndRetriesWithoutFreeze", "TestClusterCanceledSnapshotThawsAndPreservesOtherSuccess",
	}},
	{"runner", []string{
		"TestClusterRequestsOutboundIsolationAndBudgets", "TestClusterRequestsDisconnectJoinsAndFencesDelivery",
		"TestClusterRequestsUnboundFailsClosed", "TestClusterRunnerRefusesMissingContainmentBeforeAllocation",
		"TestClusterRunnerRefusesAgentWritableSnapshotLocation", "TestClusterWorkspaceStoragePlanEnforcement",
	}},
	{"shellruntime", []string{
		"TestClusterShellAtomicAdmissionFreezeAndCleanupEvidence", "TestClusterLostShellHelperCannotReconcileFromFreshMap",
		"TestClusterCleanExpiredPTYDoesNotBlockWriterFreeze",
	}},
}

type receipt struct {
	Package string  `json:"package"`
	Status  string  `json:"status"`
	Passed  int     `json:"passed"`
	Failed  int     `json:"failed"`
	Skipped int     `json:"skipped"`
	Seconds float64 `json:"seconds"`
}

func readReceipt(input io.Reader, pkg string, required []string) (receipt, error) {
	r := receipt{Package: pkg, Status: "fail"}
	seen := map[string]bool{}
	packagePass := false
	dec := json.NewDecoder(input)
	for {
		var event struct{ Action, Package, Test string }
		err := dec.Decode(&event)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return r, errors.New("invalid go test JSON stream")
		}
		if event.Package != pkg {
			continue
		}
		switch event.Action {
		case "pass":
			if event.Test == "" {
				packagePass = true
			} else {
				r.Passed++
				seen[event.Test] = true
			}
		case "fail":
			r.Failed++
		case "skip":
			r.Skipped++
		}
	}
	for _, name := range required {
		if !seen[name] {
			return r, fmt.Errorf("required test did not pass: %s", name)
		}
	}
	if !packagePass || r.Failed > 0 || r.Skipped > 0 {
		return r, errors.New("package failed, skipped coverage, or missing package completion")
	}
	r.Status = "pass"
	return r, nil
}

func runSuite(goBinary string, s suite, race bool) receipt {
	pkg := "github.com/blerglab/blerg-ai/runner/internal/" + s.Package
	names := make([]string, len(s.Tests))
	for i, name := range s.Tests {
		names[i] = regexp.QuoteMeta(name)
	}
	args := []string{"test", "-json", "-timeout=10m"}
	if race {
		args = append(args, "-race")
	}
	args = append(args, "./internal/"+s.Package, "-run", "^("+strings.Join(names, "|")+")$")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, goBinary, args...)
	cmd.Env = append(os.Environ(), "GOWORK=off", "P3_TEST_QUOTA=0")
	// Raw test output is intentionally not included in the shareable receipt.
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return receipt{Package: pkg, Status: "fail"}
	}
	start := time.Now()
	if err = cmd.Start(); err != nil {
		return receipt{Package: pkg, Status: "fail"}
	}
	r, readErr := readReceipt(pipe, pkg, s.Tests)
	if readErr != nil {
		// Drain before Wait: assertion failures must not strand a child on a full pipe.
		_, _ = io.Copy(io.Discard, pipe)
	}
	waitErr := cmd.Wait()
	if readErr != nil || waitErr != nil {
		r.Status = "fail"
	}
	r.Seconds = time.Since(start).Seconds()
	return r
}

func main() {
	goBinary := flag.String("go", "go", "Go executable")
	race := flag.Bool("race", false, "enable the race detector")
	flag.Parse()
	if runtime.GOOS != "linux" {
		fmt.Fprintln(os.Stderr, "Linux is required; no fixture coverage was run")
		os.Exit(2)
	}
	data, err := os.ReadFile("go.mod")
	if err != nil || !strings.Contains(string(data), "module github.com/blerglab/blerg-ai/runner\n") {
		fmt.Fprintln(os.Stderr, "Run from the runner module directory")
		os.Exit(2)
	}
	failed := false
	enc := json.NewEncoder(os.Stdout)
	for _, s := range suites {
		r := runSuite(*goBinary, s, *race)
		if r.Status != "pass" {
			failed = true
		}
		if enc.Encode(r) != nil {
			os.Exit(2)
		}
	}
	if failed {
		os.Exit(1)
	}
}
