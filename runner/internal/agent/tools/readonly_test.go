package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFileT(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadFileReadsWithinWorkdir(t *testing.T) {
	dir := t.TempDir()
	writeFileT(t, dir, "a.txt", "hello\nworld\n")
	tool := ReadFile(dir)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"a.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("out = %q", out)
	}
	if tool.Mutating() {
		t.Fatal("read_file must not be mutating")
	}
}

func TestReadFileRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadFile(dir).Execute(context.Background(), json.RawMessage(`{"path":"../etc/passwd"}`))
	if err == nil {
		t.Fatal("want error for path escape")
	}
}

func TestGlobFindsByPattern(t *testing.T) {
	dir := t.TempDir()
	writeFileT(t, dir, "x/one.go", "package x")
	writeFileT(t, dir, "x/two.txt", "t")
	out, err := Glob(dir).Execute(context.Background(), json.RawMessage(`{"pattern":"x/*.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "one.go") || strings.Contains(out, "two.txt") {
		t.Fatalf("out = %q", out)
	}
}

func TestGrepFindsMatchesWithLineNumbers(t *testing.T) {
	dir := t.TempDir()
	writeFileT(t, dir, "f.txt", "alpha\nbeta\ngamma beta\n")
	out, err := Grep(dir).Execute(context.Background(), json.RawMessage(`{"pattern":"beta"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "f.txt:2:") || !strings.Contains(out, "f.txt:3:") {
		t.Fatalf("out = %q", out)
	}
}

func TestOutputCap(t *testing.T) {
	dir := t.TempDir()
	writeFileT(t, dir, "big.txt", strings.Repeat("x", 40000))
	out, err := ReadFile(dir).Execute(context.Background(), json.RawMessage(`{"path":"big.txt"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > 16384+len("\n[truncated]") {
		t.Fatalf("output not capped: %d bytes", len(out))
	}
	if !strings.HasSuffix(out, "[truncated]") {
		t.Fatal("missing truncation marker")
	}
}
