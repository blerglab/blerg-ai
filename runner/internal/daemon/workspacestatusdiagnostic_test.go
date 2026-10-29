package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/hostgit"
)

func TestWorkspaceStatusTimingDiagnosticsStayPrivate(t *testing.T) {
	e, root, req := explorerFixture(t)
	initChildRepo(t, filepath.Join(root, "member"))
	e.pinMembers()
	var mu sync.Mutex
	stages := map[string]bool{}
	ctx := context.WithValue(t.Context(), workspaceStatusTimingKey{}, workspaceStatusTiming(func(stage string, elapsed, remaining time.Duration, err error) {
		mu.Lock()
		defer mu.Unlock()
		stages[stage] = true
		t.Logf("status timing: stage=%q elapsed=%v remaining=%v err=%v", stage, elapsed, remaining, err)
	}))
	req.Operation, req.Path, req.MemberID = "status", "member", "one"
	res := e.Explore(ctx, req)
	if !res.OK {
		t.Fatalf("timed status failed: %+v", res)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, stage := range []string{"pin member and metadata", "metadata guard", "private snapshot", "command prepared", "start", "wait begin", "wait selected", "Git status", "parse and delivery validation", "status total"} {
		if !stages[stage] {
			t.Errorf("missing private timing stage %q", stage)
		}
	}
	raw, err := json.Marshal(res)
	if err != nil || strings.Contains(string(raw), "elapsed") || strings.Contains(string(raw), "remaining") || strings.Contains(string(raw), "command prepared") {
		t.Fatalf("private timings exposed on wire: %s %v", raw, err)
	}
}

func TestWorkspaceStatusFailureDiagnostics(t *testing.T) {
	for _, stage := range []string{"private snapshot", "Git status"} {
		t.Run(stage, func(t *testing.T) {
			e, root, req := explorerFixture(t)
			initChildRepo(t, filepath.Join(root, "member"))
			e.pinMembers()
			if stage == "private snapshot" {
				t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
			} else {
				bin := t.TempDir()
				if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nprintf 'private diagnostic marker' >&2\nexit 23\n"), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+":/usr/bin:/bin")
				t.Cleanup(hostgit.SetBinaryForTest(filepath.Join(bin, "git")))
			}
			var gotStage, gotStderr string
			var gotErr error
			e.statusDiagnostic = func(stage string, err error, stderr string) {
				gotStage, gotErr, gotStderr = stage, err, stderr
				t.Logf("workspace status unavailable: stage=%q err=%v stderr=%q", stage, err, stderr)
			}
			req.Operation, req.Path, req.MemberID = "status", "member", "one"
			res := e.Explore(context.Background(), req)
			if res.OK || res.ErrorCode != "unavailable" || gotStage != stage || gotErr == nil {
				t.Fatalf("missing private diagnostic: stage=%q err=%v result=%+v", gotStage, gotErr, res)
			}
			if stage == "Git status" && (!strings.Contains(gotErr.Error(), "23") || gotStderr != "private diagnostic marker") {
				t.Fatalf("Git error/stderr swallowed: err=%v stderr=%q", gotErr, gotStderr)
			}
			wantReason := "snapshot_refused"
			if stage == "Git status" {
				wantReason = "git_failed"
			}
			encoded, _ := json.Marshal(res)
			var wire map[string]any
			_ = json.Unmarshal(encoded, &wire)
			if wire["status_reason"] != wantReason {
				t.Fatalf("missing safe reason %q: %s", wantReason, encoded)
			}
			raw, err := json.Marshal(res)
			if err != nil || strings.Contains(string(raw), "private diagnostic") || strings.Contains(string(raw), "stage") {
				t.Fatalf("private diagnostic exposed on wire: %s %v", raw, err)
			}
		})
	}
}

func TestWorkspaceExplorerListingFailureDiagnostics(t *testing.T) {
	for _, reason := range []string{"missing directory", "child open", "cancelled"} {
		t.Run(reason, func(t *testing.T) {
			e, root, req := explorerFixture(t)
			ctx := t.Context()
			wantStage := "list directory"
			switch reason {
			case "missing directory":
				req.Path = "missing-directory"
			case "child open":
				if err := os.Symlink("missing-target", filepath.Join(root, "visible-link")); err != nil {
					t.Fatal(err)
				}
				wantStage = "list child open"
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				wantStage = "request deadline/cancellation"
			}
			var gotStage string
			var gotErr error
			e.statusDiagnostic = func(stage string, err error, _ string) {
				gotStage, gotErr = stage, err
				t.Logf("workspace explorer diagnostic: stage=%q err=%v", stage, err)
			}
			res := e.Explore(ctx, req)
			if gotStage != wantStage || gotErr == nil || res.OK != (reason == "child open") {
				t.Fatalf("missing listing diagnostic: stage=%q err=%v result=%+v", gotStage, gotErr, res)
			}
			raw, err := json.Marshal(res)
			if err != nil || strings.Contains(string(raw), "stage") || strings.Contains(string(raw), gotErr.Error()) {
				t.Fatalf("private diagnostic exposed on wire: %s %v", raw, err)
			}
		})
	}
}

func TestWorkspaceStatusMetadataReasons(t *testing.T) {
	for _, reason := range []string{"metadata_pointer", "metadata_limit"} {
		t.Run(reason, func(t *testing.T) {
			e, root, req := explorerFixture(t)
			member := filepath.Join(root, "member")
			initChildRepo(t, member)
			e.pinMembers()
			if reason == "metadata_pointer" {
				if err := os.WriteFile(filepath.Join(member, ".git", "objects", "info", "alternates"), []byte("/untrusted/objects\n"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				f, err := os.Create(filepath.Join(member, ".git", "oversized"))
				if err != nil {
					t.Fatal(err)
				}
				if err := f.Truncate(257 << 20); err != nil {
					t.Fatal(err)
				}
				_ = f.Close()
			}
			req.Operation, req.Path, req.MemberID = "status", "member", "one"
			res := e.Explore(t.Context(), req)
			if res.OK || res.StatusReason != reason {
				t.Fatalf("want %s: %+v", reason, res)
			}
		})
	}
}
