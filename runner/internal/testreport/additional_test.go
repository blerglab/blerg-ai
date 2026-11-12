package testreport

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

const genericFixture = `{"version":1,"started_at":"2026-11-01T12:00:00Z","finished_at":"2026-11-01T12:00:02Z","complete":true,"outcome":"pass","tests":[{"package":"tests/example","test":"zero","outcome":"pass","seconds":0},{"package":"tests/example","test":"unknown","outcome":"skip"}]}`
const pytestFixture = `{"created":1793534402,"duration":2,"exitcode":0,"summary":{"total":1},"tests":[{"nodeid":"tests/test_example.py::test_one","outcome":"passed","setup":{"outcome":"passed","duration":0.1},"call":{"outcome":"passed","duration":0.5},"teardown":{"outcome":"passed","duration":0.2}}]}`

func TestAdditionalAdaptersWallTimeAndMissingDurations(t *testing.T) {
	r, err := GenericJSON(strings.NewReader(genericFixture), "generic")
	if err != nil || r.Suite.Seconds != 2 || r.Suite.Tests.Counts.Pass != 1 || r.Suite.Tests.Counts.Skip != 1 || len(r.Suite.Tests.Slowest) != 1 || r.Suite.Tests.Slowest[0].Seconds != 0 {
		t.Fatalf("generic=%+v %v", r, err)
	}
	r, err = PytestJSON(strings.NewReader(pytestFixture), "pytest")
	if err != nil || r.Suite.Seconds != 2 || r.FinishedAt.Unix() != 1793534402 || r.StartedAt.Unix() != 1793534400 || r.Suite.Tests.Slowest[0].Seconds < 0.799 || r.Suite.Tests.Slowest[0].Seconds > 0.801 {
		t.Fatalf("pytest=%+v %v", r, err)
	}
	raw := strings.ReplaceAll(pytestFixture, `"duration":0.1`, `"stdout":"private-canary"`)
	r, err = PytestJSON(strings.NewReader(raw), "pytest")
	if err != nil || len(r.Suite.Tests.Slowest) != 0 || r.Suite.Tests.Counts.Pass != 1 {
		t.Fatalf("missing stage duration became measured: %+v %v", r, err)
	}
}
func TestAdditionalAdaptersRefuseIncompleteUnsafeAndUnknown(t *testing.T) {
	for format, fixture := range map[string]string{"json": genericFixture, "pytest": pytestFixture} {
		parse := GenericJSON
		if format == "pytest" {
			parse = PytestJSON
		}
		cases := map[string]string{"truncated": fixture[:len(fixture)-1], "trailing": fixture + ` {}`, "unknown outcome": strings.ReplaceAll(fixture, `"outcome":"pass"`, `"outcome":"invented"`), "unsafe path": strings.ReplaceAll(fixture, "tests/example", "/private-canary/example"), "negative duration": strings.ReplaceAll(fixture, `"seconds":0`, `"seconds":-1`)}
		if format == "pytest" {
			cases["unknown outcome"] = strings.ReplaceAll(fixture, `"outcome":"passed"`, `"outcome":"invented"`)
			cases["unsafe path"] = strings.ReplaceAll(fixture, "tests/test_example.py", "../private-canary/test.py")
			cases["negative duration"] = strings.ReplaceAll(fixture, `"duration":0.5`, `"duration":-1`)
			cases["missing tests"] = strings.ReplaceAll(fixture, `"tests":`, `"omitted":`)
			cases["truncated tests"] = strings.ReplaceAll(fixture, `"total":1`, `"total":2`)
		} else {
			cases["missing complete"] = strings.ReplaceAll(fixture, `"complete":true,`, "")
			cases["version"] = strings.ReplaceAll(fixture, `"version":1`, `"version":2`)
			cases["unknown property"] = strings.ReplaceAll(fixture, `"version":1`, `"version":1,"private-canary":true`)
		}
		for name, raw := range cases {
			t.Run(format+"/"+name, func(t *testing.T) {
				if _, err := parse(strings.NewReader(raw), "unit"); err == nil || strings.Contains(err.Error(), "private-canary") {
					t.Fatalf("accepted or disclosed input: %v", err)
				}
			})
		}
		t.Run(format+"/invalid suite", func(t *testing.T) {
			if _, err := parse(strings.NewReader(fixture), "bad suite"); err == nil {
				t.Fatal("accepted invalid suite")
			}
		})
		t.Run(format+"/oversized", func(t *testing.T) {
			if _, err := parse(strings.NewReader(strings.Repeat(" ", MaxBytes+1)), "unit"); err == nil {
				t.Fatal("accepted oversized report")
			}
		})
	}
}
func TestPytestJSONPreservesFailuresInterruptionsAndExpectedOutcomes(t *testing.T) {
	for _, test := range []struct {
		outcome          string
		pass, fail, skip int
	}{{"passed", 1, 0, 0}, {"failed", 0, 1, 0}, {"error", 0, 1, 0}, {"skipped", 0, 0, 1}, {"xfailed", 0, 0, 1}, {"xpassed", 1, 0, 0}} {
		raw := strings.Replace(pytestFixture, `"outcome":"passed"`, `"outcome":"`+test.outcome+`"`, 1)
		r, err := PytestJSON(strings.NewReader(raw), "pytest")
		if err != nil || r.Suite.Tests.Counts.Pass != test.pass || r.Suite.Tests.Counts.Fail != test.fail || r.Suite.Tests.Counts.Skip != test.skip {
			t.Fatalf("%s=%+v %v", test.outcome, r, err)
		}
	}
	raw := strings.Replace(pytestFixture, `"outcome":"passed","duration":0.2`, `"outcome":"failed","duration":0.2`, 1)
	r, err := PytestJSON(strings.NewReader(raw), "pytest")
	if err != nil || r.Suite.Outcome != "fail" || r.Suite.Tests.Counts.Fail != 1 {
		t.Fatalf("teardown failure lost: %+v %v", r, err)
	}
	for _, code := range []int{1, 2, 3, 4} {
		r, err := PytestJSON(strings.NewReader(strings.Replace(pytestFixture, `"exitcode":0`, fmt.Sprintf(`"exitcode":%d`, code), 1)), "pytest")
		if err != nil || r.Suite.Outcome != "fail" || r.Complete != (code == 1) {
			t.Fatalf("exit %d=%+v %v", code, r, err)
		}
	}
}
func TestAdditionalAdaptersBoundSlowest(t *testing.T) {
	tests := make([]any, 60)
	for i := range tests {
		tests[i] = map[string]any{"package": "tests/example", "test": fmt.Sprint(i), "outcome": "pass", "seconds": i}
	}
	raw, _ := json.Marshal(map[string]any{"version": 1, "started_at": time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC), "finished_at": time.Date(2026, 11, 1, 12, 0, 2, 0, time.UTC), "complete": true, "outcome": "pass", "tests": tests})
	r, err := GenericJSON(strings.NewReader(string(raw)), "unit")
	if err != nil || r.Suite.Tests.Counts.Pass != 60 || len(r.Suite.Tests.Slowest) != 50 || r.Suite.Tests.Slowest[0].Seconds != 59 {
		t.Fatalf("slowest not bounded: %+v %v", r, err)
	}
}

func TestPytestJSONRefusesUnknownOutcomeEvenWithFailedStage(t *testing.T) {
	raw := strings.Replace(pytestFixture, `"outcome":"passed"`, `"outcome":"unknown"`, 1)
	raw = strings.Replace(raw, `"outcome":"passed","duration":0.1`, `"outcome":"failed","duration":0.1`, 1)
	if _, err := PytestJSON(strings.NewReader(raw), "pytest"); err == nil {
		t.Fatal("unknown test outcome hidden by stage failure")
	}
}

func TestAdditionalReviewSkipContradictionsAndDecodeCauses(t *testing.T) {
	for _, result := range []string{"pass", "fail"} {
		raw := strings.Replace(genericFixture, `"outcome":"pass"`, `"outcome":"skip"`, 1)
		if result == "fail" {
			raw = strings.Replace(raw, `"outcome":"pass"`, `"outcome":"fail"`, 1)
		}
		if _, err := GenericJSON(strings.NewReader(raw), "unit"); err == nil {
			t.Fatal("skipped suite accepted executed tests")
		}
	}
	if _, err := GenericJSON(strings.NewReader(`{"version":`), "unit"); err == nil || !strings.Contains(err.Error(), "invalid JSON report (") {
		t.Fatalf("decode cause omitted: %v", err)
	}
}
func TestAdditionalReviewPytestLongParameters(t *testing.T) {
	parameter := strings.Repeat("private-parameter-", 60)
	raw := strings.Replace(pytestFixture, "test_one", "test_one["+parameter+"]", 1)
	r, err := PytestJSON(strings.NewReader(raw), "pytest")
	if err != nil {
		t.Fatalf("parameterized identity refused: %v", err)
	}
	name := r.Suite.Tests.Slowest[0].Test
	if len(name) > 512 || !strings.HasPrefix(name, "test_one[params-sha256:") || strings.Contains(name, "private-parameter") || r.Suite.Tests.Slowest[0].Package != "tests/test_example.py" {
		t.Fatalf("identity not safely bounded: %s", name)
	}
	again, err := PytestJSON(strings.NewReader(raw), "pytest")
	if err != nil || again.Suite.Tests.Slowest[0].Test != name {
		t.Fatal("parameter hash not stable")
	}
	other, err := PytestJSON(strings.NewReader(strings.Replace(raw, parameter, parameter+"other", 1)), "pytest")
	if err != nil || other.Suite.Tests.Slowest[0].Test == name {
		t.Fatal("different parameters conflated")
	}
}

func TestAdditionalExplicitFlakeEvidence(t *testing.T) {
	evidence := `[{"package":"tests/example","test":"zero","outcomes":["fail","pass"]}]`
	raw := strings.Replace(genericFixture, `"version":1`, `"version":1,"flake_reruns":`+evidence, 1)
	r, err := GenericJSON(strings.NewReader(raw), "unit")
	encoded, _ := json.Marshal(r.Suite)
	if err != nil || !strings.Contains(string(encoded), `"flake_reruns"`) {
		t.Fatalf("explicit generic flake lost: %s %v", encoded, err)
	}
	raw = strings.Replace(pytestFixture, `"created":`, `"blerg_flake_reruns":[{"package":"tests/test_example.py","test":"test_one","outcomes":["fail","pass"]}],"created":`, 1)
	r, err = PytestJSON(strings.NewReader(raw), "pytest")
	encoded, _ = json.Marshal(r.Suite)
	if err != nil || !strings.Contains(string(encoded), `"flake_reruns"`) {
		t.Fatalf("explicit pytest flake lost: %s %v", encoded, err)
	}
}

func TestPytestJSONRerunNeedsExplicitAttempts(t *testing.T) {
	raw := strings.Replace(pytestFixture, `"outcome":"passed"`, `"outcome":"rerun"`, 1)
	if _, err := PytestJSON(strings.NewReader(raw), "pytest"); err == nil {
		t.Fatal("bare rerun marker accepted")
	}
	raw = strings.Replace(raw, `"created":`, `"blerg_flake_reruns":[{"package":"tests/test_example.py","test":"test_one","outcomes":["fail","pass"]}],"created":`, 1)
	r, err := PytestJSON(strings.NewReader(raw), "pytest")
	if err != nil || r.Suite.Tests.Counts.Pass != 1 || r.Suite.Tests.FlakeReruns == nil {
		t.Fatalf("explicit final attempt lost: %+v %v", r, err)
	}
}
