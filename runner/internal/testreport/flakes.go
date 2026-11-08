package testreport

import (
	"errors"
	"github.com/blerglab/blerg-ai/runner/internal/testtiming"
)

// ValidFlakeReruns validates explicit fail-then-pass evidence, never inferred
// from duplicate test names or a runner's bare retry marker.
func ValidFlakeReruns(rows []testtiming.FlakeRerun) bool {
	if len(rows) > 50 {
		return false
	}
	seen := map[string]bool{}
	for _, row := range rows {
		key := row.Package + "\x00" + row.Test
		if !relativeName(row.Package) || !validText(row.Test, 512) || len(row.Outcomes) < 2 || len(row.Outcomes) > 20 || seen[key] {
			return false
		}
		seen[key] = true
		failed, recovered := false, false
		for _, result := range row.Outcomes {
			if !outcome(result) {
				return false
			}
			if result == "fail" {
				failed = true
			}
			if result == "pass" && failed {
				recovered = true
			}
		}
		if !recovered {
			return false
		}
	}
	return true
}
func attachFlakes(report *Report, rows *[]testtiming.FlakeRerun, known map[string]bool) error {
	if rows == nil {
		return nil
	}
	if !ValidFlakeReruns(*rows) {
		return errors.New("invalid reported flake evidence")
	}
	for _, row := range *rows {
		if !known[row.Package+"\x00"+row.Test] {
			return errors.New("reported flake identity is absent from test outcomes")
		}
	}
	report.Suite.Tests.FlakeReruns = rows
	return nil
}
