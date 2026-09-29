package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWriteFileCreatesDirsAndWrites(t *testing.T) {
	dir := t.TempDir()
	tool := WriteFile(dir)
	if !tool.Mutating() {
		t.Fatal("write_file must be mutating")
	}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"sub/dir/f.txt","content":"abc"}`))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "sub/dir/f.txt"))
	if err != nil || string(data) != "abc" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}

func TestEditFileRequiresUniqueMatch(t *testing.T) {
	dir := t.TempDir()
	writeFileT(t, dir, "f.txt", "aaa bbb aaa")
	tool := EditFile(dir)

	// ambiguous: two matches
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"f.txt","old_string":"aaa","new_string":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "2 matches") {
		t.Fatalf("want ambiguity error, got %v", err)
	}
	// no match
	_, err = tool.Execute(context.Background(), json.RawMessage(`{"path":"f.txt","old_string":"zzz","new_string":"x"}`))
	if err == nil {
		t.Fatal("want no-match error")
	}
	// unique
	_, err = tool.Execute(context.Background(), json.RawMessage(`{"path":"f.txt","old_string":"bbb","new_string":"BBB"}`))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if string(data) != "aaa BBB aaa" {
		t.Fatalf("data=%q", data)
	}
}

func TestBashRunsInWorkdirAndReportsExit(t *testing.T) {
	dir := t.TempDir()
	tool := Bash(dir)
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"pwd && echo hi && exit 3"}`))
	if err != nil {
		t.Fatal(err) // non-zero exit is NOT a Go error; it's in the output
	}
	if !strings.Contains(out, "hi") || !strings.Contains(out, "[exit status 3]") {
		t.Fatalf("out=%q", out)
	}
	// pwd may resolve symlinks (macOS /tmp); compare resolved paths.
	resolved, _ := filepath.EvalSymlinks(dir)
	if !strings.Contains(out, dir) && !strings.Contains(out, resolved) {
		t.Fatalf("out=%q does not contain workdir %q", out, dir)
	}
}

func TestBashTimeoutKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	tool := Bash(dir)
	start := time.Now()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"command":"sleep 30","timeout_seconds":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout not enforced: took %s", elapsed)
	}
	if !strings.Contains(out, "timed out") {
		t.Fatalf("out=%q", out)
	}
}

func TestBashCancelKills(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	_, _ = Bash(dir).Execute(ctx, json.RawMessage(`{"command":"sleep 30"}`))
	if time.Since(start) > 5*time.Second {
		t.Fatal("cancel did not kill bash")
	}
}
