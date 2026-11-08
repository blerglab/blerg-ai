package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository fixture")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
}
func copyFixtureFile(t *testing.T, root, fixture, name string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(fixture, name)
	if err = os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(target, data, 0700); err != nil {
		t.Fatal(err)
	}
}
func TestRepositoryLintRejectsUnformattedVerificationAndVisitsModule(t *testing.T) {
	root := repositoryRoot(t)
	fixture := t.TempDir()
	for _, name := range []string{"Makefile", "scripts/check_lint_pin.sh", ".github/workflows/ci.yml"} {
		copyFixtureFile(t, root, fixture, name)
	}
	modules := []string{"contracts", "core", "runner", "board", "scripts/verification"}
	for _, module := range modules {
		if err := os.MkdirAll(filepath.Join(fixture, module), 0700); err != nil {
			t.Fatal(err)
		}
	}
	probe := filepath.Join(fixture, "scripts/verification/format_probe.go")
	if err := os.WriteFile(probe, []byte("package fixture\nfunc probe( ){}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(fixture, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	linter := filepath.Join(bin, "golangci-lint")
	if err = os.Symlink(self, linter); err != nil {
		t.Fatal(err)
	}
	visits := filepath.Join(fixture, "visits")
	t.Setenv("BLERG_TIMING_TEST_HELPER", "1")
	t.Setenv("BLERG_LINT_TEST_VISITS", visits)
	t.Setenv("BLERG_TOOL_WORKERS", "2")
	goTool := fixtureGoTool(t)
	t.Setenv("PATH", filepath.Dir(goTool)+string(os.PathListSeparator)+os.Getenv("PATH"))
	lint := func() ([]byte, error) {
		cmd := exec.CommandContext(t.Context(), "make", "lint-go", "GOLANGCI_LINT="+linter)
		cmd.Dir = fixture
		return cmd.CombinedOutput()
	}
	output, err := lint()
	if err == nil {
		t.Fatal("lint accepted an unformatted verification source file")
	}
	if !strings.Contains(string(output), "format_probe.go") {
		t.Fatalf("did not report the format error: %s", output)
	}
	formatter := exec.CommandContext(t.Context(), filepath.Join(filepath.Dir(goTool), "gofmt"), "-w", probe)
	if output, err = formatter.CombinedOutput(); err != nil {
		t.Fatalf("format fixture: %v %s", err, output)
	}
	output, err = lint()
	if err != nil {
		t.Fatalf("formatted fixture rejected: %v %s", err, output)
	}
	data, err := os.ReadFile(visits)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, dir := range strings.Fields(string(data)) {
		relative, err := filepath.Rel(fixture, dir)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, filepath.ToSlash(relative))
	}
	sort.Strings(got)
	sort.Strings(modules)
	if !reflect.DeepEqual(got, modules) {
		t.Fatalf("linted modules %v, want %v", got, modules)
	}
}
func TestVerificationBelongsToRepositoryWorkspace(t *testing.T) {
	goTool := fixtureGoTool(t)
	cmd := exec.CommandContext(context.Background(), goTool, "work", "edit", "-json", filepath.Join(repositoryRoot(t), "go.work"))
	data, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	var work struct{ Use []struct{ DiskPath string } }
	if err = json.Unmarshal(data, &work); err != nil {
		t.Fatal(err)
	}
	for _, entry := range work.Use {
		if filepath.ToSlash(filepath.Clean(entry.DiskPath)) == "scripts/verification" {
			return
		}
	}
	t.Fatal("verification is outside go.work")
}
