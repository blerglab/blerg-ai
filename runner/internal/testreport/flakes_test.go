package testreport

import (
	"encoding/json"
	"github.com/blerglab/blerg-ai/runner/internal/testtiming"
	"strings"
	"testing"
)

func TestReportedFlakesRequireBoundedExplicitEvidence(t *testing.T) {
	for name, rows := range map[string][]testtiming.FlakeRerun{
		"no failure":        {{Package: "tests/example", Test: "zero", Outcomes: []string{"pass", "pass"}}},
		"not recovered":     {{Package: "tests/example", Test: "zero", Outcomes: []string{"pass", "fail"}}},
		"unknown outcome":   {{Package: "tests/example", Test: "zero", Outcomes: []string{"retry", "pass"}}},
		"absolute path":     {{Package: "/private-canary", Test: "zero", Outcomes: []string{"fail", "pass"}}},
		"absent identity":   {{Package: "tests/other", Test: "zero", Outcomes: []string{"fail", "pass"}}},
		"too many attempts": {{Package: "tests/example", Test: "zero", Outcomes: append([]string{"fail"}, strings.Split(strings.Repeat("pass,", 20), ",")...)}},
	} {
		t.Run(name, func(t *testing.T) {
			encoded, _ := json.Marshal(rows)
			raw := strings.Replace(genericFixture, `"version":1`, `"version":1,"flake_reruns":`+string(encoded), 1)
			if _, err := GenericJSON(strings.NewReader(raw), "unit"); err == nil || strings.Contains(err.Error(), "private-canary") {
				t.Fatalf("invalid evidence accepted or disclosed: %v", err)
			}
		})
	}
	valid := testtiming.FlakeRerun{Package: "tests/example", Test: "zero", Outcomes: []string{"fail", "pass"}}
	if ValidFlakeReruns([]testtiming.FlakeRerun{valid, valid}) {
		t.Fatal("duplicate flake identity counted twice")
	}
	if ValidFlakeReruns(make([]testtiming.FlakeRerun, 51)) {
		t.Fatal("unbounded flake evidence accepted")
	}
	for _, raw := range []string{genericFixture, strings.Replace(genericFixture, `"version":1`, `"version":1,"flake_reruns":[]`, 1)} {
		r, err := GenericJSON(strings.NewReader(raw), "unit")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, "flake_reruns") != (r.Suite.Tests.FlakeReruns != nil) {
			t.Fatal("unknown and explicitly empty evidence conflated")
		}
	}
}
