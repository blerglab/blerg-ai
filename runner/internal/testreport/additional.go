package testreport

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/testtiming"
)

func reportJSON(reader io.Reader, target any, strict bool) error {
	data, err := io.ReadAll(io.LimitReader(reader, MaxBytes+1))
	if err != nil {
		return fmt.Errorf("report read failed (%s)", failurelog.Cause(err))
	}
	if len(data) > MaxBytes {
		return errors.New("report exceeds 32 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if strict {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid JSON report (%s)", failurelog.Cause(err))
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON report input")
	}
	return nil
}
func newReport(name string) Report {
	return Report{Complete: true, Suite: testtiming.Suite{Suite: name, Outcome: "pass", Tests: testtiming.SuiteTests{Slowest: []testtiming.TestTiming{}}}}
}
func outcome(s string) bool { return s == "pass" || s == "fail" || s == "skip" }

// GenericJSON reads Blerg's versioned report contract, with explicit wall bounds
// and reported per-test outcomes. It does not execute commands or infer flakes.
func GenericJSON(reader io.Reader, name string) (Report, error) {
	if !suiteName.MatchString(name) {
		return Report{}, errors.New("invalid suite name")
	}
	var input struct {
		Version     int                      `json:"version"`
		FlakeReruns *[]testtiming.FlakeRerun `json:"flake_reruns"`
		StartedAt   time.Time                `json:"started_at"`
		FinishedAt  time.Time                `json:"finished_at"`
		Complete    *bool                    `json:"complete"`
		Outcome     string                   `json:"outcome"`
		Tests       *[]struct {
			Package string   `json:"package"`
			Test    string   `json:"test"`
			Outcome string   `json:"outcome"`
			Seconds *float64 `json:"seconds"`
		} `json:"tests"`
	}
	if err := reportJSON(reader, &input, true); err != nil {
		return Report{}, err
	}
	if input.Version != 1 || input.Complete == nil || input.Tests == nil || !outcome(input.Outcome) || len(*input.Tests) > 100000 {
		return Report{}, errors.New("invalid or incomplete generic report")
	}
	r := newReport(name)
	r.StartedAt = input.StartedAt
	r.FinishedAt = input.FinishedAt
	r.Complete = *input.Complete
	r.Suite.Outcome = input.Outcome
	known := map[string]bool{}
	for _, test := range *input.Tests {
		known[test.Package+"\x00"+test.Test] = true
		if err := addTest(&r.Suite, test.Package, test.Test, test.Outcome, test.Seconds); err != nil {
			return Report{}, err
		}
	}
	if input.Outcome == "skip" && (r.Suite.Tests.Counts.Pass > 0 || r.Suite.Tests.Counts.Fail > 0) {
		return Report{}, errors.New("skipped suite contains executed test outcomes")
	}
	if err := attachFlakes(&r, input.FlakeReruns, known); err != nil {
		return Report{}, err
	}
	return finish(r)
}

// PytestJSON reads pytest-json-report output. The plugin writes created at
// session finish, so its session start is created minus duration. Environment,
// captured output, tracebacks, root paths and arbitrary metadata are never copied.
func PytestJSON(reader io.Reader, name string) (Report, error) {
	if !suiteName.MatchString(name) {
		return Report{}, errors.New("invalid suite name")
	}
	type stage struct {
		Duration *float64 `json:"duration"`
		Outcome  string   `json:"outcome"`
	}
	var input struct {
		Created     *float64                 `json:"created"`
		FlakeReruns *[]testtiming.FlakeRerun `json:"blerg_flake_reruns"`
		Duration    *float64                 `json:"duration"`
		ExitCode    *int                     `json:"exitcode"`
		Summary     struct {
			Total *int `json:"total"`
		} `json:"summary"`
		Tests *[]struct {
			NodeID                string `json:"nodeid"`
			Outcome               string `json:"outcome"`
			Setup, Call, Teardown *stage
		} `json:"tests"`
	}
	if err := reportJSON(reader, &input, false); err != nil {
		return Report{}, err
	}
	if input.Created == nil || math.IsNaN(*input.Created) || math.IsInf(*input.Created, 0) || *input.Created <= 0 || *input.Created > 253402300799 || input.Duration == nil || !seconds(*input.Duration) || input.ExitCode == nil || *input.ExitCode < 0 || *input.ExitCode > 5 || input.Tests == nil || input.Summary.Total == nil || *input.Summary.Total != len(*input.Tests) || len(*input.Tests) > 100000 {
		return Report{}, errors.New("invalid or incomplete pytest report")
	}
	r := newReport(name)
	whole, fraction := math.Modf(*input.Created)
	r.FinishedAt = time.Unix(int64(whole), int64(fraction*1e9)).UTC()
	r.StartedAt = r.FinishedAt.Add(-time.Duration(*input.Duration * 1e9))
	if *input.ExitCode != 0 && *input.ExitCode != 5 {
		r.Suite.Outcome = "fail"
	}
	if *input.ExitCode >= 2 && *input.ExitCode <= 4 {
		r.Complete = false
	}
	if *input.ExitCode == 5 {
		r.Suite.Outcome = "skip"
	}
	results := map[string]string{"passed": "pass", "failed": "fail", "error": "fail", "skipped": "skip", "xfailed": "skip", "xpassed": "pass"}
	known := map[string]bool{}
	for _, test := range *input.Tests {
		result := results[test.Outcome]
		file, testName, hasTest := strings.Cut(test.NodeID, "::")
		if !hasTest {
			testName = file
		}
		if len(testName) > 512 {
			function, parameters, hasParameters := strings.Cut(testName, "[")
			if !hasParameters || !validText(testName, MaxBytes) || !validText(function, 512) {
				return Report{}, errors.New("invalid pytest test identity")
			}
			hash := sha256.Sum256([]byte(parameters))
			testName = fmt.Sprintf("%s[params-sha256:%x]", function, hash[:16])
		}
		if result == "" && test.Outcome == "rerun" && input.FlakeReruns != nil {
			for _, row := range *input.FlakeReruns {
				if row.Package == file && row.Test == testName && len(row.Outcomes) > 0 {
					result = row.Outcomes[len(row.Outcomes)-1]
					break
				}
			}
		}
		if !outcome(result) {
			return Report{}, errors.New("unknown pytest test outcome")
		}
		var elapsed *float64
		total := 0.0
		measured, missing := false, false
		for _, phase := range []*stage{test.Setup, test.Call, test.Teardown} {
			if phase == nil {
				continue
			}
			if phase.Outcome != "" && results[phase.Outcome] == "" {
				return Report{}, errors.New("unknown pytest stage outcome")
			}
			if results[phase.Outcome] == "fail" {
				result = "fail"
			}
			if phase.Duration == nil {
				missing = true
				continue
			}
			if !seconds(*phase.Duration) {
				return Report{}, errors.New("invalid pytest stage duration")
			}
			total += *phase.Duration
			measured = true
		}
		if measured && !missing {
			elapsed = &total
		}
		known[file+"\x00"+testName] = true
		if err := addTest(&r.Suite, file, testName, result, elapsed); err != nil {
			return Report{}, err
		}
	}
	if err := attachFlakes(&r, input.FlakeReruns, known); err != nil {
		return Report{}, err
	}
	return finish(r)
}
