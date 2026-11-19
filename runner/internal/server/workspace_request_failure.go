package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
)

var (
	errWorkspaceBusy            = errors.New("workspace request cap reached")
	errWorkspaceRuntimeMismatch = errors.New("workspace runtime reference mismatch")
	errWorkspaceDaemonGone      = errors.New("workspace daemon connection gone")
	errWorkspaceSendFailed      = errors.New("workspace request send failed")
)

// Only fixed diagnostic labels may reach logs; errors can contain private paths.
func workspaceRequestFailure(err error) (code, message, cause string) {
	switch {
	case errors.Is(err, errWorkspaceBusy):
		return workspaceCodeBusy, "The workspace is busy. Try again.", "pending_cap"
	case errors.Is(err, errWorkspaceRuntimeMismatch):
		return workspaceCodeUnavailable, "The workspace runtime changed. Try again.", "runtime_mismatch"
	case errors.Is(err, errWorkspaceDaemonGone):
		return workspaceCodeNotRunning, "The session has no live workspace daemon.", "daemon_gone"
	case errors.Is(err, errWorkspaceSendFailed):
		return workspaceCodeUnavailable, "The workspace daemon is unavailable. Try again.", "send_failed"
	case errors.Is(err, context.DeadlineExceeded):
		return workspaceCodeDaemonTimeout, "The workspace runtime did not reply.", "deadline"
	case errors.Is(err, context.Canceled):
		return workspaceCodeUnavailable, "The workspace request was canceled.", "canceled"
	default:
		return workspaceCodeUnavailable, "The workspace is unavailable. Try again.", "unavailable"
	}
}

func logWorkspaceRequestFailure(sid, daemon, operation string, err error) {
	_, _, cause := workspaceRequestFailure(err)
	failurelog.Default.Event("workspace_request:"+operation+":"+cause, slog.LevelWarn,
		"component", "runner", "session_id", sid, "daemon_id", daemon, "operation", operation, "cause", cause)
}

func workspaceRequestProblem(w http.ResponseWriter, sid, daemon, operation string, err error) {
	logWorkspaceRequestFailure(sid, daemon, operation, err)
	code, message, _ := workspaceRequestFailure(err)
	workspaceFileProblem(w, http.StatusConflict, code, message)
}
