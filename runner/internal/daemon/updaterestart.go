package daemon

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/updatehandoff"
)

// Configured before the daemon starts any sessions; nil in isolated driver tests.
var updateObserver *updatehandoff.Observer

func SetUpdateObserver(o *updatehandoff.Observer) { updateObserver = o }

func (k *recordKeeper) observeUpdateLocked() { updateObserver.Observe(k.updateCandidateLocked()) }
func (k *recordKeeper) updateCandidateLocked() updatehandoff.Request {
	r := k.rec
	observed := updatehandoff.Request{SessionID: r.SessionID}
	if r.TurnActive && len(r.OpenCalls) == 1 && len(r.OpenTasks)+len(r.OpenSubagents) == 0 && !r.Waiting {
		call := r.OpenCalls[0]
		if k.liveCalls[call.CallID] == call && strings.EqualFold(call.Tool, "Bash") && call.CallID != "" {
			observed.Call, _ = json.Marshal(call)
			observed.EnginePID = r.EnginePID
			observed.EngineStart = updatehandoff.ProcessStart(r.EnginePID)
		}
	}
	return observed
}

// WithUpdateReceipt attaches evidence consumed and verified by the new daemon.
func WithUpdateReceipt(c RestartCause, r *updatehandoff.Receipt) RestartCause {
	if r.Fresh(time.Now()) {
		c.Planned = true
		c.Reason = "update"
		c.update = r
	}
	return c
}
func (c RestartCause) updateSucceeded(rec AgentRecord) bool {
	r := c.update
	if !c.Planned || !r.Fresh(time.Now()) || rec.SessionID != r.Request.SessionID || !rec.TurnActive || len(rec.OpenCalls) != 1 || len(rec.OpenTasks)+len(rec.OpenSubagents) != 0 {
		return false
	}
	call := rec.OpenCalls[0]
	if !strings.EqualFold(call.Tool, "Bash") || call.CallID == "" || (call.CommandHash == "" || !call.UpdateSafe) {
		return false
	}
	b, err := json.Marshal(call)
	return err == nil && bytes.Equal(b, r.Request.Call)
}

// Attribution uses the process tree. refresh-path (#237) restarts the daemon
// through the same handoff as install, without a rebuild. This scan only preserves the ordinary pause
// for destructive work alongside the installer, including beyond display truncation.
var updateInvocation = regexp.MustCompile(`(?i)(?:[^\s;&|()]+/)?install\.sh\s+(?:install|refresh-path)\b|\bmake\b[^;&|\n]*\b(?:install|restart)-daemon\b`)

// Read-only monitoring may name the installer without invoking it. Stop before shell
// separators and substitutions so a nested service command still gets checked.
var updateMonitor = regexp.MustCompile("(?i)\\b(?:pgrep|ps|grep|tail)\\b[^;&|()`\\n]*")
var installerMention = regexp.MustCompile(`(?i)\binstall\.sh\b`)

func safeUpdateCommand(command string) bool {
	if rmRecursiveForced(command) {
		return false
	}
	for _, re := range restartDenyList {
		if re != daemonCommand && re.MatchString(command) {
			return false
		}
	}
	remaining := updateInvocation.ReplaceAllString(command, "")
	remaining = updateMonitor.ReplaceAllStringFunc(remaining, func(segment string) string {
		return installerMention.ReplaceAllString(segment, "")
	})
	return !daemonCommand.MatchString(remaining)
}
