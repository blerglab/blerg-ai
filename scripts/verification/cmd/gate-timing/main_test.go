package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestComparisonRejectsMissingRepetitionsAndNewSkips(t *testing.T) {
	before := receipts{"suite|package|TestOne": {"pass"}, "suite|package|TestTwo": {"skip"}}
	for _, tc := range []struct {
		name      string
		after     receipts
		preserved bool
	}{
		{"same", before, true}, {"missing", receipts{}, false},
		{"repeat", receipts{"suite|package|TestOne": {"pass", "pass"}, "suite|package|TestTwo": {"skip"}}, false},
		{"new skip", receipts{"suite|package|TestOne": {"skip"}, "suite|package|TestTwo": {"skip"}}, false},
		{"added", receipts{"suite|package|TestOne": {"pass"}, "suite|package|TestTwo": {"skip"}, "suite|package|TestNew": {"pass"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := compareReceipts(before, tc.after); got.Preserved != tc.preserved {
				t.Fatalf("comparison=%+v", got)
			}
		})
	}
	if compareReceipts(receipts{}, receipts{}).Preserved {
		t.Fatal("empty proof accepted")
	}
}
func TestGoReceiptsRetainSubtestsAndRepetitions(t *testing.T) {
	output := `{"Action":"pass","Package":"fixture","Test":"TestOne"}
{"Action":"pass","Package":"fixture","Test":"TestOne/child"}
{"Action":"pass","Package":"fixture","Test":"TestOne"}
ok fixture`
	want := receipts{"go|fixture|TestOne": {"pass", "pass"}, "go|fixture|TestOne/child": {"pass"}}
	if got := goReceipts(output, "go"); !reflect.DeepEqual(got, want) {
		t.Fatalf("receipts=%v", got)
	}
}

func TestVitestReceiptsNormalizeWorktreeAndKeepSkips(t *testing.T) {
	root := t.TempDir()
	logs := filepath.Join(root, "logs")
	if err := os.Mkdir(logs, 0700); err != nil {
		t.Fatal(err)
	}
	summary := gateSummary{Suites: []map[string]any{{"suite": "web-tests", "log": "web-tests.log"}}}
	if err := writeJSON(filepath.Join(logs, "summary.json"), summary); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logs, "web-tests.log"), []byte("ordinary output"), 0600); err != nil {
		t.Fatal(err)
	}
	report := map[string]any{"testResults": []any{map[string]any{"name": filepath.Join(root, "web/test.ts"), "assertionResults": []any{map[string]string{"fullName": "case one", "status": "passed"}, map[string]string{"fullName": "case two", "status": "pending"}}}}}
	if err := writeJSON(filepath.Join(logs, "web-tests.vitest.json"), report); err != nil {
		t.Fatal(err)
	}
	_, got, err := gateReceipts(logs, root)
	if err != nil {
		t.Fatal(err)
	}
	want := receipts{"web-tests|web/test.ts|case one": {"pass"}, "web-tests|web/test.ts|case two": {"skip"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatal(got)
	}
}
func TestCLIKeepsTrailingOptionsAndRejectsInvalidInput(t *testing.T) {
	if _, err := parseOptions([]string{"--help"}); !errors.Is(err, errHelp) {
		t.Fatal(err)
	}
	got, err := parseOptions([]string{"BASE", "HEAD", "--output", "new-directory", "--mode", "gate"})
	if err != nil || got != (options{"BASE", "HEAD", "new-directory", "gate"}) {
		t.Fatal(got, err)
	}
	for _, args := range [][]string{{}, {"BASE", "HEAD"}, {"BASE", "HEAD", "--output", "x", "--mode", "invalid"}, {"BASE", "HEAD", "--output"}, {"BASE", "HEAD", "--unknown"}} {
		if _, err := parseOptions(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
func TestLegacyProxyOnlyAddsJSONReporter(t *testing.T) {
	for _, tc := range []struct{ args, want []string }{
		{[]string{"test", "-p", "1", "./..."}, []string{"test", "-json", "-p", "1", "./..."}},
		{[]string{"test", "-json", "./..."}, []string{"test", "-json", "./..."}},
		{[]string{"vet", "./..."}, []string{"vet", "./..."}},
	} {
		if got := proxyArgs(tc.args); !reflect.DeepEqual(got, tc.want) {
			t.Fatal(got)
		}
	}
}
func TestMissingSuitesFailClosedAndRemainSorted(t *testing.T) {
	got := missingSuites([]map[string]any{{"suite": "z"}, {"suite": "a"}, {"suite": "keep"}}, []map[string]any{{"suite": "keep"}})
	if !reflect.DeepEqual(got, []string{"a", "z"}) {
		t.Fatal(got)
	}
}

func TestMain(m *testing.M) {
	if os.Getenv("BLERG_TIMING_TEST_HELPER") == "1" {
		os.Exit(timingHelper())
	}
	os.Exit(m.Run())
}
func timingHelper() int {
	args := os.Args[1:]
	switch filepath.Base(os.Args[0]) {
	case "golangci-lint":
		if len(args) > 0 && args[0] == "version" {
			fmt.Println("golangci-lint has version 2.13.1")
			return 0
		}
		cwd, err := os.Getwd()
		if err != nil {
			return 3
		}
		file, err := os.OpenFile(os.Getenv("BLERG_LINT_TEST_VISITS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return 3
		}
		_, err = fmt.Fprintln(file, cwd)
		_ = file.Close()
		if err != nil {
			return 3
		}
		return 0

	case "git":
		if len(args) > 1 && args[0] == "rev-parse" {
			if args[1] == "--show-toplevel" {
				fmt.Println(os.Getenv("BLERG_TIMING_TEST_ROOT"))
			} else {
				fmt.Println(strings.TrimSuffix(args[len(args)-1], "^{commit}") + "-sha")
			}
			return 0
		}
		if len(args) == 5 && args[0] == "worktree" {
			if err := os.MkdirAll(filepath.Join(args[3], "core/web"), 0700); err != nil {
				return 3
			}
			return 0
		}
	case "npm":
		return 0
	case "python3":
		if len(args) != 3 || args[0] != "scripts/pre_merge.py" || args[1] != "--logs" {
			return 3
		}
		logs := args[2]
		if err := os.MkdirAll(logs, 0700); err != nil {
			return 3
		}
		if err := os.WriteFile(filepath.Join(logs, "go.log"), []byte(timingFixtureEvents), 0600); err != nil {
			return 3
		}
		if err := writeJSON(filepath.Join(logs, "summary.json"), gateSummary{Complete: true, FullMatrix: true, Suites: []map[string]any{{"suite": "runner-go", "log": "go.log", "seconds": 1, "outcome": "pass", "tests": map[string]any{"counts": map[string]int{"pass": 2, "fail": 0, "skip": 0}}}}}); err != nil {
			return 3
		}
		if os.Getenv("BLERG_TIMING_TEST_FAIL") == "1" {
			return 1
		}
		return 0
	case "make":
		cmd := exec.Command("go", "test", "-p", "1", "./...")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return 3
		}
		return 0
	case "go":
		if err := runProxy(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		return 0
	case "real-go":
		found := false
		for _, arg := range args {
			found = found || arg == "-json"
		}
		if !found {
			return 3
		}
		fmt.Print(timingFixtureEvents)
		return 0
	}
	fmt.Fprintln(os.Stderr, "unexpected fake command")
	return 3
}

const timingFixtureEvents = `{"Action":"pass","Package":"fixture","Test":"TestOne"}
{"Action":"pass","Package":"fixture","Test":"TestTwo/child"}
`

func TestTimingLifecycleRetainsBothModesAndFailedRuns(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"git", "npm", "python3", "make", "real-go"} {
				if err = os.Symlink(self, filepath.Join(bin, name)); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("BLERG_TIMING_TEST_HELPER", "1")
			t.Setenv("BLERG_TIMING_TEST_ROOT", root)
			t.Setenv("BLERG_TIMING_TEST_FAIL", map[bool]string{false: "0", true: "1"}[fail])
			t.Setenv("PATH", bin)
			t.Setenv("GO", filepath.Join(bin, "real-go"))
			t.Setenv("DATABASE_URL", "fixture")
			t.Setenv("TEST_DATABASE_URL", "fixture")
			t.Setenv("BLERG_TOOL_WORKERS", "2")
			t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
			output := filepath.Join(root, "evidence")
			var console bytes.Buffer
			code, err := runTiming(options{Base: "base", Head: "head", Output: output, Mode: "both"}, &console)
			if err != nil || (code != 0) != fail {
				t.Fatalf("exit %d: %v %s", code, err, console.String())
			}
			data, err := os.ReadFile(filepath.Join(output, "results.json"))
			if err != nil {
				t.Fatal(err)
			}
			var report struct {
				Runs []struct {
					Revision, Mode string
					ExitCode       int `json:"exit_code"`
					NamedEvents    int `json:"named_events"`
				}
				Comparisons map[string]comparison
			}
			if err = json.Unmarshal(data, &report); err != nil {
				t.Fatal(err)
			}
			if len(report.Runs) != 4 || len(report.Comparisons) != 2 {
				t.Fatalf("incomplete report: %s", data)
			}
			for _, run := range report.Runs {
				if run.NamedEvents != 2 {
					t.Fatalf("lost receipts: %s", data)
				}
			}
			for mode, c := range report.Comparisons {
				if !c.Preserved || c.CommandsPassed == (fail && mode == "gate") {
					t.Fatalf("wrong comparison: %s", data)
				}
			}
			if _, err = os.Stat(filepath.Join(output, "before-gate-receipts.json")); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func fixtureGoTool(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(home, "go/bin/go")
	if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
		return candidate
	}
	tool, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	return tool
}
func TestFixtureGoToolUsesSetupGoPATHWithoutHomeInstall(t *testing.T) {
	original := fixtureGoTool(t)
	bin := t.TempDir()
	tool := filepath.Join(bin, "go")
	if err := os.Symlink(original, tool); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", bin)
	if got := fixtureGoTool(t); got != tool {
		t.Fatalf("resolved %s, want PATH Go %s", got, tool)
	}
}
