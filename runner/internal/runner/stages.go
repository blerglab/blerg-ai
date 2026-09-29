package runner

// Start stages the pod reports itself: only the pod can see that it has
// connected, that the clone is running or failed, and that the engine is
// starting. They ride the ordinary agent_event channel (kind start_stage) so
// the server persists and fans them out like any transcript event — see
// internal/server/startstages.go for the other half.

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/blerglab/blerg-ai/runner/internal/protocol"
)

// sender is the slice of the daemon WS client the reporter needs.
type sender interface {
	Send(msg any) error
}

// stageReporter sends start_stage events for one session. Reports queue in
// order and go out as soon as the server connection allows: a report made
// while the pod is not connected (the connect wait timed out, a reconnect is
// in progress) is kept and flushed on the next report, on reconnect, and —
// for a failure, the report that matters most — retried before the pod exits.
type stageReporter struct {
	s         sender
	sessionID string
	// finalWait bounds how long a failure report is retried before exit;
	// retryEvery paces the retries. Zero means the defaults below.
	finalWait  time.Duration
	retryEvery time.Duration

	mu    sync.Mutex
	queue []any
}

const (
	defaultFinalWait  = 60 * time.Second
	defaultRetryEvery = time.Second
)

func (r *stageReporter) report(stages ...protocol.StartStage) {
	if r == nil || r.s == nil || len(stages) == 0 {
		return
	}
	raw, err := json.Marshal(protocol.StartStagePayload{Stages: stages})
	if err != nil {
		return
	}
	r.enqueue(protocol.AgentEvent{
		Type: "agent_event", SessionID: r.sessionID, ClientEventID: newEventID(),
		Ts: time.Now().UTC().Format(time.RFC3339Nano), Kind: protocol.StartStageKind, Payload: raw,
	})
}

func (r *stageReporter) enqueue(msg any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queue = append(r.queue, msg)
	r.flushLocked()
}

// flush sends whatever is queued; true when nothing is left.
func (r *stageReporter) flush() bool {
	if r == nil || r.s == nil {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.flushLocked()
}

func (r *stageReporter) flushLocked() bool {
	for len(r.queue) > 0 {
		if err := r.s.Send(r.queue[0]); err != nil {
			return false
		}
		r.queue = r.queue[1:]
	}
	return true
}

// flushWithin retries the queue until it is empty or d has passed.
func (r *stageReporter) flushWithin(d time.Duration) bool {
	every := r.retryEvery
	if every <= 0 {
		every = defaultRetryEvery
	}
	deadline := time.Now().Add(d)
	for {
		if r.flush() {
			return true
		}
		if time.Now().After(deadline) {
			r.mu.Lock()
			n := len(r.queue)
			r.mu.Unlock()
			log.Printf("runner: %d start report(s) could not be delivered before exit", n)
			return false
		}
		time.Sleep(every)
	}
}

// fail reports stage as failed and — for a fresh start — ends the session
// with the same reason, so its row says why instead of waiting for the Job to
// be reconciled as a bare "cluster job failed". A resume only reports the
// stage: the session stays disconnected and resumable, since the next resume
// may well succeed (a token rotated back, a network blip). It returns once
// the reports are delivered (or finalWait has passed): the caller exits next.
func (r *stageReporter) fail(stageID, detail, hint string, resume bool) {
	if r == nil || r.s == nil {
		return
	}
	r.report(protocol.StartStage{ID: stageID, State: protocol.StageStateFailed, Detail: detail, Hint: hint})
	if !resume {
		msg := detail
		r.enqueue(protocol.SessionStateChanged{
			Type: "session_state_changed", SessionID: r.sessionID, Status: "error", Message: &msg,
		})
	}
	wait := r.finalWait
	if wait <= 0 {
		wait = defaultFinalWait
	}
	r.flushWithin(wait)
}

func active(id, detail string) protocol.StartStage {
	return protocol.StartStage{ID: id, State: protocol.StageStateActive, Detail: detail}
}

func done(id string) protocol.StartStage {
	return protocol.StartStage{ID: id, State: protocol.StageStateDone}
}

// newEventID is a random v4 UUID (agent_events.client_event_id is a uuid).
func newEventID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// missingEngineCredential says which credential the session's engine needs
// and did not get, or "" when it has one. Checked before cloning: without it
// the engine can only fail on its first turn, minutes later and less clearly.
func missingEngineCredential(engine string, env func(string) string) (detail, hint string) {
	const operator = " or ask the operator to add one to the cluster's agent Secret."
	switch engine {
	case "", "claude":
		if env("ANTHROPIC_API_KEY") == "" && env("CLAUDE_CODE_OAUTH_TOKEN") == "" {
			return "No Claude credential reached the pod (neither an API key nor a subscription token)",
				"Connect your own Claude credential in blerg settings," + operator
		}
	case "codex":
		if env("CODEX_AUTH_JSON") == "" {
			return "No Codex credential reached the pod (CODEX_AUTH_JSON is empty)",
				"Connect your own Codex login in blerg settings," + operator
		}
	case "hermes":
		if env("HERMES_ENV_CONTENTS") == "" {
			return "No Hermes configuration reached the pod (HERMES_ENV_CONTENTS is empty)",
				"Connect your own Hermes configuration in blerg settings," + operator
		}
	}
	return "", ""
}

// cloneError marks a PrepareWorkspace failure that came from git clone
// itself, as opposed to any other workspace step.
type cloneError struct{ out string }

func (e *cloneError) Error() string { return "clone: " + e.out }

// classifyWorkspaceError turns a PrepareWorkspace failure into a fixed,
// caller-safe explanation. git's own text is never passed on: it can echo
// the clone URL, and the URL carries the git token.
func classifyWorkspaceError(err error) (detail, hint string) {
	var ce *cloneError
	if !errors.As(err, &ce) {
		return "Preparing the workspace failed", "See the pod log (kubectl logs) for details, then start the session again."
	}
	return classifyCloneError(ce.out)
}

// classifyCloneError names the cause of a failed git clone from its output.
func classifyCloneError(out string) (detail, hint string) {
	o := strings.ToLower(out)
	accessHint := "Check the repo name, and that your GitHub credential (or the cluster's BLERG_RUNNER_GIT_TOKEN) can read it."
	switch {
	case strings.Contains(o, "executable file not found") || strings.Contains(o, "command not found"):
		return "git is not available in the agent image", "Rebuild the agent image with git installed (BLERG_RUNNER_AGENT_IMAGE)."
	case strings.Contains(o, "couldn't find remote ref") || (strings.Contains(o, "remote branch") && strings.Contains(o, "not found")):
		return "The branch to check out does not exist on the remote", "Check the branch name, then start the session again."
	case strings.Contains(o, "repository not found") ||
		(strings.Contains(o, "repository '") && strings.Contains(o, "not found")) ||
		strings.Contains(o, "does not appear to be a git repository"):
		return "Repository not found, or the git credential can't see it", accessHint
	case strings.Contains(o, "authentication failed") || strings.Contains(o, "could not read username") ||
		strings.Contains(o, "returned error: 403") || strings.Contains(o, "permission denied") ||
		strings.Contains(o, "terminal prompts disabled"):
		return "Git authentication failed", accessHint
	case strings.Contains(o, "could not resolve host") || strings.Contains(o, "timed out") ||
		strings.Contains(o, "failed to connect") || strings.Contains(o, "connection refused"):
		return "Couldn't reach the git host from the cluster", "Check the cluster's outbound network/DNS, then start the session again."
	}
	return "git clone failed", "See the pod log (kubectl logs) for git's output, then start the session again."
}
