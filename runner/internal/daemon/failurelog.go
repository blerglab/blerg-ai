package daemon

import (
	"log/slog"
	"strings"

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
