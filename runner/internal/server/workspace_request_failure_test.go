package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

func TestWorkspaceExplorerPendingPerConnection(t *testing.T) {
	h := NewHub()
	dc := &DaemonConn{ID: "daemon", send: make(chan []byte, 8)}
	h.Register(dc)
	h.SetSessionOwner("session", dc.ID)
	ref := protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}
	dc.AuthorizeWorkspaceRuntime("session", ref)
	h.explorerReqs.pending = map[string]workspaceExplorerWaiter{"a": {dc: &DaemonConn{ID: "other"}}, "b": {dc: &DaemonConn{ID: "other"}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := h.requestWorkspaceExplorer(ctx, dc, protocol.WorkspaceExplorerRequest{SessionID: "session", WorkspaceRuntimeRef: ref})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("unrelated connection blocked request: %v", err)
	}
}

func TestWorkspaceRequestFailureMappingAndPrivacy(t *testing.T) {
	var logs bytes.Buffer
	previous := failurelog.Default
	failurelog.Default = failurelog.New(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { failurelog.Default = previous })
	for _, tc := range []struct {
		err         error
		code, cause string
	}{
		{errWorkspaceBusy, workspaceCodeBusy, "pending_cap"},
		{errWorkspaceRuntimeMismatch, workspaceCodeUnavailable, "runtime_mismatch"},
		{errWorkspaceDaemonGone, workspaceCodeNotRunning, "daemon_gone"},
		{errWorkspaceSendFailed, workspaceCodeUnavailable, "send_failed"},
		{context.DeadlineExceeded, workspaceCodeDaemonTimeout, "deadline"},
		{context.Canceled, workspaceCodeUnavailable, "canceled"},
		{errors.New("canary-secret/private/path"), workspaceCodeUnavailable, "unavailable"},
	} {
		t.Run(tc.cause, func(t *testing.T) {
			rec := httptest.NewRecorder()
			workspaceRequestProblem(rec, "session", "daemon", "read", fmt.Errorf("canary-secret: %w", tc.err))
			if rec.Code != 409 || workspaceProblemCode(t, rec.Body.Bytes()) != tc.code {
				t.Fatalf("response: %d %s", rec.Code, rec.Body)
			}
			if !strings.Contains(logs.String(), `"cause":"`+tc.cause+`"`) {
				t.Fatalf("missing cause: %s", logs.String())
			}
			if strings.Contains(rec.Body.String()+logs.String(), "canary-secret") {
				t.Fatal("private error disclosed")
			}
		})
	}
	for _, field := range []string{`"session_id":"session"`, `"daemon_id":"daemon"`, `"operation":"read"`, `"suppressed":0`} {
		if !strings.Contains(logs.String(), field) {
			t.Fatalf("missing %s", field)
		}
	}
	before := logs.Len()
	logWorkspaceRequestFailure("session", "daemon", "read", errWorkspaceBusy)
	if logs.Len() != before {
		t.Fatal("repeated diagnostics not bounded")
	}
}

func TestWorkspaceRequestCauses(t *testing.T) {
	for _, kind := range []string{"explorer", "file"} {
		for _, cause := range []string{"mismatch", "cap", "gone", "send", "deadline", "disconnect"} {
			t.Run(kind+"/"+cause, func(t *testing.T) {
				h := NewHub()
				dc := &DaemonConn{ID: "daemon", send: make(chan []byte, 8)}
				h.Register(dc)
				h.SetSessionOwner("session", dc.ID)
				ref := protocol.WorkspaceRuntimeRef{LaunchGeneration: "launch", Generation: 1}
				dc.AuthorizeWorkspaceRuntime("session", ref)
				req := protocol.WorkspaceExplorerRequest{SessionID: "session", Operation: "list", WorkspaceRuntimeRef: ref}
				ctx := t.Context()
				want := errWorkspaceRuntimeMismatch
				switch cause {
				case "mismatch":
					req.Generation++
				case "cap":
					want = errWorkspaceBusy
					h.explorerReqs.pending = map[string]workspaceExplorerWaiter{"a": {dc: dc}, "b": {dc: dc}}
					h.workspaceFileReqs.pending = map[string]workspaceFileWaiter{"a": {dc: dc}, "b": {dc: dc}}
				case "gone":
					want = errWorkspaceDaemonGone
					h.SetSessionOwner("session", "other")
				case "send":
					want = errWorkspaceSendFailed
					for len(dc.send) < cap(dc.send) {
						dc.send <- []byte("occupied")
					}
				case "deadline":
					want = context.DeadlineExceeded
					var cancel context.CancelFunc
					ctx, cancel = context.WithDeadline(ctx, time.Unix(0, 0))
					defer cancel()
				case "disconnect":
					want = errWorkspaceDaemonGone
				}
				done := make(chan error, 1)
				go func() {
					var err error
					if kind == "explorer" {
						_, err = h.requestWorkspaceExplorer(ctx, dc, req)
					} else {
						_, err = h.requestWorkspaceFile(ctx, dc, "session", "canary-secret", req)
					}
					done <- err
				}()
				if cause == "disconnect" {
					select {
					case <-dc.send:
					case <-time.After(time.Second):
						t.Fatal("no request")
					}
					if kind == "explorer" {
						h.abandonWorkspaceExplorerRequests(dc)
					} else {
						h.abandonWorkspaceFileRequests(dc)
					}
				}
				select {
				case err := <-done:
					if !errors.Is(err, want) {
						t.Fatalf("got %v, want %v", err, want)
					}
				case <-time.After(time.Second):
					t.Fatal("request did not complete")
				}
			})
		}
	}
}
