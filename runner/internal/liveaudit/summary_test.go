package liveaudit

import (
	"testing"
)

func TestOutcomeRequiresAssertion(t *testing.T) {
	for _, test := range []struct {
		exit         int
		output, want string
	}{
		{1, "--- FAIL: TestGuard (0s)\nassertion failed", "killed"},
		{1, "--- FAIL: TestGuard (0s)\npanic: nil pointer", "harness_or_panic"},
		{1, "panic: test timed out", "timeout"},
		{0, "PASS", "survived"},
		{1, "compile error", "harness_or_panic"},
	} {
		if got := goOutcome(test.exit, test.output); got != test.want {
			t.Errorf("got %s want %s", got, test.want)
		}
	}
}
func TestSummaryRejectsUnproven(t *testing.T) {
	for _, record := range []Object{{"error": "baseline failed"}, {"baseline": Object{"exit_code": float64(1)}}, {"restored": Object{"exit_code": float64(1)}}, {"baselines": []any{Object{"exit_code": float64(1)}}}} {
		if err := validateRun(record); err == nil {
			t.Fatal("accepted failed baseline")
		}
	}
}
