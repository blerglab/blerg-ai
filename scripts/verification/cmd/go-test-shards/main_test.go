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
	"regexp"
	"sort"
	"strings"
	"testing"
)

func discovery(output ...string) discoverFunc {
	i := 0
	return func([]string) (string, error) { s := output[i]; i++; return s, nil }
}
func TestShardsCoverEveryNameOnceAndRespectBudget(t *testing.T) {
	names := []string{"TestAlpha", "TestBeta", "TestGamma", "Example", "FuzzSeeds"}
	for _, workers := range []int{2, 3, 4, 6} {
		ps, err := plans([]string{"go", "test", "-json", "-count=3", "-race", "-p=6", "-parallel=4", "./..."}, workers, discovery("fixture/internal/server\nfixture/other", strings.Join(names, "\n")))
		if err != nil {
			t.Fatal(err)
		}
		hits := map[string]int{}
		budget := 0
		shards := 0
		for _, p := range ps {
			selector := ""
			for _, a := range p.Command {
				if strings.HasPrefix(a, "-run=") {
					selector = strings.TrimPrefix(a, "-run=")
				}
			}
			if selector == "" {
				continue
			}
			shards++
			budget += p.Workers
			for _, flag := range []string{"-count=3", "-race"} {
				if !contains(p.Command, flag) {
					t.Fatalf("lost %s: %v", flag, p)
				}
			}
			re := regexp.MustCompile(selector)
			for _, name := range names {
				if re.MatchString(name) {
					hits[name]++
				}
			}
		}
		if shards != min(workers, 4) || budget > workers {
			t.Fatalf("invalid shards/budget: %v", ps)
		}
		for _, n := range names {
			if hits[n] != 1 {
				t.Fatalf("%s selected %d times", n, hits[n])
			}
		}
	}
}
func contains(args []string, value string) bool {
	for _, a := range args {
		if a == value {
			return true
		}
	}
	return false
}
func TestDiscoveryFailsClosed(t *testing.T) {
	command := []string{"go", "test", "-json", "./..."}
	for _, d := range []discoverFunc{func([]string) (string, error) { return "", errors.New("fixture failure") }, discovery("fixture/internal/server", "ok fixture/internal/server")} {
		if _, err := plans(command, 2, d); err == nil {
			t.Fatal("discovery failure accepted")
		}
	}
}
func TestFocusedSelectorAndSingleWorkerRetainCommand(t *testing.T) {
	for _, c := range [][]string{{"go", "test", "-run=^TestA$", "./internal/server"}, {"go", "test", "-list=Test", "./..."}, {"go", "test", "-bench=Bench", "./..."}, {"go", "test", "-fuzz=Fuzz", "./..."}} {
		ps, err := plans(c, 2, func([]string) (string, error) { t.Fatal("focused command discovered packages"); return "", nil })
		if err != nil || !reflect.DeepEqual(ps, []plan{{c, 2}}) {
			t.Fatalf("changed focused command: %v %v", ps, err)
		}
	}
	c := []string{"go", "test", "./..."}
	ps, err := plans(c, 1, nil)
	if err != nil || !reflect.DeepEqual(ps, []plan{{c, 1}}) {
		t.Fatal(ps, err)
	}
}

func TestRealGoRetainsSubtestsExamplesAndFuzzSeeds(t *testing.T) {
	goTool := fixtureGoTool(t)
	var err error
	root := t.TempDir()
	server := filepath.Join(root, "internal/server")
	if err = os.MkdirAll(server, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "go.mod"), []byte("module fixture\n\ngo 1.26\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := `package server
import ("testing"; "fmt")
func TestAlpha(t *testing.T) { t.Run("child", func(t *testing.T) {}) }
func TestBeta(t *testing.T) {}
func Example() { fmt.Println("fixture")
// Output: fixture
}
func FuzzSeeds(f *testing.F) { f.Add("one"); f.Add("two"); f.Fuzz(func(t *testing.T, s string) {}) }
`
	if err = os.WriteFile(filepath.Join(server, "server_test.go"), []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Setenv("GOWORK", "off")
	t.Setenv("GOMAXPROCS", "2")
	t.Setenv("GOFLAGS", "-p=2")
	t.Setenv("BLERG_TOOL_WORKERS", "2")
	args := []string{goTool, "test", "-json", "-p=2", "-timeout=1m", "./..."}
	cmd := exec.Command(args[0], args[1:]...)
	before, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("baseline: %v %s", err, before)
	}
	var after, diagnostics bytes.Buffer
	if code := run(args, &after, &diagnostics); code != 0 {
		t.Fatalf("shards: %d %s %s", code, after.String(), diagnostics.String())
	}
	get := func(raw string) []string {
		events := []string{}
		for _, line := range strings.Split(raw, "\n") {
			var e struct{ Package, Test, Action string }
			if json.Unmarshal([]byte(line), &e) == nil && e.Test != "" && (e.Action == "pass" || e.Action == "fail" || e.Action == "skip") {
				events = append(events, e.Package+"|"+e.Test+"|"+e.Action)
			}
		}
		sort.Strings(events)
		return events
	}
	want, got := get(string(before)), get(after.String())
	if len(got) != 7 || !reflect.DeepEqual(want, got) {
		t.Fatalf("receipts differ: %v vs %v", want, got)
	}
}
func TestMain(m *testing.M) {
	if os.Getenv("BLERG_SHARD_TEST_HELPER") == "1" {
		os.Exit(fakeGo(os.Args[1:]))
	}
	os.Exit(m.Run())
}
func fakeGo(args []string) int {
	names := []string{"TestAlpha", "TestBeta", "TestGamma", "ExampleDelta", "FuzzEpsilon"}
	if len(args) > 0 && args[0] == "list" {
		fmt.Println("fixture/internal/server\nfixture/other")
		return 0
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-list=") {
			fmt.Println(strings.Join(names, "\n"))
			return 0
		}
	}
	file, err := os.OpenFile(os.Getenv("BLERG_SHARD_TEST_RECEIPTS"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return 3
	}
	data, _ := json.Marshal(map[string]any{"args": args, "maxprocs": os.Getenv("GOMAXPROCS")})
	_, err = file.Write(append(data, '\n'))
	_ = file.Close()
	if err != nil {
		return 3
	}
	selector := "."
	for _, arg := range args {
		if strings.HasPrefix(arg, "-run=") {
			selector = strings.TrimPrefix(arg, "-run=")
		}
	}
	re := regexp.MustCompile(selector)
	if contains(args, "fixture/internal/server") || contains(args, "./...") {
		for _, name := range names {
			if re.MatchString(name) {
				_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"Action": "pass", "Package": "fixture/internal/server", "Test": name})
			}
		}
	}
	if contains(args, "fixture/other") || contains(args, "./...") {
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"Action": "pass", "Package": "fixture/other", "Test": "TestOther"})
	}
	if os.Getenv("BLERG_SHARD_TEST_FAIL") == "1" && re.MatchString("TestBeta") {
		return 7
	}
	return 0
}
func TestProcessReceiptsBudgetAndFailureDrain(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			receipt := filepath.Join(t.TempDir(), "receipts")
			t.Setenv("BLERG_SHARD_TEST_HELPER", "1")
			t.Setenv("BLERG_SHARD_TEST_RECEIPTS", receipt)
			t.Setenv("BLERG_TOOL_WORKERS", "2")
			t.Setenv("BLERG_SHARD_TEST_FAIL", map[bool]string{false: "0", true: "1"}[fail])
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			var output, diagnostics bytes.Buffer
			code := run([]string{binary, "test", "-json", "-p=2", "-parallel=4", "-timeout=25m", "./..."}, &output, &diagnostics)
			if (code != 0) != fail {
				t.Fatalf("exit=%d diagnostics=%s", code, diagnostics.String())
			}
			if fail && !strings.Contains(diagnostics.String(), "exit 7") {
				t.Fatal(diagnostics.String())
			}
			names := []string{}
			for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
				var e struct{ Test string }
				if err = json.Unmarshal([]byte(line), &e); err != nil {
					t.Fatal(err)
				}
				names = append(names, e.Test)
			}
			sort.Strings(names)
			want := []string{"ExampleDelta", "FuzzEpsilon", "TestAlpha", "TestBeta", "TestGamma", "TestOther"}
			if !reflect.DeepEqual(names, want) {
				t.Fatal(names)
			}
			data, err := os.ReadFile(receipt)
			if err != nil {
				t.Fatal(err)
			}
			shards := 0
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				var call struct {
					Args     []string
					Maxprocs string
				}
				if err = json.Unmarshal([]byte(line), &call); err != nil {
					t.Fatal(err)
				}
				if contains(call.Args, "fixture/internal/server") {
					shards++
					if call.Maxprocs != "1" || !contains(call.Args, "-p=1") || contains(call.Args, "-count=1") {
						t.Fatalf("budget/cache changed: %+v", call)
					}
				}
			}
			if shards != 2 {
				t.Fatal(shards)
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
