package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testdataDir = "../../internal/testtiming/testdata"

func TestRunOnRealSummaryFixturesProducesAReport(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{
		"--summary", filepath.Join(testdataDir, "summary-c4489560.json"),
		"--summary", filepath.Join(testdataDir, "summary-d42b8f44.json"),
		"--summary", filepath.Join(testdataDir, "summary-ef53c85d.json"),
		"--repo", "blerg",
	}
	if err := run(args, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v, stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "# Test timing report: blerg") {
		t.Fatalf("missing report title:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "```vega-lite") {
		t.Fatalf("missing chart:\n%s", stdout.String())
	}
}

func TestRunOnRealHistoryFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{"--history", filepath.Join(testdataDir, "real-history.jsonl")}
	if err := run(args, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v, stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "5 gate run(s) in window") {
		t.Fatalf("expected 5 gate runs, got:\n%s", stdout.String())
	}
}

func TestRunWithNoInputFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(nil, &stdout, &stderr); err == nil {
		t.Fatal("expected an error when no input is given")
	}
}

func TestRunRejectsMalformedSincePairs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{"--history", filepath.Join(testdataDir, "real-history.jsonl"), "--since", "not-a-duration"}
	if err := run(args, &stdout, &stderr); err == nil {
		t.Fatal("expected an error for an unparseable --since value")
	}
}

func TestRunAcceptsDaySuffixSince(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{"--history", filepath.Join(testdataDir, "real-history.jsonl"), "--since", "3650d"}
	if err := run(args, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v, stderr=%s", err, stderr.String())
	}
}

func TestRunOnGoTestJSONOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "go-test.json")
	if err := os.WriteFile(path, []byte(`{"Action":"pass","Test":"TestFoo","Package":"pkg/a","Elapsed":1.5}`+"\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	var stdout, stderr bytes.Buffer
	args := []string{"--go-test-json", "unit=" + path}
	if err := run(args, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v, stderr=%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "TestFoo") {
		t.Fatalf("expected the parsed test in the report:\n%s", stdout.String())
	}
}

func TestRunRejectsMissingEqualsInPair(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{"--go-test-json", "no-equals-here"}
	if err := run(args, &stdout, &stderr); err == nil {
		t.Fatal("expected an error for a name=path pair missing '='")
	}
}

func TestGoJSONReportPackageTotals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "go.json")
	fixture := `{"Action":"start","Package":"pkg/a"}
{"Action":"pass","Package":"pkg/a","Test":"TestRoot/child","Elapsed":3}
{"Action":"pass","Package":"pkg/a","Test":"TestRoot","Elapsed":5}
{"Action":"pass","Package":"pkg/a","Elapsed":8}
`
	if err := os.WriteFile(path, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, diagnostics bytes.Buffer
	if err := run([]string{"--go-test-json", "fixture=" + path}, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	// The rolling-median column depends on any gate history present, so check only the
	// package total, the single root test count and seconds per test.
	if !strings.Contains(out.String(), "| fixture | 8.0 |") || !strings.Contains(out.String(), "| 1 | 8.000 |") {
		t.Fatalf("report lost package duration or root count:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "TestRoot/child") {
		t.Fatal("report lost nested test detail")
	}
}
