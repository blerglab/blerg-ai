package childlimits

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

func TestEnvironmentSeedsAndRanges(t *testing.T) {
	for _, tc := range []struct {
		name, value string
	}{
		{"BLERG_RUNNER_MAX_PARALLEL_CHILDREN", "51"},
		{"BLERG_RUNNER_MAX_CHILD_STARTS_PER_HOUR", "10001"},
		{"BLERG_RUNNER_MAX_CHILD_STARTS_PER_HOUR", "-1"},
		{"BLERG_RUNNER_MAX_CHILD_STARTS_PER_HOUR", "0"},
		{"BLERG_RUNNER_MAX_CHILD_STARTS_PER_HOUR", "oops"},
	} {
		t.Run(tc.name+tc.value, func(t *testing.T) {
			t.Setenv("BLERG_RUNNER_MAX_PARALLEL_CHILDREN", "3")
			t.Setenv("BLERG_RUNNER_MAX_CHILD_STARTS_PER_HOUR", "120")
			t.Setenv(tc.name, tc.value)
			var buf bytes.Buffer
			prev := log.Writer()
			log.SetOutput(&buf)
			t.Cleanup(func() { log.SetOutput(prev) })
			got := FromEnv()
			if got.Parallel != 3 || got.StartsPerHour != 120 || !strings.Contains(buf.String(), tc.name+" must be a positive integer between") {
				t.Fatalf("fallback %+v / %s", got, buf.String())
			}
		})
	}
	for _, l := range []Limits{{1, 1}, {50, 10000}, {3, 120}} {
		if err := l.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range []Limits{{0, 120}, {51, 120}, {3, 0}, {3, 10001}} {
		if l.Validate() == nil {
			t.Fatalf("accepted %+v", l)
		}
	}
}
