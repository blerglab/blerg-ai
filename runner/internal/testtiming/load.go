package testtiming

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// LoadHistory reads an append-only JSONL gate history file, oldest first. A
// line that fails to parse is skipped rather than aborting the read: a
// partial write from a killed process must not hide every earlier run.
func LoadHistory(path string) ([]Record, error) {
	file, err := os.Open(path) // #nosec G304 -- a history or test-report path the operator passes on the command line.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	var records []Record
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var record Record
		if err := json.Unmarshal(line, &record); err != nil {
			continue
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return records, err
	}
	return records, nil
}

// LoadSummary reads a single scripts/pre_merge.py summary.json (the report
// shape, without the telemetry wrapper's id/host/commit) as one Record.
func LoadSummary(path string) (Record, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- a history or test-report path the operator passes on the command line.
	if err != nil {
		return Record{}, err
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return Record{}, fmt.Errorf("%s: %w", path, err)
	}
	if record.StartedAt == "" && len(record.Suites) > 0 {
		record.StartedAt = record.Suites[0].StartedAt
	}
	return record, nil
}

// goTestEvent mirrors one line of `go test -json` output.
type goTestEvent struct {
	Action  string
	Test    string
	Package string
	Elapsed float64
}

// ParseGoTestJSON reads `go test -json` output and returns one synthetic
// Suite named after the suite argument.
func ParseGoTestJSON(path, suite string) (Suite, error) {
	file, err := os.Open(path) // #nosec G304 -- a history or test-report path the operator passes on the command line.
	if err != nil {
		return Suite{}, err
	}
	defer func() { _ = file.Close() }()
	var tests []TestTiming
	counts := Counts{}
	// Package durations include setup, cleanup and parallel test scheduling.
	// If a package has no terminal event, use only its root test durations.
	roots := map[string]float64{}
	packages := map[string]float64{}
	failed := false
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var event goTestEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		switch event.Action {
		case "pass", "fail", "skip":
		default:
			continue
		}
		if event.Test == "" {
			packages[event.Package] = event.Elapsed
			failed = failed || event.Action == "fail"
			continue
		}
		tests = append(tests, TestTiming{Package: event.Package, Test: event.Test, Seconds: event.Elapsed, Result: event.Action})
		if strings.Contains(event.Test, "/") {
			continue
		}
		roots[event.Package] += event.Elapsed
		switch event.Action {
		case "pass":
			counts.Pass++
		case "fail":
			counts.Fail++
		case "skip":
			counts.Skip++
		}
	}
	result := buildSuite(suite, tests, counts)
	result.Seconds = 0
	for pkg, seconds := range roots {
		if _, ok := packages[pkg]; !ok {
			result.Seconds += seconds
		}
	}
	for _, seconds := range packages {
		result.Seconds += seconds
	}
	if failed {
		result.Outcome = "fail"
	}
	return result, scanner.Err()
}

// vitestReport mirrors the fields a vitest `--reporter=json` file carries.
type vitestReport struct {
	TestResults []struct {
		Name             string `json:"name"`
		AssertionResults []struct {
			FullName string  `json:"fullName"`
			Title    string  `json:"title"`
			Status   string  `json:"status"`
			Duration float64 `json:"duration"`
		} `json:"assertionResults"`
	} `json:"testResults"`
}

// ParseVitestJSON reads vitest's JSON reporter output into a synthetic Suite.
func ParseVitestJSON(path, suite string) (Suite, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- a history or test-report path the operator passes on the command line.
	if err != nil {
		return Suite{}, err
	}
	var report vitestReport
	if err := json.Unmarshal(data, &report); err != nil {
		return Suite{}, fmt.Errorf("%s: %w", path, err)
	}
	statusFor := map[string]string{"passed": "pass", "failed": "fail", "pending": "skip", "skipped": "skip", "todo": "skip"}
	var tests []TestTiming
	counts := Counts{}
	for _, file := range report.TestResults {
		for _, test := range file.AssertionResults {
			result, ok := statusFor[test.Status]
			if !ok {
				continue
			}
			switch result {
			case "pass":
				counts.Pass++
			case "fail":
				counts.Fail++
			case "skip":
				counts.Skip++
			}
			name := test.FullName
			if name == "" {
				name = test.Title
			}
			tests = append(tests, TestTiming{Package: file.Name, Test: name, Seconds: test.Duration / 1000, Result: result})
		}
	}
	return buildSuite(suite, tests, counts), nil
}

// pytestDurationLine matches a line of `pytest --durations=N` output, e.g.
// "1.23s call     tests/test_x.py::test_y".
var pytestDurationLine = regexp.MustCompile(`^\s*(\d+(?:\.\d+)?)s\s+(?:call|setup|teardown)\s+(.+)$`)

// ParsePytestDurations reads `pytest --durations=N` console output into a
// synthetic Suite. pytest --durations never reports pass/fail/skip per test,
// so every row is counted as pass: the acceptance case is timing, not
// outcome.
func ParsePytestDurations(path, suite string) (Suite, error) {
	file, err := os.Open(path) // #nosec G304 -- a history or test-report path the operator passes on the command line.
	if err != nil {
		return Suite{}, err
	}
	defer func() { _ = file.Close() }()
	var tests []TestTiming
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		match := pytestDurationLine.FindStringSubmatch(scanner.Text())
		if match == nil {
			continue
		}
		seconds, err := strconv.ParseFloat(match[1], 64)
		if err != nil {
			continue
		}
		tests = append(tests, TestTiming{Test: strings.TrimSpace(match[2]), Seconds: seconds, Result: "pass"})
	}
	counts := Counts{Pass: len(tests)}
	return buildSuite(suite, tests, counts), scanner.Err()
}

func buildSuite(name string, tests []TestTiming, counts Counts) Suite {
	sorted := append([]TestTiming(nil), tests...)
	sortTestsBySecondsDesc(sorted)
	total := 0.0
	for _, t := range tests {
		total += t.Seconds
	}
	limit := 30
	if len(sorted) < limit {
		limit = len(sorted)
	}
	return Suite{
		Suite:     name,
		StartedAt: time.Now().UTC().Format(time.RFC3339),
		Seconds:   total,
		Outcome:   outcomeFor(counts),
		Tests:     SuiteTests{Counts: counts, Slowest: sorted[:limit]},
	}
}

func outcomeFor(counts Counts) string {
	if counts.Fail > 0 {
		return "fail"
	}
	return "pass"
}

func sortTestsBySecondsDesc(tests []TestTiming) {
	for i := 1; i < len(tests); i++ {
		for j := i; j > 0 && tests[j].Seconds > tests[j-1].Seconds; j-- {
			tests[j], tests[j-1] = tests[j-1], tests[j]
		}
	}
}
