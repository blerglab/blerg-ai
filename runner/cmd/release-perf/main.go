// release-perf is BLG-453's release performance check: an occasional,
// near-release tool that builds and starts the real core, board and runner
// servers and frontends, seeds a realistic dataset, drives every named
// scenarios with Playwright, and compares the result to the last release's
// baseline. It refuses to run on a busy or colliding host instead of
// producing a misleading number. See docs/design/release-performance.md.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/releaseperf"
)

const (
	exitClean     = 0
	exitRegress   = 1
	exitNoVerdict = 2
)

// scenarioBudgetP95 names the two scenarios docs/design/release-performance.md
// commits to a fixed p95 ceiling for, matched to this tool's own p95 field
// (client-observed, including render/network — not the separate server
// number): typing keeps the gate's existing 16ms client budget
// (LongSession.performance.browser.test.tsx); insights-7d keeps the 500ms
// end-to-end target TestInsights7dSeededLatency already names for its
// server number, read here as the scenario's overall ceiling. Every other
// scenario is judged on the median-and-noise rule alone (no budget).
//
// Codex review of d7e50390 (MED c): the candidate never set BudgetP95 at
// all, so accepting a candidate as the new official baseline silently
// dropped the p95 check for every future run compared against it.
var scenarioBudgetP95 = map[string]float64{
	"typing":      16,
	"insights-7d": 500,
}

func main() { os.Exit(run()) }

// service is one of the three real binaries this tool builds and runs in
// isolation (M6): its own port, environment and data/working directory.
type service struct {
	name string
	cmd  *exec.Cmd
	port int
}

func run() int {
	logsDir := flag.String("logs", "", "evidence directory (default: a fresh temp directory)")
	version := flag.String("version", "dev", "release version label for the report")
	adminDSN := flag.String("admin-dsn", os.Getenv("TEST_DATABASE_URL"), "administration DSN for the three disposable databases (TEST_DATABASE_URL by default)")
	reps := flag.Int("n", 11, "repetitions per scenario, excluding the discarded warm-up")
	baselinePath := flag.String("baseline", "", "path to the previous release's official baseline JSON (optional; its absence means this is the first run)")
	goBinary := flag.String("go", "go", "Go executable")
	sourceRoot := flag.String("source-root", "", "optional checkout used to build all three services and frontends; harness and seed stay current")
	bootOnly := flag.Bool("boot-only", false, "measure only three cache-disabled boot trials, without scenarios or soak")
	acceptanceSoak := flag.Duration("acceptance-soak", 0, "optional production boot and sustained-soak diagnostics (primary run only)")
	acceptanceDaemon := flag.Int("acceptance-daemon-pid", 0, "existing daemon PID to sample read-only during the acceptance soak")
	flag.Parse()
	if _, err := acceptanceEnv(0, *acceptanceSoak, *acceptanceDaemon, nil, ""); err != nil {
		log.Printf("refusing to start: %v", err)
		return exitNoVerdict
	}

	if err := releaseperf.ValidateVersion(*version); err != nil {
		log.Printf("refusing to start: %v", err)
		return exitNoVerdict
	}

	if *adminDSN == "" {
		log.Println("refusing to start: no administration DSN (set TEST_DATABASE_URL or pass -admin-dsn); release-perf never uses a shared database without one")
		return exitNoVerdict
	}
	repoRoot, err := gitToplevel()
	if err != nil {
		log.Printf("refusing to start: cannot find the repository root: %v", err)
		return exitNoVerdict
	}

	buildRoot, err := selectBuildRoot(repoRoot, *sourceRoot)
	if err != nil || (*bootOnly && *acceptanceSoak != 0) {
		log.Print("refusing to start: invalid source checkout or boot-only/soak combination")
		return exitNoVerdict
	}

	if *logsDir == "" {
		dir, err := os.MkdirTemp("", "blerg-release-perf-")
		if err != nil {
			log.Printf("refusing to start: cannot create an evidence directory: %v", err)
			return exitNoVerdict
		}
		*logsDir = dir
	} else if err := os.MkdirAll(*logsDir, 0o700); err != nil {
		log.Printf("refusing to start: cannot create evidence directory %s: %v", *logsDir, err)
		return exitNoVerdict
	}
	fmt.Println("Evidence directory:", *logsDir)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runID := randomHex(6)
	coreDB := "blerg_release_perf_core_" + runID
	runnerDB := "blerg_release_perf_runner_" + runID
	boardDB := "blerg_release_perf_board_" + runID
	dataDir := filepath.Join(*logsDir, "data")
	coreHome := filepath.Join(dataDir, "core")
	runnerHome := filepath.Join(dataDir, "runner")
	boardHome := filepath.Join(dataDir, "board")

	// --- Stale-leftover self-heal + preconditions (M8, failure rule) ---
	lockPath, err := releaseperf.UISuiteLockPath()
	if err != nil {
		log.Printf("refusing to start: cannot determine the UI suite lock path: %v", err)
		return exitNoVerdict
	}
	pidfilePath := filepath.Join(filepath.Dir(lockPath), "release-perf.pid")
	if err := releaseperf.CheckPidfile(pidfilePath); err != nil {
		log.Printf("refusing to start: %v", err)
		return exitNoVerdict
	}
	lock, err := releaseperf.AcquireUISuiteLock(lockPath)
	if err != nil {
		if errors.Is(err, releaseperf.ErrLockHeld) {
			log.Printf("refusing to start: the UI suite lock is held — pre-merge.sh or another heavy suite is running. No verdict.")
		} else {
			log.Printf("refusing to start: cannot take the UI suite lock: %v", err)
		}
		return exitNoVerdict
	}
	defer func() {
		if err := releaseperf.ReleaseUISuiteLock(lock); err != nil {
			log.Printf("releasing the UI suite lock failed: %v", err)
		}
	}()
	if err := releaseperf.WritePidfile(pidfilePath, os.Getpid()); err != nil {
		log.Printf("refusing to start: cannot write the pidfile: %v", err)
		return exitNoVerdict
	}
	defer func() {
		if err := os.Remove(pidfilePath); err != nil && !os.IsNotExist(err) {
			log.Printf("removing the pidfile failed: %v", err)
		}
	}()

	host, err := releaseperf.ReadHostState()
	if err != nil {
		log.Printf("refusing to start: cannot read host state: %v", err)
		return exitNoVerdict
	}
	log.Printf("host state: load1=%.2f cpus=%d available_mem_gib=%.2f", host.Load1, host.LogicalCPUs, host.AvailableMemGiB)
	if err := releaseperf.CheckHostQuiet(host); err != nil {
		log.Printf("refusing to start: %v. No verdict.", err)
		return exitNoVerdict
	}

	corePort, runnerPort, boardPort, err := threeFreePorts()
	if err != nil {
		log.Printf("refusing to start: cannot find free ports: %v", err)
		return exitNoVerdict
	}
	if err := releaseperf.CheckIsolation(releaseperf.IsolationCheck{
		Ports:    []int{corePort, runnerPort, boardPort},
		DataDirs: []string{coreHome, runnerHome, boardHome},
	}); err != nil {
		log.Printf("refusing to start: %v. No verdict.", err)
		return exitNoVerdict
	}
	for _, dir := range []string{coreHome, runnerHome, boardHome} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			log.Printf("refusing to start: cannot create data directory %s: %v", dir, err)
			return exitNoVerdict
		}
	}

	coreOrigin := fmt.Sprintf("http://127.0.0.1:%d", corePort)
	runnerOrigin := fmt.Sprintf("http://127.0.0.1:%d", runnerPort)
	boardOrigin := fmt.Sprintf("http://127.0.0.1:%d", boardPort)

	// --- Three disposable databases, isolated from every other run (M6) ---
	coreDSN, err := releaseperf.CreateDisposableDatabase(ctx, *adminDSN, coreDB)
	if err != nil {
		log.Printf("refusing to start: %v", err)
		return exitNoVerdict
	}
	defer dropDB(*adminDSN, coreDB)
	runnerDSN, err := releaseperf.CreateDisposableDatabase(ctx, *adminDSN, runnerDB)
	if err != nil {
		log.Printf("refusing to start: %v", err)
		return exitNoVerdict
	}
	defer dropDB(*adminDSN, runnerDB)
	boardDSN, err := releaseperf.CreateDisposableDatabase(ctx, *adminDSN, boardDB)
	if err != nil {
		log.Printf("refusing to start: %v", err)
		return exitNoVerdict
	}
	defer dropDB(*adminDSN, boardDB)

	buildLog, err := os.Create(filepath.Join(*logsDir, "build.log"))
	if err != nil {
		log.Printf("refusing to start: cannot create build log: %v", err)
		return exitNoVerdict
	}
	defer func() { _ = buildLog.Close() }()

	// --- Build core, board and runner: binaries plus their production frontends ---
	coreBin := filepath.Join(*logsDir, "blerg-core")
	boardBin := filepath.Join(*logsDir, "blerg-board-server")
	runnerBin := filepath.Join(*logsDir, "blerg-runner-server")
	builds := []struct{ dir, out, pkg string }{
		{filepath.Join(buildRoot, "core"), coreBin, "./cmd/blerg-core"},
		{filepath.Join(buildRoot, "board"), boardBin, "./cmd/blerg-board-server"},
		{filepath.Join(buildRoot, "runner"), runnerBin, "./cmd/server"},
	}
	for _, b := range builds {
		if err := goBuild(ctx, *goBinary, b.dir, b.out, b.pkg, buildLog); err != nil {
			log.Printf("refusing to start: building %s failed (see build.log): %v", b.pkg, err)
			return exitNoVerdict
		}
	}
	// VITE_CORE_PORT is baked into each frontend at build time (Vite env vars
	// are compile-time, not runtime) — each app's authClient.ts's
	// coreOrigin() falls back to "this host : VITE_CORE_PORT" (default 8081)
	// on a bare-IP host when it has no other way to find core. Ports must
	// therefore be chosen before this build step, not after — discovered by
	// actually running this against a live core and getting a
	// chrome-error:// navigation failure where a successful setup's
	// redirectToRefresh tried port 8081.
	frontendEnv := append(os.Environ(), "VITE_CORE_PORT="+strconv.Itoa(corePort))
	coreWebDist := filepath.Join(*logsDir, "dist", "core-web")
	boardWebDist := filepath.Join(*logsDir, "dist", "board-web")
	runnerWebDist := filepath.Join(*logsDir, "dist", "runner-frontend")
	for _, b := range []struct{ dir, outDir string }{
		{filepath.Join(buildRoot, "core", "web"), coreWebDist},
		{filepath.Join(buildRoot, "board", "web"), boardWebDist},
		{filepath.Join(buildRoot, "runner", "frontend"), runnerWebDist},
	} {
		if err := npmBuild(ctx, b.dir, b.outDir, frontendEnv, buildLog); err != nil {
			log.Printf("refusing to start: building %s failed (see build.log): %v", b.dir, err)
			return exitNoVerdict
		}
	}

	// --- Start core, board and runner, each isolated (M6) ---
	coreLocalKey := base64.StdEncoding.EncodeToString(randomBytes(32))
	coreInternalKey := randomHex(32)
	coreRegisterKey := randomHex(32)
	daemonToken := randomHex(32)
	boardServiceKey := randomHex(32)

	core := startService(ctx, "core", coreBin, coreHome, corePort, []string{
		"DATABASE_URL=" + coreDSN,
		"BLERG_CORE_LISTEN=:" + strconv.Itoa(corePort),
		"BLERG_CORE_LOCAL_KEY=" + coreLocalKey,
		"BLERG_CORE_WEB_DIST=" + coreWebDist,
		"BLERG_SETUP_ALLOW_LOCAL=true",
		"BLERG_SETUP_PUBLISHED_ADDR=localhost",
		"BLERG_CORE_INTERNAL_KEY=" + coreInternalKey,
		"BLERG_CORE_REGISTER_KEY=" + coreRegisterKey,
		"BLERG_CORE_ALLOWED_RETURN_ORIGINS=" + runnerOrigin + "," + boardOrigin,
		"BLERG_CORE_ORIGIN_AUDIENCES=" + runnerOrigin + "=blerg-runner," + boardOrigin + "=blerg-board",
	}, *logsDir)
	defer core.stop()
	if err := waitForReady(ctx, coreOrigin+"/healthz", 30*time.Second); err != nil {
		log.Printf("refusing to start: core never became ready: %v", err)
		return exitNoVerdict
	}

	runnerDir, err := runnerWorkingDir(repoRoot, *logsDir, runnerWebDist, *sourceRoot != "")
	if err != nil {
		log.Print("refusing to start: isolated historical web layout failed")
		return exitNoVerdict
	}
	runnerSvc := startServiceInDir(ctx, "runner", runnerBin, runnerDir, runnerHome, runnerPort, []string{
		"BLERG_RUNNER_LISTEN_PORT=" + strconv.Itoa(runnerPort),
		"BLERG_RUNNER_DATABASE_URL=" + runnerDSN,
		"BLERG_RUNNER_DAEMON_TOKEN=" + daemonToken,
		"BLERG_CORE_URL=" + coreOrigin,
		"BLERG_CORE_REGISTER_KEY=" + coreRegisterKey,
		"BLERG_RUNNER_CORE_INTERNAL_KEY=" + coreInternalKey,
		"BLERG_RELEASE_PERF_SERVER_TIMING=1",
		"BLERG_RUNNER_WEB_DIST=" + runnerWebDist,
	}, *logsDir)
	defer runnerSvc.stop()
	if err := waitForReady(ctx, runnerOrigin+"/agents", 30*time.Second); err != nil {
		log.Printf("refusing to start: the runner server never became ready: %v", err)
		return exitNoVerdict
	}

	if err := verifyRunnerEntry(ctx, runnerOrigin, runnerWebDist); err != nil {
		log.Print("refusing to start: runner frontend does not match the selected build")
		return exitNoVerdict
	}

	board := startServiceInDir(ctx, "board", boardBin, filepath.Join(repoRoot, "board"), boardHome, boardPort, []string{
		"DATABASE_URL=" + boardDSN,
		"BLERG_BOARD_ADDR=127.0.0.1:" + strconv.Itoa(boardPort),
		"BLERG_BOARD_SERVICE_KEY=" + boardServiceKey,
		"BLERG_CORE_URL=" + coreOrigin,
		"BLERG_CORE_REGISTER_KEY=" + coreRegisterKey,
		"BLERG_BOARD_WEB_DIR=" + boardWebDist,
	}, *logsDir)
	defer board.stop()
	if err := waitForReady(ctx, boardOrigin+"/api/version", 30*time.Second); err != nil {
		log.Printf("refusing to start: the board server never became ready: %v", err)
		return exitNoVerdict
	}

	// --- Create (or sign into) the fixed release-perf account, via the real
	// setup/login page, to learn its real account id (M1/H1: no hand-rolled
	// account row) ---
	accountPassword := "release-perf-" + randomHex(8)
	accountOut := filepath.Join(*logsDir, "account.json")
	if err := runPlaywright(ctx, repoRoot, *logsDir, map[string]string{
		"RELEASE_PERF_MODE":             "account-only",
		"RELEASE_PERF_CORE_URL":         coreOrigin,
		"RELEASE_PERF_RUNNER_URL":       runnerOrigin,
		"RELEASE_PERF_BOARD_URL":        boardOrigin,
		"RELEASE_PERF_ACCOUNT_USERNAME": "release-perf",
		"RELEASE_PERF_ACCOUNT_PASSWORD": accountPassword,
		"RELEASE_PERF_OUT":              accountOut,
	}); err != nil {
		log.Printf("refusing to start: creating the release-perf account failed: %v", err)
		return exitNoVerdict
	}
	// The account was created/signed into for real through core's own
	// setup/login page (above); its access token lives only in that page's
	// in-page JS, out of reach of a plain fetch, so its id is read back
	// directly from core's own database instead.
	corePool, err := releaseperf.OpenPool(ctx, coreDSN)
	if err != nil {
		log.Printf("refusing to start: connecting to the core database failed: %v", err)
		return exitNoVerdict
	}
	defer corePool.Close()
	accountID, err := releaseperf.ReadLocalAccountID(ctx, corePool, "release-perf")
	if err != nil {
		log.Printf("refusing to start: reading the created account id failed: %v", err)
		return exitNoVerdict
	}
	log.Printf("release-perf account id: %s", accountID)

	// --- Seed the dataset, owned by that real account (dataset and seed) ---
	runnerPool, err := releaseperf.OpenPool(ctx, runnerDSN)
	if err != nil {
		log.Printf("refusing to start: connecting to the runner database failed: %v", err)
		return exitNoVerdict
	}
	defer runnerPool.Close()
	seed := releaseperf.SeedRunnerDataset
	if *bootOnly {
		seed = releaseperf.SeedBootDataset
	}
	seedSummary, err := seed(ctx, runnerPool, accountID)
	if err != nil {
		log.Printf("refusing to start: seeding the runner dataset failed: %v", err)
		return exitNoVerdict
	}
	boardID := ""
	if !*bootOnly {
		if err := releaseperf.SeedBlackboardDataset(ctx, runnerPool, accountID, seedSummary.LongTranscriptSessionID); err != nil {
			log.Printf("refusing to start: seeding the blackboard failed: %v", err)
			return exitNoVerdict
		}
		// The board-tab scenario reads runner's own "boards" table
		// (/api/boards/{id}), not the separate standalone board service's —
		// seed it there, through runner's own real board functions.
		var err error
		boardID, _, err = releaseperf.SeedBoardDataset(ctx, runnerPool)
		if err != nil {
			log.Printf("refusing to start: seeding the board dataset failed: %v", err)
			return exitNoVerdict
		}

	}

	// --- Settle the seed's own load before the host-quiet check (M7) ---
	for _, dsn := range []string{runnerDSN, boardDSN} {
		if err := releaseperf.SettleDatabase(ctx, dsn); err != nil {
			log.Printf("refusing to start: settling a database failed: %v", err)
			return exitNoVerdict
		}
	}
	if err := waitForQuiet(10 * time.Minute); err != nil {
		log.Printf("refusing to start: %v. No verdict.", err)
		return exitNoVerdict
	}

	// --- Run the scenario matrix three times: the primary run plus two
	// calibration repeats (H4) ---
	if *bootOnly {
		out := filepath.Join(*logsDir, "boot.json")
		env := map[string]string{"RELEASE_PERF_MODE": "boot-only", "RELEASE_PERF_CORE_URL": coreOrigin, "RELEASE_PERF_RUNNER_URL": runnerOrigin, "RELEASE_PERF_BOARD_URL": boardOrigin, "RELEASE_PERF_ACCOUNT_USERNAME": "release-perf", "RELEASE_PERF_ACCOUNT_PASSWORD": accountPassword, "RELEASE_PERF_LONG_SESSION_ID": seedSummary.LongTranscriptSessionID, "RELEASE_PERF_OUT": out}
		if err := runPlaywright(ctx, repoRoot, *logsDir, env); err != nil {
			log.Print("boot measurement failed; see private Playwright evidence")
			return exitNoVerdict
		}
		if err := validateBootOutput(out); err != nil {
			log.Print("boot measurement produced incomplete samples")
			return exitNoVerdict
		}
		return exitClean
	}

	const calibrationRuns = releaseperf.CalibrationRuns
	allRuns := make([]map[string]releaseperf.ScenarioResult, 0, calibrationRuns)
	for i := 0; i < calibrationRuns; i++ {
		if err := releaseperf.CheckHostQuiet(mustReadHostState()); err != nil {
			log.Printf("host went noisy mid-run: %v. No verdict.", err)
			return exitNoVerdict
		}
		out := filepath.Join(*logsDir, fmt.Sprintf("scenarios-%d.json", i))
		browserEnv := map[string]string{
			"RELEASE_PERF_CORE_URL":         coreOrigin,
			"RELEASE_PERF_RUNNER_URL":       runnerOrigin,
			"RELEASE_PERF_BOARD_URL":        boardOrigin,
			"RELEASE_PERF_ACCOUNT_USERNAME": "release-perf",
			"RELEASE_PERF_ACCOUNT_PASSWORD": accountPassword,
			"RELEASE_PERF_REPS":             strconv.Itoa(*reps),
			"RELEASE_PERF_LONG_SESSION_ID":  seedSummary.LongTranscriptSessionID,
			"RELEASE_PERF_SWITCH_TARGET_ID": seedSummary.SwitchTargetSessionID,
			"RELEASE_PERF_BOARD_ID":         boardID,
			"RELEASE_PERF_OUT":              out,
		}
		extra, err := acceptanceEnv(i, *acceptanceSoak, *acceptanceDaemon, map[string]int{
			"core": core.cmd.Process.Pid, "runner": runnerSvc.cmd.Process.Pid, "board": board.cmd.Process.Pid,
		}, filepath.Join(*logsDir, "acceptance.json"))
		if err != nil {
			log.Printf("refusing to continue: %v", err)
			return exitNoVerdict
		}
		for key, value := range extra {
			browserEnv[key] = value
		}
		if err := runPlaywright(ctx, repoRoot, *logsDir, browserEnv); err != nil {
			log.Printf("refusing to continue: the Playwright scenario run failed: %v", err)
			return exitNoVerdict
		}
		scenarios, err := readScenarios(out)
		if err != nil {
			log.Printf("refusing to continue: reading scenario results failed: %v", err)
			return exitNoVerdict
		}
		allRuns = append(allRuns, scenarios)
	}

	fingerprint, err := releaseperf.ReadHostFingerprint()
	if err != nil {
		log.Printf("refusing to finish: reading the host fingerprint failed: %v", err)
		return exitNoVerdict
	}
	fingerprint.Browser = "Chromium (Playwright)"

	primary := releaseperf.Run{Version: *version, Commit: gitHead(repoRoot)}
	noiseByScenario := map[string]float64{}
	for name := range allRuns[0] {
		var calibrationSets [][]float64
		for _, run := range allRuns {
			if s, ok := run[name]; ok {
				calibrationSets = append(calibrationSets, s.ClientSamplesMs)
			}
		}
		noise := releaseperf.Noise(calibrationSets)
		noiseByScenario[name] = noise
		result := allRuns[0][name]
		primary.Scenarios = append(primary.Scenarios, result)
		log.Printf("scenario %s: median=%.3fms noise_floor=%.3fms", name, releaseperf.Median(result.ClientSamplesMs), noise)
	}

	var baseline *releaseperf.Baseline
	if *baselinePath != "" {
		baseline, err = releaseperf.LoadBaseline(*baselinePath)
		if err != nil {
			log.Printf("refusing to finish: loading the baseline failed: %v", err)
			return exitNoVerdict
		}
	}

	var compare releaseperf.RunComparison
	verdictLabel := "baseline recorded, no comparison made (first run; no prior baseline)"
	if baseline != nil {
		compare = releaseperf.CompareRun(primary, *baseline, fingerprint)
		verdictLabel = string(compare.Verdict)
	} else {
		compare.Verdict = releaseperf.VerdictClean
	}

	candidate := releaseperf.Baseline{
		Version:     *version,
		Commit:      gitHead(repoRoot),
		RecordedAt:  time.Now().UTC(),
		SeedVersion: releaseperf.SeedVersion,
		Host:        fingerprint,
		Scenarios:   map[string]releaseperf.BaselineScenario{},
		Verdict:     compare.Verdict,
	}
	for _, s := range primary.Scenarios {
		candidate.Scenarios[s.Name] = releaseperf.BaselineScenario{
			Unit: "ms", N: len(s.ClientSamplesMs),
			Median: releaseperf.Median(s.ClientSamplesMs), P95: releaseperf.P95(s.ClientSamplesMs),
			NoiseMedianMs: noiseByScenario[s.Name],
			BudgetP95:     scenarioBudgetP95[s.Name],
		}
	}
	candidatePath := filepath.Join(*logsDir, *version+"-candidate.json")
	if err := releaseperf.SaveBaseline(candidatePath, candidate); err != nil {
		log.Printf("writing the candidate baseline failed: %v", err)
	}

	var markdown string
	if baseline != nil {
		markdown = releaseperf.GenerateComparisonReport(primary, baseline, compare)
	} else {
		markdown = releaseperf.GenerateReport(releaseperf.Run{Version: *version, Commit: gitHead(repoRoot), Verdict: verdictLabel, Scenarios: primary.Scenarios})
	}
	reportPath := filepath.Join(*logsDir, "release-performance-report.md")
	if err := os.WriteFile(reportPath, []byte(markdown), 0o600); err != nil {
		log.Printf("writing the report failed: %v", err)
		return exitNoVerdict
	}
	fmt.Println("Candidate baseline:", candidatePath)
	fmt.Println("Report:", reportPath)
	fmt.Println("Verdict:", verdictLabel)

	switch compare.Verdict {
	case releaseperf.VerdictRegressed:
		return exitRegress
	case releaseperf.VerdictNoVerdict:
		return exitNoVerdict
	default:
		return exitClean
	}
}

func dropDB(adminDSN, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := releaseperf.DropDatabase(ctx, adminDSN, name); err != nil {
		log.Printf("dropping %s failed: %v", name, err)
	}
}

func goBuild(ctx context.Context, goBinary, dir, out, pkg string, logw *os.File) error {
	buildCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(buildCtx, goBinary, "build", "-o", out, pkg) //nolint:gosec // G204: goBinary is the --go flag (operator-controlled, defaults to "go"); out/pkg are this process's own constructed paths
	cmd.Dir = dir
	cmd.Env = goBuildEnv()
	cmd.Stdout, cmd.Stderr = logw, logw
	return cmd.Run()
}

// npmBuild builds dir's frontend straight to outDir (an absolute path
// under the run's own evidence directory), never the source checkout's
// own dist/ or *.tsbuildinfo — M6's isolation and docs/design/
// release-performance.md's "never a shared install's state" both already
// commit to that for every OTHER piece of this tool's state; the build
// output was the one thing still landing in the checkout (Codex review
// of d7e50390, MED b). Calls vite directly instead of the package's own
// "build" npm script ("tsc -b && vite build"): the tsc -b half is a
// type-check gate with noEmit — nothing this tool runs ever depends on
// its output — and it's the half that writes tsconfig.tsbuildinfo into
// the checkout; skipping it also means this tool trusts the commit it's
// measuring to already be clean, the same way it trusts the commit to
// already have passed the gate rather than re-running it.
func npmBuild(ctx context.Context, dir, outDir string, env []string, logw *os.File) error {
	buildCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(buildCtx, "npx", "vite", "build", "--outDir", outDir, "--emptyOutDir") //nolint:gosec // G204: outDir is this process's own evidence-dir-rooted path, not external input
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = logw, logw
	return cmd.Run()
}

func startService(ctx context.Context, name, bin, home string, port int, env []string, logsDir string) *service {
	return startServiceInDir(ctx, name, bin, filepath.Dir(bin), home, port, env, logsDir)
}

func startServiceInDir(ctx context.Context, name, bin, dir, home string, port int, env []string, logsDir string) *service {
	logf, err := os.Create(filepath.Join(logsDir, name+".log")) //nolint:gosec // G304: logsDir is the run's own evidence directory, created by this process
	if err != nil {
		log.Printf("%s: cannot create log file: %v", name, err)
	}
	cmd := exec.CommandContext(ctx, bin)
	cmd.Dir = dir
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}, env...)
	if logf != nil {
		cmd.Stdout, cmd.Stderr = logf, logf
	}
	if err := cmd.Start(); err != nil {
		log.Printf("%s: failed to start: %v", name, err)
	}
	return &service{name: name, cmd: cmd, port: port}
}

func (s *service) stop() {
	if s == nil || s.cmd == nil || s.cmd.Process == nil {
		return
	}
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _ = s.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		_ = s.cmd.Process.Kill()
		<-done
	}
}

func runPlaywright(ctx context.Context, repoRoot, logsDir string, env map[string]string) error {
	script := filepath.Join(repoRoot, "runner", "frontend", "scripts", "release-perf-playwright.mjs")
	cmd := exec.CommandContext(ctx, "node", script) //nolint:gosec // G204: script is this process's own fixed repo-relative path, not external input
	cmd.Dir = filepath.Join(repoRoot, "runner", "frontend")
	full := os.Environ()
	for k, v := range env {
		full = append(full, k+"="+v)
	}
	cmd.Env = full
	logf, err := os.Create(filepath.Join(logsDir, "playwright-"+randomHex(3)+".log")) //nolint:gosec // G304: logsDir is the run's own evidence directory, created by this process
	if err == nil {
		defer func() { _ = logf.Close() }()
		cmd.Stdout, cmd.Stderr = logf, logf
	}
	return cmd.Run()
}

func readScenarios(path string) (map[string]releaseperf.ScenarioResult, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is this process's own logsDir-joined scenarios file
	if err != nil {
		return nil, err
	}
	var out struct {
		Scenarios []struct {
			Name            string    `json:"name"`
			ClientSamplesMs []float64 `json:"clientSamplesMs"`
			ServerSamplesMs []float64 `json:"serverSamplesMs"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	results := map[string]releaseperf.ScenarioResult{}
	for _, s := range out.Scenarios {
		results[s.Name] = releaseperf.ScenarioResult{Name: s.Name, ClientSamplesMs: s.ClientSamplesMs, ServerSamplesMs: s.ServerSamplesMs}
	}
	return results, nil
}

func threeFreePorts() (int, int, int, error) {
	a, err := freePort()
	if err != nil {
		return 0, 0, 0, err
	}
	b, err := freePort()
	if err != nil {
		return 0, 0, 0, err
	}
	c, err := freePort()
	if err != nil {
		return 0, 0, 0, err
	}
	return a, b, c, nil
}

func freePort() (int, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected listener address type %T", l.Addr())
	}
	return addr.Port, nil
}

func randomHex(n int) string { return hex.EncodeToString(randomBytes(n)) }

func randomBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("random bytes: " + err.Error())
	}
	return b
}

func waitForReady(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
			lastErr = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not ready after %s: %w", timeout, lastErr)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func waitForQuiet(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		h, err := releaseperf.ReadHostState()
		if err != nil {
			return err
		}
		if err := releaseperf.CheckHostQuiet(h); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("host did not quiet after seeding within %s", timeout)
		}
		time.Sleep(5 * time.Second)
	}
}

func mustReadHostState() releaseperf.HostState {
	h, err := releaseperf.ReadHostState()
	if err != nil {
		return releaseperf.HostState{Load1: 1 << 20, LogicalCPUs: 1, AvailableMemGiB: 0}
	}
	return h
}

func gitHead(repoRoot string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", repoRoot, "rev-parse", "--short", "HEAD").Output() //nolint:gosec // G204: repoRoot is gitToplevel()'s own result, not external input
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func gitToplevel() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
