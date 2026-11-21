// test-timing builds a markdown test timing report, with an inline-data
// Vega-Lite chart, from a repository's gate history or a test runner's own
// machine-readable output. It never runs tests itself.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/testtiming"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "test-timing:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("test-timing", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var (
		history    repeatedFlag
		summaries  repeatedFlag
		goTestJSON repeatedFlag
		vitestJSON repeatedFlag
		pytestDur  repeatedFlag
		since      = flags.String("since", "", "only list gate runs started within this long ago, e.g. 7d, 24h (growth medians still use older history)")
		repo       = flags.String("repo", "", "label for the report's title")
	)
	flags.Var(&history, "history", "path to an append-only gate history JSONL file (repeatable)")
	flags.Var(&summaries, "summary", "path to a single scripts/pre_merge.py summary.json (repeatable, oldest first)")
	flags.Var(&goTestJSON, "go-test-json", "name=path pairs: a suite name and its `go test -json` output (repeatable)")
	flags.Var(&vitestJSON, "vitest-json", "name=path pairs: a suite name and its vitest JSON reporter output (repeatable)")
	flags.Var(&pytestDur, "pytest-durations", "name=path pairs: a suite name and its `pytest --durations` console output (repeatable)")
	if err := flags.Parse(args); err != nil {
		return err
	}

	var records []testtiming.Record
	for _, path := range history {
		loaded, err := testtiming.LoadHistory(path)
		if err != nil {
			return fmt.Errorf("--history %s: %w", path, err)
		}
		records = append(records, loaded...)
	}
	for _, path := range summaries {
		loaded, err := testtiming.LoadSummary(path)
		if err != nil {
			return fmt.Errorf("--summary %s: %w", path, err)
		}
		records = append(records, loaded)
	}

	var liveSuites []testtiming.Suite
	for _, pair := range goTestJSON {
		name, path, err := splitPair(pair)
		if err != nil {
			return fmt.Errorf("--go-test-json %s: %w", pair, err)
		}
		suite, err := testtiming.ParseGoTestJSON(path, name)
		if err != nil {
			return fmt.Errorf("--go-test-json %s: %w", pair, err)
		}
		liveSuites = append(liveSuites, suite)
	}
	for _, pair := range vitestJSON {
		name, path, err := splitPair(pair)
		if err != nil {
			return fmt.Errorf("--vitest-json %s: %w", pair, err)
		}
		suite, err := testtiming.ParseVitestJSON(path, name)
		if err != nil {
			return fmt.Errorf("--vitest-json %s: %w", pair, err)
		}
		liveSuites = append(liveSuites, suite)
	}
	for _, pair := range pytestDur {
		name, path, err := splitPair(pair)
		if err != nil {
			return fmt.Errorf("--pytest-durations %s: %w", pair, err)
		}
		suite, err := testtiming.ParsePytestDurations(path, name)
		if err != nil {
			return fmt.Errorf("--pytest-durations %s: %w", pair, err)
		}
		liveSuites = append(liveSuites, suite)
	}
	if len(liveSuites) > 0 {
		records = append(records, testtiming.Record{StartedAt: time.Now().UTC().Format(time.RFC3339), Suites: liveSuites})
	}

	if len(records) == 0 {
		return fmt.Errorf("no input given: pass --history, --summary, or one of --go-test-json/--vitest-json/--pytest-durations")
	}

	var sinceTime time.Time
	if *since != "" {
		duration, err := parseSince(*since)
		if err != nil {
			return fmt.Errorf("--since %s: %w", *since, err)
		}
		sinceTime = time.Now().Add(-duration)
	}

	report := testtiming.Build(records, sinceTime, time.Now())
	_, err := fmt.Fprint(stdout, testtiming.RenderMarkdown(report, *repo))
	return err
}

// parseSince accepts Go's own duration syntax (24h, 90m) plus a trailing
// "d" for days, since that's the unit people actually reach for.
func parseSince(value string) (time.Duration, error) {
	if strings.HasSuffix(value, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(value, "d"))
		if err != nil {
			return 0, err
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	return time.ParseDuration(value)
}

func splitPair(pair string) (name, path string, err error) {
	parts := strings.SplitN(pair, "=", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected name=path")
	}
	return parts[0], parts[1], nil
}

// repeatedFlag collects every occurrence of a repeatable flag.
type repeatedFlag []string

func (r *repeatedFlag) String() string { return strings.Join(*r, ",") }
func (r *repeatedFlag) Set(value string) error {
	*r = append(*r, value)
	return nil
}
