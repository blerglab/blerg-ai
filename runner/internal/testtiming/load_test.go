package testtiming

import (
	"os"
	"testing"
)

func TestLoadHistoryReadsRealGateRunsFile(t *testing.T) {
	records, err := LoadHistory("testdata/real-history.jsonl")
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(records) != 5 {
		t.Fatalf("len(records) = %d, want 5", len(records))
	}
	if records[0].Commit == "" {
		t.Fatalf("records[0].Commit is empty; real history always carries a commit")
	}
}

func TestLoadHistorySkipsUnparseableLines(t *testing.T) {
	path := t.TempDir() + "/history.jsonl"
	writeFile(t, path, "{\"seconds\": 1.0, \"suites\": []}\nnot json\n\n{\"seconds\": 2.0, \"suites\": []}\n")
	records, err := LoadHistory(path)
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("len(records) = %d, want 2 (bad line skipped)", len(records))
	}
}

func TestLoadHistoryMissingFile(t *testing.T) {
	if _, err := LoadHistory("testdata/does-not-exist.jsonl"); err == nil {
		t.Fatal("expected an error for a missing history file")
	}
}

func TestLoadSummaryReadsRealPreMergeSummary(t *testing.T) {
	record, err := LoadSummary("testdata/summary-c4489560.json")
	if err != nil {
		t.Fatalf("LoadSummary: %v", err)
	}
	if record.Host != "" {
		t.Fatalf("a bare summary.json has no host, got %q", record.Host)
	}
	if len(record.Suites) != 1 {
		t.Fatalf("len(record.Suites) = %d, want 1 (this fixture is trimmed down to runner-go; see derive_fixtures.py)", len(record.Suites))
	}
	if record.StartedAt == "" {
		t.Fatal("StartedAt should be derived from the first suite when absent")
	}
}

func TestLoadSummaryRejectsInvalidJSON(t *testing.T) {
	path := t.TempDir() + "/bad.json"
	writeFile(t, path, "{not json")
	if _, err := LoadSummary(path); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

func TestParseGoTestJSON(t *testing.T) {
	path := t.TempDir() + "/go-test.json"
	writeFile(t, path, `{"Action":"run","Test":"TestFoo"}
{"Action":"pass","Test":"TestFoo","Package":"pkg/a","Elapsed":1.5}
{"Action":"fail","Test":"TestBar","Package":"pkg/a","Elapsed":0.2}
{"Action":"output","Package":"pkg/a"}
`)
	suite, err := ParseGoTestJSON(path, "my-go-suite")
	if err != nil {
		t.Fatalf("ParseGoTestJSON: %v", err)
	}
	if suite.Suite != "my-go-suite" {
		t.Fatalf("suite.Suite = %q", suite.Suite)
	}
	if suite.Tests.Counts.Pass != 1 || suite.Tests.Counts.Fail != 1 {
		t.Fatalf("counts = %+v", suite.Tests.Counts)
	}
	if suite.Outcome != "fail" {
		t.Fatalf("outcome = %q, want fail (a failing test fails the suite)", suite.Outcome)
	}
	if len(suite.Tests.Slowest) != 2 || suite.Tests.Slowest[0].Test != "TestFoo" {
		t.Fatalf("slowest = %+v, want TestFoo first (1.5s > 0.2s)", suite.Tests.Slowest)
	}
}

func TestParseVitestJSON(t *testing.T) {
	path := t.TempDir() + "/vitest.json"
	writeFile(t, path, `{
		"testResults": [
			{"name": "a.test.ts", "assertionResults": [
				{"fullName": "renders", "status": "passed", "duration": 120},
				{"fullName": "skips", "status": "skipped", "duration": 0}
			]}
		]
	}`)
	suite, err := ParseVitestJSON(path, "my-vitest-suite")
	if err != nil {
		t.Fatalf("ParseVitestJSON: %v", err)
	}
	if suite.Tests.Counts.Pass != 1 || suite.Tests.Counts.Skip != 1 {
		t.Fatalf("counts = %+v", suite.Tests.Counts)
	}
	if suite.Tests.Slowest[0].Seconds != 0.12 {
		t.Fatalf("seconds = %v, want 0.12 (duration is milliseconds)", suite.Tests.Slowest[0].Seconds)
	}
}

func TestParsePytestDurations(t *testing.T) {
	path := t.TempDir() + "/durations.txt"
	writeFile(t, path, `============================= slowest durations ==============================
1.23s call     tests/test_x.py::test_slow
0.01s setup    tests/test_x.py::test_fast
=========================== 2 passed in 1.30s ============================
`)
	suite, err := ParsePytestDurations(path, "my-pytest-suite")
	if err != nil {
		t.Fatalf("ParsePytestDurations: %v", err)
	}
	if len(suite.Tests.Slowest) != 2 {
		t.Fatalf("len(slowest) = %d, want 2", len(suite.Tests.Slowest))
	}
	if suite.Tests.Slowest[0].Test != "tests/test_x.py::test_slow" || suite.Tests.Slowest[0].Seconds != 1.23 {
		t.Fatalf("slowest[0] = %+v", suite.Tests.Slowest[0])
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile(%s): %v", path, err)
	}
}

func TestParseGoTestJSONNestedPackageTotals(t *testing.T) {
	for _, tc := range []struct {
		name, events string
		seconds      float64
		outcome      string
	}{
		{"package wall", `{"Action":"start","Package":"pkg/a"}
{"Action":"pass","Package":"pkg/a","Test":"TestRoot/child/grandchild","Elapsed":2}
{"Action":"pass","Package":"pkg/a","Test":"TestRoot/child","Elapsed":3}
{"Action":"pass","Package":"pkg/a","Test":"TestRoot","Elapsed":5}
{"Action":"skip","Package":"pkg/a","Test":"TestSkip","Elapsed":0}
{"Action":"pass","Package":"pkg/a","Elapsed":8}
`, 8, "pass"},
		{"root fallback", `{"Action":"pass","Package":"pkg/a","Test":"TestRoot/child","Elapsed":3}
{"Action":"pass","Package":"pkg/a","Test":"TestRoot","Elapsed":5}
`, 5, "pass"},
		{"package failure without tests", `{"Action":"start","Package":"pkg/a"}
{"Action":"fail","Package":"pkg/a","Elapsed":4}
`, 4, "fail"},
		{"multiple packages", `{"Action":"pass","Package":"pkg/a","Test":"TestRoot","Elapsed":5}
{"Action":"pass","Package":"pkg/a","Elapsed":8}
{"Action":"pass","Package":"pkg/b","Test":"TestRoot/child","Elapsed":2}
{"Action":"pass","Package":"pkg/b","Test":"TestRoot","Elapsed":3}
`, 11, "pass"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := t.TempDir() + "/go.json"
			writeFile(t, path, tc.events)
			got, err := ParseGoTestJSON(path, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			if got.Seconds != tc.seconds || got.Outcome != tc.outcome {
				t.Fatalf("seconds/outcome = %v/%s, want %v/%s", got.Seconds, got.Outcome, tc.seconds, tc.outcome)
			}
			if tc.name == "package wall" {
				if got.Tests.Counts != (Counts{Pass: 1, Skip: 1}) {
					t.Fatalf("counts = %+v", got.Tests.Counts)
				}
				if len(got.Tests.Slowest) != 4 {
					t.Fatalf("lost subtest detail: %+v", got.Tests.Slowest)
				}
			}
		})
	}
}
