// Package testreport normalizes completed, machine-readable project test reports.
// It reads reports; it never executes project commands.
package testreport

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/testtiming"
)

type Report struct {
	StartedAt      time.Time
	FinishedAt     time.Time
	Suite          testtiming.Suite
	Complete       bool
	CachedPackages int
}

const MaxBytes = 32 << 20

var suiteName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var cachedSummary = regexp.MustCompile(`^ok\s+\S+\s+\(cached\)`)

func validText(s string, limit int) bool {
	if len(s) == 0 || len(s) > limit {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}
func relativeName(s string) bool {
	if !validText(s, 256) || filepath.IsAbs(s) || strings.ContainsAny(s, `\:`) {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

// ValidRegion accepts an explicit relative test selection, never a machine path.
func ValidRegion(s string) bool { return relativeName(s) }
func seconds(n float64) bool    { return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= 7*86400 }

func addTest(s *testtiming.Suite, packageName, name, result string, elapsed *float64) error {
	if !relativeName(packageName) || !validText(name, 512) {
		return errors.New("invalid test identity")
	}
	switch result {
	case "pass":
		s.Tests.Counts.Pass++
	case "fail":
		s.Tests.Counts.Fail++
		s.Outcome = "fail"
	case "skip":
		s.Tests.Counts.Skip++
	default:
		return errors.New("unknown test outcome")
	}
	// Missing durations stay unmeasured; they are not invented zero samples.
	if elapsed == nil {
		return nil
	}
	if !seconds(*elapsed) {
		return errors.New("invalid test duration")
	}
	s.Tests.Slowest = append(s.Tests.Slowest, testtiming.TestTiming{Package: packageName, Test: name, Seconds: *elapsed, Result: result})
	sort.SliceStable(s.Tests.Slowest, func(i, j int) bool { return s.Tests.Slowest[i].Seconds > s.Tests.Slowest[j].Seconds })
	if len(s.Tests.Slowest) > 50 {
		s.Tests.Slowest = s.Tests.Slowest[:50]
	}
	return nil
}

func finish(r Report) (Report, error) {
	if r.StartedAt.IsZero() || r.FinishedAt.IsZero() || r.FinishedAt.Before(r.StartedAt) || !seconds(r.FinishedAt.Sub(r.StartedAt).Seconds()) {
		return Report{}, errors.New("missing or invalid wall-time bounds")
	}
	r.Suite.Seconds = r.FinishedAt.Sub(r.StartedAt).Seconds()
	r.Suite.StartedAt = r.StartedAt.UTC().Format(time.RFC3339Nano)
	if r.Suite.Outcome != "fail" && r.Suite.Tests.Counts.Total() > 0 && r.Suite.Tests.Counts.Pass == 0 && r.Suite.Tests.Counts.Skip > 0 {
		r.Suite.Outcome = "skip"
	}
	return r, nil
}

func GoJSON(reader io.Reader, name string) (Report, error) {
	if !suiteName.MatchString(name) {
		return Report{}, errors.New("invalid suite name")
	}
	r := Report{Complete: true, Suite: testtiming.Suite{Suite: name, Outcome: "pass", Tests: testtiming.SuiteTests{Slowest: []testtiming.TestTiming{}}}}
	bounded := &io.LimitedReader{R: reader, N: MaxBytes + 1}
	scan := bufio.NewScanner(bounded)
	scan.Buffer(make([]byte, 64<<10), 256<<10)
	type packageReport struct {
		started time.Time
		cached  bool
		suite   testtiming.Suite
	}
	active := map[string]*packageReport{}
	packages := 0
	for line := 1; scan.Scan(); line++ {
		var event struct {
			Time                  time.Time
			Action, Package, Test string
			Output                string
			Elapsed               *float64
		}
		if json.Unmarshal(scan.Bytes(), &event) != nil {
			return Report{}, fmt.Errorf("invalid Go JSON at line %d", line)
		}
		// Compiler output events have no package or time and carry no measurements.
		if event.Action == "build-output" || event.Action == "build-fail" {
			if event.Action == "build-fail" {
				r.Suite.Outcome = "fail"
			}
			continue
		}
		if !relativeName(event.Package) || event.Time.IsZero() {
			return Report{}, fmt.Errorf("invalid Go event identity or timestamp at line %d", line)
		}
		if event.Action == "start" && event.Test == "" {
			if active[event.Package] != nil {
				return Report{}, errors.New("duplicate package start")
			}
			packages++
			if packages > 100000 {
				return Report{}, errors.New("too many packages")
			}
			active[event.Package] = &packageReport{started: event.Time, suite: testtiming.Suite{Outcome: "pass"}}
			continue
		}
		pkg := active[event.Package]
		if event.Action == "output" && event.Test == "" && cachedSummary.MatchString(event.Output) {
			if pkg == nil {
				return Report{}, errors.New("cache summary without an active Go package")
			}
			pkg.cached = true
		}
		if event.Action != "pass" && event.Action != "fail" && event.Action != "skip" {
			continue
		}
		if pkg == nil {
			return Report{}, errors.New("outcome without an active Go package")
		}
		if event.Test != "" {
			if err := addTest(&pkg.suite, event.Package, event.Test, event.Action, event.Elapsed); err != nil {
				return Report{}, err
			}
		} else {
			delete(active, event.Package)
			if pkg.cached {
				r.CachedPackages++
				r.Complete = false
				continue
			}
			if event.Time.Before(pkg.started) {
				return Report{}, errors.New("package outcome precedes its start")
			}
			if r.StartedAt.IsZero() || pkg.started.Before(r.StartedAt) {
				r.StartedAt = pkg.started
			}
			if event.Action == "fail" || pkg.suite.Outcome == "fail" {
				r.Suite.Outcome = "fail"
			}
			r.Suite.Tests.Counts.Pass += pkg.suite.Tests.Counts.Pass
			r.Suite.Tests.Counts.Fail += pkg.suite.Tests.Counts.Fail
			r.Suite.Tests.Counts.Skip += pkg.suite.Tests.Counts.Skip
			r.Suite.Tests.Slowest = append(r.Suite.Tests.Slowest, pkg.suite.Tests.Slowest...)
			sort.SliceStable(r.Suite.Tests.Slowest, func(i, j int) bool { return r.Suite.Tests.Slowest[i].Seconds > r.Suite.Tests.Slowest[j].Seconds })
			if len(r.Suite.Tests.Slowest) > 50 {
				r.Suite.Tests.Slowest = r.Suite.Tests.Slowest[:50]
			}
			if event.Time.After(r.FinishedAt) {
				r.FinishedAt = event.Time
			}
		}
	}
	if scan.Err() != nil {
		return Report{}, fmt.Errorf("report read failed or event exceeds 256 KiB (Go JSON) (%s)", failurelog.Cause(scan.Err()))
	}
	if bounded.N == 0 {
		return Report{}, errors.New("report exceeds 32 MiB (Go JSON)")
	}
	if packages == 0 || len(active) != 0 {
		return Report{}, errors.New("incomplete Go report")
	}
	if r.CachedPackages == packages {
		return Report{}, errors.New("all Go packages were cached; no new wall-time measurement (do not rerun just to fill history)")
	}
	return finish(r)
}

func VitestJSON(reader io.Reader, name, root string) (Report, error) {
	if !suiteName.MatchString(name) {
		return Report{}, errors.New("invalid suite name")
	}
	data, err := io.ReadAll(io.LimitReader(reader, MaxBytes+1))
	if err != nil {
		return Report{}, fmt.Errorf("report read failed (Vitest JSON) (%s)", failurelog.Cause(err))
	}
	if len(data) > MaxBytes {
		return Report{}, errors.New("report exceeds 32 MiB (Vitest JSON)")
	}
	var input struct {
		StartTime   *float64 `json:"startTime"`
		Success     *bool    `json:"success"`
		TestResults []struct {
			Name, Status       string
			StartTime, EndTime *float64
			AssertionResults   *[]struct {
				FullName, Title, Status string
				Duration                *float64
			}
		}
	}
	if json.Unmarshal(data, &input) != nil || input.StartTime == nil || !validMilliseconds(*input.StartTime) || input.Success == nil || len(input.TestResults) == 0 {
		return Report{}, errors.New("invalid or incomplete Vitest report")
	}
	r := Report{Complete: true, Suite: testtiming.Suite{Suite: name, Outcome: "pass", Tests: testtiming.SuiteTests{Slowest: []testtiming.TestTiming{}}}}
	if !*input.Success {
		r.Suite.Outcome = "fail"
	}
	for _, file := range input.TestResults {
		if file.AssertionResults == nil {
			return Report{}, errors.New("missing Vitest assertion outcomes")
		}
		if file.StartTime == nil || file.EndTime == nil || !validMilliseconds(*file.StartTime) || !validMilliseconds(*file.EndTime) || *file.StartTime < *input.StartTime || *file.EndTime < *file.StartTime {
			return Report{}, errors.New("missing or invalid Vitest wall-time bounds")
		}
		start := reportMilliseconds(*file.StartTime)
		if r.StartedAt.IsZero() || start.Before(r.StartedAt) {
			r.StartedAt = start
		}
		if file.Status != "passed" && file.Status != "failed" && file.Status != "skipped" && file.Status != "pending" {
			return Report{}, errors.New("unknown Vitest file outcome")
		}
		if file.Status == "failed" {
			r.Suite.Outcome = "fail"
		}
		packageName := file.Name
		if filepath.IsAbs(packageName) {
			if !filepath.IsAbs(root) {
				return Report{}, errors.New("absolute test filename requires a project root")
			}
			packageName, err = filepath.Rel(root, packageName)
			if err != nil {
				return Report{}, errors.New("test filename is outside project root")
			}
		}
		packageName = filepath.ToSlash(packageName)
		if !relativeName(packageName) {
			return Report{}, errors.New("test filename is outside project root")
		}
		end := reportMilliseconds(*file.EndTime)
		if end.After(r.FinishedAt) {
			r.FinishedAt = end
		}
		for _, test := range *file.AssertionResults {
			result := map[string]string{"passed": "pass", "failed": "fail", "pending": "skip", "skipped": "skip", "todo": "skip"}[test.Status]
			name := test.FullName
			if name == "" {
				name = test.Title
			}
			var elapsed *float64
			if test.Duration != nil {
				n := *test.Duration / 1000
				elapsed = &n
			}
			if err := addTest(&r.Suite, packageName, name, result, elapsed); err != nil {
				return Report{}, err
			}
		}
	}
	return finish(r)
}

func validMilliseconds(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 253402300799999
}

// Vitest 4 can report sub-millisecond file bounds. Split before converting to
// nanoseconds so valid millisecond epochs cannot overflow a time.Duration.
func reportMilliseconds(value float64) time.Time {
	whole, fraction := math.Modf(value)
	return time.UnixMilli(int64(whole)).Add(time.Duration(fraction * float64(time.Millisecond))).UTC()
}
