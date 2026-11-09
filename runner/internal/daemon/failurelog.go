package daemon

import (
	"errors"
	"log/slog"
	"net"
	"strings"

	"github.com/gorilla/websocket"

	"github.com/blerglab/blerg-ai/contracts/failurelog"
)

// Labels are fixed at call sites. Error strings and subprocess output may contain
// workspace paths, credentials or file contents and must never be copied here.
func logDaemonFailure(stage string, err error) {
	failurelog.Default.Event("daemon:"+stage, slog.LevelError, "component", "daemon", "stage", stage, "cause", failurelog.Cause(err))
}

// Helper replies cross a process boundary; only known codes may become labels.
func safeFailureCode(code string) string {
	switch code {
	case "unavailable", "busy", "forbidden", "not_found", "too_large", "timeout", "canceled", "invalid", "unsupported":
		return code
	default:
		return "unknown"
	}
}

func logRetainedAdoptionScratch() {
	failurelog.Default.Event("daemon:adoption scratch retained", slog.LevelWarn, "component", "daemon", "stage", "adoption scratch retained", "cause", "cleanup_not_proven")
}

// Refusals can contain a workspace path or an upstream error from spawn. Only
// developer-owned explanations enter diagnostics; the full detail stays on the
// session's existing restart pause notice. Key by session so one refusal cannot
// suppress the identity of another session refused in the same minute.
func logAdoptionRefusal(sessionID, reason string) {
	failurelog.Default.Event("daemon:adoption refused:"+sessionID, slog.LevelError,
		"component", "daemon", "stage", "adoption refused", "cause", failurelog.Cause(nil),
		"session_id", sessionID, "reason", safeAdoptionReason(reason))
}

func safeAdoptionReason(reason string) string {
	if strings.HasPrefix(reason, "its workspace ") && strings.HasSuffix(reason, " is gone") {
		return "its workspace is gone"
	}
	switch reason {
	case adoptWorkspaceUnverified, adoptWorkspaceForeign, adoptWorkspaceStopping, adoptWorkspaceClosed, adoptAwaitsServer,
		"its record names no workspace",
		"Workspace recovery is unavailable; retry after restarting the daemon.",
		"Claude Code is no longer available on this machine",
		"Codex is no longer available on this machine",
		"its native conversation identity is invalid",
		"its native conversation identity was not retained; start a new session explicitly",
		"it could not be started", noClaudeEngineRefusal:
		return reason
	}
	return "Recovery was refused; see the session's restart pause detail and preceding failure causes."
}

// Socket diagnostics retain the numeric close code, never its peer-owned text.
func logDaemonSocketFailure(stage string, err error) {
	cause := failurelog.Cause(err)
	code := 0
	var closed *websocket.CloseError
	var network net.Error
	switch {
	case errors.Is(err, websocket.ErrReadLimit):
		cause = "read_limit"
	case errors.As(err, &closed):
		cause, code = "websocket_close", closed.Code
	case errors.As(err, &network) && network.Timeout():
		cause = "socket_deadline"
	}
	failurelog.Default.Event("daemon_socket:"+stage+":"+cause, slog.LevelWarn, "component", "daemon", "stage", stage, "cause", cause, "close_code", code)
}

// Close labels are fixed at call sites; never include peer-controlled text.
func closeDaemonSocket(conn *websocket.Conn, reason string, err error) {
	logDaemonSocketFailure("close_"+reason, err)
	_ = conn.Close()
}
