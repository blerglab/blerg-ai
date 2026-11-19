// Package testtiming builds a test timing report from a repository's gate
// history (scripts/pre_merge.py's append-only JSONL, or a single summary.json)
// or from a test runner's own machine-readable output (go test -json, vitest's
// JSON reporter, pytest --durations). It never runs tests itself.
package testtiming

// TestTiming is one test's measured duration inside a suite run.
type TestTiming struct {
	Package string  `json:"package"`
	Test    string  `json:"test"`
	Seconds float64 `json:"seconds"`
	Result  string  `json:"result"`
}

// Counts tallies pass/fail/skip outcomes for a suite run.
type Counts struct {
	Pass int `json:"pass"`
	Fail int `json:"fail"`
	Skip int `json:"skip"`
}

// Total returns the number of tests observed, across every outcome.
func (c Counts) Total() int { return c.Pass + c.Fail + c.Skip }

// FlakeRerun is explicit reported retry evidence for one test identity.
type FlakeRerun struct {
	Package  string   `json:"package"`
	Test     string   `json:"test"`
	Outcomes []string `json:"outcomes"`
}

// SuiteTests holds the per-test detail gate_telemetry.py records for a suite.
type SuiteTests struct {
	FlakeReruns *[]FlakeRerun `json:"flake_reruns,omitempty"`
	Counts      Counts        `json:"counts"`
	Slowest     []TestTiming  `json:"slowest"`
}

// Suite is one gate suite's single run, matching scripts/pre_merge.py's
// per-suite row.
type Suite struct {
	Layer         string     `json:"layer,omitempty"`
	BudgetSeconds *float64   `json:"budget_seconds,omitempty"`
	Suite         string     `json:"suite"`
	StartedAt     string     `json:"started_at"`
	Seconds       float64    `json:"seconds"`
	Workers       int        `json:"workers"`
	ExitCode      int        `json:"exit_code"`
	Outcome       string     `json:"outcome"`
	Tests         SuiteTests `json:"tests"`
}

// Record is one gate run: either a scripts/gate_telemetry.py history line
// (has Host/Commit/ID) or a bare scripts/pre_merge.py summary.json (does
// not). Both shapes share the fields a report needs.
type Record struct {
	ID               string   `json:"id"`
	StartedAt        string   `json:"started_at"`
	FinishedAt       string   `json:"finished_at"`
	Commit           string   `json:"commit"`
	Host             string   `json:"host"`
	Workers          int      `json:"workers"`
	ConcurrentSuites int      `json:"concurrent_suites"`
	Seconds          float64  `json:"seconds"`
	Complete         bool     `json:"complete"`
	Changed          *string  `json:"changed"`
	Suites           []Suite  `json:"suites"`
	GrowthWarnings   []string `json:"growth_warnings"`
}

// FailedSuites returns the names of suites that did not pass in this run.
func (r Record) FailedSuites() []string {
	var names []string
	for _, s := range r.Suites {
		if s.Outcome == "fail" {
			names = append(names, s.Suite)
		}
	}
	return names
}
