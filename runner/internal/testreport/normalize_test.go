package testreport

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestGoJSONWallTimeAndPackageFailure(t *testing.T) {
	raw := `{"Time":"2026-11-01T12:00:00Z","Action":"start","Package":"example/one"}
{"Time":"2026-11-01T12:00:01Z","Action":"start","Package":"example/two"}
{"Time":"2026-11-01T12:00:08Z","Action":"pass","Package":"example/one","Test":"TestOne","Elapsed":8}
{"Time":"2026-11-01T12:00:03Z","Action":"skip","Package":"example/two","Test":"TestSkip","Elapsed":0}
{"Time":"2026-11-01T12:00:10Z","Action":"pass","Package":"example/two","Test":"TestTwo","Elapsed":9}
{"Time":"2026-11-01T12:00:09Z","Action":"pass","Package":"example/one","Elapsed":9}
{"Time":"2026-11-01T12:00:10Z","Action":"fail","Package":"example/two","Elapsed":9}`
	r, err := GoJSON(strings.NewReader(raw), "go-unit")
	if err != nil || r.Suite.Seconds != 10 || r.Suite.Outcome != "fail" || r.Suite.Tests.Counts.Pass != 2 || r.Suite.Tests.Counts.Skip != 1 || len(r.Suite.Tests.Slowest) != 3 || r.Suite.Tests.Slowest[0].Seconds != 9 {
		t.Fatalf("wall time, package failure, or test outcomes lost: %+v, %v", r, err)
	}
}

func TestVitestJSONBoundsSlowestAndKeepsMissingDurationUnknown(t *testing.T) {
	assertions := []map[string]any{}
	for i := 0; i < 60; i++ {
		assertions = append(assertions, map[string]any{"fullName": fmt.Sprintf("example %d", i), "status": "passed", "duration": i})
	}
	assertions = append(assertions, map[string]any{"fullName": "unmeasured", "status": "passed"})
	input := map[string]any{"startTime": 1793534400000, "success": true, "testResults": []any{map[string]any{"name": "src/example.test.ts", "startTime": 1793534400000, "endTime": 1793534401000, "status": "passed", "assertionResults": assertions}}}
	raw, _ := json.Marshal(input)
	r, err := VitestJSON(strings.NewReader(string(raw)), "web", "/project")
	if err != nil || r.Suite.Tests.Counts.Pass != 61 || len(r.Suite.Tests.Slowest) != 50 || r.Suite.Tests.Slowest[0].Seconds != .059 {
		t.Fatalf("bounded measurements=%+v %v", r, err)
	}
	raw = []byte(`{"startTime":1793534400000,"success":true,"testResults":[{"name":"src/example.test.ts","startTime":1793534400000,"endTime":1793534401000,"status":"passed","assertionResults":[{"fullName":"unmeasured","status":"passed"},{"fullName":"zero","status":"passed","duration":0}]}]}`)
	r, err = VitestJSON(strings.NewReader(string(raw)), "web", "/project")
	if err != nil || r.Suite.Tests.Counts.Pass != 2 || len(r.Suite.Tests.Slowest) != 1 || r.Suite.Tests.Slowest[0].Test != "zero" || r.Suite.Tests.Slowest[0].Seconds != 0 {
		t.Fatalf("missing became zero: %+v %v", r, err)
	}
}

func TestVitestJSONWallTimeAndRelativeNames(t *testing.T) {
	raw := `{"startTime":1793534400000,"success":false,"testResults":[{"name":"/project/src/example.test.ts","startTime":1793534401000,"endTime":1793534405000,"status":"failed","assertionResults":[{"fullName":"parallel one","status":"passed","duration":4000},{"fullName":"parallel two","status":"passed","duration":4000},{"fullName":"not run","status":"pending"}]}]}`
	r, err := VitestJSON(strings.NewReader(raw), "web-unit", "/project")
	if err != nil || r.Suite.Seconds != 4 || r.Suite.Outcome != "fail" || r.Suite.Tests.Counts.Pass != 2 || r.Suite.Tests.Counts.Skip != 1 || len(r.Suite.Tests.Slowest) != 2 || r.Suite.Tests.Slowest[0].Package != "src/example.test.ts" {
		t.Fatalf("wall time, setup failure, or relative test path lost: %+v, %v", r, err)
	}
}

func TestReportsRefuseIncompleteAndUnsafeInputs(t *testing.T) {
	for name, raw := range map[string]string{
		"empty": "", "bad JSON": "not JSON", "unfinished": `{"Time":"2026-11-01T12:00:00Z","Action":"start","Package":"example/one"}`,
		"missing start": `{"Time":"2026-11-01T12:00:00Z","Action":"pass","Package":"example/one"}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := GoJSON(strings.NewReader(raw), "go"); err == nil {
				t.Fatal("incomplete report accepted")
			}
		})
	}
	for name, raw := range map[string]string{
		"missing outcomes":    `{"startTime":1793534400000,"success":true,"testResults":[{"name":"src/a.test.ts","startTime":1793534400000,"endTime":1793534401000,"status":"passed"}]}`,
		"missing wall bounds": `{"success":true,"testResults":[{"name":"src/a.test.ts","status":"passed","assertionResults":[]}]}`,
		"outside root":        `{"startTime":1793534400000,"success":true,"testResults":[{"name":"/private-canary/a.test.ts","startTime":1793534400000,"endTime":1793534401000,"status":"passed","assertionResults":[]}]}`,
		"trailing input":      `{} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := VitestJSON(strings.NewReader(raw), "web", "/project"); err == nil || strings.Contains(err.Error(), "private-canary") {
				t.Fatalf("unsafe report accepted or exposed: %v", err)
			}
		})
	}
}

func TestGoJSONRefusesCachedReplay(t *testing.T) {
	raw := `{"Time":"2026-11-01T12:00:00Z","Action":"start","Package":"example/unit"}
{"Time":"2026-11-01T12:00:00Z","Action":"output","Package":"example/unit","Output":"ok  \texample/unit\t(cached)\n"}
{"Time":"2026-11-01T12:00:00Z","Action":"pass","Package":"example/unit","Elapsed":0}`
	for _, suffix := range []string{"", " coverage: 50.0% of statements", " [no tests to run]"} {
		t.Run("cached"+suffix, func(t *testing.T) {
			if _, err := GoJSON(strings.NewReader(strings.ReplaceAll(raw, "(cached)", "(cached)"+suffix)), "unit"); err == nil || !strings.Contains(err.Error(), "cached") {
				t.Fatalf("cache replay with suffix %q became measured wall time: %v", suffix, err)
			}
		})
	}
}

func TestGoJSONExcludesCachedPackagesFromMixedRun(t *testing.T) {
	raw := `{"Time":"2026-11-01T12:00:00Z","Action":"start","Package":"example/cached"}
{"Time":"2026-11-01T12:00:00Z","Action":"pass","Package":"example/cached","Test":"TestCached","Elapsed":9}
{"Time":"2026-11-01T12:00:00Z","Action":"output","Package":"example/cached","Output":"ok  \texample/cached\t(cached)\n"}
{"Time":"2026-11-01T12:00:00Z","Action":"pass","Package":"example/cached","Elapsed":0}
{"Time":"2026-11-01T12:00:02Z","Action":"start","Package":"example/measured"}
{"Time":"2026-11-01T12:00:04Z","Action":"pass","Package":"example/measured","Test":"TestMeasured","Elapsed":1}
{"Time":"2026-11-01T12:00:04Z","Action":"pass","Package":"example/measured","Elapsed":2}`
	for _, suffix := range []string{"", " coverage: 50.0% of statements", " [no tests to run]"} {
		t.Run("cached"+suffix, func(t *testing.T) {
			r, err := GoJSON(strings.NewReader(strings.ReplaceAll(raw, "(cached)", "(cached)"+suffix)), "unit")
			if err != nil || r.Complete || r.CachedPackages != 1 || r.Suite.Seconds != 2 || r.Suite.Tests.Counts.Pass != 1 || len(r.Suite.Tests.Slowest) != 1 || r.Suite.Tests.Slowest[0].Test != "TestMeasured" {
				t.Fatalf("cached package with suffix %q contaminated mixed run: %+v %v", suffix, r, err)
			}
		})
	}
}

func TestVitestJSONUsesEarliestFileStart(t *testing.T) {
	raw := `{"startTime":1793534400000,"success":true,"testResults":[{"name":"src/later.test.ts","startTime":1793534403000,"endTime":1793534406000,"status":"passed","assertionResults":[]},{"name":"src/earlier.test.ts","startTime":1793534401000,"endTime":1793534402000,"status":"passed","assertionResults":[]}]}`
	r, err := VitestJSON(strings.NewReader(raw), "web", "/project")
	if err != nil || r.StartedAt.UnixMilli() != 1793534401000 || r.Suite.Seconds != 5 {
		t.Fatalf("file execution span lost: %+v %v", r, err)
	}
}

func TestVitestJSONFractionalFileTimestamps(t *testing.T) {
	raw := `{"startTime":1793534400000,"success":true,"testResults":[{"name":"src/example.test.ts","startTime":1793534401000.25,"endTime":1793534402000.75,"status":"passed","assertionResults":[{"fullName":"example","status":"passed","duration":1000.5}]}]}`
	r, err := VitestJSON(strings.NewReader(raw), "web", "/project")
	if err != nil || r.Suite.Seconds != 1.0005 || r.StartedAt.Nanosecond() != 250000 {
		t.Fatalf("real Vitest fractional bounds lost: %+v %v", r, err)
	}
	for _, bad := range []string{"-1", "1e30"} {
		if _, err := VitestJSON(strings.NewReader(strings.Replace(raw, "1793534402000.75", bad, 1)), "web", "/project"); err == nil {
			t.Fatal("invalid timestamp accepted")
		}
	}
}
