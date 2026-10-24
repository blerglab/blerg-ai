package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
