package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPlanCoversEveryBoundary(t *testing.T) {
	p := benchmarkPlan("go", 1800, 11, 42)
	var names []string
	for _, s := range p {
		names = append(names, s.Name)
	}
	if !reflect.DeepEqual(names, []string{"quiet", "loaded", "routes", "production"}) {
		t.Fatalf("steps = %v", names)
	}
	if !strings.Contains(strings.Join(p[0].Command, " "), "SessionSwitch.performance") || !strings.Contains(strings.Join(p[1].Command, " "), "BoardGraphInsights.performance") || !strings.Contains(strings.Join(p[3].Command, " "), "-acceptance-soak 1800s") {
		t.Fatalf("missing benchmarks: %+v", p)
	}
}
func completeReport() map[string]any {
	metrics := map[string]any{}
	for _, n := range requiredMetrics {
		metrics[n] = []any{map[string]any{}}
	}
	metrics["WORKSPACE_NATIVE"] = []any{map[string]any{"width": float64(390), "paintMedianMs": float64(50)}, map[string]any{"width": float64(3840), "paintMedianMs": float64(60)}}
	routes := map[string]any{}
	for _, n := range routeNames {
		routes[n] = "pass"
	}
	scenarios := []any{}
	for _, n := range scenarioNames {
		scenarios = append(scenarios, map[string]any{"name": n, "clientSamplesMs": []any{float64(10)}})
	}
	runs := []any{}
	for range 3 {
		runs = append(runs, map[string]any{"scenarios": scenarios})
	}
	return map[string]any{"config": map[string]any{"soak_seconds": float64(1800)}, "steps": map[string]any{"quiet": map[string]any{"metrics": metrics, "exit_code": 0}, "loaded": map[string]any{"metrics": metrics, "exit_code": 0}}, "routes": routes, "production_scenarios": runs, "bundle": map[string]any{"entry_js_gzip_bytes": float64(1_000_000)}, "production": map[string]any{"status": "complete", "boot": map[string]any{"samplesMs": []any{float64(1000), float64(1100), float64(1200)}}, "soak": map[string]any{"durationSeconds": float64(1800), "samples": []any{1, 2, 3}, "processes": map[string]any{"daemon": map[string]any{"delta": float64(100)}}, "requestFamilies": map[string]any{"live": map[string]any{"count": 2}}}}}
}
func TestAssessmentFailsClosed(t *testing.T) {
	r := completeReport()
	if f := assess(r, 70, 3000, 1_100_000); len(f) != 0 {
		t.Fatal(f)
	}
	obj(r, "routes")[routeNames[0]] = "skip"
	r["production_scenarios"] = []any{}
	obj(obj(obj(r, "steps"), "loaded"), "metrics")["WORKSPACE_NATIVE"] = []any{map[string]any{"width": float64(390), "paintMedianMs": float64(78)}}
	soak := obj(obj(r, "production"), "soak")
	soak["durationSeconds"] = float64(5)
	soak["processes"] = map[string]any{}
	obj(obj(r, "steps"), "quiet")["exit_code"] = 1
	f := strings.Join(assess(r, 70, 3000, 1_100_000), "|")
	for _, want := range []string{"missing_or_skipped_route_tests", "missing_production_scenarios", "loaded:typing_budget", "loaded:missing_native_input", "soak_duration", "missing_daemon_samples", "quiet:process_exit"} {
		if !strings.Contains(f, want) {
			t.Errorf("missing %s in %s", want, f)
		}
	}
}

func TestProductionTimingPreservesOnlyNumericDriverDiagnostics(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, filepath.Join(dir, "scenarios-0.json"), `{"scenarios":[{"name":"typing","clientSamplesMs":[1],"serverSamplesMs":[],"preKeydownSamplesMs":[31],"commandToCommitSamplesMs":[32],"private":"canary"}]}`)
	runs, err := productionScenarios(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(runs)
	if !strings.Contains(string(b), `"preKeydownSamplesMs":[31]`) || !strings.Contains(string(b), `"commandToCommitSamplesMs":[32]`) || strings.Contains(string(b), "canary") {
		t.Fatalf("diagnostics = %s", b)
	}
}

func TestProductionTypingMarkBudget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		samples []any
		want    string
	}{
		{"below", []any{float64(15)}, ""},
		{"boundary", []any{float64(16)}, "production:typing_mark_budget"},
		{"observed_regression", []any{float64(27.7), float64(49.3)}, "production:typing_mark_budget"},
		{"missing", nil, "production:missing_or_invalid_typing_mark"},
		{"invalid", []any{math.NaN()}, "production:missing_or_invalid_typing_mark"},
		{"negative", []any{float64(-1)}, "production:missing_or_invalid_typing_mark"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := completeReport()
			// A later calibration must fail even when the primary run passes.
			last := mapping(array(r, "production_scenarios")[2])
			items := append([]any(nil), array(last, "scenarios")...)
			last["scenarios"] = items
			for i, v := range items {
				m := mapping(v)
				if m["name"] == "typing" {
					items[i] = map[string]any{"name": "typing", "clientSamplesMs": tc.samples}
				}
			}
			got := strings.Join(assess(r, 70, 3000, 1100000), "|")
			if tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("assessment = %q, want %q", got, tc.want)
			}
		})
	}
}
func TestMetricsExcludeUnrelatedLogsAndDeduplicate(t *testing.T) {
	m := extractMetrics("SECRET=canary\nWORKSPACE_NATIVE {\"width\":3840,\"paintMedianMs\":60}\nAuthorization: canary\nWORKSPACE_NATIVE {\"width\":3840,\"paintMedianMs\":60}")
	b, _ := json.Marshal(m)
	if len(m["WORKSPACE_NATIVE"]) != 1 || strings.Contains(string(b), "canary") {
		t.Fatalf("metrics = %s", b)
	}
}
func TestBundleCountsEntryAndPreloadsNotLazy(t *testing.T) {
	d := t.TempDir()
	writeFixture(t, filepath.Join(d, "index.html"), `<script type="module" src="/entry.js"></script><link rel="modulepreload" href="/vendor.js"><link rel="stylesheet" href="/entry.css">`)
	for _, n := range []string{"entry.js", "vendor.js", "lazy.js", "entry.css"} {
		writeFixture(t, filepath.Join(d, n), "fixture")
	}
	b, err := bundleSize(d)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(b)
	if strings.Contains(string(encoded), "lazy.js") || number(b, "entry_js_gzip_bytes") <= 0 || len(array(b, "assets")) != 3 {
		t.Fatalf("bundle = %s", encoded)
	}
	writeFixture(t, filepath.Join(filepath.Dir(d), "outside.js"), "fixture outside bundle")
	writeFixture(t, filepath.Join(d, "index.html"), `<script src="../outside.js"></script>`)
	if _, err = bundleSize(d); err == nil {
		t.Fatal("allowed outside asset")
	}
}
func TestProductionSelectsOnlyTimingEvidence(t *testing.T) {
	d := t.TempDir()
	writeFixture(t, filepath.Join(d, "scenarios-0.json"), `{"secret":"canary","scenarios":[{"name":"cold-load","clientSamplesMs":[10,20],"serverSamplesMs":[5],"token":"canary"}]}`)
	r, err := productionScenarios(d)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(r)
	if len(r) != 1 || strings.Contains(string(b), "canary") || !strings.Contains(string(b), "[10,20]") {
		t.Fatalf("samples = %s", b)
	}
}
func TestPlanCLIAndPreflightPersistJSON(t *testing.T) {
	d := t.TempDir()
	output := filepath.Join(d, "plan.json")
	// Every subprocess is faked: a broken guard can never start a real benchmark.
	calls := 0
	deps := fakeDependencies(t)
	deps.execute = func(context.Context, step, string, []string, []int) (int, error) { calls++; return 0, nil }
	if code := run(context.Background(), []string{"--plan", "--output", output}, deps); code != 0 {
		t.Fatalf("plan exit %d", code)
	}
	if readReport(t, output)["status"] != "planned" || calls != 0 {
		t.Fatal("plan ran benchmarks")
	}
	output = filepath.Join(t.TempDir(), "results.json")
	deps.nice = func() (int, error) { return 5, nil }
	if code := run(context.Background(), []string{"--output", output}, deps); code != 2 {
		t.Fatalf("guard exit %d", code)
	}
	r := readReport(t, output)
	if r["status"] != "incomplete" || r["cause"] != "foreground_priority_required" || calls != 0 {
		t.Fatalf("guard report = %v; calls %d", r, calls)
	}
	if code := run(context.Background(), []string{"--plan", "--output", output}, deps); code != 2 {
		t.Fatal("overwrote existing output")
	}
}
func writeFixture(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}
func readReport(t *testing.T, p string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	r := map[string]any{}
	if err = json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func fakeDependencies(t *testing.T) dependencies {
	t.Helper()
	return dependencies{root: t.TempDir(), nice: func() (int, error) { return 0, nil }, load: func() ([]float64, error) { return []float64{0, 0, 0}, nil }, affinity: func() ([]int, error) { return []int{0, 1}, nil }, getenv: func(string) string { return "" }, revision: func(context.Context) (string, error) { return "fixture-revision", nil }, daemonAlive: func(int) bool { return true }, startLoad: func(context.Context, []int) (func(), error) { return func() {}, nil }}
}

func TestCompleteRunPreservesStepsAndJSON(t *testing.T) {
	deps := fakeDependencies(t)
	deps.getenv = func(key string) string {
		if key == "TEST_DATABASE_URL" {
			return "private-fixture-canary"
		}
		return ""
	}
	active := false
	stopped := false
	deps.startLoad = func(context.Context, []int) (func(), error) {
		active = true
		return func() { active = false; stopped = true }, nil
	}
	calls := []string{}
	deps.execute = func(_ context.Context, s step, log string, _ []string, cpus []int) (int, error) {
		calls = append(calls, s.Name)
		if (s.Name == "loaded") != active {
			t.Fatalf("load active during %s = %v", s.Name, active)
		}
		if (s.Name == "quiet" || s.Name == "loaded") != (len(cpus) > 0) {
			t.Fatalf("affinity for %s = %v", s.Name, cpus)
		}
		switch s.Name {
		case "quiet", "loaded":
			r := completeReport()
			metrics := obj(obj(obj(r, "steps"), s.Name), "metrics")
			lines := []string{}
			for name, items := range metrics {
				for _, v := range items.([]any) {
					if name == "TAB_SWITCH" {
						v = map[string]any{"width": 390, "commitMedianMs": 1}
					}
					b, err := json.Marshal(v)
					if err != nil {
						t.Fatal(err)
					}
					lines = append(lines, name+" "+string(b))
				}
			}
			// Tab-switch fixture prints raw JSON, with width before commitMedianMs.
			lines = append(lines, `{"width":390,"commitMedianMs":1}`)
			writeFixture(t, log, strings.Join(lines, "\n"))
			for _, arg := range s.Command {
				if p, ok := strings.CutPrefix(arg, "--outputFile.json="); ok {
					writeFixture(t, p, `{"numPassedTests":10,"numFailedTests":0,"numPendingTests":2}`)
				}
			}
		case "routes":
			lines := []string{}
			for _, n := range routeNames {
				lines = append(lines, `{"Action":"pass","Test":"`+n+`"}`)
			}
			writeFixture(t, log, strings.Join(lines, "\n"))
		case "production":
			if !stopped {
				t.Fatal("load workers not stopped before production")
			}
			dir := filepath.Join(filepath.Dir(log), "production")
			if err := os.MkdirAll(filepath.Join(dir, "dist", "runner-frontend"), 0o700); err != nil {
				t.Fatal(err)
			}
			report := completeReport()
			if err := writeJSON(filepath.Join(dir, "acceptance.json"), report["production"]); err != nil {
				t.Fatal(err)
			}
			for i, run := range array(report, "production_scenarios") {
				if err := writeJSON(filepath.Join(dir, fmt.Sprintf("scenarios-%d.json", i)), run); err != nil {
					t.Fatal(err)
				}
			}
			writeFixture(t, filepath.Join(dir, "dist", "runner-frontend", "index.html"), `<script src="entry.js"></script>`)
			writeFixture(t, filepath.Join(dir, "dist", "runner-frontend", "entry.js"), "fixture")
		}
		return 0, nil
	}
	out := filepath.Join(t.TempDir(), "results.json")
	if code := run(context.Background(), []string{"--output", out, "--daemon-pid", "42"}, deps); code != 0 {
		t.Fatalf("run exit %d; report=%v", code, readReport(t, out))
	}
	r := readReport(t, out)
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if r["status"] != "passed" || len(obj(r, "steps")) != 4 || strings.Contains(string(data), "private-fixture-canary") {
		t.Fatalf("report = %s", data)
	}
	if !reflect.DeepEqual(calls, []string{"quiet", "loaded", "routes", "production"}) {
		t.Fatal(calls)
	}
	info, err := os.Stat(out)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("private mode = %v; %v", info, err)
	}
}

func TestInterruptedExecutionRetainsCauseAndStopsLoad(t *testing.T) {
	deps := fakeDependencies(t)
	deps.getenv = func(string) string { return "private-fixture-canary" }
	stopped := false
	deps.startLoad = func(context.Context, []int) (func(), error) { return func() { stopped = true }, nil }
	deps.execute = func(context.Context, step, string, []string, []int) (int, error) {
		return -1, errors.New("transport with private-fixture-canary")
	}
	r := map[string]any{"steps": map[string]any{}}
	out := filepath.Join(t.TempDir(), "result.json")
	err := measure(context.Background(), deps, r, []step{{Name: "loaded"}}, out, t.TempDir(), "go", []int{0}, 42, 0, []float64{0, 0, 0})
	if err == nil || !stopped || len(obj(readReport(t, out), "steps")) != 1 || safeCause(err) != "measurement_error" {
		t.Fatalf("partial = %v; stopped = %v; error = %v", r, stopped, err)
	}
}
func TestCPUSelection(t *testing.T) {
	got, err := parseCPUList("2-4,4,6")
	if err != nil || !reflect.DeepEqual(got, []int{2, 3, 4, 6}) {
		t.Fatalf("%v %v", got, err)
	}
	for _, v := range []string{"", "-1", "4-2", "1-2-3", "0-999999999"} {
		if _, err = parseCPUList(v); err == nil {
			t.Errorf("accepted %q", v)
		}
	}
}

func TestExecuteRecordsRealProcessExit(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cpus, err := allowedCPUs()
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "child.log")
	s := step{Name: "fixture", Cwd: t.TempDir(), Command: []string{exe, "-test.run=^TestCommandHelper$"}, Timeout: 10 * time.Second}
	code, err := execute(context.Background(), s, log, append(os.Environ(), "ACCEPTANCE_TEST_HELPER=exit7"), cpus[:1])
	if err != nil || code != 7 {
		t.Fatalf("exit=%d err=%v", code, err)
	}
	b, err := os.ReadFile(log)
	if err != nil || string(b) != "fixture child\n" {
		t.Fatalf("log=%q err=%v", b, err)
	}
}
func TestCommandHelper(t *testing.T) {
	if os.Getenv("ACCEPTANCE_TEST_HELPER") != "exit7" {
		return
	}
	fmt.Println("fixture child")
	os.Exit(7)
}

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "--cpu-load-worker" {
		worker()
		return
	}
	os.Exit(m.Run())
}
func TestLoadWorkersReadyAndStopped(t *testing.T) {
	cpus, err := allowedCPUs()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stop, err := startLoad(ctx, cpus[:1])
	if err != nil {
		t.Fatal(err)
	}
	stop()
}

func TestBootTargetIsInformationalAndOnlyMeasuredBaselineCanRegress(t *testing.T) {
	r := completeReport()
	obj(r, "production")["boot"] = map[string]any{"samplesMs": []any{float64(5400), float64(5410), float64(5420)}}
	if f := assess(r, 70, 3000, 1100000); len(f) != 0 {
		t.Fatalf("unmeasured 3 s target became gate: %v", f)
	}
	r["boot_baseline"] = map[string]any{"samplesMs": []any{float64(6000), float64(6010), float64(6020)}}
	if f := assess(r, 70, 3000, 1100000); len(f) != 0 {
		t.Fatalf("faster than baseline but rejected: %v", f)
	}
	r["boot_baseline"] = map[string]any{"samplesMs": []any{float64(5000), float64(5010), float64(5020)}}
	if f := strings.Join(assess(r, 70, 3000, 1100000), "|"); !strings.Contains(f, "boot_regression_vs_baseline") {
		t.Fatalf("slower measured boot passed: %s", f)
	}
}

func TestBootBaselineCopiesOnlyValidatedTimingSamples(t *testing.T) {
	d := t.TempDir()
	baseline := filepath.Join(d, "baseline.json")
	out := filepath.Join(d, "plan.json")
	writeFixture(t, baseline, `{"status":"complete","secret":"private-canary","boot":{"samplesMs":[6000,6100,6200],"secret":"private-canary"}}`)
	if code := run(context.Background(), []string{"--plan", "--output", out, "--boot-baseline", baseline}, fakeDependencies(t)); code != 0 {
		t.Fatalf("exit %d", code)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "private-canary") {
		t.Fatal("baseline copied unrelated private fields")
	}
}
