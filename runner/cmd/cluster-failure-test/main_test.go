package main

import (
	"strings"
	"testing"
)

func TestReceiptRequiresEveryTestAndPackage(t *testing.T) {
	good := `{"Action":"pass","Package":"fixture","Test":"TestOne"}
{"Action":"pass","Package":"fixture"}
`
	for _, tc := range []struct {
		name, raw string
		failed    bool
	}{
		{"complete", good, false},
		{"missing", `{"Action":"pass","Package":"fixture"}`, true},
		{"skip", strings.Replace(good, `"Action":"pass"`, `"Action":"skip"`, 1), true},
		{"subtest skip", good + `{"Action":"skip","Package":"fixture","Test":"TestOne/needs_privilege"}`, true},
		{"failure", good + `{"Action":"fail","Package":"fixture","Test":"TestOne"}`, true},
		{"package missing", strings.Split(good, "\n")[0], true},
		{"malformed", good + "not json", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readReceipt(strings.NewReader(tc.raw), "fixture", []string{"TestOne"})
			if (err != nil) != tc.failed {
				t.Fatalf("error = %v, want failure %v", err, tc.failed)
			}
		})
	}
}

func TestCampaignHasUniqueRequiredTests(t *testing.T) {
	packages := map[string]bool{}
	for _, s := range suites {
		if packages[s.Package] || len(s.Tests) == 0 {
			t.Fatalf("invalid suite: %v", s)
		}
		packages[s.Package] = true
		seen := map[string]bool{}
		for _, name := range s.Tests {
			if !strings.HasPrefix(name, "Test") || seen[name] {
				t.Fatalf("invalid test: %s", name)
			}
			seen[name] = true
		}
	}
}
